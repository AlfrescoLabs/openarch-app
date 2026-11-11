package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// Runner runs a command in dir with stdin and returns its stdout. On failure
// the error holds the first line of stderr.
type Runner func(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error)

// Env is the access to the system used by Init. Tests replace it with fakes.
type Env struct {
	Run Runner
	// Stream runs a command in dir with extra environment variables and
	// copies its output to stdout and stderr while it runs.
	Stream   func(ctx context.Context, dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error
	LookPath func(file string) (string, error)
	// HTTP calls the APIs of the running stack.
	HTTP *http.Client
}

// SystemEnv returns an Env backed by the real system.
func SystemEnv() Env {
	return Env{Run: runCommand, Stream: streamCommand, LookPath: exec.LookPath, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func streamCommand(ctx context.Context, dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, args[0], err)
	}
	return nil
}

func runCommand(ctx context.Context, dir string, stdin io.Reader, name string, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if line != "" {
			return stdout.Bytes(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), line)
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// InitOptions are the inputs of Init. Settings holds the values that go to
// openarch-app.yaml; its SchemaVersion and Upstream are set by Init.
type InitOptions struct {
	Settings Config
	Dir      string
	// Force replaces an openarch context with the same name.
	Force bool
	// OpenArchBin is the openarch command. Empty means "openarch" from PATH.
	OpenArchBin string
	// AlfrescoNames are the names of the Alfresco sources that the sample UI
	// shows, one for each instance. They need two instances: upstream names
	// only a list of sources. Empty means DefaultAlfrescoNames.
	AlfrescoNames []string
	// FilesystemReaders are the users who can read the files of the
	// filesystem source. FilesystemEveryone lets every user read them.
	FilesystemReaders  []string
	FilesystemEveryone bool
}

// DefaultAlfrescoNames are the names of the two Alfresco sources when init
// gets none.
var DefaultAlfrescoNames = []string{"Alfresco 1", "Alfresco 2"}

// maxNameLength is the longest Alfresco source name.
const maxNameLength = 60

// alfrescoNames returns the names of the Alfresco sources of c, or a usage
// error. A name goes into .env.local in single quotes, so it cannot hold one.
func alfrescoNames(c Config, names []string) ([]string, error) {
	if c.Alfresco < 2 {
		if len(names) > 0 {
			return nil, output.Usage("--alfresco-name needs --alfresco 2", "With one Alfresco, the sources are named Alfresco and Nuxeo.")
		}
		return nil, nil
	}
	if len(names) == 0 {
		return DefaultAlfrescoNames, nil
	}
	if len(names) != c.Alfresco {
		return nil, output.Usage(fmt.Sprintf("--alfresco-name given %d times, want %d", len(names), c.Alfresco), "Give one name for each Alfresco instance, in order.")
	}
	for _, n := range names {
		bad := strings.TrimSpace(n) == "" || len(n) > maxNameLength || strings.ContainsRune(n, '\'') ||
			strings.ContainsFunc(n, unicode.IsControl)
		if bad {
			return nil, output.Usage(fmt.Sprintf("invalid --alfresco-name %q", n), fmt.Sprintf("Use 1 to %d characters, with no single quote.", maxNameLength))
		}
	}
	return names, nil
}

// InitResult describes a new setup.
type InitResult struct {
	Dir         string   `json:"dir"`
	Config      Config   `json:"config"`
	OpenArchURL string   `json:"openarchURL"`
	Reused      []string `json:"reused,omitempty"`
	// Next are the commands to run next, in order.
	Next []string `json:"next"`
}

// WriteText writes the human-readable form.
func (r InitResult) WriteText(w io.Writer) error {
	var b strings.Builder
	what := r.Config.String()
	if r.Config.Filesystem != "" {
		what += ", the folder " + r.Config.Filesystem
	}
	if r.Config.SampleUI {
		what += ", sample UI"
	}
	fmt.Fprintf(&b, "Created the App for OpenArch setup in %s (%s).\n", r.Dir, what)
	fmt.Fprintf(&b, "Upstream commit %s.\n", r.Config.Upstream)
	for _, f := range r.Reused {
		fmt.Fprintf(&b, "Kept the existing %s.\n", f)
	}
	fmt.Fprintf(&b, "Added the openarch context %q for %s.\n", r.Config.Context, r.OpenArchURL)
	b.WriteString("\nNext:\n")
	for _, n := range r.Next {
		fmt.Fprintf(&b, "  %s\n", n)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Init creates a setup folder: it fetches upstream at the pinned commit,
// writes the generated files and adds the openarch context. A setup folder
// that has no openarch-app.yaml yet, because an earlier Init failed, is
// completed: the upstream checkout and .env.local are kept.
func Init(ctx context.Context, env Env, opts InitOptions) (InitResult, error) {
	c := opts.Settings
	c.SchemaVersion = SchemaVersion
	c.Upstream = stack.UpstreamRef
	c.ManagedNuxeo = c.Nuxeo > 0
	if err := c.Valid(); err != nil {
		return InitResult{}, err
	}
	names, err := alfrescoNames(c, opts.AlfrescoNames)
	if err != nil {
		return InitResult{}, err
	}
	readers, err := filesystemSource(&c, opts.FilesystemReaders, opts.FilesystemEveryone)
	if err != nil {
		return InitResult{}, err
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return InitResult{}, err
	}
	res := InitResult{Dir: dir}
	if _, err := os.Stat(filepath.Join(dir, ConfigFile)); err == nil {
		return res, output.Usage(fmt.Sprintf("%s is already a setup folder", dir), "Use another folder, or delete this one first.")
	}

	bin := opts.OpenArchBin
	if bin == "" {
		if bin, err = env.LookPath("openarch"); err != nil {
			return res, output.NewError(output.ExitPrerequisite, "openarch command not found in PATH", "Install the CLI for OpenArch: https://github.com/AlfrescoLabs/openarch-cli#install")
		}
	}
	if _, err := env.Run(ctx, "", nil, bin, "config", "get", "--name", c.Context, "--output", "json"); err == nil && !opts.Force {
		return res, output.Usage(fmt.Sprintf("the openarch context %q already exists", c.Context), "Use --context-name with another name, or --force to replace it.")
	}
	if _, err := env.LookPath("git"); err != nil {
		return res, output.NewError(output.ExitPrerequisite, "git command not found in PATH", "Install Git: https://git-scm.com/downloads")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	reused, err := fetchUpstream(ctx, env, dir)
	if err != nil {
		return res, err
	}
	if reused {
		res.Reused = append(res.Reused, UpstreamDir+"/")
	}
	if c.ManagedNuxeo {
		reused, err := fetchCheckout(ctx, env, dir, nuxeoRepo)
		if err != nil {
			return res, err
		}
		if reused {
			res.Reused = append(res.Reused, stack.NuxeoDeploymentDir+"/")
		}
	}

	merged, reused, err := writeEnv(dir, &c, names, readers)
	if err != nil {
		return res, err
	}
	if reused {
		res.Reused = append(res.Reused, EnvFile)
	}
	if c.Filesystem != "" && merged[syncPasswordKey] == "" {
		return res, output.Usage(fmt.Sprintf("the existing %s has no settings for the filesystem source", EnvFile), "Run init again without --filesystem, or in a new folder.")
	}
	user, password := merged[adminUserKey], merged[adminPasswordKey]
	if user == "" || password == "" {
		return res, fmt.Errorf("%s and %s are not set in %s or %s", adminUserKey, adminPasswordKey, EnvFile, filepath.Join(UpstreamDir, ".env"))
	}
	if !c.DefaultUsers {
		users, err := renderUsers(password)
		if err != nil {
			return res, err
		}
		if err := writeFile(filepath.Join(dir, UsersFile), users, 0o644); err != nil {
			return res, err
		}
	}
	if err := WriteOverride(dir, c); err != nil {
		return res, err
	}

	res.OpenArchURL = fmt.Sprintf("http://%s:%d", contextHost(c.BindIP), stack.OpenArchPort)
	// The stack is not running yet, so the CLI cannot check the login now.
	if _, err := env.Run(ctx, "", strings.NewReader(password), bin, "config", "set", "--name", c.Context,
		"-s", res.OpenArchURL, "-u", user, "--password-stdin", "--no-verify"); err != nil {
		return res, fmt.Errorf("cannot add the openarch context: %w", err)
	}
	if err := Save(dir, c); err != nil {
		return res, err
	}
	res.Config = c
	res.Next = nextSteps(c, opts.Dir)
	return res, nil
}

// nextSteps returns the commands that start the new setup. They need --dir
// unless the setup folder is the one that the commands find by default.
func nextSteps(c Config, dir string) []string {
	flag := ""
	if d := filepath.Clean(dir); d != DefaultDir && d != "." {
		flag = " --dir " + dir
	}
	up := "openarch app up" + flag
	if c.ManagedNuxeo {
		up += "   (the first start builds Nuxeo from source, which takes long)"
	}
	steps := []string{"openarch app models pull" + flag, up}
	if c.Filesystem != "" {
		steps = append(steps, "openarch app sync"+flag+"   (reads the folder; run it again after a change)")
	}
	return steps
}

// pinnedRepo is a Git repository that a setup holds at a pinned commit, in a
// folder of its own.
type pinnedRepo struct {
	Dir, URL, Ref string
}

var (
	upstreamRepo = pinnedRepo{UpstreamDir, stack.UpstreamURL, stack.UpstreamRef}
	nuxeoRepo    = pinnedRepo{stack.NuxeoDeploymentDir, stack.NuxeoDeploymentURL, stack.NuxeoDeploymentRef}
)

// fetchUpstream fetches the pinned upstream commit into upstream/. It reports
// true when upstream/ already holds that commit.
func fetchUpstream(ctx context.Context, env Env, dir string) (bool, error) {
	return fetchCheckout(ctx, env, dir, upstreamRepo)
}

// fetchCheckout fetches the pinned commit of r into its folder. It reports true
// when the folder already holds that commit, and refuses a folder at another
// commit.
func fetchCheckout(ctx context.Context, env Env, dir string, r pinnedRepo) (bool, error) {
	path := filepath.Join(dir, r.Dir)
	if _, err := os.Stat(path); err == nil {
		head, err := env.Run(ctx, path, nil, "git", "rev-parse", "HEAD")
		if err == nil && strings.TrimSpace(string(head)) == r.Ref {
			return true, nil
		}
		return false, output.Usage(fmt.Sprintf("%s is not a checkout of commit %s", path, r.Ref), "Delete "+path+" and run init again.")
	}
	tmp, err := fetchPinned(ctx, env, dir, r, "init")
	if err != nil {
		return false, err
	}
	return false, os.Rename(tmp, path)
}

// fetchPinned fetches the pinned commit of r into a temporary folder next to
// its folder and returns its path, so a failed fetch leaves the folder as it
// was. cmd names the command to run again after a network error.
func fetchPinned(ctx context.Context, env Env, dir string, r pinnedRepo, cmd string) (string, error) {
	tmp := filepath.Join(dir, r.Dir+".partial")
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	steps := [][]string{
		{"-c", "init.defaultBranch=main", "init", "-q", tmp},
		{"-C", tmp, "fetch", "-q", "--depth", "1", r.URL, r.Ref},
		{"-C", tmp, "checkout", "-q", "--detach", "FETCH_HEAD"},
	}
	for _, args := range steps {
		if _, err := env.Run(ctx, dir, nil, "git", args...); err != nil {
			return "", output.NewError(output.ExitRemote, fmt.Sprintf("cannot fetch %s: %v", r.Dir, err), "Check your network, then run "+cmd+" again.")
		}
	}
	head, err := env.Run(ctx, dir, nil, "git", "-C", tmp, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	if got := strings.TrimSpace(string(head)); got != r.Ref {
		return "", fmt.Errorf("%s checkout is at %s, expected %s", r.Dir, got, r.Ref)
	}
	return tmp, nil
}

// writeEnv writes .env.local, or keeps an existing one, and returns the
// upstream .env values with the .env.local values on top. An existing file
// sets the bind IP in c, so the config matches what Compose will use.
func writeEnv(dir string, c *Config, names, readers []string) (map[string]string, bool, error) {
	path := filepath.Join(dir, EnvFile)
	local, err := os.ReadFile(path)
	reused := err == nil
	switch {
	case errors.Is(err, fs.ErrNotExist):
		values, err := envValues(*c, names, readers)
		if err != nil {
			return nil, false, err
		}
		local = renderEnv(values)
		if err := writeFile(path, local, 0o600); err != nil {
			return nil, false, err
		}
	case err != nil:
		return nil, false, err
	}
	upstream, err := os.ReadFile(filepath.Join(dir, UpstreamDir, ".env"))
	if err != nil {
		return nil, false, err
	}
	merged := ParseEnv(upstream)
	for k, v := range ParseEnv(local) {
		merged[k] = v
	}
	if ip := merged["BIND_IP"]; reused && ip != "" {
		c.BindIP = ip
	}
	return merged, reused, nil
}

// contextHost is the host of the openarch context URL. A wildcard bind
// address is reachable on the loopback address.
func contextHost(bindIP string) string {
	switch bindIP {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	if strings.Contains(bindIP, ":") && !strings.HasPrefix(bindIP, "[") {
		return "[" + bindIP + "]"
	}
	return bindIP
}
