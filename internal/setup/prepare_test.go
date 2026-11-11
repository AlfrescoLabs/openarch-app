package setup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

func fixtureProject(t *testing.T) AlfrescoProject {
	t.Helper()
	p, err := parseAlfrescoProject("/home/user/alfresco", mustRead(t, "testdata/compose-installer-26.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRenderProjectOverride(t *testing.T) {
	p := fixtureProject(t)
	out := renderProjectOverride(p)
	if dest := os.Getenv("OPENARCH_APP_WRITE_OVERRIDE"); dest != "" {
		// For a manual check with 'docker compose config'.
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(string(out), OverrideHeader) {
		t.Errorf("no header:\n%s", out)
	}
	type mount struct {
		Type     string `yaml:"type"`
		Source   string `yaml:"source"`
		Target   string `yaml:"target"`
		ReadOnly bool   `yaml:"read_only"`
	}
	var doc struct {
		Networks map[string]struct {
			External bool `yaml:"external"`
		} `yaml:"networks"`
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Build       any               `yaml:"build"`
			Restart     string            `yaml:"restart"`
			Environment map[string]string `yaml:"environment"`
			Volumes     []mount           `yaml:"volumes"`
			Networks    map[string]struct {
				Aliases []string `yaml:"aliases"`
			} `yaml:"networks"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, out)
	}
	if !doc.Networks[SharedNetwork].External {
		t.Errorf("networks = %+v", doc.Networks)
	}
	repo := doc.Services["alfresco"]
	opts := repo.Environment["JAVA_OPTS"]
	for _, want := range []string{"-Ddb.url=jdbc:postgresql://postgres:5432/alfresco", "-Dcontent.lake.permission.sync.enabled=true",
		"-Dcontent.lake.permission.sync.broker-url=tcp://activemq:61616", "-Dcontent.lake.permission.sync.broker-user=admin",
		"-Dcontent.lake.permission.sync.broker-password=test-secret"} {
		if !strings.Contains(opts, want) {
			t.Errorf("JAVA_OPTS lacks %q", want)
		}
	}
	// The repository keeps the image of the project, and mounts the JAR.
	if repo.Image != "" || repo.Build != nil || strings.Contains(string(out), "!reset") {
		t.Errorf("repository image %q, build %v", repo.Image, repo.Build)
	}
	if len(repo.Volumes) != 1 || repo.Volumes[0] != (mount{"bind", "./openarch-app/content-lake-repo-model.jar", moduleTarget, true}) {
		t.Errorf("repository volumes = %+v", repo.Volumes)
	}
	// Each service keeps its own network and joins the shared one.
	for svc, alias := range map[string]string{"alfresco": alfrescoAlias, "activemq": brokerAlias, "content-app": ""} {
		n := doc.Services[svc].Networks
		if _, ok := n["default"]; !ok {
			t.Errorf("%s left the default network: %+v", svc, n)
		}
		if got := strings.Join(n[SharedNetwork].Aliases, ","); got != alias {
			t.Errorf("%s alias = %q, want %q", svc, got, alias)
		}
	}
	idx := doc.Services["batch-indexer"]
	if idx.Environment["ALFRESCO_REINDEX_PREFIXES_FILE"] != "file:"+prefixesTarget || len(idx.Volumes) != 1 ||
		idx.Volumes[0] != (mount{"bind", "./openarch-app/reindex.prefixes-file.json", prefixesTarget, true}) {
		t.Errorf("indexer = %+v", idx)
	}
	aca := doc.Services["content-app"]
	if aca.Image != "openarch-app/aca-extension:8.0.0" || aca.Restart != "" || aca.Environment["APP_CONFIG_RAG_URL"] != "http://"+proxyAlias ||
		aca.Environment["APP_CONFIG_ECM_HOST"] != "http://alfresco:8080" {
		t.Errorf("content-app = %+v", aca)
	}

	// A $ of the project stays a $ for Compose, and Solr gets no prefixes file.
	p.javaOpts += " -Dfoo=a$b"
	p.Search, p.ContentApp = "solr", ""
	out = renderProjectOverride(p)
	if !strings.Contains(string(out), "-Dfoo=a$$b") || strings.Contains(string(out), "batch-indexer") || strings.Contains(string(out), "content-app") {
		t.Errorf("solr, no ACA:\n%s", out)
	}
}

func TestProjectFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := projectFiles(dir); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("no main file: %v", err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docker-compose.yml", "services: {}\n")
	l, err := projectFiles(dir)
	if err != nil || l.main != "docker-compose.yml" || l.override != filepath.Join(dir, "compose.override.yaml") || l.exists || l.ours {
		t.Errorf("no override: %+v, %v", l, err)
	}
	// Compose merges the first override file in its own order, whatever the
	// main file is called.
	write("docker-compose.override.yml", OverrideHeader)
	if l, _ = projectFiles(dir); l.override != filepath.Join(dir, "docker-compose.override.yml") || !l.ours {
		t.Errorf("ours: %+v", l)
	}
	write("compose.override.yaml", "services: {}\n")
	if l, _ = projectFiles(dir); l.override != filepath.Join(dir, "compose.override.yaml") || !l.exists || l.ours {
		t.Errorf("a user file first: %+v", l)
	}
	write(".env", "COMPOSE_FILE=docker-compose.yml\n")
	if l, _ = projectFiles(dir); l.composeFile != "docker-compose.yml" {
		t.Errorf("COMPOSE_FILE: %+v", l)
	}
}

// preparedConfig returns the fixture config with the module mounted into the
// repository, as Compose reports a prepared project.
func preparedConfig(t *testing.T, prepared bool) []byte {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(mustRead(t, "testdata/compose-installer-26.2.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if prepared {
		repo := cfg["services"].(map[string]any)["alfresco"].(map[string]any)
		repo["volumes"] = append(repo["volumes"].([]any), map[string]any{"type": "bind", "target": moduleTarget})
	}
	out, _ := json.Marshal(cfg)
	return out
}

func TestPrepared(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		p, err := parseAlfrescoProject("/p", preparedConfig(t, prepared))
		if err != nil || p.Prepared() != prepared {
			t.Errorf("prepared %v: %v, %v", prepared, p.Prepared(), err)
		}
	}
}

func TestAlfrescoStackValues(t *testing.T) {
	v := alfrescoStackValues(fixtureProject(t), "admin", "pw")
	for k, want := range map[string]string{"ALFRESCO_URL": "http://openarch-alfresco:8080", "ALFRESCO_PROXY_UPSTREAM": "openarch-alfresco:8080",
		"ALFRESCO_EVENT_BROKER_URL": "tcp://openarch-activemq:61616", "ACTIVEMQ_USER": "admin", "ACTIVEMQ_PASSWORD": "test-secret",
		"ALFRESCO_ADMIN_PASSWORD": "pw", "TRANSFORM_ACTIVEMQ_URL": "false"} {
		if v[k] != want {
			t.Errorf("%s = %q, want %q", k, v[k], want)
		}
	}
}

func TestInitAlfrescoCompose(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	opts := initOpts(dir)
	opts.AlfrescoCompose = "/home/user/alfresco"
	opts.AlfrescoPassword = func() (string, error) { return "adm$pw", nil }
	f := &fakeSystem{composeConfig: mustRead(t, "testdata/compose-installer-26.2.json")}
	res, err := Init(context.Background(), f.env(), opts)
	if err != nil {
		t.Fatal(err)
	}
	local := ParseEnv(mustRead(t, filepath.Join(dir, EnvFile)))
	if local["ALFRESCO_URL"] != "http://openarch-alfresco:8080" || local["ALFRESCO_ADMIN_PASSWORD"] != "adm$pw" || local["ACTIVEMQ_PASSWORD"] != "test-secret" {
		t.Errorf(".env.local = %v", local)
	}
	project, _ := filepath.Abs("/home/user/alfresco")
	c, err := Load(dir)
	if err != nil || c.AlfrescoCompose != project || c.Profile() != "alfresco-external" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	if !slices.ContainsFunc(f.calls, func(call string) bool { return call == "docker compose config --format json" }) {
		t.Errorf("calls = %v", f.calls)
	}
	if !strings.HasPrefix(res.Next[1], "openarch app alfresco-prepare --dir "+dir) {
		t.Errorf("next = %v", res.Next)
	}
	// The stack services join the shared network, the proxy with its alias.
	override := string(mustRead(t, filepath.Join(dir, OverrideFile)))
	for _, want := range []string{
		"  batch-ingester:\n    environment:\n      INGESTION_FOLDER_1: \"openarch-app-no-default-folder\"\n    networks:\n      openarch-app-alfresco: {}",
		"  rag-service:\n    environment:\n      RAG_SOURCE_LINKS_ALFRESCO_TEMPLATE: \"/#/search/(viewer:view/{nodeId})?location=%2Frag\"\n    networks:\n      openarch-app-alfresco: {}",
		"  proxy:\n    networks:\n      openarch-app-alfresco:\n        aliases: [openarch-app-proxy]",
		"networks:\n  openarch-app-alfresco:\n    external: true"} {
		if !strings.Contains(override, want) {
			t.Errorf("override lacks %q:\n%s", want, override)
		}
	}

	for _, c := range []struct {
		name string
		mod  func(*InitOptions)
		want string
	}{
		{"with env file", func(o *InitOptions) { o.AlfrescoEnv = writeCMIS(t, goodAlfrescoEnv) }, "cannot be combined"},
		{"with Nuxeo", func(o *InitOptions) { o.Settings.Nuxeo = 1 }, "works with --alfresco 1 --nuxeo 0 only"},
		{"no password", func(o *InitOptions) { o.AlfrescoPassword = nil }, "password of the Alfresco admin is missing"},
	} {
		o := initOpts(filepath.Join(t.TempDir(), "setup"))
		o.AlfrescoCompose = "/home/user/alfresco"
		o.AlfrescoPassword = func() (string, error) { return "pw", nil }
		c.mod(&o)
		if _, err := Init(context.Background(), f.env(), o); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestEnsureSharedNetwork(t *testing.T) {
	var calls []string
	exists := false
	env := Env{Run: func(_ context.Context, _ string, _ io.Reader, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "inspect" && !exists {
			return nil, errors.New("no such network")
		}
		return nil, nil
	}}
	if err := EnsureSharedNetwork(context.Background(), env); err != nil || strings.Join(calls, ",") != "network inspect openarch-app-alfresco,network create openarch-app-alfresco" {
		t.Errorf("create: %v, %v", calls, err)
	}
	calls, exists = nil, true
	if err := EnsureSharedNetwork(context.Background(), env); err != nil || len(calls) != 1 {
		t.Errorf("exists: %v, %v", calls, err)
	}
}

// fakePrepareDocker plays docker for Prepare, Undo and Up.
type fakePrepareDocker struct {
	config []byte
	runs   []string
	builds []string
	failUI bool
}

func (f *fakePrepareDocker) env() Env {
	return Env{
		Run: func(_ context.Context, dir string, _ io.Reader, name string, args ...string) ([]byte, error) {
			f.runs = append(f.runs, strings.Join(args, " "))
			switch {
			case slices.Contains(args, "config"):
				return f.config, nil
			case args[0] == "image" || args[0] == "network":
				return nil, nil
			}
			return nil, errors.New("unexpected " + strings.Join(args, " "))
		},
		Stream: func(_ context.Context, dir string, _ []string, _, _ io.Writer, name string, args ...string) error {
			f.builds = append(f.builds, strings.Join(args, " "))
			if f.failUI && slices.Contains(args, "docker/Dockerfile") {
				return errors.New("exit status 1")
			}
			for i, a := range args {
				if a == "--output" {
					dest := strings.TrimPrefix(args[i+1], "type=local,dest=")
					return os.WriteFile(filepath.Join(dest, "content-lake-repo-model-1.0.0.jar"), []byte("jar"), 0o644)
				}
			}
			return nil
		},
	}
}

func prepareSetup(t *testing.T) (Setup, string) {
	t.Helper()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app", AlfrescoCompose: project, ExternalAlfresco: "http://openarch-alfresco:8080"})
	search := filepath.Join(s.Dir, UpstreamDir, "acs", "search")
	if err := os.MkdirAll(search, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(search, prefixesFile), []byte(`{"prefixUriMap":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return s, project
}

func TestPrepare(t *testing.T) {
	s, project := prepareSetup(t)
	f := &fakePrepareDocker{config: mustRead(t, "testdata/compose-installer-26.2.json")}
	st := Streams{Out: io.Discard, Err: io.Discard}
	res, err := Prepare(context.Background(), f.env(), s, PrepareOptions{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || strings.Join(res.Images, ",") != "openarch-app/aca-extension:8.0.0" {
		t.Errorf("result = %+v", res)
	}
	// The JAR and the prefixes file wait in the setup folder; nothing in the
	// project changed.
	for _, name := range []string{moduleFile, prefixesFile} {
		if _, err := os.Stat(filepath.Join(res.Assets, name)); err != nil {
			t.Errorf("assets lack %s: %v", name, err)
		}
	}
	if entries, _ := os.ReadDir(project); len(entries) != 1 {
		t.Errorf("instructions mode changed the project: %v", entries)
	}
	steps := strings.Join(res.Steps, "\n")
	for _, want := range []string{"Copy the folder " + res.Assets + " to " + filepath.Join(project, ProjectAssets), "Copy " + res.Override + " to " + filepath.Join(project, "compose.override.yaml")} {
		if !strings.Contains(steps, want) {
			t.Errorf("steps lack %q:\n%s", want, steps)
		}
	}
	builds := strings.Join(f.builds, "\n")
	if strings.Contains(builds, "compose build") || strings.Contains(builds, ":module") {
		t.Errorf("the repository image was rebuilt:\n%s", builds)
	}
	if !strings.Contains(builds, "build --build-arg ACA_TAG=8.0.0 -t openarch-app/aca-extension:8.0.0 -f docker/Dockerfile https://github.com/AlfrescoLabs/alfresco-content-lake-ui.git#") {
		t.Errorf("builds:\n%s", builds)
	}

	// --apply asks, copies the files into the project and recreates the services.
	asked := ""
	f.builds = nil
	res, err = Prepare(context.Background(), f.env(), s, PrepareOptions{Apply: true, Confirm: func(p string) error { asked = p; return nil }}, st)
	if err != nil || !res.Applied || asked != project {
		t.Fatalf("apply: %+v, %v, asked %q", res, err, asked)
	}
	installed := filepath.Join(project, "compose.override.yaml")
	if !strings.HasPrefix(string(mustRead(t, installed)), OverrideHeader) {
		t.Errorf("override not installed")
	}
	if string(mustRead(t, filepath.Join(project, ProjectAssets, moduleFile))) != "jar" {
		t.Errorf("JAR not copied into the project")
	}
	if !strings.Contains(strings.Join(f.builds, "\n"), "compose up -d --wait alfresco activemq batch-indexer\ncompose up -d") {
		t.Errorf("apply did not recreate: %v", f.builds)
	}
	// A second run reads the project without the plugin's own file.
	f.runs = nil
	if _, err := Prepare(context.Background(), f.env(), s, PrepareOptions{}, st); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.runs, "compose -f docker-compose.yml config --format json") {
		t.Errorf("second run: %v", f.runs)
	}
	// A cancelled confirmation changes nothing.
	if _, err := Prepare(context.Background(), f.env(), s, PrepareOptions{Apply: true, Confirm: func(string) error { return errors.New("cancelled") }}, st); err == nil || err.Error() != "cancelled" {
		t.Errorf("cancel: %v", err)
	}
}

func TestPrepareUserOverride(t *testing.T) {
	st := Streams{Out: io.Discard, Err: io.Discard}
	s, project := prepareSetup(t)
	cfg := mustRead(t, "testdata/compose-installer-26.2.json")
	user := filepath.Join(project, "docker-compose.override.yml")
	if err := os.WriteFile(user, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// --apply never replaces it.
	if _, err := Prepare(context.Background(), (&fakePrepareDocker{config: cfg}).env(), s, PrepareOptions{Apply: true}, st); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "is not from openarch-app") {
		t.Errorf("foreign override: %v", err)
	}
	// The steps say to merge, not to copy over it.
	res, err := Prepare(context.Background(), (&fakePrepareDocker{config: cfg}).env(), s, PrepareOptions{}, st)
	steps := strings.Join(res.Steps, "\n")
	if err != nil || !strings.Contains(steps, "Merge "+res.Override+" into your "+user) || strings.Contains(steps, "Copy "+res.Override) {
		t.Errorf("steps: %v\n%s", err, steps)
	}
	if string(mustRead(t, user)) != "services: {}\n" {
		t.Error("the user's override changed")
	}
	// With COMPOSE_FILE, --apply is refused and the steps say what to add.
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("COMPOSE_FILE=docker-compose.yml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), (&fakePrepareDocker{config: cfg}).env(), s, PrepareOptions{Apply: true}, st); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "COMPOSE_FILE") {
		t.Errorf("COMPOSE_FILE: %v", err)
	}
	if res, _ := Prepare(context.Background(), (&fakePrepareDocker{config: cfg}).env(), s, PrepareOptions{}, st); !strings.Contains(strings.Join(res.Steps, "\n"), "add docker-compose.override.yml to COMPOSE_FILE") {
		t.Errorf("COMPOSE_FILE steps: %v", res.Steps)
	}
}

func TestPrepareErrors(t *testing.T) {
	st := Streams{Out: io.Discard, Err: io.Discard}
	s, _ := prepareSetup(t)
	cfg := mustRead(t, "testdata/compose-installer-26.2.json")
	// An ACA that the extension cannot build for.
	f := &fakePrepareDocker{config: cfg, failUI: true}
	if _, err := Prepare(context.Background(), f.env(), s, PrepareOptions{}, st); err == nil || !strings.Contains(err.Error(), "cannot build ACA 8.0.0") {
		t.Errorf("ACA build: %v", err)
	}
	// Checks that come before any build.
	for name, change := range map[string][2]string{
		"ACA latest":   {`"alfresco/alfresco-content-app:8.0.0"`, `"alfresco/alfresco-content-app:latest"`},
		"own prefixes": {`"ALFRESCO_REINDEX_CONTINUOUS_OVERLAP": "10m"`, `"ALFRESCO_REINDEX_CONTINUOUS_OVERLAP": "10m", "ALFRESCO_REINDEX_PREFIXES_FILE": "file:/mine.json"`},
	} {
		if !strings.Contains(string(cfg), change[0]) {
			t.Fatalf("%s: fixture lacks %s", name, change[0])
		}
		f := &fakePrepareDocker{config: []byte(strings.Replace(string(cfg), change[0], change[1], 1))}
		if _, err := Prepare(context.Background(), f.env(), s, PrepareOptions{}, st); output.CodeOf(err) != output.ExitUsage || len(f.builds) != 0 {
			t.Errorf("%s: %v, builds %v", name, err, f.builds)
		}
	}
	// A setup without a Compose project.
	plain := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app"})
	if _, err := Prepare(context.Background(), Env{}, plain, PrepareOptions{}, st); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("no project: %v", err)
	}
}

func TestUpNeedsPreparedProject(t *testing.T) {
	s, _ := prepareSetup(t)
	f := &fakePrepareDocker{config: preparedConfig(t, false)}
	if _, err := Up(context.Background(), f.env(), s, UpOptions{}, Streams{Out: io.Discard, Err: io.Discard}); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "is not prepared") {
		t.Errorf("not prepared: %v", err)
	}
	if strings.Contains(strings.Join(f.builds, "\n"), " up ") {
		t.Errorf("the stack started: %v", f.builds)
	}
}
