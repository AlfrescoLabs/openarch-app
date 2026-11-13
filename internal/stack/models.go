package stack

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// RequiredEmbeddingDimension is the size of the vector index of Hyland
// OpenArch. Shorter vectors are stored, but a search with them fails.
const RequiredEmbeddingDimension = 1024

// ValidateModelURL checks a --model-url: an http or https URL with a host and
// no path. The services add /v1 to it, the OpenAI-compatible API of Docker
// Model Runner, Ollama and llama.cpp.
func ValidateModelURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return output.Usage(fmt.Sprintf("invalid --model-url %q", raw), "Use scheme://host:port, for example http://localhost:11434 for Ollama.")
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "" || u.RawQuery != "" {
		return output.Usage(fmt.Sprintf("--model-url %q has a path", raw),
			"Leave out the path: the services add /v1 themselves. For example http://localhost:11434, not http://localhost:11434/v1.")
	}
	return nil
}

// ContainerModelURL returns a model URL as the services in the containers
// reach it: a server on this machine (localhost) is host.docker.internal for
// them. Any other host is kept.
func ContainerModelURL(raw string) string {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return raw
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		host := "host.docker.internal"
		if p := u.Port(); p != "" {
			host = net.JoinHostPort(host, p)
		}
		u.Host = host
	}
	return u.String()
}

// SameModel reports whether a model id from a model server names the model of
// the setup. Servers add a registry or a :latest tag: Docker Model Runner lists
// docker.io/ai/qwen2.5:latest for ai/qwen2.5, Ollama mxbai-embed-large:latest
// for mxbai-embed-large.
func SameModel(listed, model string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimPrefix(s, "docker.io/")
		return strings.TrimSuffix(s, ":latest")
	}
	return norm(listed) == norm(model)
}
