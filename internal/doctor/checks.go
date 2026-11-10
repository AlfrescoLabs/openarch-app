package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

const (
	gib            = 1 << 30
	commandTimeout = 20 * time.Second
	// minOpenArch is the first CLI for OpenArch release that removes
	// OPENARCH_PASSWORD from the environment of plugins.
	minOpenArch = "0.5.2"
)

// memory holds the Docker memory thresholds of one profile, in bytes. No
// upstream service sets a memory limit, and the Alfresco and transform JVMs take
// a share of what they see, so these values are estimates.
type memory struct{ min, recommended int64 }

var profileMemory = map[string]memory{
	"nuxeo":    {6 * gib, 8 * gib},
	"alfresco": {8 * gib, 12 * gib},
	"full":     {10 * gib, 16 * gib},
}

// alfresco2Memory is added for the alfresco-2 extra: a second Alfresco,
// Postgres, ActiveMQ, indexer and ingesters. Upstream limits the JVM services
// of both instances to 13.5 GB in total.
var alfresco2Memory = memory{8 * gib, 10 * gib}

const installDockerFix = "Install Docker Desktop: https://docs.docker.com/get-started/get-docker/"

type dockerInfo struct {
	ServerVersion   string
	OperatingSystem string
	MemTotal        int64
	ServerErrors    []string
}

// state caches data shared by several checks.
type state struct {
	env  Env
	opts Options

	cliDone bool
	cliPath string
	cliErr  error

	infoDone bool
	info     dockerInfo
	infoErr  error

	runnerDone bool
	runnerErr  error
}

func (s *state) dockerCLI() (string, error) {
	if !s.cliDone {
		s.cliPath, s.cliErr = s.env.LookPath("docker")
		s.cliDone = true
	}
	return s.cliPath, s.cliErr
}

func (s *state) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	stdout, stderr, err := s.env.Run(ctx, name, args...)
	if err != nil {
		if msg := firstLine(stderr); msg != "" {
			return stdout, errors.New(msg)
		}
		return stdout, err
	}
	return stdout, nil
}

func (s *state) dockerInfo(ctx context.Context) (dockerInfo, error) {
	if !s.infoDone {
		s.infoDone = true
		out, err := s.run(ctx, "docker", "info", "--format", "{{json .}}")
		switch {
		case err != nil:
			s.infoErr = err
		case json.Unmarshal(out, &s.info) != nil:
			s.infoErr = errors.New("cannot parse the output of 'docker info'")
		case len(s.info.ServerErrors) > 0:
			s.infoErr = errors.New(s.info.ServerErrors[0])
		}
	}
	return s.info, s.infoErr
}

// modelRunner reports whether the 'docker model' command works.
func (s *state) modelRunner(ctx context.Context) error {
	if !s.runnerDone {
		s.runnerDone = true
		if _, err := s.dockerCLI(); err != nil {
			s.runnerErr = errors.New("docker CLI is missing")
		} else {
			_, s.runnerErr = s.run(ctx, "docker", "model", "version")
		}
	}
	return s.runnerErr
}

func checks(_ Env, opts Options) []check {
	cs := []check{
		{"docker.cli", "Docker CLI", checkDockerCLI},
		{"docker.daemon", "Docker daemon", checkDockerDaemon},
		{"docker.compose", "Docker Compose v2 or later", checkCompose},
		{"docker.memory", "Docker memory for the profile", checkMemory},
		{"git.cli", "Git", checkGit},
		{"model.runner", "Docker Model Runner", checkModelRunner},
		{"model.url", "Docker Model Runner API", checkModelURL},
		{"model.embedding", "Embedding model", modelCheck(func(o Options) string { return o.EmbeddingModel })},
		{"model.chat", "Chat model", modelCheck(func(o Options) string { return o.LLMModel })},
	}
	for _, p := range opts.Ports {
		cs = append(cs, check{fmt.Sprintf("port.%d", p), fmt.Sprintf("Port %d is free", p), portCheck(p)})
	}
	if stack.NeedsNuxeo(opts.Profile) {
		cs = append(cs, check{"nuxeo.deployment", "nuxeo-deployment folder", checkNuxeoDeployment})
	}
	return append(cs, check{"openarch.cli", "CLI for OpenArch " + minOpenArch + " or later", checkOpenArch})
}

func checkDockerCLI(_ context.Context, s *state) Result {
	path, err := s.dockerCLI()
	if err != nil {
		return Result{Status: Fail, Message: "docker command not found in PATH", Fix: installDockerFix}
	}
	return Result{Status: OK, Message: "found " + path}
}

func checkDockerDaemon(ctx context.Context, s *state) Result {
	if _, err := s.dockerCLI(); err != nil {
		return skipped("docker CLI is missing")
	}
	info, err := s.dockerInfo(ctx)
	if err != nil {
		fix := "Start Docker Desktop."
		if s.env.GOOS == "linux" {
			fix = "Start Docker Desktop, or start the Docker service: sudo systemctl start docker"
		}
		return Result{Status: Fail, Message: "cannot connect to the Docker daemon: " + err.Error(), Fix: fix}
	}
	return Result{Status: OK, Message: fmt.Sprintf("Docker %s (%s)", info.ServerVersion, info.OperatingSystem)}
}

func checkCompose(ctx context.Context, s *state) Result {
	if _, err := s.dockerCLI(); err != nil {
		return skipped("docker CLI is missing")
	}
	const fix = "Install the Docker Compose plugin (included in Docker Desktop): https://docs.docker.com/compose/install/"
	out, err := s.run(ctx, "docker", "compose", "version", "--short")
	if err != nil {
		return Result{Status: Fail, Message: "'docker compose' is not available: " + err.Error(), Fix: fix}
	}
	v := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	if err != nil {
		return Result{Status: Warn, Message: fmt.Sprintf("cannot parse Docker Compose version %q", v)}
	}
	if major < 2 {
		return Result{Status: Fail, Message: "Docker Compose " + v + " is too old", Fix: fix}
	}
	return Result{Status: OK, Message: "Docker Compose " + v}
}

func checkMemory(ctx context.Context, s *state) Result {
	info, ok := s.infoOrSkip(ctx)
	if !ok {
		return skipped("Docker daemon is not reachable")
	}
	need := profileMemory[s.opts.Profile]
	if slices.Contains(s.opts.Extras, "alfresco-2") {
		need.min += alfresco2Memory.min
		need.recommended += alfresco2Memory.recommended
	}
	mem := fmt.Sprintf("%.1f GiB available to Docker", float64(info.MemTotal)/gib)
	fix := fmt.Sprintf("Give Docker at least %d GiB of memory (Docker Desktop: Settings > Resources), or use a smaller profile.", need.recommended/gib)
	switch {
	case info.MemTotal < need.min:
		return Result{Status: Fail, Message: mem + fmt.Sprintf(", profile %s needs at least %d GiB", s.opts.Profile, need.min/gib), Fix: fix}
	case info.MemTotal < need.recommended:
		return Result{Status: Warn, Message: mem + fmt.Sprintf(", %d GiB recommended for profile %s", need.recommended/gib, s.opts.Profile), Fix: fix}
	}
	return Result{Status: OK, Message: mem}
}

func checkGit(_ context.Context, s *state) Result {
	path, err := s.env.LookPath("git")
	if err != nil {
		return Result{Status: Fail, Message: "git command not found in PATH; init fetches upstream with git",
			Fix: "Install Git: https://git-scm.com/downloads"}
	}
	return Result{Status: OK, Message: "found " + path}
}

func checkModelRunner(ctx context.Context, s *state) Result {
	if _, err := s.dockerCLI(); err != nil {
		return skipped("docker CLI is missing")
	}
	if err := s.modelRunner(ctx); err != nil {
		fix := "Enable it in Docker Desktop: docker desktop enable model-runner --tcp 12434"
		if s.env.GOOS == "linux" {
			fix = "Install the plugin (sudo apt-get install docker-model-plugin), or enable it in Docker Desktop: docker desktop enable model-runner --tcp 12434"
		}
		return Result{Status: Fail, Message: "'docker model' is not available: " + err.Error(), Fix: fix}
	}
	return Result{Status: OK, Message: "'docker model' works"}
}

func checkModelURL(ctx context.Context, s *state) Result {
	url := strings.TrimSuffix(s.opts.ModelURL, "/") + "/engines/v1/models"
	code, err := s.env.HTTPStatus(ctx, url)
	if err != nil {
		return Result{Status: Fail, Message: "cannot reach " + url + ": " + err.Error(),
			Fix: "Turn on host TCP access: docker desktop enable model-runner --tcp 12434"}
	}
	if code != 200 {
		return Result{Status: Fail, Message: fmt.Sprintf("%s returned HTTP %d", url, code),
			Fix: "Check that --model-url points to Docker Model Runner."}
	}
	return Result{Status: OK, Message: url + " answers"}
}

func modelCheck(name func(Options) string) func(context.Context, *state) Result {
	return func(ctx context.Context, s *state) Result {
		model := name(s.opts)
		if err := s.modelRunner(ctx); err != nil {
			return skipped("Docker Model Runner is not available")
		}
		if _, err := s.run(ctx, "docker", "model", "inspect", model); err != nil {
			fix := "docker model pull " + model
			if strings.HasPrefix(model, "local/") {
				fix = "No registry serves " + model + ". Build it with scripts/package-olmo3-local.sh from content-lake-app-deployment, or use --llm-model ai/qwen2.5."
			}
			return Result{Status: Fail, Message: model + " is not pulled", Fix: fix}
		}
		return Result{Status: OK, Message: model + " is pulled"}
	}
}

func portCheck(port int) func(context.Context, *state) Result {
	return func(ctx context.Context, s *state) Result {
		if s.env.PortFree(ctx, port) {
			return Result{Status: OK, Message: fmt.Sprintf("port %d is free", port)}
		}
		find := fmt.Sprintf("lsof -nP -iTCP:%d -sTCP:LISTEN", port)
		if s.env.GOOS == "windows" {
			find = fmt.Sprintf("netstat -ano | findstr :%d", port)
		}
		return Result{Status: Fail, Message: fmt.Sprintf("port %d is in use", port),
			Fix: fmt.Sprintf("Find the process with '%s' and stop it, or use another port.", find)}
	}
}

// checkNuxeoDeployment reports the nuxeo-deployment folder. A missing folder is
// fine: 'init' fetches it at its pinned commit.
func checkNuxeoDeployment(_ context.Context, s *state) Result {
	dir := filepath.Join(s.opts.Dir, stack.NuxeoDeploymentDir)
	if info, err := s.env.Stat(dir); err != nil || !info.IsDir() {
		return Result{Status: OK, Message: fmt.Sprintf("%s does not exist yet; 'openarch app init' fetches it at commit %s", dir, stack.NuxeoDeploymentRef)}
	}
	return Result{Status: OK, Message: "found " + dir}
}

func checkOpenArch(ctx context.Context, s *state) Result {
	const fix = "Install or update the CLI for OpenArch: https://github.com/AlfrescoLabs/openarch-cli#install (or run 'openarch self-update')"
	bin := s.opts.OpenArchBin
	if bin == "" {
		path, err := s.env.LookPath("openarch")
		if err != nil {
			return Result{Status: Fail, Message: "openarch command not found in PATH", Fix: fix}
		}
		bin = path
	}
	out, err := s.run(ctx, bin, "version", "--output", "json")
	if err != nil {
		return Result{Status: Fail, Message: "'openarch version' failed: " + err.Error(), Fix: fix}
	}
	var v struct{ Version string }
	if json.Unmarshal(out, &v) != nil {
		return Result{Status: Fail, Message: "cannot parse the output of 'openarch version'", Fix: fix}
	}
	cmp, ok := compareVersion(v.Version, minOpenArch)
	switch {
	case !ok:
		return Result{Status: Warn, Message: fmt.Sprintf("cannot tell if openarch %s is %s or later", v.Version, minOpenArch)}
	case cmp < 0:
		return Result{Status: Fail, Message: fmt.Sprintf("openarch %s is too old", v.Version), Fix: fix}
	}
	return Result{Status: OK, Message: "openarch " + v.Version}
}

// compareVersion compares the major.minor.patch part of a and b, so a
// development build such as v0.5.2-3-gabc counts as 0.5.2. It reports false
// when a has no such part.
func compareVersion(a, b string) (int, bool) {
	pa, ok := parseVersion(a)
	if !ok {
		return 0, false
	}
	pb, _ := parseVersion(b)
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func parseVersion(v string) ([3]int, bool) {
	var p [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return p, false
	}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil {
			return p, false
		}
		p[i] = n
	}
	return p, true
}

func (s *state) infoOrSkip(ctx context.Context) (dockerInfo, bool) {
	if _, err := s.dockerCLI(); err != nil {
		return dockerInfo{}, false
	}
	info, err := s.dockerInfo(ctx)
	return info, err == nil
}

func skipped(reason string) Result {
	return Result{Status: Skip, Message: "skipped: " + reason}
}

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(line)
}
