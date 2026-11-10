// Package stack holds the facts about the upstream App for OpenArch stack that
// several commands share: profiles, default models and ports.
package stack

import (
	"fmt"
	"slices"
	"strings"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// Defaults of a new setup.
const (
	// DefaultLLMModel is the upstream default chat model. It is a local package
	// that no registry serves; upstream builds it with scripts/package-olmo3-local.sh.
	DefaultLLMModel       = "local/olmo3-7b-instruct:Q4_K_M"
	DefaultEmbeddingModel = "ai/mxbai-embed-large"
	// DefaultModelURL is Docker Model Runner as seen from the host. Containers
	// reach the same runner at http://host.docker.internal:12434.
	DefaultModelURL = "http://localhost:12434"
	DefaultProfile  = "alfresco"
	// ProxyPort and ProxyHTTPSPort are the upstream nginx ports (PUBLIC_PORT
	// and HTTPS_PORT). nginx publishes both, also with USE_HTTPS=false.
	ProxyPort      = 80
	ProxyHTTPSPort = 443
	// Alfresco2Port and Alfresco2HTTPSPort are the proxy ports of the
	// alfresco-2 extra (ALFRESCO_2_PUBLIC_PORT and ALFRESCO_2_HTTPS_PORT).
	Alfresco2Port      = 8082
	Alfresco2HTTPSPort = 8445
	// OpenArchPort publishes the bundled Hyland OpenArch. It avoids 8080 (a CLI
	// workspace) and 8081 (the Nuxeo of nuxeo-deployment).
	OpenArchPort = 8090
	// NuxeoDeploymentDir is the folder that upstream expects next to its own
	// repository for the profiles with Nuxeo.
	NuxeoDeploymentDir = "nuxeo-deployment"
	NuxeoDeploymentURL = "https://github.com/aborroy/nuxeo-deployment.git"
	// NuxeoDeploymentRef is the nuxeo-deployment commit to use with the
	// pinned upstream commit.
	NuxeoDeploymentRef = "0a5569054e6bb2763770d8ecd1d5c26f019fdc5d"
)

// The upstream repository and the commit that every setup uses.
const (
	UpstreamURL = "https://github.com/AlfrescoLabs/content-lake-app-deployment.git"
	UpstreamRef = "160f618096c326445cbe4f9c65858f65852dad74"
	// ProjectName replaces the fixed upstream Compose project name content-lake-app.
	ProjectName = "openarch-app"
)

// PreviousUpstreamRefs are the upstream commits of earlier releases, oldest
// first. 'upgrade' moves a setup from any of them to UpstreamRef. A setup at a
// commit that is in neither comes from a newer release.
var PreviousUpstreamRefs = []string{}

// KnownUpstream reports whether ref is the upstream commit of this release or
// of an earlier one.
func KnownUpstream(ref string) bool {
	return ref == UpstreamRef || slices.Contains(PreviousUpstreamRefs, ref)
}

// GitContext is one remote BuildKit context of the upstream stack.
type GitContext struct {
	// Var is the upstream variable that holds the context URL.
	Var string
	// Repo is the Git URL without the fragment.
	Repo string
	// Ref is the full commit SHA, pinned with UpstreamRef.
	Ref string
}

// URL returns the context URL with the pinned commit as fragment.
func (g GitContext) URL() string { return g.Repo + "#" + g.Ref }

// GitContexts are the remote build contexts, pinned to the commits of their
// main branches at the time of UpstreamRef.
var GitContexts = []GitContext{
	{"CONTENT_LAKE_GIT_CONTEXT", "https://github.com/AlfrescoLabs/content-lake-app.git", "fd220ce85c09749b88610dca6b3f6ce2605740b2"},
	{"CONTENT_LAKE_ACS_GIT_CONTEXT", "https://github.com/AlfrescoLabs/content-lake-app.git", "fd220ce85c09749b88610dca6b3f6ce2605740b2"},
	{"CONTENT_LAKE_UI_GIT_CONTEXT", "https://github.com/AlfrescoLabs/alfresco-content-lake-ui.git", "1df264e4fad2c22c92f151684d3a23bb4c90c819"},
	{"CONTENT_LAKE_APP_UI_CONTEXT", "https://github.com/AlfrescoLabs/content-lake-app-ui.git", "a6ddcbe1b68db6d7030e21a77844a8e848a7aed4"},
}

// Profiles are the upstream base profiles that the plugin supports. A setup
// uses exactly one.
var Profiles = []string{"alfresco", "nuxeo", "full"}

// Extras are the opt-in additions that the plugin supports.
var Extras = []string{"alfresco-2"}

// ValidProfile returns a usage error when p is not a base profile.
func ValidProfile(p string) error {
	if slices.Contains(Profiles, p) {
		return nil
	}
	return output.Usage(fmt.Sprintf("unknown profile %q", p), "Valid profiles: "+strings.Join(Profiles, ", ")+".")
}

// HostPorts returns the host ports that a setup with profile and extras
// publishes. Every profile publishes the same base ports.
func HostPorts(extras []string) []int {
	ports := []int{ProxyPort, ProxyHTTPSPort, OpenArchPort}
	if slices.Contains(extras, "alfresco-2") {
		ports = append(ports, Alfresco2Port, Alfresco2HTTPSPort)
	}
	return ports
}

// ValidExtra returns a usage error when e is not a supported extra.
func ValidExtra(e string) error {
	if slices.Contains(Extras, e) {
		return nil
	}
	return output.Usage(fmt.Sprintf("unknown extra %q", e), "Valid extras: "+strings.Join(Extras, ", ")+".")
}

// ValidExtras returns a usage error when an extra is unknown or does not fit
// the profile. alfresco-2 adds a second Alfresco, so it needs the first one.
func ValidExtras(profile string, extras []string) error {
	for _, e := range extras {
		if err := ValidExtra(e); err != nil {
			return err
		}
		if e == "alfresco-2" && profile == "nuxeo" {
			return output.Usage("the alfresco-2 extra needs the alfresco or full profile", "Use --profile alfresco or --profile full.")
		}
	}
	return nil
}

// NeedsNuxeo reports whether profile p runs the Nuxeo ingesters, which need
// nuxeo-deployment running next to the stack.
func NeedsNuxeo(p string) bool {
	return p == "nuxeo" || p == "full"
}
