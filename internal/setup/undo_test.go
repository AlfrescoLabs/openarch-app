package setup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// fakeRepository plays the REST API of an Alfresco with cl: aspects. The
// search index lists a node for one more search after its aspects are gone,
// and a node in late joins the index only at search number lateAt.
type fakeRepository struct {
	aspects  map[string][]string
	stale    map[string]bool
	late     map[string]int
	locked   map[string]bool
	searches int
	puts     []string
}

func (f *fakeRepository) RoundTrip(r *http.Request) (*http.Response, error) {
	reply := func(code int, v any) (*http.Response, error) {
		b, _ := json.Marshal(v)
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	}
	path := r.URL.Path
	id := path[strings.LastIndex(path, "/")+1:]
	switch {
	case strings.HasSuffix(path, "/search/versions/1/search"):
		f.searches++
		var entries []map[string]any
		for id, as := range f.aspects {
			if at, ok := f.late[id]; ok && f.searches < at {
				continue
			}
			if slices.ContainsFunc(as, func(a string) bool { return strings.HasPrefix(a, "cl:") }) || f.stale[id] {
				entries = append(entries, map[string]any{"entry": map[string]string{"id": id}})
			}
		}
		f.stale = map[string]bool{}
		return reply(http.StatusOK, map[string]any{"list": map[string]any{"pagination": map[string]bool{"hasMoreItems": false}, "entries": entries}})
	case r.Method == http.MethodGet:
		as, ok := f.aspects[id]
		if !ok {
			return reply(http.StatusNotFound, map[string]any{})
		}
		return reply(http.StatusOK, map[string]any{"entry": map[string]any{"aspectNames": as}})
	case r.Method == http.MethodPut:
		if f.locked[id] {
			return reply(http.StatusConflict, map[string]any{})
		}
		var body struct {
			AspectNames []string `json:"aspectNames"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.aspects[id] = body.AspectNames
		f.stale[id] = true
		f.puts = append(f.puts, id)
		return reply(http.StatusOK, map[string]any{})
	}
	return nil, errors.New("unexpected " + r.Method + " " + path)
}

func newRepository() *fakeRepository {
	return &fakeRepository{aspects: map[string][]string{
		"folder": {"cm:titled", "cl:indexed"},
		"doc":    {"cm:versionable", "cl:syncStatus"},
		"other":  {"cm:titled"},
	}, stale: map[string]bool{}, late: map[string]int{}, locked: map[string]bool{}}
}

var fastUndo = UndoOptions{Wait: time.Millisecond}

func TestUndo(t *testing.T) {
	s, project := prepareSetup(t)
	override := filepath.Join(project, "compose.override.yaml")
	if err := os.WriteFile(override, []byte(OverrideHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ProjectAssets), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := newRepository()
	// A node that changed just before the ingesters stopped reaches the index
	// only at the third search: the first one finds nothing at all.
	repo.aspects["late"] = []string{"cl:syncStatus"}
	repo.late = map[string]int{"late": 3, "folder": 3, "doc": 3}
	f := &fakePrepareDocker{config: preparedConfig(t, true)}
	env := f.env()
	env.HTTP = &http.Client{Transport: repo}
	res, err := Undo(context.Background(), env, s, fastUndo, Streams{Out: io.Discard, Err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleaned != 3 || !res.Restored || strings.Join(repo.aspects["folder"], ",") != "cm:titled" || strings.Join(repo.aspects["late"], ",") != "" {
		t.Errorf("result %+v, aspects %v", res, repo.aspects)
	}
	for _, gone := range []string{override, filepath.Join(project, ProjectAssets)} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s still there", gone)
		}
	}
	got := strings.Join(f.builds, "\n")
	for _, want := range []string{"up -d --wait proxy", "stop batch-ingester live-ingester", "compose up -d", "down"} {
		if !strings.Contains(got, want) {
			t.Errorf("streams lack %q:\n%s", want, got)
		}
	}
	// A project that does not mount the module has nothing to undo.
	f.config = preparedConfig(t, false)
	if _, err := Undo(context.Background(), env, s, fastUndo, Streams{Out: io.Discard, Err: io.Discard}); output.CodeOf(err) != output.ExitUsage || !strings.Contains(err.Error(), "nothing to undo") {
		t.Errorf("not prepared: %v", err)
	}
}

func TestUndoNodeErrors(t *testing.T) {
	s, project := prepareSetup(t)
	if err := os.WriteFile(filepath.Join(project, "compose.override.yaml"), []byte(OverrideHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := newRepository()
	// A node that the index lists but that is gone is skipped; a locked node
	// is reported, and the project keeps the module.
	repo.aspects["locked"] = []string{"cl:syncStatus"}
	repo.locked["locked"] = true
	f := &fakePrepareDocker{config: preparedConfig(t, true)}
	env := f.env()
	env.HTTP = &http.Client{Transport: repo}
	_, err := Undo(context.Background(), env, s, fastUndo, Streams{Out: io.Discard, Err: io.Discard})
	if output.CodeOf(err) != output.ExitRemote || !strings.Contains(err.Error(), "locked: the existing Alfresco answered 409") {
		t.Errorf("locked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "compose.override.yaml")); err != nil {
		t.Error("the override was removed although a node failed")
	}
	if !slices.Contains(repo.puts, "doc") || !slices.Contains(repo.puts, "folder") {
		t.Errorf("the other nodes were not cleaned: %v", repo.puts)
	}
}

func TestUndoGivesUp(t *testing.T) {
	s, project := prepareSetup(t)
	if err := os.WriteFile(filepath.Join(project, "compose.override.yaml"), []byte(OverrideHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := newRepository()
	f := &fakePrepareDocker{config: preparedConfig(t, true)}
	env := f.env()
	env.HTTP = &http.Client{Transport: repo}
	// Two rounds cannot give three quiet searches.
	_, err := Undo(context.Background(), env, s, UndoOptions{Wait: time.Millisecond, Rounds: 2}, Streams{Out: io.Discard, Err: io.Discard})
	if output.CodeOf(err) != output.ExitUnhealthy || !strings.Contains(err.Error(), "still lists") {
		t.Errorf("rounds: %v", err)
	}
}

func TestUndoUserOverride(t *testing.T) {
	s, project := prepareSetup(t)
	user := filepath.Join(project, "docker-compose.override.yml")
	if err := os.WriteFile(user, []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newRepository()
	f := &fakePrepareDocker{config: preparedConfig(t, true)}
	env := f.env()
	env.HTTP = &http.Client{Transport: repo}
	res, err := Undo(context.Background(), env, s, fastUndo, Streams{Out: io.Discard, Err: io.Discard})
	if err != nil || res.Restored || res.Cleaned != 2 || !strings.Contains(strings.Join(res.Steps, "\n"), "Remove the App for OpenArch lines from "+user) {
		t.Errorf("merged override: %+v, %v", res, err)
	}
	if string(mustRead(t, user)) != "services: {}\n" {
		t.Error("the user's override changed")
	}
}

func TestUndoAlfrescoEnv(t *testing.T) {
	s := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app", ExternalAlfresco: "http://alfresco.example:8080"})
	repo := newRepository()
	var streams []string
	env := Env{HTTP: &http.Client{Transport: repo}, Stream: func(_ context.Context, _ string, _ []string, _, _ io.Writer, _ string, args ...string) error {
		streams = append(streams, strings.Join(args, " "))
		return nil
	}}
	res, err := Undo(context.Background(), env, s, fastUndo, Streams{Out: io.Discard, Err: io.Discard})
	if err != nil || res.Cleaned != 2 || res.Restored || !strings.Contains(strings.Join(res.Steps, "\n"), "Remove the module JAR") {
		t.Errorf("alfresco-env: %+v, %v", res, err)
	}
	if strings.Contains(strings.Join(streams, "\n"), "network") {
		t.Errorf("no shared network for --alfresco-env: %v", streams)
	}
	plain := newSetup(t, Config{Sources: srcOf("alfresco"), Context: "app"})
	if _, err := Undo(context.Background(), Env{}, plain, fastUndo, Streams{Out: io.Discard, Err: io.Discard}); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("bundled Alfresco: %v", err)
	}
}
