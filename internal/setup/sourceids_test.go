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
	discovery1 = "alfresco"
	discovery2 = "alfresco-2"
)

// upCalls returns the 'docker compose up' calls, without the discovery calls.
func upCalls(calls []string) []string {
	var ups []string
	for _, c := range calls {
		if strings.Contains(c, " up -d") {
			ups = append(ups, c)
		}
	}
	return ups
}

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
	ids, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60, true)
	if err != nil {
		t.Fatal(err)
	}
	if ids["ALFRESCO_1_SOURCE_ID"] != "uuid-1" || ids["ALFRESCO_2_SOURCE_ID"] != "uuid-2" {
		t.Errorf("ids = %v", ids)
	}
	// Each instance is asked inside its own container, with its own admin login
	// on stdin.
	if strings.Join(f.gets, ",") != `alfresco user = "admin:Pw-One-1",alfresco-2 user = "admin:Pw-Two-2"` {
		t.Errorf("gets = %v", f.gets)
	}
	local := ParseEnv(mustRead(t, filepath.Join(s.Dir, EnvFile)))
	if local["ALFRESCO_1_SOURCE_ID"] != "uuid-1" || local["ALFRESCO_2_SOURCE_ID"] != "uuid-2" || local["ALFRESCO_ADMIN_PASSWORD"] != "Pw-One-1" {
		t.Errorf(".env.local = %v", local)
	}
	if ups := upCalls(f.calls); len(ups) != 1 || !strings.HasSuffix(ups[0], "up -d --no-deps --wait --wait-timeout 60 rag-service") {
		t.Errorf("calls = %v", f.calls)
	}

	// The same ids again: nothing to write, no restart.
	f.calls = nil
	if _, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60, true); err != nil || len(upCalls(f.calls)) != 0 {
		t.Errorf("second run: calls %v, %v", f.calls, err)
	}

	// After 'down -v' the second repository has a new id.
	f.repoIDs[discovery2] = "uuid-2b"
	f.calls = nil
	if ids, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60, true); err != nil || ids["ALFRESCO_2_SOURCE_ID"] != "uuid-2b" || len(upCalls(f.calls)) != 1 {
		t.Errorf("new repository: %v, calls %v, %v", ids, f.calls, err)
	}
	if got := ParseEnv(mustRead(t, filepath.Join(s.Dir, EnvFile)))["ALFRESCO_2_SOURCE_ID"]; got != "uuid-2b" {
		t.Errorf("ALFRESCO_2_SOURCE_ID = %q", got)
	}
}

func TestSyncSourceIDsFails(t *testing.T) {
	s := twoAlfrescoSetup(t)
	f := &fakeDocker{repoIDs: map[string]string{discovery1: "uuid-1"}}
	_, err := SyncSourceIDs(context.Background(), f.env(), s, Streams{Err: io.Discard}, 60, true)
	if output.CodeOf(err) != output.ExitRemote || !strings.Contains(err.Error(), "alfresco-2") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := ParseEnv(mustRead(t, filepath.Join(s.Dir, EnvFile)))["ALFRESCO_1_SOURCE_ID"]; ok {
		t.Error("an id was written although the second instance failed")
	}
}

func TestSyncSourceIDsOneAlfresco(t *testing.T) {
	f := &fakeDocker{}
	ids, err := SyncSourceIDs(context.Background(), f.env(), newSetup(t, Config{}), Streams{Err: io.Discard}, 60, true)
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
	// rag-service cannot start without the ids, so the two repositories start
	// alone first. The ids are then in place, and rag-service needs no restart.
	if ups := upCalls(f.calls); len(ups) != 2 || !strings.HasSuffix(ups[0], "up -d --build --wait --wait-timeout 60 alfresco alfresco-2") ||
		!strings.HasSuffix(ups[1], "up -d --build --wait --wait-timeout 60") {
		t.Errorf("calls = %v", f.calls)
	}

	// --no-wait still waits for the two repositories when an id is missing.
	f = &fakeDocker{repoIDs: map[string]string{discovery1: "uuid-1", discovery2: "uuid-2"}}
	s = twoAlfrescoSetup(t)
	if _, err := Up(context.Background(), f.env(), s, UpOptions{WaitTimeout: 60}, Streams{Err: io.Discard}); err != nil || len(f.gets) != 2 {
		t.Fatalf("no wait, no ids: gets %v, %v", f.gets, err)
	}
	if ups := upCalls(f.calls); len(ups) != 2 || !strings.HasSuffix(ups[1], "up -d --build") {
		t.Errorf("calls = %v", f.calls)
	}
	// With the ids set, --no-wait does not ask the repositories at all.
	f.calls, f.gets = nil, nil
	if res, err := Up(context.Background(), f.env(), s, UpOptions{WaitTimeout: 60}, Streams{Err: io.Discard}); err != nil || res.SourceIDs != nil || len(f.gets) != 0 || len(upCalls(f.calls)) != 1 {
		t.Errorf("no wait, ids set: %+v, gets %v, calls %v, %v", res, f.gets, f.calls, err)
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
