package setup

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
			Volumes     []string          `yaml:"volumes"`
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
	if idx.Environment["ALFRESCO_REINDEX_PREFIXES_FILE"] != "file:"+prefixesTarget ||
		len(idx.Volumes) != 1 || idx.Volumes[0] != "/home/user/openarch-app/upstream/acs/search/reindex.prefixes-file.json:"+prefixesTarget+":ro" {
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
