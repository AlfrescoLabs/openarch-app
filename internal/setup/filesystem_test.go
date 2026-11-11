package setup

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

func TestFilesystemSource(t *testing.T) {
	folder := t.TempDir()
	file := filepath.Join(folder, "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		config   Config
		readers  []string
		everyone bool
		want     string
	}{
		{"no filesystem", Config{}, nil, false, ""},
		{"reader without folder", Config{}, []string{"jim"}, false, "need --filesystem"},
		{"everyone without folder", Config{}, nil, true, "need --filesystem"},
		{"no readers", Config{Filesystem: folder}, nil, false, "needs --filesystem-reader or --filesystem-everyone"},
		{"both", Config{Filesystem: folder}, []string{"jim"}, true, "cannot be combined"},
		{"group", Config{Filesystem: folder}, []string{"jim", "GROUP_Sales"}, false, "is a group"},
		{"lower case group", Config{Filesystem: folder}, []string{"group_sales"}, false, "is a group"},
		{"comma", Config{Filesystem: folder}, []string{"jim,dwight"}, false, "invalid --filesystem-reader"},
		{"space", Config{Filesystem: folder}, []string{"jim halpert"}, false, "invalid --filesystem-reader"},
		{"empty", Config{Filesystem: folder}, []string{""}, false, "invalid --filesystem-reader"},
		{"missing folder", Config{Filesystem: filepath.Join(folder, "none")}, []string{"jim"}, false, "is not a folder"},
		{"file", Config{Filesystem: file}, []string{"jim"}, false, "is not a folder"},
		{"quote", Config{Filesystem: folder + "/it's"}, []string{"jim"}, false, "invalid --filesystem"},
		{"readers", Config{Filesystem: folder}, []string{"jim", "dwight@northgate.example"}, false, ""},
		{"everyone", Config{Filesystem: folder}, nil, true, ""},
	} {
		c.config.Alfresco = max(c.config.Alfresco, 1)
		got, err := filesystemSource(&c.config, c.readers, c.everyone)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		case c.want == "" && c.config.Filesystem != "" && (got[readersKey] != strings.Join(c.readers, ",") || got["CONNECTOR_HOST_PATH"] != "'"+folder+"'"):
			t.Errorf("%s: values %v", c.name, got)
		case c.want == "" && c.config.Filesystem == "" && got != nil:
			t.Errorf("%s: values %v without a folder", c.name, got)
		}
	}

	// A relative folder becomes absolute.
	t.Chdir(folder)
	c := Config{Sources: stack.Sources{Alfresco: 1}, Filesystem: "."}
	if _, err := filesystemSource(&c, nil, true); err != nil || !filepath.IsAbs(c.Filesystem) {
		t.Errorf("relative folder: %q, %v", c.Filesystem, err)
	}
}

func TestCheckConnector(t *testing.T) {
	for _, c := range []struct {
		config Config
		want   string
	}{
		{Config{Sources: stack.Sources{Alfresco: 2}, SampleUI: true, Filesystem: "/data"}, "--filesystem does not work with --alfresco 2 --sample-ui"},
		{Config{Sources: stack.Sources{Alfresco: 2}, SampleUI: true, CMIS: "http://cmis"}, "--cmis-env does not work with --alfresco 2 --sample-ui"},
		{Config{Sources: stack.Sources{Alfresco: 1}, Filesystem: "/data", CMIS: "http://cmis"}, "cannot be combined"},
		{Config{Sources: stack.Sources{Alfresco: 2}, Filesystem: "/data"}, ""},
		{Config{Sources: stack.Sources{Alfresco: 2}, SampleUI: true}, ""},
		{Config{Sources: stack.Sources{Alfresco: 1, Nuxeo: 1}, SampleUI: true, CMIS: "http://cmis"}, ""},
	} {
		err := checkConnector(c.config, false)
		if (c.want == "" && err != nil) || (c.want != "" && (output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), c.want))) {
			t.Errorf("%+v: %v, want %q", c.config, err, c.want)
		}
	}
}

func TestEnvValuesFilesystem(t *testing.T) {
	c := Config{BindIP: "0.0.0.0", Filesystem: "/data/shared docs", DefaultSecrets: true}
	v, err := envValues(c, nil, map[string]string{"CONNECTOR_HOST_PATH": "'/data/shared docs'", readersKey: "jim,dwight"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"CONNECTOR_SOURCE_TYPE":      "filesystem",
		"CONNECTOR_HOST_PATH":        "'/data/shared docs'",
		"CONNECTOR_PORT":             "0.0.0.0:9096",
		"FILESYSTEM_READ_PRINCIPALS": "jim,dwight",
		"CONNECTOR_SYNC_USERNAME":    "sync",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	// The sync account is random also with --default-secrets: upstream has none.
	if len(v[syncPasswordKey]) != passwordLength {
		t.Errorf("%s = %q", syncPasswordKey, v[syncPasswordKey])
	}
	if got := ParseEnv(renderEnv(v)); got["CONNECTOR_HOST_PATH"] != "/data/shared docs" {
		t.Errorf("CONNECTOR_HOST_PATH reads back as %q", got["CONNECTOR_HOST_PATH"])
	}
	if v, err = envValues(Config{BindIP: "127.0.0.1"}, nil, nil); err != nil || v["CONNECTOR_SOURCE_TYPE"] != "" || v[syncPasswordKey] != "" {
		t.Errorf("no filesystem: %v, %v", v, err)
	}
}

func TestComposeArgsFilesystem(t *testing.T) {
	got := strings.Join(ComposeArgs(Config{Sources: srcOf("alfresco"), Filesystem: "/data", SampleUI: true}), " ")
	if !strings.HasSuffix(got, "--profile alfresco --profile connector --profile platform") {
		t.Errorf("filesystem: %s", got)
	}
}

func TestInitFilesystem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	folder := t.TempDir()
	opts := initOpts(dir)
	opts.Settings.Filesystem = folder
	opts.FilesystemReaders = []string{"jim", "dwight"}
	res, err := Init(context.Background(), (&fakeSystem{}).env(), opts)
	if err != nil {
		t.Fatal(err)
	}
	local := ParseEnv(mustRead(t, filepath.Join(dir, EnvFile)))
	if local["CONNECTOR_HOST_PATH"] != folder || local[readersKey] != "jim,dwight" || local["CONNECTOR_PORT"] != "127.0.0.1:9096" {
		t.Errorf(".env.local = %v", local)
	}
	c, err := Load(dir)
	if err != nil || c.Filesystem != folder {
		t.Errorf("config = %+v, %v", c, err)
	}
	if last := res.Next[len(res.Next)-1]; !strings.HasPrefix(last, "openarch app sync --dir "+dir) {
		t.Errorf("next = %v", res.Next)
	}
	var b strings.Builder
	if err := res.WriteText(&b); err != nil || !strings.Contains(b.String(), "(1 Alfresco, the folder "+folder+")") {
		t.Errorf("text = %q, %v", b.String(), err)
	}

	// A kept .env.local without the filesystem settings is refused.
	dir2 := filepath.Join(t.TempDir(), "setup")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir2, EnvFile), []byte("HXPR_PASSWORD=p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.Dir = dir2
	if _, err := Init(context.Background(), (&fakeSystem{}).env(), opts); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "no settings for the filesystem source") {
		t.Errorf("kept .env.local: %v", err)
	}
}

func TestEndpointsFilesystem(t *testing.T) {
	s := newSetup(t, Config{Sources: srcOf("alfresco"), BindIP: "0.0.0.0", Context: "app", Filesystem: "/data"})
	last := s.Endpoints()[len(s.Endpoints())-1]
	if last.URL != "http://127.0.0.1:9096/api/sync/" {
		t.Errorf("last endpoint = %+v", last)
	}
}

// fakeSyncAPI plays the sync API of plugin-batch-ingester. Each status call
// answers the next of statuses.
type fakeSyncAPI struct {
	statuses []string
	code     int
	err      error
	requests []string
}

func (f *fakeSyncAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	user, password, _ := r.BasicAuth()
	f.requests = append(f.requests, r.Method+" "+r.URL.String()+" "+user+":"+password)
	if f.err != nil {
		return nil, f.err
	}
	code, body := http.StatusOK, `{"jobId":"j1","status":"RUNNING","discoveredCount":0}`
	if f.code != 0 {
		code, body = f.code, ""
	}
	if r.Method == http.MethodGet && len(f.statuses) > 0 {
		body = `{"jobId":"j1","status":"` + f.statuses[0] + `","discoveredCount":3,"syncedCount":2,"skippedCount":1,"failedCount":0,"deletedCount":0}`
		f.statuses = f.statuses[1:]
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func syncSetup(t *testing.T) Setup {
	t.Helper()
	s := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app", Filesystem: "/data"})
	f, err := os.OpenFile(filepath.Join(s.Dir, EnvFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("CONNECTOR_SYNC_USERNAME=sync\nCONNECTOR_SYNC_PASSWORD=secret\n"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSync(t *testing.T) {
	s := syncSetup(t)
	api := &fakeSyncAPI{statuses: []string{"RUNNING", "COMPLETED"}}
	env := Env{HTTP: &http.Client{Transport: api}}
	res, err := Sync(context.Background(), env, s, SyncOptions{Timeout: 60, Interval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.JobID != "j1" || res.Discovered != 3 || res.Synced != 2 || res.Skipped != 1 || res.Source != "/data" {
		t.Errorf("result = %+v", res)
	}
	want := []string{
		"POST http://127.0.0.1:9096/api/sync/configured sync:secret",
		"GET http://127.0.0.1:9096/api/sync/status/j1 sync:secret",
		"GET http://127.0.0.1:9096/api/sync/status/j1 sync:secret",
	}
	if strings.Join(api.requests, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s", strings.Join(api.requests, "\n"))
	}

	// A failed job is a result, not an error.
	api = &fakeSyncAPI{statuses: []string{"FAILED"}}
	res, err = Sync(context.Background(), Env{HTTP: &http.Client{Transport: api}}, s, SyncOptions{Timeout: 60, Interval: time.Millisecond})
	if err != nil || res.OK || res.Status != "FAILED" {
		t.Errorf("failed job: %+v, %v", res, err)
	}
	var b strings.Builder
	if err := res.WriteText(&b); err != nil || !strings.HasPrefix(b.String(), "Sync FAILED for /data: 3 found") {
		t.Errorf("text = %q", b.String())
	}
}

func TestSyncErrors(t *testing.T) {
	s := syncSetup(t)
	opts := SyncOptions{Timeout: 60, Interval: time.Millisecond}
	for _, c := range []struct {
		name string
		api  *fakeSyncAPI
		code output.ExitCode
	}{
		{"not running", &fakeSyncAPI{err: errors.New("connection refused")}, output.ExitUnhealthy},
		{"refused login", &fakeSyncAPI{code: http.StatusUnauthorized}, output.ExitAuth},
		{"server error", &fakeSyncAPI{code: http.StatusInternalServerError}, output.ExitRemote},
	} {
		if _, err := Sync(context.Background(), Env{HTTP: &http.Client{Transport: c.api}}, s, opts); output.CodeOf(err) != c.code {
			t.Errorf("%s: %v, want code %d", c.name, err, c.code)
		}
	}
	// A job that runs longer than the timeout.
	api := &fakeSyncAPI{statuses: []string{"RUNNING", "RUNNING", "RUNNING"}}
	if _, err := Sync(context.Background(), Env{HTTP: &http.Client{Transport: api}}, s, SyncOptions{Timeout: 0, Interval: time.Millisecond}); output.CodeOf(err) != output.ExitUnhealthy || !strings.Contains(err.Error(), "still runs") {
		t.Errorf("timeout: %v", err)
	}
	// A setup without the filesystem source.
	plain := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app"})
	if _, err := Sync(context.Background(), Env{}, plain, opts); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "no filesystem or CMIS source") {
		t.Errorf("no filesystem: %v", err)
	}
}
