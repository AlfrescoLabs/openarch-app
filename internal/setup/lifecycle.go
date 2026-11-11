package setup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// Streams are where a long command writes its output.
type Streams struct {
	Out io.Writer
	Err io.Writer
}

// Setup is a loaded setup folder.
type Setup struct {
	Dir    string
	Config Config
}

// Open loads the setup folder dir.
func Open(dir string) (Setup, error) {
	return open(dir, Load)
}

// OpenForUpgrade loads the setup folder dir, also when its openarch-app.yaml
// has an older version.
func OpenForUpgrade(dir string) (Setup, error) {
	return open(dir, LoadForUpgrade)
}

func open(dir string, load func(string) (Config, error)) (Setup, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Setup{}, err
	}
	c, err := load(abs)
	if err != nil {
		return Setup{}, err
	}
	return Setup{Dir: abs, Config: c}, nil
}

// env returns the upstream .env values with the .env.local values on top.
func (s Setup) env() (map[string]string, error) {
	upstream, err := os.ReadFile(filepath.Join(s.Dir, UpstreamDir, ".env"))
	if err != nil {
		return nil, err
	}
	merged := ParseEnv(upstream)
	local, err := os.ReadFile(filepath.Join(s.Dir, EnvFile))
	if err != nil {
		return nil, err
	}
	for k, v := range ParseEnv(local) {
		merged[k] = v
	}
	return merged, nil
}

func (s Setup) compose(args ...string) []string {
	return append(ComposeArgs(s.Config), args...)
}

// Endpoint is one URL of a running stack.
type Endpoint struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Endpoints returns the URLs that the setup publishes.
func (s Setup) Endpoints() []Endpoint {
	host := contextHost(s.Config.BindIP)
	base := "http://" + host
	var e []Endpoint
	if s.Config.SampleUI {
		e = append(e, Endpoint{"Sample UI", base + "/"})
	}
	if s.Config.Alfresco > 0 {
		e = append(e, Endpoint{"Alfresco UI (ACA)", base + "/aca/"}, Endpoint{"Alfresco repository", base + "/alfresco/"})
	}
	if s.Config.Nuxeo > 0 {
		e = append(e, Endpoint{"Nuxeo", base + "/nuxeo/"})
	}
	e = append(e,
		Endpoint{"RAG API", base + "/api/rag/"},
		Endpoint{"MCP server", base + "/mcp"},
		Endpoint{"Hyland OpenArch (openarch --context " + s.Config.Context + ")", fmt.Sprintf("%s:%d", base, stack.OpenArchPort)})
	if s.Config.Alfresco >= 2 {
		e = append(e, Endpoint{"Second Alfresco (alfresco-2)", fmt.Sprintf("%s:%d/", base, stack.Alfresco2Port)})
	}
	return e
}

// UpOptions select how Up starts the stack.
type UpOptions struct {
	// Wait waits until every service is running or healthy.
	Wait bool
	// WaitTimeout is the wait limit in seconds.
	WaitTimeout int
}

// UpResult describes a started stack.
type UpResult struct {
	Dir string `json:"dir"`
	stack.Sources
	Built     bool       `json:"built"`
	Endpoints []Endpoint `json:"endpoints"`
	// SourceIDs are the repository ids of the two Alfresco instances of the
	// alfresco-2 extra, as rag-service uses them.
	SourceIDs map[string]string `json:"sourceIds,omitempty"`
}

// WriteText writes the human-readable form.
func (r UpResult) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "App for OpenArch is up (%s).\n\n", r.Sources)
	for _, e := range r.Endpoints {
		fmt.Fprintf(&b, "%s\t%s\n", e.Name, e.URL)
	}
	return writeTable(w, b.String())
}

// writeTable aligns the tab-separated columns of text.
func writeTable(w io.Writer, text string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := io.WriteString(tw, text); err != nil {
		return err
	}
	return tw.Flush()
}

// builtUpstreamKey records in BuiltContexts the upstream commit of the last
// build. Some images build from files in upstream/ itself (the Dockerfile,
// acs/alfresco), so a new upstream commit needs a build too.
const builtUpstreamKey = "upstream"

// NeedsBuild reports whether the images must be built: the upstream commit or
// the pinned build contexts differ from the ones of the last build.
func (s Setup) NeedsBuild() bool {
	if s.Config.BuiltContexts[builtUpstreamKey] != s.Config.Upstream {
		return true
	}
	for _, g := range stack.GitContexts {
		if s.Config.BuiltContexts[g.Var] != g.Ref {
			return true
		}
	}
	return false
}

// CheckNuxeo fails when the profile needs nuxeo-deployment and it is missing.
func (s Setup) CheckNuxeo() error {
	if s.Config.Nuxeo == 0 {
		return nil
	}
	if info, err := os.Stat(filepath.Join(s.Dir, stack.NuxeoDeploymentDir)); err == nil && info.IsDir() {
		return nil
	}
	if s.Config.ManagedNuxeo {
		dir := filepath.Join(s.Dir, stack.NuxeoDeploymentDir)
		return output.NewError(output.ExitPrerequisite, fmt.Sprintf("Nuxeo needs %s, which init fetched and is now missing", dir),
			fmt.Sprintf("Fetch it again: git clone %s %s && git -C %s checkout --detach %s", stack.NuxeoDeploymentURL, dir, dir, stack.NuxeoDeploymentRef))
	}
	return output.NewError(output.ExitPrerequisite, fmt.Sprintf("Nuxeo needs %s in %s", stack.NuxeoDeploymentDir, s.Dir),
		"Clone nuxeo-deployment there and start it, as for setups made before 0.3.0.")
}

// UsesUpstreamAlfrescoAdmin reports whether an Alfresco of the setup still
// has the upstream admin password, without the user having asked for it with
// --default-secrets. Setups made before 0.2.0 have it.
func (s Setup) UsesUpstreamAlfrescoAdmin() bool {
	if s.Config.Alfresco == 0 || s.Config.DefaultSecrets {
		return false
	}
	values, err := s.env()
	return err == nil && values["ALFRESCO_ADMIN_PASSWORD"] == UpstreamAlfrescoPassword
}

// MissingModels returns the configured models that Docker Model Runner does
// not have.
func (s Setup) MissingModels(ctx context.Context, env Env) []string {
	var missing []string
	for _, m := range []string{s.Config.EmbeddingModel, s.Config.LLMModel} {
		if _, err := env.Run(ctx, s.Dir, nil, "docker", "model", "inspect", m); err != nil {
			missing = append(missing, m)
		}
	}
	return missing
}

// Up starts the stack. Compose writes its progress to st.Err.
func Up(ctx context.Context, env Env, s Setup, opts UpOptions, st Streams) (UpResult, error) {
	res := UpResult{Dir: s.Dir, Sources: s.Config.Sources}
	if err := s.CheckNuxeo(); err != nil {
		return res, err
	}
	if err := s.CheckPins(); err != nil {
		return res, err
	}
	if err := WriteOverride(s.Dir, s.Config); err != nil {
		return res, err
	}
	args := []string{"up", "-d"}
	res.Built = s.NeedsBuild()
	if res.Built {
		args = append(args, "--build")
	}
	if s.missingSourceIDs() {
		// rag-service refuses to start with two Alfresco sources and an empty
		// id, and an id exists only once its repository has booted. So the two
		// repositories start alone first, always with a wait, and their ids are
		// recorded before the rest of the stack starts.
		first := append(slices.Clone(args), "--wait", "--wait-timeout", strconv.Itoa(opts.WaitTimeout), "alfresco", "alfresco-2")
		if err := env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", s.compose(first...)...); err != nil {
			return res, output.NewError(output.ExitUnhealthy, "the Alfresco instances did not start: "+err.Error(),
				"Run 'openarch app status' and 'openarch app logs alfresco alfresco-2' to see which one failed.")
		}
		if _, err := SyncSourceIDs(ctx, env, s, st, opts.WaitTimeout, false); err != nil {
			return res, err
		}
	}
	if opts.Wait {
		args = append(args, "--wait", "--wait-timeout", strconv.Itoa(opts.WaitTimeout))
	}
	if err := env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", s.compose(args...)...); err != nil {
		return res, output.NewError(output.ExitUnhealthy, "the stack did not start: "+err.Error(),
			"Run 'openarch app status' and 'openarch app logs <service>' to see which service failed.")
	}
	if res.Built {
		s.Config.BuiltContexts = map[string]string{builtUpstreamKey: s.Config.Upstream}
		for _, g := range stack.GitContexts {
			s.Config.BuiltContexts[g.Var] = g.Ref
		}
		if err := Save(s.Dir, s.Config); err != nil {
			return res, err
		}
	}
	// The repository ids exist only once Alfresco has started, so they need the
	// wait. Without it the next 'up' sets them.
	if opts.Wait {
		ids, err := SyncSourceIDs(ctx, env, s, st, opts.WaitTimeout, true)
		if err != nil {
			return res, err
		}
		res.SourceIDs = ids
	}
	res.Endpoints = s.Endpoints()
	return res, nil
}

// Down stops the stack and keeps the volumes.
func Down(ctx context.Context, env Env, s Setup, st Streams) error {
	return env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", s.compose("down")...)
}

// Service is the state of one Compose service.
type Service struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	Health   string `json:"health,omitempty"`
	ExitCode int    `json:"exitCode"`
	Ports    string `json:"ports,omitempty"`
}

// OK reports whether the service is running and not unhealthy, or finished
// with exit code 0 (a one-shot job).
func (v Service) OK() bool {
	switch v.State {
	case "running":
		return v.Health == "" || v.Health == "healthy"
	case "exited":
		return v.ExitCode == 0
	}
	return false
}

// StatusResult lists the services of the stack.
type StatusResult struct {
	Dir string `json:"dir"`
	stack.Sources
	Services []Service `json:"services"`
	Healthy  bool      `json:"healthy"`
}

// WriteText writes the human-readable form.
func (r StatusResult) WriteText(w io.Writer) error {
	if len(r.Services) == 0 {
		_, err := fmt.Fprintf(w, "The stack in %s is not running. Start it with 'openarch app up'.\n", r.Dir)
		return err
	}
	var b strings.Builder
	b.WriteString("SERVICE\tSTATE\tHEALTH\tPORTS\n")
	for _, v := range r.Services {
		state := v.State
		if v.State == "exited" {
			state = fmt.Sprintf("exited (%d)", v.ExitCode)
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", v.Service, state, v.Health, v.Ports)
	}
	return writeTable(w, b.String())
}

// Status lists the services of the stack.
func Status(ctx context.Context, env Env, s Setup) (StatusResult, error) {
	res := StatusResult{Dir: s.Dir, Sources: s.Config.Sources, Services: []Service{}}
	out, err := env.Run(ctx, s.Dir, nil, "docker", s.compose("ps", "-a", "--format", "json")...)
	if err != nil {
		return res, err
	}
	res.Services, err = parsePS(out)
	if err != nil {
		return res, err
	}
	res.Healthy = len(res.Services) > 0
	for _, v := range res.Services {
		res.Healthy = res.Healthy && v.OK()
	}
	return res, nil
}

// parsePS reads 'docker compose ps --format json': one JSON object per line
// in current Compose versions, one JSON array in older ones.
func parsePS(out []byte) ([]Service, error) {
	type row struct {
		Service    string
		State      string
		Health     string
		ExitCode   int
		Publishers []struct {
			URL           string
			PublishedPort int
			TargetPort    int
		}
	}
	var rows []row
	trimmed := bytes.TrimSpace(out)
	if bytes.HasPrefix(trimmed, []byte("[")) {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("cannot parse 'docker compose ps': %w", err)
		}
	} else {
		sc := bufio.NewScanner(bytes.NewReader(trimmed))
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}
			var r row
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				return nil, fmt.Errorf("cannot parse 'docker compose ps': %w", err)
			}
			rows = append(rows, r)
		}
	}
	services := make([]Service, 0, len(rows))
	for _, r := range rows {
		var ports []string
		for _, p := range r.Publishers {
			if p.PublishedPort != 0 {
				ports = append(ports, fmt.Sprintf("%s:%d->%d", p.URL, p.PublishedPort, p.TargetPort))
			}
		}
		slices.Sort(ports)
		services = append(services, Service{Service: r.Service, State: r.State, Health: r.Health, ExitCode: r.ExitCode, Ports: strings.Join(slices.Compact(ports), ", ")})
	}
	slices.SortFunc(services, func(a, b Service) int { return strings.Compare(a.Service, b.Service) })
	return services, nil
}

// LogsOptions select the logs.
type LogsOptions struct {
	Services []string
	Follow   bool
	// Tail is the number of lines per service; 0 means all.
	Tail int
}

func (o LogsOptions) args() []string {
	args := []string{"logs"}
	if o.Follow {
		args = append(args, "--follow")
	}
	if o.Tail > 0 {
		args = append(args, "--tail", strconv.Itoa(o.Tail))
	}
	return append(args, o.Services...)
}

// StreamLogs copies the logs to st.Out.
func StreamLogs(ctx context.Context, env Env, s Setup, o LogsOptions, st Streams) error {
	return env.Stream(ctx, s.Dir, nil, st.Out, st.Err, "docker", s.compose(o.args()...)...)
}

// LogsResult holds the logs for JSON output.
type LogsResult struct {
	Logs string `json:"logs"`
}

// Logs returns the logs. It does not follow them.
func Logs(ctx context.Context, env Env, s Setup, o LogsOptions) (LogsResult, error) {
	o.Follow = false
	out, err := env.Run(ctx, s.Dir, nil, "docker", s.compose(o.args()...)...)
	return LogsResult{Logs: string(out)}, err
}

// ModelResult is the outcome for one model.
type ModelResult struct {
	Model  string `json:"model"`
	Action string `json:"action"`
}

// ModelsResult is the outcome of ModelsPull.
type ModelsResult struct {
	Models []ModelResult `json:"models"`
}

// WriteText writes the human-readable form.
func (r ModelsResult) WriteText(w io.Writer) error {
	for _, m := range r.Models {
		if _, err := fmt.Fprintf(w, "%s: %s\n", m.Model, m.Action); err != nil {
			return err
		}
	}
	return nil
}

// ModelsPull makes the embedding and chat models of the setup available in
// Docker Model Runner. An ai/ model is pulled. The upstream default chat model
// is a local package that no registry serves, so the upstream script builds
// it. Any other model is left alone: something else serves it.
func ModelsPull(ctx context.Context, env Env, s Setup, st Streams) (ModelsResult, error) {
	var res ModelsResult
	for _, m := range []string{s.Config.EmbeddingModel, s.Config.LLMModel} {
		if slices.ContainsFunc(res.Models, func(r ModelResult) bool { return r.Model == m }) {
			continue
		}
		var err error
		switch {
		case m == stack.DefaultLLMModel:
			if _, lerr := env.LookPath("bash"); lerr != nil {
				return res, output.NewError(output.ExitPrerequisite, "bash not found in PATH; it builds "+m, "Install bash (on Windows: Git Bash), or use --llm-model ai/qwen2.5 at init.")
			}
			err = env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "bash", UpstreamDir+"/scripts/package-olmo3-local.sh")
			res.Models = append(res.Models, ModelResult{m, "ready (upstream scripts/package-olmo3-local.sh)"})
		case strings.HasPrefix(m, "ai/"):
			err = env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", "model", "pull", m)
			res.Models = append(res.Models, ModelResult{m, "ready (docker model pull)"})
		default:
			res.Models = append(res.Models, ModelResult{m, "skipped: not an ai/ model; it must be served by something else"})
		}
		if err != nil {
			return res, output.NewError(output.ExitPrerequisite, fmt.Sprintf("cannot get %s: %v", m, err), "Run 'openarch app doctor' to check Docker Model Runner.")
		}
	}
	return res, nil
}

// SmokeResult is the outcome of SmokeTest.
type SmokeResult struct {
	Script  string `json:"script"`
	Passed  int    `json:"passed"`
	Failed  int    `json:"failed"`
	Skipped int    `json:"skipped"`
	OK      bool   `json:"ok"`
}

// WriteText writes the human-readable form.
func (r SmokeResult) WriteText(w io.Writer) error {
	verdict := "passed"
	if !r.OK {
		verdict = "FAILED"
	}
	_, err := fmt.Fprintf(w, "\nSmoke test %s (%s): %d passed, %d failed, %d skipped.\n", verdict, r.Script, r.Passed, r.Failed, r.Skipped)
	return err
}

// smokeScripts are the upstream suites for each profile, the ones that the
// upstream test/run-tests.sh runs. test-rag-full.sh covers Alfresco and Nuxeo
// together. (test/smoke-test.sh is a different script, for a demo host: it
// wants the demo UI at /, which the full profile does not serve.)
var smokeScripts = map[string]string{
	"alfresco": "test/test-alfresco.sh",
	"nuxeo":    "test/test-nuxeo.sh",
	"full":     "test/test-rag-full.sh",
}

// SmokeTest runs the upstream test suite for the profile against the running
// stack. The suite writes its output to out. The suites take the Alfresco and
// Nuxeo logins as environment variables.
func SmokeTest(ctx context.Context, env Env, s Setup, out io.Writer, st Streams) (SmokeResult, error) {
	scripts := []string{smokeScripts[s.Config.Profile()]}
	if s.Config.Alfresco >= 2 {
		// It checks what the profile suite cannot: two sources, their ids and
		// the permissions of each.
		scripts = append(scripts, "test/test-two-alfresco.sh")
	}
	res := SmokeResult{Script: strings.Join(scripts, " + ")}
	if err := s.CheckPins(); err != nil {
		return res, err
	}
	for _, tool := range []string{"bash", "curl", "jq"} {
		if _, err := env.LookPath(tool); err != nil {
			return res, output.NewError(output.ExitPrerequisite, tool+" not found in PATH; the upstream test suite needs bash, curl and jq", "")
		}
	}
	values, err := s.env()
	if err != nil {
		return res, err
	}
	login := func(user, password, defUser, defPassword string) string {
		if values[user] != "" {
			defUser = values[user]
		}
		if values[password] != "" {
			defPassword = values[password]
		}
		return defUser + ":" + defPassword
	}
	extra := []string{
		// The suites stop and start services with 'docker compose' from
		// upstream/, which would use the upstream project name.
		"COMPOSE_PROJECT_NAME=" + stack.ProjectName,
		"HOST=" + contextHost(s.Config.BindIP),
		"USE_HTTPS=false",
		"ALF_AUTH=" + login("ALFRESCO_ADMIN_USER", "ALFRESCO_ADMIN_PASSWORD", "admin", "admin"),
		"ALF2_AUTH=" + login("ALFRESCO_2_INTERNAL_USERNAME", "ALFRESCO_2_INTERNAL_PASSWORD", "admin", "admin"),
		"NUXEO_AUTH=" + login("NUXEO_USERNAME", "NUXEO_PASSWORD", "Administrator", "Administrator"),
	}
	res.OK = true
	for _, script := range scripts {
		var buf bytes.Buffer
		// The suites run from the setup folder, so a log they leave behind never
		// lands in upstream/.
		runErr := env.Stream(ctx, s.Dir, extra, io.MultiWriter(out, &buf), st.Err, "bash", UpstreamDir+"/"+script)
		text := buf.String()
		res.Passed += strings.Count(text, "[PASS]")
		res.Failed += strings.Count(text, "[FAIL]")
		res.Skipped += strings.Count(text, "[SKIP]")
		res.OK = res.OK && runErr == nil && strings.Count(text, "[FAIL]") == 0
	}
	return res, nil
}
