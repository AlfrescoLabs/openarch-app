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
	// ProxyPort is the upstream nginx port (PUBLIC_PORT).
	ProxyPort = 80
	// Alfresco2Port is the proxy port of the alfresco-2 extra (ALFRESCO_2_PUBLIC_PORT).
	Alfresco2Port = 8082
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

// ValidExtra returns a usage error when e is not a supported extra.
func ValidExtra(e string) error {
	if slices.Contains(Extras, e) {
		return nil
	}
	return output.Usage(fmt.Sprintf("unknown extra %q", e), "Valid extras: "+strings.Join(Extras, ", ")+".")
}

// NeedsNuxeo reports whether profile p runs the Nuxeo ingesters, which need
// nuxeo-deployment running next to the stack.
func NeedsNuxeo(p string) bool {
	return p == "nuxeo" || p == "full"
}
