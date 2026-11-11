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
	out := renderProjectOverride(p, "/home/user/openarch-app")
	if dest := os.Getenv("OPENARCH_APP_WRITE_OVERRIDE"); dest != "" {
		// For a manual check with 'docker compose config'.
		if err := os.WriteFile(dest, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.HasPrefix(string(out), OverrideHeader) {
		t.Errorf("no header:\n%s", out)
	}
	var doc struct {
		Networks map[string]struct {
			External bool `yaml:"external"`
		} `yaml:"networks"`
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Environment map[string]string `yaml:"environment"`
			Volumes     []struct {
				Type     string `yaml:"type"`
				Source   string `yaml:"source"`
				Target   string `yaml:"target"`
				ReadOnly bool   `yaml:"read_only"`
			} `yaml:"volumes"`
			Networks map[string]struct {
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
	if repo.Image != "openarch-app/stock-alfresco:module" || !strings.Contains(string(out), "build: !reset null") {
		t.Errorf("repository image %q", repo.Image)
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
	prefixes := filepath.Join("/home/user/openarch-app", "upstream", "acs", "search", "reindex.prefixes-file.json")
	if idx.Environment["ALFRESCO_REINDEX_PREFIXES_FILE"] != "file:"+prefixesTarget || len(idx.Volumes) != 1 ||
		idx.Volumes[0].Type != "bind" || idx.Volumes[0].Source != prefixes || idx.Volumes[0].Target != prefixesTarget || !idx.Volumes[0].ReadOnly {
		t.Errorf("indexer = %+v", idx)
	}
	aca := doc.Services["content-app"]
	if aca.Image != "openarch-app/aca-extension:8.0.0" || aca.Environment["APP_CONFIG_RAG_URL"] != "http://"+proxyAlias ||
		aca.Environment["APP_CONFIG_ECM_HOST"] != "http://alfresco:8080" {
		t.Errorf("content-app = %+v", aca)
	}

	// A $ of the project stays a $ for Compose, and Solr gets no prefixes file.
	p.javaOpts += " -Dfoo=a$b"
	p.Search, p.ContentApp = "solr", ""
	out = renderProjectOverride(p, "/s")
	if !strings.Contains(string(out), "-Dfoo=a$$b") || strings.Contains(string(out), "batch-indexer") || strings.Contains(string(out), "content-app") {
		t.Errorf("solr, no ACA:\n%s", out)
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
	for _, want := range []string{"  rag-service:\n    networks:\n      openarch-app-alfresco: {}", "  proxy:\n    networks:\n      openarch-app-alfresco:\n        aliases: [openarch-app-proxy]",
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

// fakePrepareDocker plays docker for Prepare.
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
	return newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app", AlfrescoCompose: project, ExternalAlfresco: "http://openarch-alfresco:8080"}), project
}

func TestPrepare(t *testing.T) {
	s, project := prepareSetup(t)
	f := &fakePrepareDocker{config: mustRead(t, "testdata/compose-installer-26.2.json")}
	st := Streams{Out: io.Discard, Err: io.Discard}
	res, err := Prepare(context.Background(), f.env(), s, PrepareOptions{}, st)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.Steps) != 4 || strings.Join(res.Images, ",") != "openarch-app/stock-alfresco:module,openarch-app/aca-extension:8.0.0" {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(project, "docker-compose.override.yml")); err == nil {
		t.Error("instructions mode changed the project")
	}
	if !strings.HasPrefix(string(mustRead(t, res.Override)), OverrideHeader) {
		t.Errorf("override file %s", res.Override)
	}
	dockerfile := string(mustRead(t, filepath.Join(s.Dir, "alfresco-prepare", "Dockerfile")))
	if !strings.HasPrefix(dockerfile, "FROM stock-alfresco\n") || !strings.Contains(dockerfile, "COPY content-lake-repo-model-1.0.0.jar /usr/local/tomcat/webapps/alfresco/WEB-INF/lib/") {
		t.Errorf("Dockerfile:\n%s", dockerfile)
	}
	builds := strings.Join(f.builds, "\n")
	for _, want := range []string{"build -t openarch-app/stock-alfresco:module .", "build --build-arg ACA_TAG=8.0.0 -t openarch-app/aca-extension:8.0.0 -f docker/Dockerfile https://github.com/AlfrescoLabs/alfresco-content-lake-ui.git#"} {
		if !strings.Contains(builds, want) {
			t.Errorf("builds lack %q:\n%s", want, builds)
		}
	}
	if strings.Contains(builds, "compose up") {
		t.Error("instructions mode ran compose up")
	}

	// --apply asks, installs the file and recreates the services.
	asked := ""
	f.builds = nil
	res, err = Prepare(context.Background(), f.env(), s, PrepareOptions{Apply: true, Confirm: func(p string) error { asked = p; return nil }}, st)
	if err != nil || !res.Applied || asked != project {
		t.Fatalf("apply: %+v, %v, asked %q", res, err, asked)
	}
	installed := filepath.Join(project, "docker-compose.override.yml")
	if !strings.HasPrefix(string(mustRead(t, installed)), OverrideHeader) || !strings.Contains(strings.Join(f.builds, "\n"), "compose up -d --wait alfresco activemq batch-indexer\ncompose up -d") {
		t.Errorf("apply did not install and recreate: %v", f.builds)
	}
	if !slices.Contains(f.runs, "network inspect "+SharedNetwork) {
		t.Errorf("runs = %v", f.runs)
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

func TestPrepareErrors(t *testing.T) {
	st := Streams{Out: io.Discard, Err: io.Discard}
	s, project := prepareSetup(t)
	cfg := mustRead(t, "testdata/compose-installer-26.2.json")
	// An override file of the user is never replaced.
	if err := os.WriteFile(filepath.Join(project, "docker-compose.override.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), (&fakePrepareDocker{config: cfg}).env(), s, PrepareOptions{Apply: true}, st); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "is not from openarch-app") {
		t.Errorf("foreign override: %v", err)
	}
	// An ACA that the extension cannot build for.
	f := &fakePrepareDocker{config: cfg, failUI: true}
	if _, err := Prepare(context.Background(), f.env(), s, PrepareOptions{}, st); err == nil || !strings.Contains(err.Error(), "cannot build ACA 8.0.0") {
		t.Errorf("ACA build: %v", err)
	}
	// A setup without a Compose project.
	plain := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app"})
	if _, err := Prepare(context.Background(), Env{}, plain, PrepareOptions{}, st); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("no project: %v", err)
	}
}
