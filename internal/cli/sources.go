package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// sourceFlags are --alfresco and --nuxeo: how many instances of each source a
// setup runs. --profile and --extra are their deprecated, upstream-shaped form.
type sourceFlags struct {
	alfresco, nuxeo int
	profile         string
	extras          []string
}

func addSourceFlags(cmd *cobra.Command, sf *sourceFlags) {
	f := cmd.Flags()
	f.IntVar(&sf.alfresco, "alfresco", 1, fmt.Sprintf("number of Alfresco instances (0 to %d)", stack.MaxAlfresco))
	f.IntVar(&sf.nuxeo, "nuxeo", 0, fmt.Sprintf("number of Nuxeo instances (0 or %d)", stack.MaxNuxeo))
	f.StringVar(&sf.profile, "profile", "", "upstream profile: alfresco, nuxeo or full")
	f.StringSliceVar(&sf.extras, "extra", nil, "upstream extra: alfresco-2")
	// Hidden rather than marked deprecated: cobra keeps pflag's deprecation
	// notice in a buffer that nobody prints, so sources warns itself.
	_ = f.MarkHidden("profile")
	_ = f.MarkHidden("extra")
}

// sources returns the sources of the flags. The deprecated flags cannot be
// mixed with the new ones, and using them prints a warning.
func (sf *sourceFlags) sources(cmd *cobra.Command, p *output.Printer) (stack.Sources, error) {
	f := cmd.Flags()
	if f.Changed("profile") || f.Changed("extra") {
		p.Warn("--profile and --extra are deprecated and will be removed: use --alfresco and --nuxeo, for example --alfresco 1 --nuxeo 1 or --alfresco 2.")
		if f.Changed("alfresco") || f.Changed("nuxeo") {
			return stack.Sources{}, output.Usage("--profile and --extra cannot be combined with --alfresco and --nuxeo", "Use only --alfresco and --nuxeo.")
		}
		profile := sf.profile
		if profile == "" {
			profile = stack.DefaultProfile
		}
		return stack.SourcesOf(profile, sf.extras)
	}
	s := stack.Sources{Alfresco: sf.alfresco, Nuxeo: sf.nuxeo}
	return s, s.Valid()
}
