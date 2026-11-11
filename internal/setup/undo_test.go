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
// search index lists a node for one more search after its aspects are gone.
type fakeRepository struct {
	aspects map[string][]string
	stale   map[string]bool
	puts    []string
}

func (f *fakeRepository) RoundTrip(r *http.Request) (*http.Response, error) {
	reply := func(v any) (*http.Response, error) {
		b, _ := json.Marshal(v)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(b))), Request: r}, nil
	}
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/search/versions/1/search"):
		var entries []map[string]any
		for id, as := range f.aspects {
			if slices.ContainsFunc(as, func(a string) bool { return strings.HasPrefix(a, "cl:") }) || f.stale[id] {
				entries = append(entries, map[string]any{"entry": map[string]string{"id": id}})
			}
		}
		f.stale = map[string]bool{}
		return reply(map[string]any{"list": map[string]any{"pagination": map[string]bool{"hasMoreItems": false}, "entries": entries}})
	case r.Method == http.MethodGet:
		id := path[strings.LastIndex(path, "/")+1:]
		return reply(map[string]any{"entry": map[string]any{"aspectNames": f.aspects[id]}})
	case r.Method == http.MethodPut:
		id := path[strings.LastIndex(path, "/")+1:]
		var body struct {
			AspectNames []string `json:"aspectNames"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.aspects[id] = body.AspectNames
		f.stale[id] = true
		f.puts = append(f.puts, id+"="+strings.Join(body.AspectNames, ","))
		return reply(map[string]any{})
	}
	return nil, errors.New("unexpected " + r.Method + " " + path)
}

func TestUndo(t *testing.T) {
	s, project := prepareSetup(t)
	override := filepath.Join(project, "docker-compose.override.yml")
	if err := os.WriteFile(override, []byte(OverrideHeader), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepository{aspects: map[string][]string{
		"folder": {"cm:titled", "cl:indexed"},
		"doc":    {"cm:versionable", "cl:syncStatus"},
		"other":  {"cm:titled"},
	}, stale: map[string]bool{}}
	var streams []string
	env := Env{
		HTTP: &http.Client{Transport: repo},
		Run:  func(context.Context, string, io.Reader, string, ...string) ([]byte, error) { return nil, nil },
		Stream: func(_ context.Context, dir string, _ []string, _, _ io.Writer, _ string, args ...string) error {
			streams = append(streams, filepath.Base(dir)+": "+strings.Join(args[len(args)-min(len(args), 4):], " "))
			return nil
		},
	}
	res, err := Undo(context.Background(), env, s, UndoOptions{Wait: time.Millisecond}, Streams{Out: io.Discard, Err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleaned != 2 || strings.Join(repo.aspects["folder"], ",") != "cm:titled" || strings.Join(repo.aspects["doc"], ",") != "cm:versionable" {
		t.Errorf("result %+v, aspects %v", res, repo.aspects)
	}
	if _, err := os.Stat(override); err == nil {
		t.Error("override file still there")
	}
	got := strings.Join(streams, "\n")
	for _, want := range []string{"up -d --wait proxy", "stop batch-ingester live-ingester", filepath.Base(project) + ": compose up -d", "down"} {
		if !strings.Contains(got, want) {
			t.Errorf("streams lack %q:\n%s", want, got)
		}
	}
	// The ingesters stop before the aspects go, and the project changes after.
	if strings.Index(got, "stop batch-ingester") > strings.Index(got, filepath.Base(project)+": compose up -d") {
		t.Errorf("order:\n%s", got)
	}

	// Nothing to undo without the plugin's file.
	if _, err := Undo(context.Background(), env, s, UndoOptions{}, Streams{Out: io.Discard, Err: io.Discard}); output.CodeOf(err) != output.ExitUsage {
		t.Errorf("second undo: %v", err)
	}
}
