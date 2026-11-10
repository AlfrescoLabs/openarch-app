package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// sourceInstance is one Alfresco whose repository id rag-service needs.
type sourceInstance struct {
	// Key is the .env.local key that rag-service reads as RAG_SOURCES_n_ID.
	Key string
	// Service is the Compose service of the instance.
	Service string
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
	{"ALFRESCO_1_SOURCE_ID", "alfresco", "ALFRESCO_ADMIN_USER", "ALFRESCO_ADMIN_PASSWORD", "admin"},
	{"ALFRESCO_2_SOURCE_ID", "alfresco-2", "ALFRESCO_2_INTERNAL_USERNAME", "ALFRESCO_2_INTERNAL_PASSWORD", "admin"},
}

// missingSourceIDs reports whether the setup has the alfresco-2 extra and a
// source id is still empty. rag-service refuses to start then.
func (s Setup) missingSourceIDs() bool {
	if !slices.Contains(s.Config.Extras, "alfresco-2") {
		return false
	}
	values, err := s.env()
	if err != nil {
		return true
	}
	for _, in := range sourceInstances {
		if values[in.Key] == "" {
			return true
		}
	}
	return false
}

// SyncSourceIDs reads the repository id of each Alfresco instance and records
// it in .env.local. With restart, it then recreates rag-service when an id
// changed. The id is the repository UUID that the ingesters already write on
// every document, so it is read, never chosen. It runs on every 'up', so the
// ids follow a repository that 'down -v' replaced. It returns the ids, or nil
// when the setup has no second Alfresco.
func SyncSourceIDs(ctx context.Context, env Env, s Setup, st Streams, waitTimeout int, restart bool) (map[string]string, error) {
	if !slices.Contains(s.Config.Extras, "alfresco-2") {
		return nil, nil
	}
	values, err := s.env()
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	changed := map[string]string{}
	for _, in := range sourceInstances {
		user := values[in.User]
		if user == "" {
			user = in.DefUser
		}
		password := values[in.Password]
		if password == "" {
			password = UpstreamAlfrescoPassword
		}
		id, err := repositoryID(ctx, env, s, in.Service, user, password)
		if err != nil {
			return nil, output.NewError(output.ExitRemote, fmt.Sprintf("cannot read the repository id of %s: %v", in.Service, err), "Run 'openarch app up' again when Alfresco answers.")
		}
		ids[in.Key] = id
		if values[in.Key] != id {
			changed[in.Key] = id
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
	if !restart {
		return ids, nil
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

// repositoryID asks an Alfresco container for its repository id. It runs curl
// inside the container, so it needs neither the proxy nor a published port,
// and it passes the login on stdin, so the password is on no command line.
func repositoryID(ctx context.Context, env Env, s Setup, service, user, password string) (string, error) {
	login := strings.NewReader(fmt.Sprintf("user = %q\n", user+":"+password))
	out, err := env.Run(ctx, s.Dir, login, "docker", s.compose("exec", "-T", service,
		"curl", "-sf", "-K", "-", "http://localhost:8080/alfresco/api/discovery")...)
	if err != nil {
		return "", err
	}
	var d struct {
		Entry struct {
			Repository struct {
				ID string `json:"id"`
			} `json:"repository"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		return "", err
	}
	if d.Entry.Repository.ID == "" {
		return "", fmt.Errorf("no repository id in the answer")
	}
	return d.Entry.Repository.ID, nil
}
