package setup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

func TestNewPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p, err := NewPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != passwordLength || seen[p] {
			t.Fatalf("bad or repeated password %q", p)
		}
		seen[p] = true
		if !strings.ContainsAny(p, "abcdefghijkmnopqrstuvwxyz") || !strings.ContainsAny(p, "ABCDEFGHJKLMNPQRSTUVWXYZ") ||
			!strings.ContainsAny(p, "23456789") || !strings.ContainsAny(p, passwordSymbols) {
			t.Fatalf("%q misses a character class", p)
		}
		if strings.Trim(p, passwordLetters+passwordSymbols) != "" {
			t.Fatalf("%q has a character outside the set", p)
		}
	}
}

func TestAlfrescoHash(t *testing.T) {
	// The upstream default: the hash of admin in compose.alfresco.yaml.
	if got := AlfrescoHash("admin"); got != "209c6174da490caeb422f3fa5a7ae634" {
		t.Errorf("AlfrescoHash(admin) = %s", got)
	}
	if got := AlfrescoHash("Hash-Test-Pw1"); got != "f7fc0df7dfa832e0f112baf446673a09" {
		t.Errorf("AlfrescoHash(Hash-Test-Pw1) = %s", got)
	}
}

func TestParseEnv(t *testing.T) {
	env := ParseEnv([]byte("# comment\n\nA=1\nB = two \nC=\"quoted\"\nD='single'\nE=a=b\nnoequals\n"))
	want := map[string]string{"A": "1", "B": "two", "C": "quoted", "D": "single", "E": "a=b"}
	if len(env) != len(want) {
		t.Fatalf("env = %v", env)
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestEnvValues(t *testing.T) {
	c := Config{BindIP: "127.0.0.1", LLMModel: "m", EmbeddingModel: "e"}
	v, err := envValues(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range append(RandomSecrets, adminPasswordKey) {
		if len(v[k]) != passwordLength {
			t.Errorf("%s = %q", k, v[k])
		}
	}
	if v["USE_HTTPS"] != "false" || v["BIND_IP"] != "127.0.0.1" || v[adminUserKey] != "admin" {
		t.Errorf("values = %v", v)
	}
	for _, g := range stack.GitContexts {
		if !strings.HasSuffix(v[g.Var], "#"+g.Ref) || len(g.Ref) != 40 {
			t.Errorf("%s = %q", g.Var, v[g.Var])
		}
	}
	for _, a := range alfrescoAdmins {
		if len(v[a.Password]) != passwordLength || v[a.Hash] != AlfrescoHash(v[a.Password]) {
			t.Errorf("%s = %q, %s = %q", a.Password, v[a.Password], a.Hash, v[a.Hash])
		}
	}
	if v["ALFRESCO_ADMIN_PASSWORD"] == v["ALFRESCO_2_INTERNAL_PASSWORD"] {
		t.Error("both Alfresco instances got the same password")
	}
	if _, ok := v["NUXEO_PASSWORD"]; ok {
		t.Error("NUXEO_PASSWORD must keep its upstream value")
	}
	if _, ok := v["NGINX_ROOT_DIRECTIVE"]; ok {
		t.Error("NGINX_ROOT_DIRECTIVE set without the sample UI")
	}
	c.SampleUI = true
	if v, err = envValues(c, []string{"Branch office", "Head $office"}); err != nil {
		t.Fatal(err)
	}
	// The quotes keep Compose from expanding $demo_ui_upstream.
	if got := ParseEnv([]byte("X=" + v["NGINX_ROOT_DIRECTIVE"])); got["X"] != "proxy_pass http://$demo_ui_upstream;" || v["NGINX_ROOT_DIRECTIVE"][0] != '\'' {
		t.Errorf("NGINX_ROOT_DIRECTIVE = %q", v["NGINX_ROOT_DIRECTIVE"])
	}
	if v["ALFRESCO_1_DISPLAY_NAME"] != "'Branch office'" || v["ALFRESCO_2_DISPLAY_NAME"] != "'Head $office'" {
		t.Errorf("names = %q, %q", v["ALFRESCO_1_DISPLAY_NAME"], v["ALFRESCO_2_DISPLAY_NAME"])
	}
	c.SampleUI = false

	c.DefaultSecrets, c.DefaultUsers = true, true
	v, err = envValues(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range append(RandomSecrets, adminUserKey, adminPasswordKey, "ALFRESCO_ADMIN_PASSWORD", "ALFRESCO_ADMIN_PASSWORD_HASH") {
		if _, ok := v[k]; ok {
			t.Errorf("%s set with --default-secrets --default-users", k)
		}
	}
	// renderEnv sorts the keys, so the file is stable.
	if got := string(renderEnv(map[string]string{"B": "2", "A": "1"})); !strings.HasSuffix(got, "A=1\nB=2\n") {
		t.Errorf("renderEnv = %q", got)
	}
}

func TestRenderUsers(t *testing.T) {
	data, err := renderUsers("s3cret-Pass")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Users []struct {
			Username string
			Password string
			Groups   []string
		}
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if len(f.Users) != 1 || f.Users[0].Username != "admin" || !strings.HasPrefix(f.Users[0].Password, "{bcrypt}$2a$") {
		t.Fatalf("users = %+v", f.Users)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(f.Users[0].Password, "{bcrypt}")), []byte("s3cret-Pass")); err != nil {
		t.Error(err)
	}
}

func TestRenderOverride(t *testing.T) {
	got := string(renderOverride(Config{}))
	volume := []string{"- hxpr-data:/deployments/nxserver/data", "hxpr-data-init:\n        condition: service_completed_successfully",
		`command: ["chown", "33010:1000", "/data"]`, "network_mode: none", "volumes:\n  hxpr-data:\n"}
	for _, want := range append([]string{`"${BIND_IP:-127.0.0.1}:8090:8080"`, "OPENARCH_USERS_FILE: file:/etc/openarch/users.yml", "../openarch-users.yml:/etc/openarch/users.yml:ro"}, volume...) {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, got)
	}
	got = string(renderOverride(Config{DefaultUsers: true}))
	if strings.Contains(got, "users.yml") {
		t.Errorf("default users must not mount a users file:\n%s", got)
	}
	for _, want := range volume {
		if !strings.Contains(got, want) {
			t.Errorf("default users: missing %q in\n%s", want, got)
		}
	}
}

func TestComposeArgs(t *testing.T) {
	got := strings.Join(ComposeArgs(Config{Sources: srcOf("full", "alfresco-2")}), " ")
	want := strings.Join([]string{"compose -p openarch-app --project-directory upstream",
		"--env-file " + filepath.Join("upstream", ".env") + " --env-file .env.local",
		"-f " + filepath.Join("upstream", "compose.yaml") + " -f " + filepath.Join("upstream", "compose.alfresco-2.yaml"),
		"-f compose.override.yaml --profile full --profile alfresco-2"}, " ")
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// A managed nuxeo-deployment runs in the same project.
	got = strings.Join(ComposeArgs(Config{Sources: srcOf("nuxeo"), ManagedNuxeo: true}), " ")
	if !strings.Contains(got, "-f "+filepath.Join("upstream", "compose.nuxeo-deployment.yaml")+" -f compose.override.yaml") {
		t.Errorf("managed nuxeo: %s", got)
	}
	for _, c := range []Config{{Sources: srcOf("nuxeo")}, {Sources: srcOf("alfresco"), ManagedNuxeo: true}} {
		if got := strings.Join(ComposeArgs(c), " "); strings.Contains(got, "nuxeo-deployment") {
			t.Errorf("%+v: %s", c, got)
		}
	}
	// The sample UI adds the upstream platform profile after the others.
	got = strings.Join(ComposeArgs(Config{Sources: srcOf("alfresco", "alfresco-2"), SampleUI: true}), " ")
	if !strings.HasSuffix(got, "--profile alfresco --profile alfresco-2 --profile platform") {
		t.Errorf("sample UI: %s", got)
	}
	if got := strings.Join(ComposeArgs(Config{Sources: srcOf("full")}), " "); strings.Contains(got, "platform") {
		t.Errorf("no sample UI: %s", got)
	}
}

func TestConfigLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("missing file: %v", err)
	}
	c := Config{SchemaVersion: SchemaVersion, Upstream: "abc", Sources: srcOf("alfresco", "alfresco-2"), BindIP: "127.0.0.1", Context: "app", SampleUI: true}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || got.Sources != (stack.Sources{Alfresco: 2}) || got.Context != "app" || !got.SampleUI {
		t.Errorf("round trip = %+v, %v", got, err)
	}
	for _, bad := range []string{"schemaVersion: 3\n", "schemaVersion: 2\nunknown: x\n"} {
		if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadForUpgrade(dir); output.CodeOf(err) != output.ExitUsage {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A file of 0.2.0 or earlier needs 'upgrade', which reads it.
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte("schemaVersion: 1\nprofile: full\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var e *output.Error
	if _, err := Load(dir); !errors.As(err, &e) || !strings.Contains(e.Hint, "openarch app upgrade") {
		t.Errorf("version 1: %v", err)
	}
	if c, err := LoadForUpgrade(dir); err != nil || c.SchemaVersion != 1 || c.ManagedNuxeo {
		t.Errorf("LoadForUpgrade(version 1) = %+v, %v", c, err)
	}
}

func TestNextSteps(t *testing.T) {
	for _, c := range []struct {
		profile, dir, want string
	}{
		{"alfresco", "openarch-app", "openarch app models pull,openarch app up"},
		{"alfresco", "./openarch-app/", "openarch app models pull,openarch app up"},
		{"alfresco", "my-app", "openarch app models pull --dir my-app,openarch app up --dir my-app"},
		{"full", "openarch-app", "openarch app models pull,openarch app up   (the first start builds Nuxeo from source, which takes long)"},
	} {
		if got := strings.Join(nextSteps(Config{Sources: srcOf(c.profile), ManagedNuxeo: stack.NeedsNuxeo(c.profile)}, c.dir), ","); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.profile, c.dir, got, c.want)
		}
	}
}

func TestContextHost(t *testing.T) {
	for in, want := range map[string]string{"127.0.0.1": "127.0.0.1", "0.0.0.0": "127.0.0.1", "::": "127.0.0.1", "192.168.1.5": "192.168.1.5", "::1": "[::1]"} {
		if got := contextHost(in); got != want {
			t.Errorf("contextHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeSystem plays git and openarch for Init.
type fakeSystem struct {
	contextExists bool
	fetchFails    bool
	head          string
	calls         []string
	passwordIn    string
}

const upstreamEnv = "HXPR_USERNAME=admin\nHXPR_PASSWORD=password\nBIND_IP=0.0.0.0\n"

func (f *fakeSystem) env() Env {
	return Env{
		LookPath: func(file string) (string, error) { return "/usr/bin/" + file, nil },
		Run: func(_ context.Context, _ string, stdin io.Reader, name string, args ...string) ([]byte, error) {
			call := filepath.Base(name) + " " + strings.Join(args, " ")
			f.calls = append(f.calls, call)
			switch {
			case strings.HasPrefix(call, "openarch config get"):
				if f.contextExists {
					return []byte("{}"), nil
				}
				return nil, errors.New("context not found")
			case strings.HasPrefix(call, "openarch config set"):
				b, _ := io.ReadAll(stdin)
				f.passwordIn = string(b)
				return nil, nil
			case name == "git" && slices.Contains(args, "fetch"):
				if f.fetchFails {
					return nil, errors.New("git fetch: could not resolve host")
				}
				return nil, nil
			case name == "git" && slices.Contains(args, "init"):
				return nil, os.MkdirAll(args[len(args)-1], 0o755)
			case name == "git" && slices.Contains(args, "checkout"):
				// The checkout writes the upstream files.
				return nil, os.WriteFile(filepath.Join(args[1], ".env"), []byte(upstreamEnv), 0o644)
			case name == "git" && slices.Contains(args, "rev-parse"):
				head := f.head
				if head == "" {
					head = stack.UpstreamRef
				}
				if i := slices.Index(args, "-C"); i >= 0 && strings.HasPrefix(filepath.Base(args[i+1]), stack.NuxeoDeploymentDir) {
					head = stack.NuxeoDeploymentRef
				}
				return []byte(head + "\n"), nil
			}
			return nil, errors.New("unexpected call " + call)
		},
	}
}

func initOpts(dir string) InitOptions {
	return InitOptions{Dir: dir, Settings: Config{Sources: srcOf("alfresco"), LLMModel: "m", EmbeddingModel: "e", BindIP: "127.0.0.1", Context: "app"}}
}

func TestInit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	f := &fakeSystem{}
	res, err := Init(context.Background(), f.env(), initOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	local := ParseEnv(mustRead(t, filepath.Join(dir, EnvFile)))
	password := local[adminPasswordKey]
	if len(password) != passwordLength || f.passwordIn != password {
		t.Errorf("password in .env.local %q, on stdin %q", password, f.passwordIn)
	}
	users := string(mustRead(t, filepath.Join(dir, UsersFile)))
	hash := users[strings.Index(users, "{bcrypt}")+len("{bcrypt}"):]
	hash, _, _ = strings.Cut(hash, `"`)
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		t.Errorf("users file does not match HXPR_PASSWORD: %v", err)
	}
	if runtime.GOOS != "windows" {
		for name, mode := range map[string]os.FileMode{EnvFile: 0o600, UsersFile: 0o644, OverrideFile: 0o644, ConfigFile: 0o644} {
			if info, err := os.Stat(filepath.Join(dir, name)); err != nil || info.Mode().Perm() != mode {
				t.Errorf("%s mode = %v, want %v (%v)", name, info.Mode().Perm(), mode, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, UpstreamDir, ".env")); err != nil {
		t.Errorf("upstream checkout missing: %v", err)
	}
	c, err := Load(dir)
	if err != nil || c.Upstream != stack.UpstreamRef || c.Profile() != "alfresco" {
		t.Errorf("config = %+v, %v", c, err)
	}
	set := f.calls[len(f.calls)-1]
	if !strings.Contains(set, "config set --name app -s http://127.0.0.1:8090 -u admin --password-stdin --no-verify") || strings.Contains(set, password) {
		t.Errorf("config set call = %q", set)
	}
	if res.OpenArchURL != "http://127.0.0.1:8090" || strings.Join(res.Next, ",") != "openarch app models pull --dir "+dir+",openarch app up --dir "+dir {
		t.Errorf("result = %+v", res)
	}

	// A second init on the same folder is refused.
	if _, err := Init(context.Background(), f.env(), initOpts(dir)); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("second init: %v", err)
	}
}

func TestInitNuxeo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	f := &fakeSystem{}
	o := initOpts(dir)
	o.Settings.Sources = srcOf("full")
	res, err := Init(context.Background(), f.env(), o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Config.ManagedNuxeo || !strings.Contains(res.Next[1], "builds Nuxeo from source") {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, stack.NuxeoDeploymentDir, ".env")); err != nil {
		t.Errorf("nuxeo-deployment was not fetched: %v", err)
	}
	var fetches []string
	for _, c := range f.calls {
		if strings.Contains(c, " fetch ") {
			fetches = append(fetches, c)
		}
	}
	if len(fetches) != 2 || !strings.HasSuffix(fetches[1], stack.NuxeoDeploymentURL+" "+stack.NuxeoDeploymentRef) {
		t.Errorf("fetches = %v", fetches)
	}
	if c, err := Load(dir); err != nil || !c.ManagedNuxeo {
		t.Errorf("config = %+v, %v", c, err)
	}
}

func TestInitExistingContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	f := &fakeSystem{contextExists: true}
	_, err := Init(context.Background(), f.env(), initOpts(dir))
	var e *output.Error
	if !errors.As(err, &e) || e.Code != output.ExitUsage || !strings.Contains(e.Hint, "--force") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("init wrote files before it failed")
	}
	o := initOpts(dir)
	o.Force = true
	if _, err := Init(context.Background(), f.env(), o); err != nil {
		t.Errorf("--force: %v", err)
	}
}

func TestInitResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	if err := os.MkdirAll(filepath.Join(dir, UpstreamDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, UpstreamDir, ".env"), []byte(upstreamEnv), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, EnvFile), []byte("HXPR_PASSWORD=Kept-Pass1\nBIND_IP=0.0.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{}
	res, err := Init(context.Background(), f.env(), initOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if f.passwordIn != "Kept-Pass1" || res.Config.BindIP != "0.0.0.0" || len(res.Reused) != 2 {
		t.Errorf("password %q, result %+v", f.passwordIn, res)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "fetch") {
			t.Errorf("fetched again: %s", c)
		}
	}
	if got := string(mustRead(t, filepath.Join(dir, EnvFile))); got != "HXPR_PASSWORD=Kept-Pass1\nBIND_IP=0.0.0.0\n" {
		t.Errorf(".env.local was changed: %q", got)
	}
}

func TestInitWrongCheckout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	if err := os.MkdirAll(filepath.Join(dir, UpstreamDir), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystem{head: "0000000000000000000000000000000000000000"}
	if _, err := Init(context.Background(), f.env(), initOpts(dir)); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("err = %v", err)
	}
}

func TestInitFetchFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	f := &fakeSystem{fetchFails: true}
	_, err := Init(context.Background(), f.env(), initOpts(dir))
	if output.CodeOf(err) != output.ExitRemote {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, UpstreamDir)); !os.IsNotExist(err) {
		t.Error("a failed fetch left upstream/")
	}
}

func TestInitDefaultUsers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "setup")
	f := &fakeSystem{}
	o := initOpts(dir)
	o.Settings.DefaultUsers = true
	if _, err := Init(context.Background(), f.env(), o); err != nil {
		t.Fatal(err)
	}
	if f.passwordIn != "password" {
		t.Errorf("context password %q, want the upstream value", f.passwordIn)
	}
	if _, err := os.Stat(filepath.Join(dir, UsersFile)); !os.IsNotExist(err) {
		t.Error("users file written with --default-users")
	}
}

func TestInitValidation(t *testing.T) {
	for _, o := range []InitOptions{
		{Settings: Config{}},
		{Settings: Config{Sources: stack.Sources{Alfresco: 3}}},
		{Settings: Config{Sources: stack.Sources{Alfresco: 1, Nuxeo: 2}}},
	} {
		o.Dir = t.TempDir()
		if _, err := Init(context.Background(), (&fakeSystem{}).env(), o); output.CodeOf(err) != output.ExitUsage {
			t.Errorf("%+v: %v", o.Settings, err)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInitResultSampleUI(t *testing.T) {
	var b strings.Builder
	r := InitResult{Dir: "d", Config: Config{Sources: srcOf("alfresco", "alfresco-2"), SampleUI: true}}
	if err := r.WriteText(&b); err != nil || !strings.Contains(b.String(), "in d (2 Alfresco, sample UI).") {
		t.Errorf("text = %q, %v", b.String(), err)
	}
}

func TestAlfrescoNames(t *testing.T) {
	two := Config{Sources: stack.Sources{Alfresco: 2}}
	if got, err := alfrescoNames(two, nil); err != nil || strings.Join(got, ",") != "Alfresco 1,Alfresco 2" {
		t.Errorf("defaults = %v, %v", got, err)
	}
	if got, err := alfrescoNames(two, []string{"Branch", "Head Office"}); err != nil || got[1] != "Head Office" {
		t.Errorf("names = %v, %v", got, err)
	}
	if got, err := alfrescoNames(Config{Sources: stack.Sources{Alfresco: 1, Nuxeo: 1}}, nil); err != nil || got != nil {
		t.Errorf("one Alfresco = %v, %v", got, err)
	}
	for _, c := range []struct {
		c     Config
		names []string
	}{
		{Config{Sources: stack.Sources{Alfresco: 1}}, []string{"A"}},
		{two, []string{"only one"}},
		{two, []string{"A", " "}},
		{two, []string{"A", "it's"}},
		{two, []string{"A", "a\nb"}},
		{two, []string{"A", strings.Repeat("x", maxNameLength+1)}},
	} {
		if _, err := alfrescoNames(c.c, c.names); output.CodeOf(err) != output.ExitUsage {
			t.Errorf("%v %q: %v", c.c.Sources, c.names, err)
		}
	}
}
