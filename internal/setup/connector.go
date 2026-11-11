package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// The sync account in .env.local. It guards the sync API of
// plugin-batch-ingester; upstream has no default for it.
const (
	syncUserKey     = "CONNECTOR_SYNC_USERNAME"
	syncPasswordKey = "CONNECTOR_SYNC_PASSWORD"
	syncUser        = "sync"
)

// checkConnector returns a usage error when the connector sources of c, with
// a CMIS source when cmis is set, do not fit together or with the other
// sources. Upstream runs one connector in
// plugin-batch-ingester. With two Alfresco instances, the sample UI sends one
// login for each Alfresco source and none for a connector source, so the RAG
// service finds none of its documents.
func checkConnector(c Config, cmis bool) error {
	cmis = cmis || c.CMIS != ""
	if !cmis && c.Filesystem == "" {
		return nil
	}
	flag, name := "--filesystem", "filesystem"
	if cmis {
		flag, name = "--cmis-env", "CMIS"
	}
	switch {
	case cmis && c.Filesystem != "":
		return output.Usage("--filesystem and --cmis-env cannot be combined", "Upstream runs one connector source in a setup. Choose one of them.")
	case c.Alfresco >= 2 && c.SampleUI:
		return output.Usage(flag+" does not work with --alfresco 2 --sample-ui",
			"With two Alfresco instances, the sample UI does not find the documents of a "+name+" source. Leave out --sample-ui or use one Alfresco.")
	}
	return nil
}

// connectorValues returns the .env.local values of the connector source of c,
// with source as the values of its own settings. The sync API binds to the
// bind IP.
func connectorValues(c Config, source map[string]string) (map[string]string, error) {
	// Upstream has no sync account to keep, so it is random also with
	// --default-secrets.
	password, err := NewPassword()
	if err != nil {
		return nil, err
	}
	v := map[string]string{
		"CONNECTOR_SOURCE_TYPE": c.Connector(),
		"CONNECTOR_PORT":        fmt.Sprintf("%s:%d", c.BindIP, stack.ConnectorPort),
		syncUserKey:             syncUser,
		syncPasswordKey:         password,
	}
	for k, x := range source {
		v[k] = x
	}
	return v, nil
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
	// Source is the folder of the filesystem source, or the URL of the CMIS
	// source.
	Source     string `json:"source"`
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
		verdict, r.Source, r.Discovered, r.Synced, r.Skipped, r.Failed, r.Deleted, r.JobID, r.Status)
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

// Sync starts a sync of the connector source and waits until it ends.
// plugin-batch-ingester has no schedule: a sync runs only when its API is
// called, with the sync account of .env.local.
func Sync(ctx context.Context, env Env, s Setup, opts SyncOptions) (SyncResult, error) {
	res := SyncResult{Source: s.Config.Filesystem}
	if s.Config.CMIS != "" {
		res.Source = s.Config.CMIS
	}
	if s.Config.Connector() == "" {
		return res, output.Usage("the setup has no filesystem or CMIS source",
			"Create a setup with one: openarch app init --filesystem <folder> --filesystem-reader <user>, or --cmis-env <file>.")
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
		return job, output.NewError(output.ExitUnhealthy, fmt.Sprintf("the sync API of plugin-batch-ingester does not answer: %v", err),
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
