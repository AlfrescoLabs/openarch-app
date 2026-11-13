package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// fake is a healthy system. Tests change one field to break one thing.
type fake struct {
	goos        string
	noCLI       bool
	noOpenArch  bool
	noGit       bool
	infoJSON    string
	infoErr     string
	composeOut  string
	composeErr  string
	noRunner    bool
	models      map[string]bool
	busyPorts   map[int]bool
	modelStatus int
	modelErr    bool
	dirs        map[string]bool
	openarchOut string
	calls       []string
	// served are the models that a --model-url server lists, and dims the size
	// of its embeddings. serverDown makes it unreachable.
	served     []string
	dims       int
	serverDown bool
}

func newFake() *fake {
	return &fake{
		goos:        "darwin",
		infoJSON:    info(16 * gib),
		composeOut:  "v2.29.1\n",
		models:      map[string]bool{"ai/mxbai-embed-large": true, "ai/qwen2.5": true},
		modelStatus: 200,
		dirs:        map[string]bool{},
		openarchOut: `{"version": "0.5.2"}`,
		served:      []string{"mxbai-embed-large:latest", "qwen2.5:latest"},
		dims:        1024,
	}
}

func info(mem int64) string {
	return fmt.Sprintf(`{"ServerVersion":"29.0.0","OperatingSystem":"Docker Desktop","MemTotal":%d}`, mem)
}

func opts() Options {
	return Options{
		Sources:        stack.Sources{Alfresco: 1},
		LLMModel:       "ai/qwen2.5",
		EmbeddingModel: "ai/mxbai-embed-large",
		ModelURL:       "http://localhost:12434",
		Ports:          []int{80, 8090},
		Dir:            "setup",
	}
}

type dirInfo struct{ fs.FileInfo }

func (dirInfo) IsDir() bool { return true }

func (f *fake) env() Env {
	return Env{
		GOOS: f.goos,
		LookPath: func(file string) (string, error) {
			if (file == "docker" && f.noCLI) || (file == "openarch" && f.noOpenArch) || (file == "git" && f.noGit) {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + file, nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
			f.calls = append(f.calls, name+" "+strings.Join(args, " "))
			if name != "docker" {
				return []byte(f.openarchOut), nil, nil
			}
			switch args[0] {
			case "info":
				if f.infoErr != "" {
					return nil, []byte(f.infoErr + "\n"), errors.New("exit status 1")
				}
				return []byte(f.infoJSON), nil, nil
			case "compose":
				if f.composeErr != "" {
					return nil, []byte(f.composeErr), errors.New("exit status 1")
				}
				return []byte(f.composeOut), nil, nil
			case "model":
				if f.noRunner {
					return nil, []byte("docker: 'model' is not a docker command.\n"), errors.New("exit status 1")
				}
				if args[1] == "inspect" && !f.models[args[2]] {
					return nil, []byte("model not found\n"), errors.New("exit status 1")
				}
				return nil, nil, nil
			}
			return nil, nil, fmt.Errorf("unexpected command %v", args)
		},
		PortFree: func(_ context.Context, p int) bool { return !f.busyPorts[p] },
		HTTPStatus: func(context.Context, string) (int, error) {
			if f.modelErr {
				return 0, errors.New("dial tcp 127.0.0.1:12434: connect: connection refused")
			}
			return f.modelStatus, nil
		},
		HTTPJSON: func(_ context.Context, url string, body []byte) (int, []byte, error) {
			f.calls = append(f.calls, "http "+url)
			if f.serverDown {
				return 0, nil, errors.New("connection refused")
			}
			if strings.HasSuffix(url, "/v1/models") {
				var data []map[string]string
				for _, m := range f.served {
					data = append(data, map[string]string{"id": m})
				}
				b, _ := json.Marshal(map[string]any{"data": data})
				return 200, b, nil
			}
			var req struct{ Model string }
			_ = json.Unmarshal(body, &req)
			if !slices.ContainsFunc(f.served, func(m string) bool { return stack.SameModel(m, req.Model) }) {
				return 404, []byte(`{"error":"model not found"}`), nil
			}
			b, _ := json.Marshal(map[string]any{"data": []map[string]any{{"embedding": make([]float64, f.dims)}}})
			return 200, b, nil
		},
		Stat: func(name string) (os.FileInfo, error) {
			if f.dirs[name] {
				return dirInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
	}
}

func run(t *testing.T, f *fake, o Options) Report {
	t.Helper()
	r, err := Run(context.Background(), f.env(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func status(r Report) map[string]Status {
	m := map[string]Status{}
	for _, c := range r.Checks {
		m[c.ID] = c.Status
	}
	return m
}

func find(t *testing.T, r Report, id string) Result {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %s not in report", id)
	return Result{}
}

func TestHealthySystem(t *testing.T) {
	f := newFake()
	r := run(t, f, opts())
	if r.Failed() || r.Summary.Warn != 0 || r.Summary.Skip != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
	want := []string{"docker.cli", "docker.daemon", "docker.compose", "docker.memory", "git.cli", "model.runner", "model.url", "model.embedding", "model.chat", "port.80", "port.8090", "openarch.cli"}
	if got := IDs(f.env(), opts()); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("IDs = %v, want %v", got, want)
	}
	// docker info and docker model version run once each and are shared.
	for _, cmd := range []string{"docker info --format {{json .}}", "docker model version"} {
		n := 0
		for _, c := range f.calls {
			if c == cmd {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%q ran %d times, want 1", cmd, n)
		}
	}
}

func TestNoDockerCLI(t *testing.T) {
	f := newFake()
	f.noCLI = true
	s := status(run(t, f, opts()))
	if s["docker.cli"] != Fail {
		t.Errorf("docker.cli = %s", s["docker.cli"])
	}
	for _, id := range []string{"docker.daemon", "docker.compose", "docker.memory", "model.runner", "model.embedding", "model.chat"} {
		if s[id] != Skip {
			t.Errorf("%s = %s, want skip", id, s[id])
		}
	}
}

func TestDaemonDown(t *testing.T) {
	f := newFake()
	f.infoErr = "failed to connect to the docker API at unix:///var/run/docker.sock"
	r := run(t, f, opts())
	d := find(t, r, "docker.daemon")
	if d.Status != Fail || !strings.Contains(d.Message, "failed to connect") || d.Fix == "" {
		t.Errorf("daemon = %+v", d)
	}
	if s := status(r); s["docker.memory"] != Skip || s["docker.compose"] != OK {
		t.Errorf("statuses = %v", s)
	}
}

func TestComposeVersion(t *testing.T) {
	cases := []struct {
		out, errOut string
		want        Status
	}{
		{"v2.29.1", "", OK},
		{"5.7.0", "", OK},
		{"1.29.2", "", Fail},
		{"garbage", "", Warn},
		{"", "docker: 'compose' is not a docker command.", Fail},
	}
	for _, c := range cases {
		f := newFake()
		f.composeOut, f.composeErr = c.out, c.errOut
		if got := find(t, run(t, f, opts()), "docker.compose"); got.Status != c.want {
			t.Errorf("compose %q/%q = %+v, want %s", c.out, c.errOut, got, c.want)
		}
	}
}

func TestMemoryByProfile(t *testing.T) {
	cases := []struct {
		profile    string
		extras     []string
		filesystem bool
		mem        int64
		want       Status
	}{
		{"alfresco", nil, false, 12 * gib, OK},
		{"alfresco", nil, false, 10 * gib, Warn},
		{"alfresco", nil, false, 6 * gib, Fail},
		{"nuxeo", nil, false, 8 * gib, OK},
		{"full", nil, false, 12 * gib, Warn},
		{"full", nil, false, 8 * gib, Fail},
		{"alfresco", []string{"alfresco-2"}, false, 18 * gib, Warn},
		{"alfresco", []string{"alfresco-2"}, false, 14 * gib, Fail},
		{"alfresco", []string{"alfresco-2"}, false, 22 * gib, OK},
		{"alfresco", nil, true, 12 * gib, Warn},
		{"alfresco", nil, true, 14 * gib, OK},
	}
	for _, c := range cases {
		f := newFake()
		f.infoJSON = info(c.mem)
		o := opts()
		o.Connector = c.filesystem
		var err error
		if o.Sources, err = stack.SourcesOf(c.profile, c.extras); err != nil {
			t.Fatal(err)
		}
		got := find(t, run(t, f, o), "docker.memory")
		if got.Status != c.want {
			t.Errorf("%s with %d GiB = %+v, want %s", c.profile, c.mem/gib, got, c.want)
		}
		if c.want != OK && !strings.Contains(got.Fix, "Settings > Resources") {
			t.Errorf("%s: fix = %q", c.profile, got.Fix)
		}
	}
}

func TestNoGit(t *testing.T) {
	f := newFake()
	f.noGit = true
	if c := find(t, run(t, f, opts()), "git.cli"); c.Status != Fail || !strings.Contains(c.Fix, "git-scm.com") {
		t.Errorf("git = %+v", c)
	}
}

func TestNoModelRunner(t *testing.T) {
	f := newFake()
	f.noRunner = true
	r := run(t, f, opts())
	c := find(t, r, "model.runner")
	if c.Status != Fail || !strings.Contains(c.Fix, "docker desktop enable model-runner") {
		t.Errorf("runner = %+v", c)
	}
	if s := status(r); s["model.embedding"] != Skip || s["model.chat"] != Skip {
		t.Errorf("statuses = %v", s)
	}
	f.goos = "linux"
	if c := find(t, run(t, f, opts()), "model.runner"); !strings.Contains(c.Fix, "docker-model-plugin") {
		t.Errorf("linux fix = %q", c.Fix)
	}
}

func TestModelURL(t *testing.T) {
	f := newFake()
	f.modelErr = true
	if c := find(t, run(t, f, opts()), "model.url"); c.Status != Fail || !strings.Contains(c.Fix, "--tcp 12434") {
		t.Errorf("refused = %+v", c)
	}
	f.modelErr, f.modelStatus = false, 404
	if c := find(t, run(t, f, opts()), "model.url"); c.Status != Fail || !strings.Contains(c.Message, "HTTP 404") {
		t.Errorf("404 = %+v", c)
	}
}

func TestModelMissing(t *testing.T) {
	f := newFake()
	o := opts()
	o.EmbeddingModel = "ai/other-embed"
	o.LLMModel = "local/olmo3-7b-instruct:Q4_K_M"
	r := run(t, f, o)
	if c := find(t, r, "model.embedding"); c.Status != Fail || c.Fix != "docker model pull ai/other-embed" {
		t.Errorf("embedding = %+v", c)
	}
	if c := find(t, r, "model.chat"); c.Status != Fail || !strings.Contains(c.Fix, "package-olmo3-local.sh") {
		t.Errorf("local chat model = %+v", c)
	}
}

func TestPortInUse(t *testing.T) {
	f := newFake()
	f.busyPorts = map[int]bool{80: true}
	c := find(t, run(t, f, opts()), "port.80")
	if c.Status != Fail || !strings.Contains(c.Fix, "lsof") {
		t.Errorf("unix: %+v", c)
	}
	f.goos = "windows"
	if c := find(t, run(t, f, opts()), "port.80"); !strings.Contains(c.Fix, "netstat") {
		t.Errorf("windows: %+v", c)
	}
}

func TestNuxeoDeployment(t *testing.T) {
	f := newFake()
	if _, ok := status(run(t, f, opts()))["nuxeo.deployment"]; ok {
		t.Error("nuxeo.deployment must not run for the alfresco profile")
	}
	o := opts()
	o.Sources = stack.Sources{Alfresco: 1, Nuxeo: 1}
	// A missing folder is fine: init fetches it.
	c := find(t, run(t, f, o), "nuxeo.deployment")
	if c.Status != OK || !strings.Contains(c.Message, "'openarch app init' fetches it at commit 0a5569054e6b") {
		t.Errorf("missing: %+v", c)
	}
	f.dirs["setup/nuxeo-deployment"] = true
	f.dirs[`setup\nuxeo-deployment`] = true
	if c := find(t, run(t, f, o), "nuxeo.deployment"); c.Status != OK {
		t.Errorf("present: %+v", c)
	}
}

func TestOpenArch(t *testing.T) {
	cases := []struct {
		out  string
		want Status
	}{
		{`{"version": "0.5.2"}`, OK},
		{`{"version": "0.6.0"}`, OK},
		{`{"version": "v0.5.2-3-gabc1234-dirty"}`, OK},
		{`{"version": "0.5.1"}`, Fail},
		{`{"version": "0.2.0"}`, Fail},
		{`{"version": "dev"}`, Warn},
		{`not json`, Fail},
	}
	for _, c := range cases {
		f := newFake()
		f.openarchOut = c.out
		if got := find(t, run(t, f, opts()), "openarch.cli"); got.Status != c.want {
			t.Errorf("%s = %+v, want %s", c.out, got, c.want)
		}
	}

	f := newFake()
	f.noOpenArch = true
	if c := find(t, run(t, f, opts()), "openarch.cli"); c.Status != Fail || !strings.Contains(c.Fix, "openarch-cli") {
		t.Errorf("not found = %+v", c)
	}
	o := opts()
	o.OpenArchBin = "/opt/openarch"
	if c := find(t, run(t, f, o), "openarch.cli"); c.Status != OK {
		t.Errorf("OpenArchBin = %+v", c)
	}
	if last := f.calls[len(f.calls)-1]; last != "/opt/openarch version --output json" {
		t.Errorf("last call = %q", last)
	}
}

func TestOnly(t *testing.T) {
	f := newFake()
	f.noCLI = true
	o := opts()
	o.Only = []string{"port.80", "docker.memory"}
	r := run(t, f, o)
	if len(r.Checks) != 2 || r.Checks[0].ID != "docker.memory" || r.Checks[1].ID != "port.80" {
		t.Fatalf("checks = %+v", r.Checks)
	}
	if r.Checks[0].Status != Skip {
		t.Errorf("docker.memory without CLI = %s, want skip", r.Checks[0].Status)
	}
}

func TestUnknownCheck(t *testing.T) {
	o := opts()
	o.Only = []string{"nope"}
	_, err := Run(context.Background(), newFake().env(), o)
	var e *output.Error
	if !errors.As(err, &e) || e.Code != output.ExitUsage || !strings.Contains(e.Hint, "model.chat") {
		t.Errorf("err = %v", err)
	}
}

func TestWriteText(t *testing.T) {
	f := newFake()
	f.busyPorts = map[int]bool{80: true}
	var b bytes.Buffer
	if err := run(t, f, opts()).WriteText(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"[ok]   docker.cli", "[fail] port.80 ", "fix: Find the process", "11 ok, 0 warn, 1 fail, 0 skip"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// ollama returns options for a setup whose models come from Ollama.
func ollama() Options {
	o := opts()
	o.ModelURL = "http://localhost:11434"
	o.EmbeddingModel = "mxbai-embed-large"
	o.LLMModel = "qwen2.5"
	return o
}

func TestModelServer(t *testing.T) {
	f := newFake()
	f.noRunner = true // not needed: nothing may call 'docker model'
	r := run(t, f, ollama())
	st := status(r)
	if st["model.runner"] != Skip || st["model.url"] != OK || st["model.embedding"] != OK || st["model.chat"] != OK {
		t.Errorf("status = %v", st)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "docker model") {
			t.Errorf("called %s", c)
		}
	}
	if !strings.Contains(find(t, r, "model.embedding").Message, "1024 dimensions") {
		t.Errorf("embedding = %+v", find(t, r, "model.embedding"))
	}
}

func TestModelServerSmallEmbeddings(t *testing.T) {
	f := newFake()
	f.dims = 768
	res := find(t, run(t, f, ollama()), "model.embedding")
	if res.Status != Fail || !strings.Contains(res.Message, "768 dimensions") || !strings.Contains(res.Fix, "1024") {
		t.Errorf("embedding = %+v", res)
	}
}

func TestModelServerMissingModel(t *testing.T) {
	f := newFake()
	f.served = []string{"mxbai-embed-large:latest"}
	res := find(t, run(t, f, ollama()), "model.chat")
	if res.Status != Fail || !strings.Contains(res.Fix, "ollama pull qwen2.5") {
		t.Errorf("chat = %+v", res)
	}
}

func TestModelServerDown(t *testing.T) {
	f := newFake()
	f.serverDown = true
	st := status(run(t, f, ollama()))
	if st["model.url"] != Fail || st["model.embedding"] != Skip || st["model.chat"] != Skip {
		t.Errorf("status = %v", st)
	}
}
