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
	// NuxeoDeploymentFile is the upstream override file that runs
	// nuxeo-deployment inside the stack project.
	NuxeoDeploymentFile = "compose.nuxeo-deployment.yaml"
	// NuxeoPort publishes the Nuxeo of nuxeo-deployment (NUXEO_HTTP_PORT).
	NuxeoPort = 8081
	// SampleUIProfile is the upstream profile that runs the sample UI
	// (content-lake-app-ui). Next to alfresco or full it adds only the UI: its
	// other services are in every profile.
	SampleUIProfile = "platform"
	// SampleUIPort publishes the sample UI container (DEMO_UI_PORT). The proxy
	// also serves it at /.
	SampleUIPort = 4200
	// SampleUIRoot is the proxy directive for / with the sample UI. The
	// upstream nginx template sets $demo_ui_upstream and resolves it on each
	// request, so the proxy starts before the UI does.
	SampleUIRoot = "proxy_pass http://$demo_ui_upstream;"
	// ConnectorProfile is the upstream profile that runs plugin-batch-ingester
	// with a connector jar, here the shipped filesystem connector.
	ConnectorProfile = "connector"
	// ConnectorPort publishes plugin-batch-ingester (CONNECTOR_PORT). Its sync
	// API is the only way to start a sync: the proxy does not route to it.
	ConnectorPort = 9096
)

// The upstream repository and the commit that every setup uses.
const (
	UpstreamURL = "https://github.com/AlfrescoLabs/content-lake-app-deployment.git"
	UpstreamRef = "66615041c84bda9565953a9285e0cbe03db7d01f"
	// ProjectName replaces the fixed upstream Compose project name content-lake-app.
	ProjectName = "openarch-app"
)

// PreviousUpstreamRefs are the upstream commits of earlier releases, oldest
// first. 'upgrade' moves a setup from any of them to UpstreamRef. A setup at a
// commit that is in neither comes from a newer release.
var PreviousUpstreamRefs = []string{
	"160f618096c326445cbe4f9c65858f65852dad74", // 0.1.0 to 0.1.2
	"25b1e7c2b680b8468291750d92e363f746904a9d", // 0.2.0
	"1a0f58791dd550058507b3cd9f7bc390025e214a", // 0.3.0
}

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

// The most instances of each source type that one setup can have. Upstream
// has one second Alfresco (the alfresco-2 extra) and one Nuxeo.
const (
	MaxAlfresco = 2
	MaxNuxeo    = 1
)

// Sources are the content sources of a setup: how many Alfresco and Nuxeo
// instances it runs. Users choose them; the upstream profile and extras follow
// from them.
type Sources struct {
	Alfresco int `yaml:"alfresco" json:"alfresco"`
	Nuxeo    int `yaml:"nuxeo" json:"nuxeo"`
}

// Valid returns a usage error when the setup has no source, or more instances
// than upstream supports.
func (s Sources) Valid() error {
	switch {
	case s.Alfresco < 0 || s.Nuxeo < 0:
		return output.Usage("--alfresco and --nuxeo cannot be negative", "")
	case s.Alfresco == 0 && s.Nuxeo == 0:
		return output.Usage("the setup has no source", "Use --alfresco, --nuxeo or both, for example --alfresco 1 --nuxeo 1.")
	case s.Alfresco > MaxAlfresco:
		return output.Usage(fmt.Sprintf("--alfresco %d: at most %d Alfresco instances", s.Alfresco, MaxAlfresco), "")
	case s.Nuxeo > MaxNuxeo:
		return output.Usage(fmt.Sprintf("--nuxeo %d: at most %d Nuxeo instance", s.Nuxeo, MaxNuxeo), "")
	}
	return nil
}

// Profile returns the upstream base profile that runs the sources.
func (s Sources) Profile() string {
	switch {
	case s.Nuxeo == 0:
		return "alfresco"
	case s.Alfresco == 0:
		return "nuxeo"
	}
	return "full"
}

// Extras returns the upstream extras that the sources need on top of the
// profile: alfresco-2 for a second Alfresco.
func (s Sources) Extras() []string {
	if s.Alfresco >= 2 {
		return []string{"alfresco-2"}
	}
	return nil
}

// String names the sources, for example "2 Alfresco, 1 Nuxeo".
func (s Sources) String() string {
	var parts []string
	if s.Alfresco > 0 {
		parts = append(parts, fmt.Sprintf("%d Alfresco", s.Alfresco))
	}
	if s.Nuxeo > 0 {
		parts = append(parts, fmt.Sprintf("%d Nuxeo", s.Nuxeo))
	}
	return strings.Join(parts, ", ")
}

// SourcesOf returns the sources of an upstream profile and extras, the form of
// the deprecated --profile and --extra flags and of openarch-app.yaml version 1.
func SourcesOf(profile string, extras []string) (Sources, error) {
	if err := ValidProfile(profile); err != nil {
		return Sources{}, err
	}
	if err := ValidExtras(profile, extras); err != nil {
		return Sources{}, err
	}
	s := Sources{Alfresco: 1}
	switch profile {
	case "nuxeo":
		s = Sources{Nuxeo: 1}
	case "full":
		s = Sources{Alfresco: 1, Nuxeo: 1}
	}
	if slices.Contains(extras, "alfresco-2") {
		s.Alfresco++
	}
	return s, nil
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

// HostPorts returns the host ports that a setup with profile and extras, and
// with or without the sample UI and the filesystem source, publishes. Every
// profile publishes the same base ports, and the Nuxeo profiles publish Nuxeo
// too.
func HostPorts(profile string, extras []string, sampleUI, filesystem bool) []int {
	ports := []int{ProxyPort, ProxyHTTPSPort, OpenArchPort}
	if NeedsNuxeo(profile) {
		ports = append(ports, NuxeoPort)
	}
	if slices.Contains(extras, "alfresco-2") {
		ports = append(ports, Alfresco2Port, Alfresco2HTTPSPort)
	}
	if sampleUI {
		ports = append(ports, SampleUIPort)
	}
	if filesystem {
		ports = append(ports, ConnectorPort)
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
// the Nuxeo of nuxeo-deployment.
func NeedsNuxeo(p string) bool {
	return p == "nuxeo" || p == "full"
}
