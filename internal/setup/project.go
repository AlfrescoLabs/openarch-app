package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// AlfrescoProject is the Docker Compose project of an existing Alfresco, as
// 'docker compose config' reports it. The service fields hold service names,
// empty when the project has no such service.
type AlfrescoProject struct {
	Dir  string `json:"dir"`
	Name string `json:"name"`
	// Repository runs the Alfresco repository, Broker the ActiveMQ of Event2,
	// Indexer the search indexer, ContentApp ACA.
	Repository string `json:"repository"`
	Broker     string `json:"broker"`
	Indexer    string `json:"indexer,omitempty"`
	ContentApp string `json:"contentApp,omitempty"`
	// RepositoryImage is the image of the repository service. A service that
	// Compose builds gets the name <project>-<service>.
	RepositoryImage string `json:"repositoryImage"`
	ACSVersion      string `json:"acsVersion"`
	ACAVersion      string `json:"acaVersion,omitempty"`
	// Search is "opensearch" for Alfresco Search Community, "solr" for Search
	// Services, or empty.
	Search     string `json:"search,omitempty"`
	BrokerUser string `json:"brokerUser"`
	// The secrets of the project, never printed.
	brokerPassword string
	javaOpts       string
	// indexerPrefixes is ALFRESCO_REINDEX_PREFIXES_FILE of the indexer, and
	// repositoryMounts are the container paths of the repository's volumes.
	indexerPrefixes  string
	repositoryMounts []string
	// networks maps each service to the networks it joins.
	networks map[string][]string
}

// composeConfig is the part of 'docker compose config --format json' that the
// plugin reads.
type composeConfig struct {
	Name     string `json:"name"`
	Services map[string]struct {
		Image string `json:"image"`
		Build *struct {
			Args map[string]string `json:"args"`
		} `json:"build"`
		Environment map[string]*string         `json:"environment"`
		Networks    map[string]json.RawMessage `json:"networks"`
		Volumes     []struct {
			Target string `json:"target"`
		} `json:"volumes"`
	} `json:"services"`
}

// InspectAlfrescoProject reads the Compose project in dir with
// 'docker compose config', which resolves its files, .env and profiles as the
// user's own 'docker compose up' does.
// files, when given, replace the files that Compose finds by itself, so the
// plugin can read a project without its own override file.
func InspectAlfrescoProject(ctx context.Context, env Env, dir string, files ...string) (AlfrescoProject, error) {
	out, err := env.Run(ctx, dir, nil, "docker", append(composeArgsOf(files), "config", "--format", "json")...)
	if err != nil {
		return AlfrescoProject{}, output.Usage(fmt.Sprintf("cannot read the Compose project in %s: %v", dir, err), "Give the folder where you run 'docker compose up' for that Alfresco.")
	}
	return parseAlfrescoProject(dir, out)
}

var optRe = regexp.MustCompile(`-D([A-Za-z0-9._-]+)=("[^"]*"|\S*)`)

// javaOpt returns the value of -Dname=value in opts, or empty.
func javaOpt(opts, name string) string {
	for _, m := range optRe.FindAllStringSubmatch(opts, -1) {
		if m[1] == name {
			return strings.Trim(m[2], `"`)
		}
	}
	return ""
}

// imageTag returns the tag of image, or empty.
func imageTag(image string) string {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return ""
}

func parseAlfrescoProject(dir string, data []byte) (AlfrescoProject, error) {
	var cfg composeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AlfrescoProject{}, fmt.Errorf("cannot parse 'docker compose config': %w", err)
	}
	p := AlfrescoProject{Dir: dir, Name: cfg.Name, networks: map[string][]string{}}
	names := make([]string, 0, len(cfg.Services))
	for n := range cfg.Services {
		names = append(names, n)
	}
	slices.Sort(names)
	var repositories []string
	for _, n := range names {
		s := cfg.Services[n]
		opts := ""
		if v := s.Environment["JAVA_OPTS"]; v != nil {
			opts = *v
		}
		for net := range s.Networks {
			p.networks[n] = append(p.networks[n], net)
		}
		slices.Sort(p.networks[n])
		img := s.Image
		switch {
		case strings.Contains(img, "alfresco-content-repository") || strings.Contains(opts, "-Ddb.url="):
			repositories = append(repositories, n)
		case strings.Contains(img, "activemq"):
			p.Broker = n
			if v := s.Environment["ACTIVEMQ_ADMIN_LOGIN"]; v != nil {
				p.BrokerUser = *v
			}
			if v := s.Environment["ACTIVEMQ_ADMIN_PASSWORD"]; v != nil {
				p.brokerPassword = *v
			}
		case strings.Contains(img, "alfresco-elasticsearch-batch-indexing"):
			p.Indexer, p.Search = n, "opensearch"
			if v := s.Environment["ALFRESCO_REINDEX_PREFIXES_FILE"]; v != nil {
				p.indexerPrefixes = *v
			}
		case strings.Contains(img, "alfresco-search-services") || strings.Contains(img, "solr"):
			p.Indexer, p.Search = n, "solr"
		case strings.Contains(img, "alfresco-content-app"):
			p.ContentApp, p.ACAVersion = n, imageTag(img)
		}
	}
	switch {
	case len(repositories) == 0:
		return p, output.Usage("the Compose project in "+dir+" has no Alfresco repository service", "")
	case len(repositories) > 1:
		return p, output.Usage("the Compose project in "+dir+" has more than one Alfresco repository: "+strings.Join(repositories, ", "), "One existing Alfresco is supported.")
	}
	p.Repository = repositories[0]
	repo := cfg.Services[p.Repository]
	if v := repo.Environment["JAVA_OPTS"]; v != nil {
		p.javaOpts = *v
	}
	for _, v := range repo.Volumes {
		p.repositoryMounts = append(p.repositoryMounts, v.Target)
	}
	p.RepositoryImage = repo.Image
	p.ACSVersion = imageTag(repo.Image)
	if repo.Build != nil {
		if p.RepositoryImage == "" {
			p.RepositoryImage = cfg.Name + "-" + p.Repository
		}
		if t := repo.Build.Args["ALFRESCO_TAG"]; t != "" {
			p.ACSVersion = t
		}
	}
	if p.Broker == "" {
		return p, output.Usage("the Compose project in "+dir+" has no ActiveMQ broker", "App for OpenArch reads Event2 from the broker of Alfresco. With alfresco-docker-installer, generate the project with --activemq=true.")
	}
	if p.BrokerUser == "" {
		p.BrokerUser = javaOpt(p.javaOpts, "messaging.broker.username")
	}
	if p.brokerPassword == "" {
		p.brokerPassword = javaOpt(p.javaOpts, "messaging.broker.password")
	}
	if major, err := strconv.Atoi(strings.SplitN(p.ACSVersion, ".", 2)[0]); err != nil || major < MinAlfrescoVersion {
		return p, output.NewError(output.ExitPrerequisite, fmt.Sprintf("the Alfresco of %s is version %q; App for OpenArch needs %d or later", dir, p.ACSVersion, MinAlfrescoVersion), "")
	}
	return p, nil
}
