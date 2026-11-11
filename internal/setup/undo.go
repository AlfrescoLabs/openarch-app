package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// clAspects are the aspects of content-lake-repo-model. A node that keeps one
// of them after the module is removed cannot be read or updated any more: the
// repository answers 500, "A namespace prefix is not registered".
var clAspects = []string{"cl:indexed", "cl:syncStatus", "cl:fileScope"}

// UndoOptions select how Undo works.
type UndoOptions struct {
	// Confirm is asked before Undo changes anything.
	Confirm func(project string) error
	// Wait is the time between two searches. Zero means 10 seconds.
	Wait time.Duration
	// Quiet is the number of searches in a row that must change no node before
	// Undo trusts the search index. The time they take must be longer than the
	// polling interval of the search indexer (15 s for alfresco-docker-installer),
	// so a node that changed just before the ingesters stopped is indexed. Zero
	// means 3.
	Quiet int
	// Rounds is the most searches. Zero means 30.
	Rounds int
}

// UndoResult is the outcome of Undo.
type UndoResult struct {
	Project string `json:"project,omitempty"`
	// Cleaned is the number of nodes whose cl: aspects were removed.
	Cleaned int `json:"cleaned"`
	// Restored says whether the plugin also removed its changes from the
	// project. Otherwise Steps say what the user removes.
	Restored bool     `json:"restored"`
	Steps    []string `json:"steps,omitempty"`
}

// WriteText writes the human-readable form.
func (r UndoResult) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Removed the cl: aspects from %d nodes. The stack is stopped.\n", r.Cleaned)
	if r.Restored {
		fmt.Fprintf(&b, "Removed the override file and the %s folder, and recreated the changed services of %s.\n", ProjectAssets, r.Project)
	}
	if len(r.Steps) > 0 {
		b.WriteString("\nNow, on the Alfresco side:\n")
		for i, s := range r.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
	}
	b.WriteString("The index of the stack still holds the documents: delete the setup folder and its volumes if you do not need them.\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// Undo removes the cl: aspects from every node of the existing Alfresco, while
// the module is still installed, because nodes that keep them break once it is
// gone. The ingesters stop first, so none of them adds an aspect again; the
// stack proxy keeps running, because the plugin reaches the repository through
// it. For a Compose project with the plugin's own override file, Undo then
// removes that file and the mounted files, and recreates the services. In
// every other case it prints what the user removes.
func Undo(ctx context.Context, env Env, s Setup, opts UndoOptions, st Streams) (UndoResult, error) {
	res := UndoResult{Project: s.Config.AlfrescoCompose}
	if s.Config.ExternalAlfresco == "" {
		return res, output.Usage("the setup has no existing Alfresco", "")
	}
	var l projectLayout
	restore := false
	if s.Config.AlfrescoCompose != "" {
		var err error
		if l, err = projectFiles(s.Config.AlfrescoCompose); err != nil {
			return res, err
		}
		p, err := InspectAlfrescoProject(ctx, env, s.Config.AlfrescoCompose)
		if err != nil {
			return res, err
		}
		if !p.Prepared() {
			return res, output.Usage("the repository of "+s.Config.AlfrescoCompose+" does not mount the module: nothing to undo",
				"If nodes answer 500 because the module was removed by hand, run 'openarch app alfresco-prepare --apply' first, then --undo.")
		}
		restore = l.ours && l.composeFile == ""
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(s.Config.ExternalAlfresco); err != nil {
			return res, err
		}
	}
	if s.Config.AlfrescoCompose != "" {
		if err := EnsureSharedNetwork(ctx, env); err != nil {
			return res, err
		}
	}
	for _, args := range [][]string{{"up", "-d", "--wait", "proxy"}, {"stop", "batch-ingester", "live-ingester"}} {
		if err := env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", s.compose(args...)...); err != nil {
			return res, output.NewError(output.ExitUnhealthy, "cannot prepare the stack for the undo: "+err.Error(), "")
		}
	}
	values, err := s.env()
	if err != nil {
		return res, err
	}
	a := alfrescoAPI{env: env, base: "http://" + contextHost(s.Config.BindIP) + "/alfresco/api/-default-/public",
		user: values["ALFRESCO_ADMIN_USER"], password: values["ALFRESCO_ADMIN_PASSWORD"]}
	if res.Cleaned, err = a.removeAspects(ctx, opts); err != nil {
		return res, err
	}
	switch {
	case restore:
		// Without the plugin's files, Compose recreates the services from the
		// user's files again.
		if err := os.Remove(l.override); err != nil {
			return res, err
		}
		if err := os.RemoveAll(filepath.Join(s.Config.AlfrescoCompose, ProjectAssets)); err != nil {
			return res, err
		}
		if err := env.Stream(ctx, s.Config.AlfrescoCompose, nil, st.Err, st.Err, "docker", "compose", "up", "-d"); err != nil {
			return res, output.NewError(output.ExitUnhealthy, "the Alfresco project did not start again: "+err.Error(), "See 'docker compose logs' in "+s.Config.AlfrescoCompose+".")
		}
		res.Restored = true
	case s.Config.AlfrescoCompose != "":
		res.Steps = []string{
			"Remove the App for OpenArch lines from " + l.override + " (and from COMPOSE_FILE, if you added the file there), and delete " + filepath.Join(s.Config.AlfrescoCompose, ProjectAssets) + ".",
			"Run 'docker compose up -d' in " + s.Config.AlfrescoCompose + ".",
		}
	default:
		res.Steps = []string{
			"Remove the module JAR and the content.lake.permission.sync.* properties from the repository.",
			"Restart the repository.",
		}
	}
	if err := Down(ctx, env, s, st); err != nil {
		return res, err
	}
	return res, nil
}

// alfrescoAPI calls the public REST API of the existing Alfresco.
type alfrescoAPI struct {
	env            Env
	base           string
	user, password string
}

// apiError is an answer of the REST API that is not 2xx.
type apiError struct {
	code   int
	method string
	path   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("the existing Alfresco answered %d to %s %s", e.code, e.method, e.path)
}

func (a alfrescoAPI) do(ctx context.Context, method, path string, body, v any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, r)
	if err != nil {
		return err
	}
	req.SetBasicAuth(a.user, a.password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.env.HTTP.Do(req)
	if err != nil {
		return output.NewError(output.ExitUnhealthy, "the existing Alfresco does not answer through the proxy: "+err.Error(), "")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return &apiError{code: resp.StatusCode, method: method, path: req.URL.Path}
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
}

// searchPage is the most nodes that one search returns.
const searchPage = 1000

// searchCl returns the ids of the first nodes that the search index lists with
// a cl: aspect, and whether it lists more. Each round reads the first page
// again: cleaned nodes leave the list, and deep paging fails past the result
// window of the index.
func (a alfrescoAPI) searchCl(ctx context.Context) ([]string, bool, error) {
	var q []string
	for _, as := range clAspects {
		q = append(q, `ASPECT:"`+as+`"`)
	}
	var out struct {
		List struct {
			Pagination struct {
				HasMoreItems bool `json:"hasMoreItems"`
			} `json:"pagination"`
			Entries []struct {
				Entry struct {
					ID string `json:"id"`
				} `json:"entry"`
			} `json:"entries"`
		} `json:"list"`
	}
	body := map[string]any{"query": map[string]string{"query": strings.Join(q, " OR ")}, "paging": map[string]int{"maxItems": searchPage}}
	if err := a.do(ctx, http.MethodPost, "/search/versions/1/search", body, &out); err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(out.List.Entries))
	for _, e := range out.List.Entries {
		ids = append(ids, e.Entry.ID)
	}
	return ids, out.List.Pagination.HasMoreItems, nil
}

// removeAspects removes the cl: aspects from every node that has one. It stops
// when Quiet searches in a row changed no node and listed no more than one
// page. A node that is gone is skipped. A node that cannot be changed (locked,
// checked out) is reported at the end, and the module stays installed.
func (a alfrescoAPI) removeAspects(ctx context.Context, opts UndoOptions) (int, error) {
	wait, quiet, rounds := opts.Wait, opts.Quiet, opts.Rounds
	if wait == 0 {
		wait = 10 * time.Second
	}
	if quiet == 0 {
		quiet = 3
	}
	if rounds == 0 {
		rounds = 30
	}
	cleaned := map[string]bool{}
	failed := map[string]string{}
	calm := 0
	for round := 0; round < rounds && calm < quiet; round++ {
		if round > 0 {
			select {
			case <-ctx.Done():
				return len(cleaned), ctx.Err()
			case <-time.After(wait):
			}
		}
		ids, more, err := a.searchCl(ctx)
		if err != nil {
			return len(cleaned), err
		}
		changed := 0
		for _, id := range ids {
			if _, ok := failed[id]; ok {
				continue
			}
			var node struct {
				Entry struct {
					AspectNames []string `json:"aspectNames"`
				} `json:"entry"`
			}
			if err := a.do(ctx, http.MethodGet, "/alfresco/versions/1/nodes/"+id, nil, &node); err != nil {
				if !gone(err) {
					failed[id] = err.Error()
				}
				continue
			}
			keep := slices.DeleteFunc(slices.Clone(node.Entry.AspectNames), func(s string) bool { return strings.HasPrefix(s, "cl:") })
			if len(keep) == len(node.Entry.AspectNames) {
				// The index has not caught up with an earlier removal.
				continue
			}
			if err := a.do(ctx, http.MethodPut, "/alfresco/versions/1/nodes/"+id, map[string]any{"aspectNames": keep}, nil); err != nil {
				if !gone(err) {
					failed[id] = err.Error()
				}
				continue
			}
			cleaned[id] = true
			changed++
		}
		if changed == 0 && !more {
			calm++
		} else {
			calm = 0
		}
	}
	if len(failed) > 0 {
		var list []string
		for id, why := range failed {
			list = append(list, id+": "+why)
		}
		slices.Sort(list)
		if len(list) > 10 {
			list = append(list[:10], fmt.Sprintf("and %d more", len(failed)-10))
		}
		return len(cleaned), output.NewError(output.ExitRemote, fmt.Sprintf("cannot remove the cl: aspects from %d nodes:\n  %s", len(failed), strings.Join(list, "\n  ")),
			"Unlock them or cancel their checkout, then run 'openarch app alfresco-prepare --undo' again. The module is still installed.")
	}
	if calm < quiet {
		return len(cleaned), output.NewError(output.ExitUnhealthy, "the search index still lists nodes with cl: aspects",
			"Run 'openarch app alfresco-prepare --undo' again in a few minutes. The module is still installed.")
	}
	return len(cleaned), nil
}

// gone reports whether err says that the node no longer exists.
func gone(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.code == http.StatusNotFound
}
