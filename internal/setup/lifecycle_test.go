package setup

import (
	"bytes"
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

// fakeDocker records commands and plays docker, bash and docker model.
type fakeDocker struct {
	calls      []string
	extraEnv   []string
	psOut      string
	streamErr  error
	scriptOut  string
	hasModels  bool
	missingBin string
	// repoIDs maps an Alfresco service to the repository id that its discovery
	// answers; a service that is not in it fails like curl -f on a 401.
	repoIDs map[string]string
	// gets records each discovery as "service login" from the curl config on stdin.
	gets []string
}

func (f *fakeDocker) env() Env {
	return Env{
		LookPath: func(file string) (string, error) {
			if file == f.missingBin {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + file, nil
		},
		Run: func(_ context.Context, _ string, stdin io.Reader, name string, args ...string) ([]byte, error) {
			call := name + " " + strings.Join(args, " ")
			f.calls = append(f.calls, call)
			switch {
			case strings.Contains(call, "model inspect"):
				if !f.hasModels {
					return nil, errors.New("not found")
				}
				return nil, nil
			case strings.Contains(call, " ps -a --format json"):
				return []byte(f.psOut), nil
			case strings.Contains(call, " exec -T "):
				service := args[slices.Index(args, "-T")+1]
				login, _ := io.ReadAll(stdin)
				f.gets = append(f.gets, service+" "+strings.TrimSpace(string(login)))
				id, ok := f.repoIDs[service]
				if !ok {
					return nil, errors.New("exit status 22")
				}
				return []byte(`{"entry":{"repository":{"id":"` + id + `","edition":"Community"}}}`), nil
			case strings.Contains(call, " logs"):
				return []byte("rag-service | started\n"), nil
			}
			return nil, errors.New("unexpected " + call)
		},
		Stream: func(_ context.Context, _ string, extraEnv []string, stdout, _ io.Writer, name string, args ...string) error {
			f.calls = append(f.calls, name+" "+strings.Join(args, " "))
			f.extraEnv = extraEnv
			if name == "bash" && f.scriptOut != "" {
				_, _ = io.WriteString(stdout, f.scriptOut)
			}
			return f.streamErr
		},
	}
}

// srcOf returns the sources of an upstream profile and extras, the shape of
// most test cases.
func srcOf(profile string, extras ...string) stack.Sources {
	s, err := stack.SourcesOf(profile, extras)
	if err != nil {
		panic(err)
	}
	return s
}

func newSetup(t *testing.T, c Config) Setup {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, UpstreamDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, UpstreamDir, ".env"), []byte("ALFRESCO_ADMIN_USER=admin\nALFRESCO_ADMIN_PASSWORD=admin\nNUXEO_USERNAME=Administrator\nNUXEO_PASSWORD=Administrator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := "BIND_IP=127.0.0.1\n"
	for _, g := range stack.GitContexts {
		local += g.Var + "=" + g.URL() + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, EnvFile), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	c.SchemaVersion = SchemaVersion
	if c.Upstream == "" {
		c.Upstream = stack.UpstreamRef
	}
	if c.Sources == (stack.Sources{}) {
		c.Sources = stack.Sources{Alfresco: 1}
	}
	if c.BindIP == "" {
		c.BindIP = "127.0.0.1"
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUpBuildsOnceAndRecords(t *testing.T) {
	s := newSetup(t, Config{Context: "app"})
	f := &fakeDocker{}
	var errOut bytes.Buffer
	res, err := Up(context.Background(), f.env(), s, UpOptions{Wait: true, WaitTimeout: 60}, Streams{Err: &errOut})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Built || !strings.HasSuffix(f.calls[0], "up -d --build --wait --wait-timeout 60") {
		t.Errorf("first up: built=%v call=%q", res.Built, f.calls[0])
	}
	// up writes the override, so a setup from an older build gets the volume.
	if b, err := os.ReadFile(filepath.Join(s.Dir, OverrideFile)); err != nil || !strings.Contains(string(b), "hxpr-data") {
		t.Errorf("override not written: %v", err)
	}
	s, err = Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.NeedsBuild() {
		t.Errorf("the build was not recorded: %v", s.Config.BuiltContexts)
	}
	// A build recorded before the upstream commit was part of it (0.1.x), or
	// for another upstream commit, needs a new build.
	for _, up := range []string{"", "1111111111111111111111111111111111111111"} {
		old := s
		old.Config.BuiltContexts = map[string]string{}
		for k, v := range s.Config.BuiltContexts {
			old.Config.BuiltContexts[k] = v
		}
		old.Config.BuiltContexts[builtUpstreamKey] = up
		if !old.NeedsBuild() {
			t.Errorf("built upstream %q: no build", up)
		}
	}
	f.calls = nil
	res, err = Up(context.Background(), f.env(), s, UpOptions{}, Streams{Err: &errOut})
	if err != nil || res.Built || !strings.HasSuffix(f.calls[0], "up -d") {
		t.Errorf("second up: built=%v call=%q err=%v", res.Built, f.calls[0], err)
	}
	if len(res.Endpoints) == 0 || res.Endpoints[0].URL != "http://127.0.0.1/aca/" {
		t.Errorf("endpoints = %+v", res.Endpoints)
	}
}

func TestUpFails(t *testing.T) {
	s := newSetup(t, Config{})
	f := &fakeDocker{streamErr: errors.New("exit status 1")}
	_, err := Up(context.Background(), f.env(), s, UpOptions{}, Streams{Err: io.Discard})
	if output.CodeOf(err) != output.ExitUnhealthy {
		t.Fatalf("err = %v", err)
	}
	if s, _ := Open(s.Dir); !s.NeedsBuild() {
		t.Error("a failed up recorded the build")
	}
}

func TestUpNeedsNuxeo(t *testing.T) {
	s := newSetup(t, Config{Sources: srcOf("full")})
	f := &fakeDocker{}
	if _, err := Up(context.Background(), f.env(), s, UpOptions{}, Streams{Err: io.Discard}); output.CodeOf(err) != output.ExitPrerequisite {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("compose ran: %v", f.calls)
	}
	if err := os.Mkdir(filepath.Join(s.Dir, stack.NuxeoDeploymentDir), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Up(context.Background(), f.env(), s, UpOptions{}, Streams{Err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range res.Endpoints {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "Nuxeo") || !strings.Contains(got, "Alfresco UI") {
		t.Errorf("endpoints = %s", got)
	}
}

func TestEndpointsAlfresco2(t *testing.T) {
	s := newSetup(t, Config{Sources: srcOf("alfresco", "alfresco-2"), BindIP: "0.0.0.0", Context: "app"})
	last := s.Endpoints()[len(s.Endpoints())-1]
	if last.URL != "http://127.0.0.1:8082/" {
		t.Errorf("last endpoint = %+v", last)
	}
}

func TestEndpointsSampleUI(t *testing.T) {
	s := newSetup(t, Config{Sources: srcOf("full"), BindIP: "127.0.0.1", Context: "app", SampleUI: true})
	if first := s.Endpoints()[0]; first != (Endpoint{"Sample UI", "http://127.0.0.1/"}) {
		t.Errorf("first endpoint = %+v", first)
	}
	s.Config.SampleUI = false
	if first := s.Endpoints()[0]; first.Name == "Sample UI" {
		t.Errorf("sample UI endpoint without the sample UI")
	}
}

func TestParsePS(t *testing.T) {
	lines := `{"Service":"rag-service","State":"running","Health":"healthy","ExitCode":0,"Publishers":[]}
{"Service":"hxpr-app","State":"running","Health":"starting","ExitCode":0,"Publishers":[{"URL":"127.0.0.1","PublishedPort":8090,"TargetPort":8080},{"URL":"127.0.0.1","PublishedPort":8090,"TargetPort":8080}]}
{"Service":"init-job","State":"exited","Health":"","ExitCode":0}
`
	got, err := parsePS([]byte(lines))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Service != "hxpr-app" || got[0].Ports != "127.0.0.1:8090->8080" {
		t.Fatalf("services = %+v", got)
	}
	if got[0].OK() || !got[1].OK() || !got[2].OK() {
		t.Errorf("OK = %v %v %v", got[0].OK(), got[1].OK(), got[2].OK())
	}
	array := `[{"Service":"a","State":"exited","ExitCode":1}]`
	got, err = parsePS([]byte(array))
	if err != nil || len(got) != 1 || got[0].OK() {
		t.Errorf("array = %+v, %v", got, err)
	}
	if got, err := parsePS(nil); err != nil || len(got) != 0 {
		t.Errorf("empty = %+v, %v", got, err)
	}
}

func TestStatus(t *testing.T) {
	s := newSetup(t, Config{})
	f := &fakeDocker{psOut: `{"Service":"proxy","State":"running"}` + "\n"}
	res, err := Status(context.Background(), f.env(), s)
	if err != nil || !res.Healthy || len(res.Services) != 1 {
		t.Errorf("res = %+v, %v", res, err)
	}
	f.psOut = ""
	if res, _ := Status(context.Background(), f.env(), s); res.Healthy {
		t.Error("an empty stack is healthy")
	}
}

func TestLogs(t *testing.T) {
	s := newSetup(t, Config{})
	f := &fakeDocker{}
	res, err := Logs(context.Background(), f.env(), s, LogsOptions{Services: []string{"rag-service"}, Follow: true, Tail: 5})
	if err != nil || !strings.Contains(res.Logs, "started") {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if !strings.HasSuffix(f.calls[0], "logs --tail 5 rag-service") {
		t.Errorf("call = %q", f.calls[0])
	}
}

func TestModelsPull(t *testing.T) {
	s := newSetup(t, Config{EmbeddingModel: "ai/mxbai-embed-large", LLMModel: stack.DefaultLLMModel})
	f := &fakeDocker{}
	res, err := ModelsPull(context.Background(), f.env(), s, Streams{Err: io.Discard})
	if err != nil || len(res.Models) != 2 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if f.calls[0] != "docker model pull ai/mxbai-embed-large" || f.calls[1] != "bash upstream/scripts/package-olmo3-local.sh" {
		t.Errorf("calls = %q", f.calls)
	}

	s = newSetup(t, Config{EmbeddingModel: "ai/x", LLMModel: "vllm/served-model"})
	f = &fakeDocker{}
	res, err = ModelsPull(context.Background(), f.env(), s, Streams{Err: io.Discard})
	if err != nil || len(f.calls) != 1 || !strings.HasPrefix(res.Models[1].Action, "skipped") {
		t.Errorf("res = %+v, calls %q, %v", res, f.calls, err)
	}

	s = newSetup(t, Config{EmbeddingModel: "ai/x", LLMModel: stack.DefaultLLMModel})
	f = &fakeDocker{missingBin: "bash"}
	if _, err := ModelsPull(context.Background(), f.env(), s, Streams{Err: io.Discard}); output.CodeOf(err) != output.ExitPrerequisite {
		t.Errorf("no bash: %v", err)
	}
}

func TestSmokeTest(t *testing.T) {
	s := newSetup(t, Config{Sources: srcOf("full")})
	f := &fakeDocker{scriptOut: "\x1b[0;32m[PASS]\x1b[0m a\n[PASS] b\n[SKIP] c\n"}
	var out bytes.Buffer
	res, err := SmokeTest(context.Background(), f.env(), s, &out, Streams{Err: io.Discard})
	if err != nil || !res.OK || res.Passed != 2 || res.Skipped != 1 || res.Script != "test/test-rag-full.sh" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if f.calls[0] != "bash upstream/test/test-rag-full.sh" || !strings.Contains(out.String(), "[PASS] b") {
		t.Errorf("call %q, out %q", f.calls[0], out.String())
	}
	env := strings.Join(f.extraEnv, " ")
	for _, want := range []string{"COMPOSE_PROJECT_NAME=openarch-app", "HOST=127.0.0.1", "USE_HTTPS=false", "ALF_AUTH=admin:admin", "ALF2_AUTH=admin:admin", "NUXEO_AUTH=Administrator:Administrator"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %s in %s", want, env)
		}
	}

	f = &fakeDocker{scriptOut: "[PASS] a\n[FAIL] b\n", streamErr: errors.New("exit status 1")}
	if res, err := SmokeTest(context.Background(), f.env(), newSetup(t, Config{}), io.Discard, Streams{Err: io.Discard}); err != nil || res.OK || res.Failed != 1 || res.Script != "test/test-alfresco.sh" {
		t.Errorf("failing suite: %+v, %v", res, err)
	}

	f = &fakeDocker{missingBin: "jq"}
	if _, err := SmokeTest(context.Background(), f.env(), newSetup(t, Config{}), io.Discard, Streams{Err: io.Discard}); output.CodeOf(err) != output.ExitPrerequisite {
		t.Errorf("no jq: %v", err)
	}
}

func TestUsesUpstreamAlfrescoAdmin(t *testing.T) {
	// newSetup gives .env.local no Alfresco password, so the upstream admin applies.
	if !newSetup(t, Config{}).UsesUpstreamAlfrescoAdmin() {
		t.Error("a setup made before 0.2.0 is not reported")
	}
	for _, c := range []Config{{DefaultSecrets: true}, {Sources: srcOf("nuxeo")}} {
		if newSetup(t, c).UsesUpstreamAlfrescoAdmin() {
			t.Errorf("%+v is reported", c)
		}
	}
	s := newSetup(t, Config{})
	local := string(mustRead(t, filepath.Join(s.Dir, EnvFile))) + "ALFRESCO_ADMIN_PASSWORD=Random-Pw-1\n"
	if err := os.WriteFile(filepath.Join(s.Dir, EnvFile), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	if s.UsesUpstreamAlfrescoAdmin() {
		t.Error("a random password is reported")
	}
}

func TestMissingModels(t *testing.T) {
	s := newSetup(t, Config{EmbeddingModel: "ai/e", LLMModel: "ai/c"})
	if got := s.MissingModels(context.Background(), (&fakeDocker{}).env()); len(got) != 2 {
		t.Errorf("missing = %v", got)
	}
	if got := s.MissingModels(context.Background(), (&fakeDocker{hasModels: true}).env()); len(got) != 0 {
		t.Errorf("missing = %v", got)
	}
}
