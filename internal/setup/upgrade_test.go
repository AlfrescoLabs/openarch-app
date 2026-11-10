package setup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

const oldRef = "1111111111111111111111111111111111111111"

// withPreviousRef makes oldRef the upstream commit of an earlier release.
func withPreviousRef(t *testing.T) {
	t.Helper()
	saved := stack.PreviousUpstreamRefs
	stack.PreviousUpstreamRefs = []string{oldRef}
	t.Cleanup(func() { stack.PreviousUpstreamRefs = saved })
}

// oldSetup returns a setup made by the earlier release: oldRef, old context
// URLs, and other keys that an upgrade must keep.
func oldSetup(t *testing.T, profile string) Setup {
	t.Helper()
	s := newSetup(t, Config{Upstream: oldRef, Sources: srcOf(profile), Context: "app"})
	local := "# Written by 'openarch app init'.\nBIND_IP=127.0.0.1\nHXPR_PASSWORD=Kept-Pass1\n"
	for _, g := range stack.GitContexts {
		local += g.Var + "=" + g.Repo + "#" + oldRef + "\n"
	}
	if err := os.WriteFile(filepath.Join(s.Dir, EnvFile), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, UpstreamDir, "old-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

// fakeUpgrade plays docker compose ps and git for Upgrade.
type fakeUpgrade struct {
	psOut      string
	fetchFails bool
	// heads maps a folder name (upstream, upstream.partial, nuxeo-deployment)
	// to the commit that 'git rev-parse HEAD' prints there.
	heads map[string]string
	calls []string
}

func (f *fakeUpgrade) env() Env {
	return Env{
		LookPath: func(file string) (string, error) { return "/usr/bin/" + file, nil },
		Run: func(_ context.Context, dir string, _ io.Reader, name string, args ...string) ([]byte, error) {
			call := name + " " + strings.Join(args, " ")
			f.calls = append(f.calls, call)
			switch {
			case strings.Contains(call, " ps -a --format json"):
				return []byte(f.psOut), nil
			case name == "git" && slices.Contains(args, "init"):
				return nil, os.MkdirAll(args[len(args)-1], 0o755)
			case name == "git" && slices.Contains(args, "fetch"):
				if f.fetchFails {
					return nil, errors.New("git fetch: could not resolve host")
				}
				return nil, nil
			case name == "git" && slices.Contains(args, "checkout"):
				return nil, os.WriteFile(filepath.Join(args[1], "new-file"), nil, 0o644)
			case name == "git" && slices.Contains(args, "rev-parse"):
				where := filepath.Base(dir)
				if i := slices.Index(args, "-C"); i >= 0 {
					where = filepath.Base(args[i+1])
				}
				if h, ok := f.heads[where]; ok {
					return []byte(h + "\n"), nil
				}
				return nil, errors.New("not a git repository")
			}
			return nil, errors.New("unexpected " + call)
		},
	}
}

func newFakeUpgrade() *fakeUpgrade {
	return &fakeUpgrade{heads: map[string]string{UpstreamDir: oldRef, UpstreamDir + ".partial": stack.UpstreamRef}}
}

func TestCheckPins(t *testing.T) {
	withPreviousRef(t)
	if err := newSetup(t, Config{}).CheckPins(); err != nil {
		t.Errorf("current setup: %v", err)
	}
	var e *output.Error
	err := oldSetup(t, "alfresco").CheckPins()
	if !errors.As(err, &e) || e.Code != output.ExitUsage || !strings.Contains(e.Hint, "openarch app upgrade") {
		t.Errorf("old setup: %v", err)
	}
	err = newSetup(t, Config{Upstream: "2222222222222222222222222222222222222222"}).CheckPins()
	if !errors.As(err, &e) || !strings.Contains(e.Message, "does not know") || !strings.Contains(e.Hint, "update openarch-app") {
		t.Errorf("newer setup: %v", err)
	}
	// The upstream commit is current, but .env.local still builds old code.
	s := newSetup(t, Config{})
	if err := os.WriteFile(filepath.Join(s.Dir, EnvFile), []byte("CONTENT_LAKE_GIT_CONTEXT=x#old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPins(); !errors.As(err, &e) || !strings.Contains(e.Message, "CONTENT_LAKE_GIT_CONTEXT") {
		t.Errorf("old context URL: %v", err)
	}
}

func TestUpRefusesOldSetup(t *testing.T) {
	withPreviousRef(t)
	s := oldSetup(t, "alfresco")
	f := &fakeDocker{}
	if _, err := Up(context.Background(), f.env(), s, UpOptions{}, Streams{Err: io.Discard}); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("up ran %v", f.calls)
	}
}

func TestUpgrade(t *testing.T) {
	withPreviousRef(t)
	s := oldSetup(t, "alfresco")
	f := newFakeUpgrade()
	var asked []string
	res, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{Confirm: func(from, to string) error {
		asked = append(asked, from, to)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.From != oldRef || res.To != stack.UpstreamRef || len(res.Next) != 1 {
		t.Errorf("result = %+v", res)
	}
	if strings.Join(asked, " ") != oldRef+" "+stack.UpstreamRef {
		t.Errorf("confirm got %v", asked)
	}
	s, err = Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckPins(); err != nil {
		t.Errorf("after upgrade: %v", err)
	}
	local := string(mustRead(t, filepath.Join(s.Dir, EnvFile)))
	if !strings.HasPrefix(local, "# Written by 'openarch app init'.\nBIND_IP=127.0.0.1\nHXPR_PASSWORD=Kept-Pass1\n") {
		t.Errorf("other lines changed: %q", local)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, UpstreamDir, "new-file")); err != nil {
		t.Errorf("upstream/ was not replaced: %v", err)
	}
	for _, gone := range []string{UpstreamDir + "/old-file", UpstreamDir + ".partial", UpstreamDir + ".old"} {
		if _, err := os.Stat(filepath.Join(s.Dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s still exists", gone)
		}
	}
	// A second run finds nothing to do. (The real checkout is now at the pin.)
	f.heads[UpstreamDir] = stack.UpstreamRef
	res, err = Upgrade(context.Background(), f.env(), s, UpgradeOptions{Confirm: func(string, string) error { return errors.New("asked again") }})
	if err != nil || res.Changed {
		t.Errorf("second run: %+v, %v", res, err)
	}
}

func TestUpgradeRefuses(t *testing.T) {
	withPreviousRef(t)
	tests := []struct {
		name string
		f    func(*fakeUpgrade)
		s    func(*testing.T) Setup
		code output.ExitCode
	}{
		{"running stack", func(f *fakeUpgrade) { f.psOut = `{"Service":"rag-service","State":"running"}` }, func(t *testing.T) Setup { return oldSetup(t, "alfresco") }, output.ExitUsage},
		{"newer setup", func(*fakeUpgrade) {}, func(t *testing.T) Setup {
			return newSetup(t, Config{Upstream: "2222222222222222222222222222222222222222"})
		}, output.ExitUsage},
		{"fetch fails", func(f *fakeUpgrade) { f.fetchFails = true }, func(t *testing.T) Setup { return oldSetup(t, "alfresco") }, output.ExitRemote},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.s(t)
			before := string(mustRead(t, filepath.Join(s.Dir, EnvFile)))
			f := newFakeUpgrade()
			tt.f(f)
			_, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{})
			if output.CodeOf(err) != tt.code {
				t.Fatalf("err = %v, want code %v", err, tt.code)
			}
			if after := string(mustRead(t, filepath.Join(s.Dir, EnvFile))); after != before {
				t.Errorf(".env.local changed")
			}
			if c, _ := Load(s.Dir); c.Upstream != s.Config.Upstream {
				t.Errorf("upstream recorded as %s", c.Upstream)
			}
			if _, err := os.Stat(filepath.Join(s.Dir, UpstreamDir, "new-file")); !os.IsNotExist(err) {
				t.Error("upstream/ was replaced")
			}
		})
	}
}

func TestUpgradeCancelled(t *testing.T) {
	withPreviousRef(t)
	s := oldSetup(t, "alfresco")
	f := newFakeUpgrade()
	_, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{Confirm: func(string, string) error { return errors.New("cancelled") }})
	if err == nil || err.Error() != "cancelled" {
		t.Fatalf("err = %v", err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "git") && !strings.Contains(c, "rev-parse") {
			t.Errorf("ran %q after a cancel", c)
		}
	}
}

func TestUpgradeResumes(t *testing.T) {
	withPreviousRef(t)
	// An earlier run swapped upstream/ but stopped before it saved the config.
	s := oldSetup(t, "alfresco")
	f := newFakeUpgrade()
	f.heads[UpstreamDir] = stack.UpstreamRef
	res, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{})
	if err != nil || !res.Changed {
		t.Fatalf("%+v, %v", res, err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "fetch") {
			t.Errorf("fetched again: %s", c)
		}
	}
	if s, _ := Open(s.Dir); s.CheckPins() != nil {
		t.Error("the setup was not completed")
	}
}

func TestUpgradeNuxeoWarning(t *testing.T) {
	withPreviousRef(t)
	s := oldSetup(t, "full")
	if err := os.MkdirAll(filepath.Join(s.Dir, stack.NuxeoDeploymentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	f := newFakeUpgrade()
	f.heads[stack.NuxeoDeploymentDir] = oldRef
	res, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{})
	if err != nil || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], stack.NuxeoDeploymentRef) {
		t.Fatalf("%+v, %v", res, err)
	}
	f.heads[stack.NuxeoDeploymentDir] = stack.NuxeoDeploymentRef
	if res, _ := Upgrade(context.Background(), f.env(), s, UpgradeOptions{}); len(res.Warnings) != 0 {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestSetEnvKeys(t *testing.T) {
	in := "# A=comment\nA=1\nB = 2\nC=3\n"
	got := string(setEnvKeys([]byte(in), map[string]string{"B": "new", "D": "4", "A": "x"}))
	want := "# A=comment\nA=x\nB=new\nC=3\nD=4\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRecoverSwap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), UpstreamDir)
	if err := os.MkdirAll(dir+".old", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := recoverSwap(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("old folder not put back: %v", err)
	}
}

func TestUpgradeMigratesVersion1(t *testing.T) {
	// A setup of 0.2.0: the current upstream commit, version 1, and a
	// nuxeo-deployment that the user started as a project of its own.
	s := newSetup(t, Config{Sources: srcOf("full"), Context: "app"})
	// The file as 0.2.0 wrote it: the upstream profile and extras.
	v1 := "schemaVersion: 1\nupstream: " + stack.UpstreamRef + "\nprofile: full\nextras:\n  - alfresco-2\n" +
		"llmModel: m\nembeddingModel: e\nbindIP: 127.0.0.1\ncontext: app\n"
	if err := os.WriteFile(filepath.Join(s.Dir, ConfigFile), []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.Dir); err == nil {
		t.Fatal("Open read a version 1 file")
	}
	s, err := OpenForUpgrade(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeUpgrade()
	f.heads[UpstreamDir] = stack.UpstreamRef
	res, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{})
	if err != nil || !res.Changed || res.Next[0] != "openarch app up" {
		t.Fatalf("%+v, %v", res, err)
	}
	c, err := Load(s.Dir)
	if err != nil || c.SchemaVersion != SchemaVersion || c.ManagedNuxeo || c.Sources != (stack.Sources{Alfresco: 2, Nuxeo: 1}) {
		t.Errorf("config = %+v, %v", c, err)
	}
	// The migrated file has the counts and no upstream keys.
	if got := string(mustRead(t, filepath.Join(s.Dir, ConfigFile))); !strings.Contains(got, "alfresco: 2\nnuxeo: 1\n") || strings.Contains(got, "profile") {
		t.Errorf("file = %s", got)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "fetch") {
			t.Errorf("fetched although the commit did not change: %s", call)
		}
	}
}

func TestUpgradeManagedNuxeo(t *testing.T) {
	// A managed nuxeo-deployment at an older commit moves with the upgrade.
	s := newSetup(t, Config{Sources: srcOf("nuxeo"), ManagedNuxeo: true, Context: "app"})
	nd := filepath.Join(s.Dir, stack.NuxeoDeploymentDir)
	if err := os.MkdirAll(nd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nd, "old-file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFakeUpgrade()
	f.heads[UpstreamDir] = stack.UpstreamRef
	f.heads[stack.NuxeoDeploymentDir] = oldRef
	f.heads[stack.NuxeoDeploymentDir+".partial"] = stack.NuxeoDeploymentRef
	res, err := Upgrade(context.Background(), f.env(), s, UpgradeOptions{})
	if err != nil || !res.Changed || len(res.Warnings) != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(nd, "new-file")); err != nil {
		t.Errorf("nuxeo-deployment was not replaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nd, "old-file")); !os.IsNotExist(err) {
		t.Error("the old nuxeo-deployment is still there")
	}
}
