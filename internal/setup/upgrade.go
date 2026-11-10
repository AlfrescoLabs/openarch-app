package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// upgradeHint tells the user how to move a setup of an earlier release.
const upgradeHint = "Stop it with 'openarch app down', back up the volumes, then run 'openarch app upgrade'."

// CheckPins fails when the setup does not use the pins of this build: its
// upstream commit, or the build-context URLs in .env.local. Compose builds from
// those URLs, so a setup with old URLs would build old code.
func (s Setup) CheckPins() error {
	if err := s.checkUpstream(); err != nil {
		return err
	}
	local, err := os.ReadFile(filepath.Join(s.Dir, EnvFile))
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", EnvFile, err)
	}
	values := ParseEnv(local)
	for _, g := range stack.GitContexts {
		if values[g.Var] != g.URL() {
			return output.Usage(fmt.Sprintf("%s builds %s from %s, this build pins %s", EnvFile, g.Var, values[g.Var], g.URL()), upgradeHint)
		}
	}
	return nil
}

// checkUpstream fails when the setup is not at the upstream commit of this
// build.
func (s Setup) checkUpstream() error {
	switch {
	case s.Config.Upstream == stack.UpstreamRef:
		return nil
	case stack.KnownUpstream(s.Config.Upstream):
		return output.Usage(fmt.Sprintf("the setup in %s uses upstream commit %s, this build uses %s", s.Dir, s.Config.Upstream, stack.UpstreamRef), upgradeHint)
	}
	return unknownUpstream(s)
}

func unknownUpstream(s Setup) error {
	return output.Usage(fmt.Sprintf("the setup in %s uses upstream commit %s, which this build does not know", s.Dir, s.Config.Upstream),
		"It comes from a newer release: update openarch-app.")
}

// UpgradeOptions select how Upgrade asks for confirmation.
type UpgradeOptions struct {
	// Confirm is called once the setup can be upgraded, before anything
	// changes. An error stops the upgrade.
	Confirm func(from, to string) error
}

// UpgradeResult describes an upgrade.
type UpgradeResult struct {
	Dir     string `json:"dir"`
	From    string `json:"from"`
	To      string `json:"to"`
	Changed bool   `json:"changed"`
	// Warnings are steps that the user must do by hand.
	Warnings []string `json:"warnings,omitempty"`
	Next     []string `json:"next,omitempty"`
}

// WriteText writes the human-readable form.
func (r UpgradeResult) WriteText(w io.Writer) error {
	var b strings.Builder
	switch {
	case !r.Changed:
		fmt.Fprintf(&b, "The setup in %s already uses upstream commit %s.\n", r.Dir, r.To)
	case r.From == r.To:
		fmt.Fprintf(&b, "Updated the setup in %s to this release. The upstream commit stays %s.\n", r.Dir, r.To)
	default:
		fmt.Fprintf(&b, "Moved the setup in %s from upstream commit %s to %s.\n", r.Dir, r.From, r.To)
	}
	for _, m := range r.Warnings {
		fmt.Fprintf(&b, "\n%s\n", m)
	}
	if len(r.Next) > 0 {
		b.WriteString("\nNext:\n")
		for _, n := range r.Next {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Upgrade moves the setup to the upstream commit and the build contexts of
// this build. It refuses a running stack and a setup from a newer release.
// Every step can run again, so an upgrade that stopped halfway is completed by
// running Upgrade again. It does not build: the next 'up' does.
func Upgrade(ctx context.Context, env Env, s Setup, opts UpgradeOptions) (UpgradeResult, error) {
	res := UpgradeResult{Dir: s.Dir, From: s.Config.Upstream, To: stack.UpstreamRef}
	if !stack.KnownUpstream(s.Config.Upstream) {
		return res, unknownUpstream(s)
	}
	repos := []pinnedRepo{upstreamRepo}
	if s.Config.ManagedNuxeo {
		repos = append(repos, nuxeoRepo)
	}
	// upgrade replaces upstream/ before it records the new commit, so a setup
	// that records the current commit already has it. nuxeo-deployment has no
	// such record, so its checkout is asked.
	if s.CheckPins() == nil && s.Config.SchemaVersion == SchemaVersion && atPins(ctx, env, s, repos[1:]) {
		res.Warnings = nuxeoWarnings(ctx, env, s)
		return res, nil
	}
	if _, err := env.LookPath("git"); err != nil {
		return res, output.NewError(output.ExitPrerequisite, "git command not found in PATH", "Install Git: https://git-scm.com/downloads")
	}
	for _, r := range repos {
		if err := recoverSwap(filepath.Join(s.Dir, r.Dir)); err != nil {
			return res, err
		}
	}
	status, err := Status(ctx, env, s)
	if err != nil {
		return res, err
	}
	if running := runningServices(status.Services); len(running) > 0 {
		return res, output.Usage("the stack is running ("+strings.Join(running, ", ")+")", "Stop it with 'openarch app down', back up the volumes, then run 'openarch app upgrade' again.")
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(s.Config.Upstream, stack.UpstreamRef); err != nil {
			return res, err
		}
	}

	// Fetch every repository that is not at its pin before anything changes, so
	// a network error leaves the setup as it was.
	fetched := map[string]string{}
	for _, r := range repos {
		if atPins(ctx, env, s, []pinnedRepo{r}) {
			continue
		}
		tmp, err := fetchPinned(ctx, env, s.Dir, r, "'openarch app upgrade'")
		if err != nil {
			return res, err
		}
		fetched[r.Dir] = tmp
	}
	if err := rewriteContexts(s.Dir); err != nil {
		return res, err
	}
	for _, r := range repos {
		if tmp, ok := fetched[r.Dir]; ok {
			if err := swapDir(filepath.Join(s.Dir, r.Dir), tmp); err != nil {
				return res, err
			}
		}
	}
	// Version 1 had no managedNuxeo: its nuxeo-deployment is the user's, so the
	// zero value is the right one.
	s.Config.SchemaVersion = SchemaVersion
	s.Config.Upstream = stack.UpstreamRef
	if err := Save(s.Dir, s.Config); err != nil {
		return res, err
	}
	res.Changed = true
	res.Warnings = nuxeoWarnings(ctx, env, s)
	res.Next = []string{"openarch app up"}
	if res.From != res.To {
		res.Next[0] += "   (builds the new images)"
	}
	return res, nil
}

// atPins reports whether the folder of every repository is a checkout of its
// pinned commit.
func atPins(ctx context.Context, env Env, s Setup, repos []pinnedRepo) bool {
	for _, r := range repos {
		head, err := env.Run(ctx, filepath.Join(s.Dir, r.Dir), nil, "git", "rev-parse", "HEAD")
		if err != nil || strings.TrimSpace(string(head)) != r.Ref {
			return false
		}
	}
	return true
}

// runningServices returns the services that are not stopped.
func runningServices(services []Service) []string {
	var names []string
	for _, v := range services {
		if v.State != "exited" && v.State != "created" && v.State != "dead" {
			names = append(names, v.Service)
		}
	}
	return names
}

// rewriteContexts sets the build-context keys of .env.local to the pinned
// URLs, in place. Every other line stays as it is.
func rewriteContexts(dir string) error {
	path := filepath.Join(dir, EnvFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	values := map[string]string{}
	for _, g := range stack.GitContexts {
		values[g.Var] = g.URL()
	}
	return writeFile(path, setEnvKeys(data, values), 0o600)
}

// setEnvKeys replaces the KEY=VALUE lines of the keys in values and appends
// the keys that data does not have, in a stable order. Comments, blank lines
// and the other keys stay as they are.
func setEnvKeys(data []byte, values map[string]string) []byte {
	var b bytes.Buffer
	done := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if k, _, ok := strings.Cut(trimmed, "="); ok && !strings.HasPrefix(trimmed, "#") {
			k = strings.TrimSpace(k)
			if v, set := values[k]; set {
				line = k + "=" + v
				done[k] = true
			}
		}
		b.WriteString(line + "\n")
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		if !done[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, values[k])
	}
	return b.Bytes()
}

// swapDir replaces dir with next. The old folder is renamed first, so a
// failure between the two renames leaves both folders on disk, and
// recoverSwap puts the old one back.
func swapDir(dir, next string) error {
	old := dir + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(next, dir); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

// recoverSwap puts back the old folder of a swap that stopped between its two
// renames.
func recoverSwap(dir string) error {
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if _, err := os.Stat(dir + ".old"); err != nil {
		return nil
	}
	return os.Rename(dir+".old", dir)
}

// nuxeoWarnings returns a warning when a Nuxeo profile uses a nuxeo-deployment
// that the user manages and that is not at the commit of this build.
func nuxeoWarnings(ctx context.Context, env Env, s Setup) []string {
	if s.Config.Nuxeo == 0 || s.Config.ManagedNuxeo {
		return nil
	}
	nd := filepath.Join(s.Dir, stack.NuxeoDeploymentDir)
	if _, err := os.Stat(nd); err != nil {
		return nil
	}
	head, err := env.Run(ctx, nd, nil, "git", "rev-parse", "HEAD")
	if err == nil && strings.TrimSpace(string(head)) == stack.NuxeoDeploymentRef {
		return nil
	}
	return []string{fmt.Sprintf("%s is not at commit %s, which this release uses. Stop it, then run:\n  git -C %s fetch --depth 1 origin %s\n  git -C %s checkout --detach FETCH_HEAD",
		stack.NuxeoDeploymentDir, stack.NuxeoDeploymentRef, nd, stack.NuxeoDeploymentRef, nd)}
}
