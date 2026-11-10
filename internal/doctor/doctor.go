// Package doctor checks the prerequisites to run App for OpenArch.
package doctor

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// Status is the result of one check.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	// Skip means the check could not run because a check it depends on failed.
	Skip Status = "skip"
)

// Result is the outcome of one check.
type Result struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Fix     string `json:"fix,omitempty"`
}

// Summary counts results by status.
type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Report is the outcome of a doctor run.
type Report struct {
	Checks  []Result `json:"checks"`
	Summary Summary  `json:"summary"`
}

// Failed reports whether any check failed.
func (r Report) Failed() bool { return r.Summary.Fail > 0 }

// Options select what to check.
type Options struct {
	// Sources are the Alfresco and Nuxeo instances. They set the memory
	// threshold and whether the nuxeo-deployment check runs.
	Sources        stack.Sources
	LLMModel       string
	EmbeddingModel string
	// ModelURL is Docker Model Runner as seen from the host.
	ModelURL string
	// Ports are the host ports that must be free.
	Ports []int
	// Dir is the setup folder. nuxeo-deployment must be inside it.
	Dir string
	// OpenArchBin is the openarch command. Empty means "openarch" from PATH.
	OpenArchBin string
	// Only runs the checks with these IDs. Empty means all checks.
	Only []string
}

type check struct {
	id    string
	title string
	run   func(ctx context.Context, s *state) Result
}

// IDs returns the IDs of the checks that Run would execute with opts and env.
func IDs(env Env, opts Options) []string {
	var ids []string
	for _, c := range checks(env, opts) {
		ids = append(ids, c.id)
	}
	return ids
}

// Run executes the checks. It returns an error only for invalid options.
func Run(ctx context.Context, env Env, opts Options) (Report, error) {
	all := checks(env, opts)
	selected := all
	if len(opts.Only) > 0 {
		ids := IDs(env, opts)
		selected = nil
		for _, id := range opts.Only {
			if !slices.Contains(ids, id) {
				return Report{}, output.Usage(fmt.Sprintf("unknown check %q", id), "Valid checks: "+strings.Join(ids, ", ")+".")
			}
		}
		for _, c := range all {
			if slices.Contains(opts.Only, c.id) {
				selected = append(selected, c)
			}
		}
	}

	s := &state{env: env, opts: opts}
	var r Report
	for _, c := range selected {
		res := c.run(ctx, s)
		res.ID, res.Title = c.id, c.title
		r.Checks = append(r.Checks, res)
		switch res.Status {
		case OK:
			r.Summary.OK++
		case Warn:
			r.Summary.Warn++
		case Fail:
			r.Summary.Fail++
		case Skip:
			r.Summary.Skip++
		}
	}
	return r, nil
}

// WriteText writes the human-readable form.
func (r Report) WriteText(w io.Writer) error {
	width := 0
	for _, c := range r.Checks {
		width = max(width, len(c.ID))
	}
	var b strings.Builder
	for _, c := range r.Checks {
		fmt.Fprintf(&b, "%-6s %-*s  %s\n", "["+string(c.Status)+"]", width, c.ID, c.Message)
		if c.Fix != "" {
			fmt.Fprintf(&b, "%-6s %-*s  fix: %s\n", "", width, "", c.Fix)
		}
	}
	fmt.Fprintf(&b, "\n%d ok, %d warn, %d fail, %d skip\n", r.Summary.OK, r.Summary.Warn, r.Summary.Fail, r.Summary.Skip)
	_, err := io.WriteString(w, b.String())
	return err
}
