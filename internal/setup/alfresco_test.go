package setup

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

const goodAlfrescoEnv = `ALFRESCO_URL=http://host.docker.internal:8180
ALFRESCO_EVENT_BROKER_URL=tcp://host.docker.internal:61616
ALFRESCO_ADMIN_USER=admin
ALFRESCO_ADMIN_PASSWORD=s3cret$
ACTIVEMQ_USER=admin
ACTIVEMQ_PASSWORD=mq#pass
`

func TestAlfrescoSource(t *testing.T) {
	for _, c := range []struct {
		name    string
		sources stack.Sources
		file    string
		want    string
	}{
		{"nuxeo too", stack.Sources{Alfresco: 1, Nuxeo: 1}, goodAlfrescoEnv, "works with --alfresco 1 --nuxeo 0 only"},
		{"two Alfresco", stack.Sources{Alfresco: 2}, goodAlfrescoEnv, "works with --alfresco 1 --nuxeo 0 only"},
		{"other keys", stack.Sources{Alfresco: 1}, goodAlfrescoEnv + "HXPR_PASSWORD=x\n", "keys that init does not copy: HXPR_PASSWORD"},
		{"missing", stack.Sources{Alfresco: 1}, "ALFRESCO_URL=http://alf:8080\n", "needs ALFRESCO_EVENT_BROKER_URL, ALFRESCO_ADMIN_USER"},
		{"https", stack.Sources{Alfresco: 1}, strings.Replace(goodAlfrescoEnv, "http://host", "https://host", 1), "is not a http URL"},
		{"path", stack.Sources{Alfresco: 1}, strings.Replace(goodAlfrescoEnv, ":8180", ":8180/alfresco", 1), "has the path /alfresco"},
		{"localhost", stack.Sources{Alfresco: 1}, strings.Replace(goodAlfrescoEnv, "http://host.docker.internal", "http://localhost", 1), "which is the container itself"},
		{"broker localhost", stack.Sources{Alfresco: 1}, strings.Replace(goodAlfrescoEnv, "tcp://host.docker.internal", "tcp://127.0.0.1", 1), "which is the container itself"},
		{"broker scheme", stack.Sources{Alfresco: 1}, strings.Replace(goodAlfrescoEnv, "tcp://", "http://", 1), "is not a tcp, nio, ssl, failover URL"},
		{"quote", stack.Sources{Alfresco: 1}, goodAlfrescoEnv + "ALFRESCO_PROXY_UPSTREAM=it's:80\n", "has a single quote"},
		{"good", stack.Sources{Alfresco: 1}, goodAlfrescoEnv, ""},
	} {
		cfg := Config{Sources: c.sources}
		v, err := alfrescoSource(&cfg, writeCMIS(t, c.file))
		if c.want != "" {
			if output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: %v, want %q", c.name, err, c.want)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := ParseEnv(renderEnv(v))
		for k, want := range map[string]string{
			"ALFRESCO_URL":                            "http://host.docker.internal:8180",
			"ALFRESCO_PROXY_UPSTREAM":                 "host.docker.internal:8180",
			"CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL": "tcp://host.docker.internal:61616",
			"ALFRESCO_ADMIN_PASSWORD":                 "s3cret$",
			"ACTIVEMQ_PASSWORD":                       "mq#pass",
			"TRANSFORM_ACTIVEMQ_URL":                  "false",
		} {
			if got[k] != want {
				t.Errorf("%s = %q, want %q", k, got[k], want)
			}
		}
		if cfg.ExternalAlfresco != "http://host.docker.internal:8180" || cfg.Profile() != "alfresco-external" {
			t.Errorf("config %q, profile %q", cfg.ExternalAlfresco, cfg.Profile())
		}
	}
	// A failover broker URL has no host of its own, and a URL without a port
	// gets port 80 on the proxy.
	cfg := Config{Sources: stack.Sources{Alfresco: 1}}
	file := strings.NewReplacer("tcp://host.docker.internal:61616", "failover:(tcp://mq1:61616,tcp://mq2:61616)", ":8180", "").Replace(goodAlfrescoEnv)
	v, err := alfrescoSource(&cfg, writeCMIS(t, file))
	if err != nil || v["ALFRESCO_PROXY_UPSTREAM"] != "'host.docker.internal:80'" {
		t.Errorf("failover: %v, %v", v, err)
	}
	if v, err := alfrescoSource(&Config{}, ""); v != nil || err != nil {
		t.Errorf("no file: %v, %v", v, err)
	}
}

func TestInitExistingAlfresco(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	opts := initOpts(dir)
	opts.AlfrescoEnv = writeCMIS(t, goodAlfrescoEnv)
	res, err := Init(context.Background(), (&fakeSystem{}).env(), opts)
	if err != nil {
		t.Fatal(err)
	}
	local := ParseEnv(mustRead(t, filepath.Join(dir, EnvFile)))
	if local["ALFRESCO_ADMIN_PASSWORD"] != "s3cret$" || local["ACTIVEMQ_PASSWORD"] != "mq#pass" || local["ALFRESCO_URL"] != "http://host.docker.internal:8180" {
		t.Errorf(".env.local = %v", local)
	}
	// The bundled Alfresco is not bootstrapped, so no hash is written.
	for _, k := range []string{"ALFRESCO_ADMIN_PASSWORD_HASH", "ALFRESCO_2_INTERNAL_PASSWORD"} {
		if _, ok := local[k]; ok {
			t.Errorf("%s written for an existing Alfresco", k)
		}
	}
	c, err := Load(dir)
	if err != nil || c.ExternalAlfresco != "http://host.docker.internal:8180" {
		t.Fatalf("config = %+v, %v", c, err)
	}
	if got := strings.Join(ComposeArgs(c), " "); !strings.HasSuffix(got, "-f compose.override.yaml --profile alfresco-external") {
		t.Errorf("compose args: %s", got)
	}
	if len(res.Next) != 3 || !strings.HasPrefix(res.Next[1], "openarch app alfresco-module ./alfresco-module --dir "+dir) {
		t.Errorf("next = %v", res.Next)
	}
	var b strings.Builder
	if err := res.WriteText(&b); err != nil || !strings.Contains(b.String(), "(the existing Alfresco http://host.docker.internal:8180)") {
		t.Errorf("text = %q", b.String())
	}
	var up strings.Builder
	if err := (UpResult{Sources: c.Sources, ExternalAlfresco: c.ExternalAlfresco}).WriteText(&up); err != nil || !strings.HasPrefix(up.String(), "App for OpenArch is up (the existing Alfresco http://host.docker.internal:8180).") {
		t.Errorf("up text = %q", up.String())
	}
	if (Setup{Dir: dir, Config: c}).UsesUpstreamAlfrescoAdmin() {
		t.Error("existing Alfresco reported as using the upstream admin")
	}
}

// fakeAlfresco plays the existing Alfresco behind the proxy.
type fakeAlfresco struct {
	major     string
	login     int
	aspect    int
	requested []string
}

func (f *fakeAlfresco) RoundTrip(r *http.Request) (*http.Response, error) {
	user, password, _ := r.BasicAuth()
	f.requested = append(f.requested, r.URL.String()+" "+user+":"+password)
	code, body := http.StatusOK, ""
	switch {
	case f.login != 0:
		code = f.login
	case strings.HasSuffix(r.URL.Path, "/discovery"):
		body = `{"entry":{"repository":{"version":{"major":"` + f.major + `","display":"` + f.major + `.2.0"}}}}`
	case strings.HasSuffix(r.URL.Path, "/aspects/cl:indexed"):
		code = f.aspect
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestCheckExternalAlfresco(t *testing.T) {
	s := newSetup(t, Config{Sources: stack.Sources{Alfresco: 1}, Context: "app", ExternalAlfresco: "http://alf:8080"})
	f, err := os.OpenFile(filepath.Join(s.Dir, EnvFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("ALFRESCO_ADMIN_USER=svc\nALFRESCO_ADMIN_PASSWORD=pw\n")
	_ = f.Close()
	for _, c := range []struct {
		name string
		alf  fakeAlfresco
		code output.ExitCode
	}{
		{"ok", fakeAlfresco{major: "26", aspect: http.StatusOK}, output.ExitOK},
		{"no module", fakeAlfresco{major: "26", aspect: http.StatusNotFound}, output.ExitPrerequisite},
		{"old", fakeAlfresco{major: "23", aspect: http.StatusOK}, output.ExitPrerequisite},
		{"login", fakeAlfresco{login: http.StatusUnauthorized}, output.ExitAuth},
		{"proxy", fakeAlfresco{login: http.StatusBadGateway}, output.ExitUnhealthy},
	} {
		alf := c.alf
		err := CheckExternalAlfresco(context.Background(), Env{HTTP: &http.Client{Transport: &alf}}, s)
		if output.CodeOf(err) != c.code {
			t.Errorf("%s: %v, want code %d", c.name, err, c.code)
		}
		if !strings.HasPrefix(alf.requested[0], "http://127.0.0.1/alfresco/api/discovery svc:pw") {
			t.Errorf("%s: requests %v", c.name, alf.requested)
		}
	}
}

func TestAlfrescoModule(t *testing.T) {
	s := newSetup(t, Config{Sources: stack.Sources{Alfresco: 1}, Context: "app"})
	var got []string
	env := Env{Stream: func(_ context.Context, dir string, _ []string, _, _ io.Writer, name string, args ...string) error {
		got = append([]string{dir, name}, args...)
		for i, a := range args {
			if a == "--output" {
				dest := strings.TrimPrefix(args[i+1], "type=local,dest=")
				return os.WriteFile(filepath.Join(dest, "content-lake-repo-model-1.0.0.jar"), []byte("jar"), 0o644)
			}
		}
		return nil
	}}
	out := filepath.Join(t.TempDir(), "module")
	res, err := AlfrescoModule(context.Background(), env, s, out, Streams{Out: io.Discard, Err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	call := strings.Join(got, " ")
	for _, want := range []string{s.Dir + " docker build --target model-jar --build-arg ALFRESCO_TAG=26.2.0",
		"--build-context content_lake_app=https://github.com/AlfrescoLabs/content-lake-app.git#",
		"--output type=local,dest=" + out + " " + filepath.Join("upstream", "acs", "alfresco")} {
		if !strings.Contains(call, want) {
			t.Errorf("call %q lacks %q", call, want)
		}
	}
	if res.JAR != filepath.Join(out, "content-lake-repo-model-1.0.0.jar") || len(res.Steps) == 0 {
		t.Errorf("result = %+v", res)
	}
}
