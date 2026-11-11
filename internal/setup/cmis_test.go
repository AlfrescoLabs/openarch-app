package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

const cmisURL = "http://host.docker.internal:8080/alfresco/api/-default-/public/cmis/versions/1.1/browser"

func writeCMIS(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cmis.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCMISSource(t *testing.T) {
	good := "CMIS_URL=" + cmisURL + "\nCMIS_USERNAME=reader\n"
	fromPrompt := func() (string, error) { return "pr$mpt", nil }
	for _, c := range []struct {
		name, file string
		password   func() (string, error)
		want       string
	}{
		{"other keys", good + "CMIS_PASSWORD=p\nHXPR_PASSWORD=x\nBIND_IP=0.0.0.0\n", nil, "keys that init does not copy: BIND_IP, HXPR_PASSWORD"},
		{"public fallback", good + "CMIS_PASSWORD=p\nCMIS_ACL_FALLBACK=Public\n", nil, "CMIS_ACL_FALLBACK=public is refused"},
		{"no URL", "CMIS_USERNAME=reader\nCMIS_PASSWORD=p\n", nil, "needs CMIS_URL"},
		{"ftp URL", "CMIS_URL=ftp://cmis\nCMIS_USERNAME=reader\nCMIS_PASSWORD=p\n", nil, "needs CMIS_URL"},
		{"login in URL", "CMIS_URL=http://u:p@cmis/x\nCMIS_USERNAME=reader\nCMIS_PASSWORD=p\n", nil, "CMIS_URL holds a login"},
		{"localhost", "CMIS_URL=http://localhost:8080/x\nCMIS_USERNAME=reader\nCMIS_PASSWORD=p\n", nil, "which is the container itself"},
		{"no user", "CMIS_URL=" + cmisURL + "\nCMIS_PASSWORD=p\n", nil, "needs CMIS_USERNAME"},
		{"no password", good, nil, "the CMIS password is missing"},
		{"empty password", good, func() (string, error) { return "", nil }, "the CMIS password is empty"},
		{"quote", good + "CMIS_PASSWORD=it's\n", nil, "CMIS_PASSWORD has a single quote"},
		{"file password", good + "CMIS_PASSWORD='p$ss#1'\nCONNECTOR_ROOTS=abc\nRAG_SECURITY_CMIS_ENABLED=true\n", nil, ""},
		{"prompt password", good, fromPrompt, ""},
	} {
		cfg := Config{}
		v, err := cmisSource(&cfg, writeCMIS(t, c.file), c.password)
		if c.want != "" {
			if output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %v, want %q", c.name, err, c.want)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if cfg.CMIS != cmisURL || v["CMIS_URL"] != "'"+cmisURL+"'" || v["CMIS_USERNAME"] != "'reader'" {
			t.Errorf("%s: config %q, values %v", c.name, cfg.CMIS, v)
		}
		// The values read back as they were written, with $ and # kept.
		got := ParseEnv(renderEnv(v))
		want := "p$ss#1"
		if c.password != nil {
			want = "pr$mpt"
		}
		if got[cmisPasswordKey] != want {
			t.Errorf("%s: password reads back as %q", c.name, got[cmisPasswordKey])
		}
	}

	if _, err := cmisSource(&Config{}, filepath.Join(t.TempDir(), "none.env"), nil); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("missing file: %v", err)
	}
	failing := func() (string, error) { return "", errors.New("no terminal") }
	if _, err := cmisSource(&Config{}, writeCMIS(t, good), failing); err == nil || err.Error() != "no terminal" {
		t.Errorf("failing prompt: %v", err)
	}
}

func TestInitCMIS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	opts := initOpts(dir)
	opts.CMISEnv = writeCMIS(t, "CMIS_URL="+cmisURL+"\nCMIS_USERNAME=reader\nCMIS_ROOT_PATH=/Sites/sales\n")
	opts.CMISPassword = func() (string, error) { return "secret", nil }
	res, err := Init(context.Background(), (&fakeSystem{}).env(), opts)
	if err != nil {
		t.Fatal(err)
	}
	local := ParseEnv(mustRead(t, filepath.Join(dir, EnvFile)))
	for k, want := range map[string]string{"CONNECTOR_SOURCE_TYPE": "cmis", "CMIS_URL": cmisURL, "CMIS_PASSWORD": "secret",
		"CMIS_ROOT_PATH": "/Sites/sales", "CONNECTOR_PORT": "127.0.0.1:9096", syncUserKey: syncUser} {
		if local[k] != want {
			t.Errorf("%s = %q, want %q", k, local[k], want)
		}
	}
	if _, ok := local[readersKey]; ok {
		t.Errorf("%s set for a CMIS source", readersKey)
	}
	c, err := Load(dir)
	if err != nil || c.CMIS != cmisURL || c.Connector() != "cmis" {
		t.Errorf("config = %+v, %v", c, err)
	}
	if last := res.Next[len(res.Next)-1]; !strings.Contains(last, "reads the CMIS repository") {
		t.Errorf("next = %v", res.Next)
	}
	if got := strings.Join(ComposeArgs(c), " "); !strings.HasSuffix(got, "--profile alfresco --profile connector") {
		t.Errorf("compose args: %s", got)
	}

	// --filesystem and --cmis-env cannot be combined.
	opts.Dir = filepath.Join(t.TempDir(), "setup")
	opts.Settings.Filesystem = t.TempDir()
	opts.FilesystemEveryone = true
	if _, err := Init(context.Background(), (&fakeSystem{}).env(), opts); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("both connectors: %v", err)
	}
}
