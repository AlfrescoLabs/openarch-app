package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// Settings of the filesystem source in .env.local. The sync account guards
// the sync API of plugin-batch-ingester; upstream has no default for it.
const (
	syncUserKey     = "CONNECTOR_SYNC_USERNAME"
	syncPasswordKey = "CONNECTOR_SYNC_PASSWORD"
	syncUser        = "sync"
	readersKey      = "FILESYSTEM_READ_PRINCIPALS"
)

// maxReaderLength is the longest reader name.
const maxReaderLength = 100

// filesystemSource checks the filesystem flags of init and returns the
// readers. It makes c.Filesystem absolute. A reader is a user name: the RAG
// service does not look up groups for the filesystem source, so a group
// would match nobody. No reader means everyone, which needs everyone.
func filesystemSource(c *Config, readers []string, everyone bool) ([]string, error) {
	if c.Filesystem == "" {
		if len(readers) > 0 || everyone {
			return nil, output.Usage("--filesystem-reader and --filesystem-everyone need --filesystem", "")
		}
		return nil, nil
	}
	if c.Alfresco >= 2 && c.SampleUI {
		return nil, output.Usage("--filesystem does not work with --alfresco 2 --sample-ui",
			"With two Alfresco instances, the sample UI does not find the documents of the filesystem source. Leave out --sample-ui or use one Alfresco.")
	}
	switch {
	case len(readers) > 0 && everyone:
		return nil, output.Usage("--filesystem-reader and --filesystem-everyone cannot be combined", "")
	case len(readers) == 0 && !everyone:
		return nil, output.Usage("--filesystem needs --filesystem-reader or --filesystem-everyone",
			"Name the users who can read the files, for example --filesystem-reader jim. With --filesystem-everyone, every user who can log in reads them.")
	}
	for _, r := range readers {
		bad := r == "" || len(r) > maxReaderLength || strings.ContainsAny(r, ",'\"") ||
			strings.ContainsFunc(r, func(c rune) bool { return unicode.IsSpace(c) || unicode.IsControl(c) })
		if bad {
			return nil, output.Usage(fmt.Sprintf("invalid --filesystem-reader %q", r), fmt.Sprintf("Use a user name of 1 to %d characters, with no space, comma or quote.", maxReaderLength))
		}
		if strings.HasPrefix(strings.ToUpper(r), "GROUP_") {
			return nil, output.Usage(fmt.Sprintf("--filesystem-reader %s is a group", r),
				"The RAG service does not look up groups for the filesystem source. Name each user instead.")
		}
	}
	dir, err := filepath.Abs(c.Filesystem)
	if err != nil {
		return nil, err
	}
	// The path goes into .env.local in single quotes.
	if strings.ContainsRune(dir, '\'') || strings.ContainsFunc(dir, unicode.IsControl) {
		return nil, output.Usage(fmt.Sprintf("invalid --filesystem %q", c.Filesystem), "Use a folder path with no single quote.")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, output.Usage(fmt.Sprintf("--filesystem %s is not a folder", c.Filesystem), "Create the folder first, or give the path of an existing one.")
	}
	c.Filesystem = dir
	return readers, nil
}

// filesystemValues returns the .env.local values of the filesystem source of
// c. The folder is mounted read-only, and the sync API binds to the bind IP.
func filesystemValues(c Config, readers []string) (map[string]string, error) {
	password, err := NewPassword()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"CONNECTOR_SOURCE_TYPE": "filesystem",
		"CONNECTOR_HOST_PATH":   "'" + c.Filesystem + "'",
		"CONNECTOR_PORT":        fmt.Sprintf("%s:%d", c.BindIP, stack.ConnectorPort),
		readersKey:              strings.Join(readers, ","),
		syncUserKey:             syncUser,
		syncPasswordKey:         password,
	}, nil
}

// SyncOptions select how Sync waits for the sync job.
type SyncOptions struct {
	// Timeout is the wait limit in seconds.
	Timeout int
	// Interval is the time between two status calls. Zero means 2 seconds.
	Interval time.Duration
}

// SyncResult is the outcome of Sync: the counts of the sync job.
type SyncResult struct {
	Folder     string `json:"folder"`
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	Discovered int    `json:"discovered"`
	Synced     int    `json:"synced"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
	Deleted    int    `json:"deleted"`
	OK         bool   `json:"ok"`
}

// WriteText writes the human-readable form.
func (r SyncResult) WriteText(w io.Writer) error {
	verdict := "Synced"
	if !r.OK {
		verdict = "Sync FAILED for"
	}
	_, err := fmt.Fprintf(w, "%s %s: %d found, %d synced, %d skipped, %d failed, %d deleted (job %s, %s).\n",
		verdict, r.Folder, r.Discovered, r.Synced, r.Skipped, r.Failed, r.Deleted, r.JobID, r.Status)
	return err
}

// syncJob is a job of the plugin-batch-ingester sync API.
type syncJob struct {
	JobID      string `json:"jobId"`
	Status     string `json:"status"`
	Discovered int    `json:"discoveredCount"`
	Synced     int    `json:"syncedCount"`
	Skipped    int    `json:"skippedCount"`
	Failed     int    `json:"failedCount"`
	Deleted    int    `json:"deletedCount"`
}

// Sync starts a sync of the filesystem source and waits until it ends.
// plugin-batch-ingester has no schedule: a sync runs only when its API is
// called, with the sync account of .env.local.
func Sync(ctx context.Context, env Env, s Setup, opts SyncOptions) (SyncResult, error) {
	res := SyncResult{Folder: s.Config.Filesystem}
	if s.Config.Filesystem == "" {
		return res, output.Usage("the setup has no filesystem source", "Create a setup with one: openarch app init --filesystem <folder> --filesystem-reader <user>.")
	}
	values, err := s.env()
	if err != nil {
		return res, err
	}
	user, password := values[syncUserKey], values[syncPasswordKey]
	if user == "" || password == "" {
		return res, output.Usage(fmt.Sprintf("%s has no %s or %s", EnvFile, syncUserKey, syncPasswordKey), "")
	}
	interval := opts.Interval
	if interval == 0 {
		interval = 2 * time.Second
	}
	api := fmt.Sprintf("http://%s:%d/api/sync", contextHost(s.Config.BindIP), stack.ConnectorPort)
	job, err := syncCall(ctx, env, http.MethodPost, api+"/configured", user, password)
	deadline := time.Now().Add(time.Duration(opts.Timeout) * time.Second)
	for err == nil && job.Status == "RUNNING" {
		if time.Now().After(deadline) {
			return res, output.NewError(output.ExitUnhealthy, fmt.Sprintf("the sync job %s still runs after %d s", job.JobID, opts.Timeout),
				"It goes on in the background. Run 'openarch app sync' again later, or see 'openarch app logs plugin-batch-ingester'.")
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(interval):
		}
		job, err = syncCall(ctx, env, http.MethodGet, api+"/status/"+url.PathEscape(job.JobID), user, password)
	}
	if err != nil {
		return res, err
	}
	res.JobID, res.Status = job.JobID, job.Status
	res.Discovered, res.Synced, res.Skipped, res.Failed, res.Deleted = job.Discovered, job.Synced, job.Skipped, job.Failed, job.Deleted
	res.OK = job.Status == "COMPLETED" && job.Failed == 0
	return res, nil
}

// syncCall calls the sync API and returns its job.
func syncCall(ctx context.Context, env Env, method, u, user, password string) (syncJob, error) {
	var job syncJob
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return job, err
	}
	req.SetBasicAuth(user, password)
	resp, err := env.HTTP.Do(req)
	if err != nil {
		return job, output.NewError(output.ExitUnhealthy, fmt.Sprintf("the sync API of the filesystem source does not answer: %v", err),
			"Start the stack with 'openarch app up', then check 'openarch app status'.")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return job, output.NewError(output.ExitAuth, "the sync API refused the account in "+EnvFile,
			fmt.Sprintf("Check %s and %s in %s, then run 'openarch app up' so that the container gets them.", syncUserKey, syncPasswordKey, EnvFile))
	case resp.StatusCode != http.StatusOK:
		return job, output.NewError(output.ExitRemote, fmt.Sprintf("the sync API answered %s to %s %s", resp.Status, method, req.URL.Path), "See 'openarch app logs plugin-batch-ingester'.")
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&job); err != nil || job.JobID == "" {
		return job, output.NewError(output.ExitRemote, "the sync API sent no job for "+req.URL.Path, "See 'openarch app logs plugin-batch-ingester'.")
	}
	return job, nil
}
