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
	// ProxyPort and HTTPSPort are the upstream nginx ports.
	ProxyPort = 80
	HTTPSPort = 443
	// OpenArchPort publishes the bundled Hyland OpenArch. It avoids 8080 (a CLI
	// workspace) and 8081 (the Nuxeo of nuxeo-deployment).
	OpenArchPort = 8090
	// NuxeoDeploymentDir is the folder that upstream expects next to its own
	// repository for the profiles with Nuxeo.
	NuxeoDeploymentDir = "nuxeo-deployment"
	NuxeoDeploymentURL = "https://github.com/aborroy/nuxeo-deployment.git"
)

// Profiles are the upstream base profiles. A setup uses exactly one.
var Profiles = []string{"alfresco", "nuxeo", "full", "demo", "platform"}

// ValidProfile returns a usage error when p is not a base profile.
func ValidProfile(p string) error {
	if slices.Contains(Profiles, p) {
		return nil
	}
	return output.Usage(fmt.Sprintf("unknown profile %q", p), "Valid profiles: "+strings.Join(Profiles, ", ")+".")
}

// NeedsNuxeo reports whether profile p runs the Nuxeo ingesters, which need
// nuxeo-deployment running next to the stack.
func NeedsNuxeo(p string) bool {
	return p == "nuxeo" || p == "full" || p == "demo"
}
