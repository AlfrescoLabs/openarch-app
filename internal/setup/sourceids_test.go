package setup

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

const (
	discovery1 = "http://127.0.0.1:80/alfresco/api/discovery"
	discovery2 = "http://127.0.0.1:8082/alfresco/api/discovery"
)

func twoAlfrescoSetup(t *testing.T) Setup {
	t.Helper()
	s := newSetup(t, Config{Extras: []string{"alfresco-2"}})
	local := string(mustRead(t, filepath.Join(s.Dir, EnvFile))) + "ALFRESCO_ADMIN_PASSWORD=Pw-One-1\nALFRESCO_2_INTERNAL_PASSWORD=Pw-Two-2\n"
	if err := os.WriteFile(filepath.Join(s.Dir, EnvFile), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSyncSourceIDs(t *testing.T) {
	s := twoAlfrescoSetup(t)
	f := &fakeDocker{repoIDs: map[string]string{discovery1: "uuid-1", discovery2: "uuid-2"}}
	ids, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60)
	if err != nil {
		t.Fatal(err)
	}
	if ids["ALFRESCO_1_SOURCE_ID"] != "uuid-1" || ids["ALFRESCO_2_SOURCE_ID"] != "uuid-2" {
		t.Errorf("ids = %v", ids)
	}
	// Each instance is asked with its own admin login.
	if strings.Join(f.gets, ",") != discovery1+" admin:Pw-One-1,"+discovery2+" admin:Pw-Two-2" {
		t.Errorf("gets = %v", f.gets)
	}
	local := ParseEnv(mustRead(t, filepath.Join(s.Dir, EnvFile)))
	if local["ALFRESCO_1_SOURCE_ID"] != "uuid-1" || local["ALFRESCO_2_SOURCE_ID"] != "uuid-2" || local["ALFRESCO_ADMIN_PASSWORD"] != "Pw-One-1" {
		t.Errorf(".env.local = %v", local)
	}
	if len(f.calls) != 1 || !strings.HasSuffix(f.calls[0], "up -d --no-deps --wait --wait-timeout 60 rag-service") {
		t.Errorf("calls = %v", f.calls)
	}

	// The same ids again: nothing to write, no restart.
	f.calls = nil
	if _, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60); err != nil || len(f.calls) != 0 {
		t.Errorf("second run: calls %v, %v", f.calls, err)
	}

	// After 'down -v' the second repository has a new id.
	f.repoIDs[discovery2] = "uuid-2b"
	if ids, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60); err != nil || ids["ALFRESCO_2_SOURCE_ID"] != "uuid-2b" || len(f.calls) != 1 {
		t.Errorf("new repository: %v, calls %v, %v", ids, f.calls, err)
	}
	if got := ParseEnv(mustRead(t, filepath.Join(s.Dir, EnvFile)))["ALFRESCO_2_SOURCE_ID"]; got != "uuid-2b" {
		t.Errorf("ALFRESCO_2_SOURCE_ID = %q", got)
	}
}

func TestSyncSourceIDsFails(t *testing.T) {
	s := twoAlfrescoSetup(t)
	f := &fakeDocker{repoIDs: map[string]string{discovery1: "uuid-1"}}
	_, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60)
	if output.CodeOf(err) != output.ExitRemote || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := ParseEnv(mustRead(t, filepath.Join(s.Dir, EnvFile)))["ALFRESCO_1_SOURCE_ID"]; ok {
		t.Error("an id was written although the second instance failed")
	}
}

func TestSyncSourceIDsOneAlfresco(t *testing.T) {
	f := &fakeDocker{}
	ids, err := SyncSourceIDs(context.Background(), f.env(), newSetup(t, Config{}), Streams{Err: io.Discard}, 60)
	if ids != nil || err != nil || len(f.gets) != 0 {
		t.Errorf("ids %v, gets %v, %v", ids, f.gets, err)
	}
}

func TestUpSetsSourceIDs(t *testing.T) {
	s := twoAlfrescoSetup(t)
	f := &fakeDocker{repoIDs: map[string]string{discovery1: "uuid-1", discovery2: "uuid-2"}}
	res, err := Up(context.Background(), f.env(), s, UpOptions{Wait: true, WaitTimeout: 60}, Streams{Err: io.Discard})
	if err != nil || res.SourceIDs["ALFRESCO_2_SOURCE_ID"] != "uuid-2" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	// Without the wait the repositories may not be up yet: the next up sets them.
	f = &fakeDocker{}
	if res, err := Up(context.Background(), f.env(), twoAlfrescoSetup(t), UpOptions{}, Streams{Err: io.Discard}); err != nil || res.SourceIDs != nil || len(f.gets) != 0 {
		t.Errorf("no wait: %+v, gets %v, %v", res, f.gets, err)
	}
}

func TestSmokeTestTwoAlfresco(t *testing.T) {
	f := &fakeDocker{scriptOut: "[PASS] a\n[PASS] b\n"}
	res, err := SmokeTest(context.Background(), f.env(), twoAlfrescoSetup(t), io.Discard, Streams{Err: io.Discard})
	if err != nil || !res.OK || res.Passed != 4 || res.Script != "test/test-alfresco.sh + test/test-two-alfresco.sh" {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if len(f.calls) != 2 || f.calls[1] != "bash upstream/test/test-two-alfresco.sh" {
		t.Errorf("calls = %v", f.calls)
	}
	if env := strings.Join(f.extraEnv, " "); !strings.Contains(env, "ALF_AUTH=admin:Pw-One-1") || !strings.Contains(env, "ALF2_AUTH=admin:Pw-Two-2") {
		t.Errorf("env = %s", env)
	}
}
