package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	// Confirm is asked before Undo changes the project.
	Confirm func(project string) error
	// Wait is the time between two searches for nodes that still have a cl:
	// aspect, because the search index lags. Zero means 5 seconds.
	Wait time.Duration
	// Rounds is the most searches. Zero means 12.
	Rounds int
}

// UndoResult is the outcome of Undo.
type UndoResult struct {
	Project string `json:"project"`
	// Cleaned is the number of nodes whose cl: aspects were removed.
	Cleaned int `json:"cleaned"`
}

// WriteText writes the human-readable form.
func (r UndoResult) WriteText(w io.Writer) error {
	_, err := fmt.Fprintf(w, "Removed the cl: aspects from %d nodes, removed the override file, and recreated the changed services of %s.\n"+
		"The stack is stopped. Its index still holds the documents: delete the setup folder and its volumes if you do not need them.\n", r.Cleaned, r.Project)
	return err
}

// Undo removes what Prepare applied to the existing Alfresco project. It first
// removes the cl: aspects from every node, while the module is still installed,
// because nodes that keep them break once it is gone. The ingesters stop
// first, so none of them adds an aspect again; the stack proxy keeps running,
// because the plugin reaches the repository through it.
func Undo(ctx context.Context, env Env, s Setup, opts UndoOptions, st Streams) (UndoResult, error) {
	res := UndoResult{Project: s.Config.AlfrescoCompose}
	if s.Config.AlfrescoCompose == "" {
		return res, output.Usage("the setup has no Alfresco Compose project", "")
	}
	_, override, ours, err := projectFiles(s.Config.AlfrescoCompose)
	if err != nil {
		return res, err
	}
	if !ours {
		return res, output.Usage("nothing to undo: "+override+" is not from openarch-app", "")
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(s.Config.AlfrescoCompose); err != nil {
			return res, err
		}
	}
	if err := EnsureSharedNetwork(ctx, env); err != nil {
		return res, err
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
	// Without the plugin's file, Compose recreates the services from the
	// user's files again.
	if err := os.Remove(override); err != nil {
		return res, err
	}
	if err := env.Stream(ctx, s.Config.AlfrescoCompose, nil, st.Err, st.Err, "docker", "compose", "up", "-d"); err != nil {
		return res, output.NewError(output.ExitUnhealthy, "the Alfresco project did not start again: "+err.Error(), "See 'docker compose logs' in "+s.Config.AlfrescoCompose+".")
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
		return output.NewError(output.ExitRemote, fmt.Sprintf("the existing Alfresco answered %d to %s %s", resp.StatusCode, method, req.URL.Path), "")
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
}

// searchCl returns the ids of the nodes that the search index lists with a
// cl: aspect, page by page.
func (a alfrescoAPI) searchCl(ctx context.Context) ([]string, error) {
	var q []string
	for _, as := range clAspects {
		q = append(q, `ASPECT:"`+as+`"`)
	}
	var ids []string
	for skip := 0; ; {
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
		body := map[string]any{"query": map[string]string{"query": strings.Join(q, " OR ")}, "paging": map[string]int{"maxItems": 500, "skipCount": skip}}
		if err := a.do(ctx, http.MethodPost, "/search/versions/1/search", body, &out); err != nil {
			return nil, err
		}
		for _, e := range out.List.Entries {
			ids = append(ids, e.Entry.ID)
		}
		skip += len(out.List.Entries)
		if !out.List.Pagination.HasMoreItems || len(out.List.Entries) == 0 {
			return ids, nil
		}
	}
}

// removeAspects removes the cl: aspects from every node that has one, and
// searches again until the index lists none that still has one.
func (a alfrescoAPI) removeAspects(ctx context.Context, opts UndoOptions) (int, error) {
	wait, rounds := opts.Wait, opts.Rounds
	if wait == 0 {
		wait = 5 * time.Second
	}
	if rounds == 0 {
		rounds = 12
	}
	cleaned := map[string]bool{}
	for round := 0; round < rounds; round++ {
		ids, err := a.searchCl(ctx)
		if err != nil {
			return len(cleaned), err
		}
		left := 0
		for _, id := range ids {
			var node struct {
				Entry struct {
					AspectNames []string `json:"aspectNames"`
				} `json:"entry"`
			}
			if err := a.do(ctx, http.MethodGet, "/alfresco/versions/1/nodes/"+id, nil, &node); err != nil {
				return len(cleaned), err
			}
			keep := slices.DeleteFunc(slices.Clone(node.Entry.AspectNames), func(s string) bool { return strings.HasPrefix(s, "cl:") })
			if len(keep) == len(node.Entry.AspectNames) {
				// The index has not caught up with an earlier removal.
				continue
			}
			left++
			if err := a.do(ctx, http.MethodPut, "/alfresco/versions/1/nodes/"+id, map[string]any{"aspectNames": keep}, nil); err != nil {
				return len(cleaned), err
			}
			cleaned[id] = true
		}
		if len(ids) == 0 || (left == 0 && round > 0) {
			return len(cleaned), nil
		}
		select {
		case <-ctx.Done():
			return len(cleaned), ctx.Err()
		case <-time.After(wait):
		}
	}
	return len(cleaned), output.NewError(output.ExitUnhealthy, "the search index still lists nodes with cl: aspects", "Run 'openarch app alfresco-prepare --undo' again in a minute; the module is still installed.")
}
