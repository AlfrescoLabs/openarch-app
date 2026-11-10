package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// httpGet does a GET with basic auth and returns the status and the body.
func httpGet(ctx context.Context, url, user, password string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(user, password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}

// sourceInstance is one Alfresco whose repository id rag-service needs.
type sourceInstance struct {
	// Key is the .env.local key that rag-service reads as RAG_SOURCES_n_ID.
	Key string
	// Port is the host port of the proxy in front of the instance.
	Port int
	// User and Password are the keys of the admin login of the instance.
	User, Password string
	// DefUser is the user when the key is not set.
	DefUser string
}

// sourceInstances are the Alfresco instances of the alfresco-2 extra. Upstream
// lists both in rag-service only with that extra, and leaves their ids empty
// until someone sets them. A single Alfresco needs no id: rag-service finds it
// in the index.
var sourceInstances = []sourceInstance{
	{"ALFRESCO_1_SOURCE_ID", stack.ProxyPort, "ALFRESCO_ADMIN_USER", "ALFRESCO_ADMIN_PASSWORD", "admin"},
	{"ALFRESCO_2_SOURCE_ID", stack.Alfresco2Port, "ALFRESCO_2_INTERNAL_USERNAME", "ALFRESCO_2_INTERNAL_PASSWORD", "admin"},
}

// SyncSourceIDs reads the repository id of each Alfresco instance and records
// it in .env.local, then recreates rag-service when an id changed. The id is the
// repository UUID that the ingesters already write on every document, so it is
// read, never chosen. It runs on every 'up', so the ids follow a repository
// that 'down -v' replaced. It returns the ids, or nil when the setup has no
// second Alfresco.
func SyncSourceIDs(ctx context.Context, env Env, s Setup, st Streams, waitTimeout int) (map[string]string, error) {
	if !slices.Contains(s.Config.Extras, "alfresco-2") {
		return nil, nil
	}
	values, err := s.env()
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	changed := map[string]string{}
	host := contextHost(s.Config.BindIP)
	for _, in := range sourceInstances {
		user := values[in.User]
		if user == "" {
			user = in.DefUser
		}
		password := values[in.Password]
		if password == "" {
			password = UpstreamAlfrescoPassword
		}
		url := fmt.Sprintf("http://%s:%d/alfresco/api/discovery", host, in.Port)
		code, body, err := env.HTTPGet(ctx, url, user, password)
		var d struct {
			Entry struct {
				Repository struct {
					ID string `json:"id"`
				} `json:"repository"`
			} `json:"entry"`
		}
		if err == nil && code == http.StatusOK {
			err = json.Unmarshal(body, &d)
		}
		if err == nil && code != http.StatusOK {
			err = fmt.Errorf("HTTP %d", code)
		}
		if err == nil && d.Entry.Repository.ID == "" {
			err = fmt.Errorf("no repository id in the answer")
		}
		if err != nil {
			return nil, output.NewError(output.ExitRemote, fmt.Sprintf("cannot read the repository id from %s: %v", url, err), "Run 'openarch app up' again when Alfresco answers.")
		}
		ids[in.Key] = d.Entry.Repository.ID
		if values[in.Key] != d.Entry.Repository.ID {
			changed[in.Key] = d.Entry.Repository.ID
		}
	}
	if len(changed) == 0 {
		return ids, nil
	}
	path := filepath.Join(s.Dir, EnvFile)
	local, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := writeFile(path, setEnvKeys(local, changed), 0o600); err != nil {
		return nil, err
	}
	// rag-service reads the ids only at startup. Compose sees the new values
	// and recreates it; nothing else changes.
	args := s.compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", strconv.Itoa(waitTimeout), "rag-service")
	if err := env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", args...); err != nil {
		return nil, output.NewError(output.ExitUnhealthy, "rag-service did not restart with the source ids: "+err.Error(),
			"Run 'openarch app logs rag-service'.")
	}
	return ids, nil
}
