package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// Names that join an existing Alfresco project and the stack. Both projects
// join the external network, where the repository and its broker get fixed
// aliases, so the stack reaches them by name and no host port is published.
const (
	SharedNetwork = "openarch-app-alfresco"
	alfrescoAlias = "openarch-alfresco"
	brokerAlias   = "openarch-activemq"
	proxyAlias    = "openarch-app-proxy"
	// prefixesTarget is where the batch indexer of the project reads the
	// prefixes file with the cl namespace.
	prefixesTarget = "/opt/openarch-app/reindex.prefixes-file.json"
)

// OverrideHeader starts every override file that the plugin writes into an
// existing Alfresco project, so the plugin knows its own file.
const OverrideHeader = "# Written by 'openarch app alfresco-prepare'. It connects this Alfresco to App for OpenArch.\n"

var tagChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// RepositoryModuleImage is the image that the plugin builds for the
// repository of project p: the image of the project plus the module JAR.
func RepositoryModuleImage(p AlfrescoProject) string {
	return "openarch-app/" + tagChars.ReplaceAllString(strings.ToLower(p.Name), "-") + "-" + tagChars.ReplaceAllString(p.Repository, "-") + ":module"
}

// ContentAppImage is the ACA image with the App for OpenArch extension that
// the plugin builds for the ACA version of project p.
func ContentAppImage(p AlfrescoProject) string {
	return "openarch-app/aca-extension:" + tagChars.ReplaceAllString(p.ACAVersion, "-")
}

// alfrescoStackValues returns the .env.local values that point the stack at
// the existing Alfresco of project p, through the shared network.
func alfrescoStackValues(p AlfrescoProject, adminUser, adminPassword string) map[string]string {
	broker := fmt.Sprintf("tcp://%s:61616", brokerAlias)
	return map[string]string{
		"ALFRESCO_URL":                            "http://" + alfrescoAlias + ":8080",
		"ALFRESCO_PROXY_UPSTREAM":                 alfrescoAlias + ":8080",
		"ALFRESCO_EVENT_BROKER_URL":               broker,
		"CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL": broker,
		"ALFRESCO_ADMIN_USER":                     adminUser,
		"ALFRESCO_ADMIN_PASSWORD":                 adminPassword,
		"ACTIVEMQ_USER":                           p.BrokerUser,
		"ACTIVEMQ_PASSWORD":                       p.brokerPassword,
		"TRANSFORM_ACTIVEMQ_URL":                  "false",
	}
}

// quote returns s as a YAML double-quoted scalar. Compose interpolates $ in
// every file, so a value from the project gets $$ to keep its $ as it is.
func quote(s string) string {
	b, _ := json.Marshal(strings.ReplaceAll(s, "$", "$$"))
	return string(b)
}

// renderProjectOverride returns the override file for project p. setupDir is
// the setup folder of the stack, which holds the prefixes file.
func renderProjectOverride(p AlfrescoProject, setupDir string) []byte {
	var b strings.Builder
	b.WriteString(OverrideHeader)
	b.WriteString("# Remove this file and run 'docker compose up -d' to undo it. Nodes keep the cl: aspects.\n")
	fmt.Fprintf(&b, "networks:\n  %s:\n    external: true\n", SharedNetwork)
	b.WriteString("services:\n")
	// The repository: the image of the project plus the module JAR, and the
	// module properties. Its broker is the broker service of the project.
	opts := strings.TrimSpace(p.javaOpts) + fmt.Sprintf(
		" -Dcontent.lake.permission.sync.enabled=true -Dcontent.lake.permission.sync.broker-url=tcp://%s:61616"+
			" -Dcontent.lake.permission.sync.broker-user=%s -Dcontent.lake.permission.sync.broker-password=%s",
		p.Broker, p.BrokerUser, p.brokerPassword)
	fmt.Fprintf(&b, "  %s:\n    image: %s\n    build: !reset null\n    environment:\n      JAVA_OPTS: %s\n", p.Repository, quote(RepositoryModuleImage(p)), quote(opts))
	b.WriteString(p.networksBlock(p.Repository, alfrescoAlias))
	fmt.Fprintf(&b, "  %s:\n", p.Broker)
	b.WriteString(p.networksBlock(p.Broker, brokerAlias))
	if p.Search == "opensearch" {
		prefixes := filepath.Join(setupDir, UpstreamDir, "acs", "search", "reindex.prefixes-file.json")
		fmt.Fprintf(&b, "  %s:\n    environment:\n      ALFRESCO_REINDEX_PREFIXES_FILE: %s\n    volumes:\n      - %s\n",
			p.Indexer, quote("file:"+prefixesTarget), quote(prefixes+":"+prefixesTarget+":ro"))
	}
	if p.ContentApp != "" {
		// The image has its own nginx, which forwards /alfresco/ to the
		// repository and the App for OpenArch paths to the stack proxy.
		fmt.Fprintf(&b, "  %s:\n    image: %s\n    environment:\n      APP_CONFIG_ECM_HOST: %s\n      APP_CONFIG_RAG_URL: %s\n      APP_CONFIG_CONTENT_LAKE_URL: %s\n",
			p.ContentApp, quote(ContentAppImage(p)), quote("http://"+p.Repository+":8080"), quote("http://"+proxyAlias), quote("http://"+proxyAlias))
		b.WriteString(p.networksBlock(p.ContentApp, ""))
	}
	return []byte(b.String())
}

// networks returns the networks block of service in the override: its own
// networks, which Compose would drop when the override names only the shared
// one, plus the shared network with alias, if any.
func (p AlfrescoProject) networksBlock(service, alias string) string {
	var b strings.Builder
	b.WriteString("    networks:\n")
	for _, n := range p.networks[service] {
		fmt.Fprintf(&b, "      %s: {}\n", n)
	}
	if alias == "" {
		fmt.Fprintf(&b, "      %s: {}\n", SharedNetwork)
	} else {
		fmt.Fprintf(&b, "      %s:\n        aliases: [%s]\n", SharedNetwork, alias)
	}
	return b.String()
}

// alfrescoComposeSource reads the Compose project of opts.AlfrescoCompose and
// returns the .env.local values that point the stack at its Alfresco.
func alfrescoComposeSource(ctx context.Context, env Env, c *Config, opts InitOptions) (map[string]string, error) {
	if opts.AlfrescoEnv != "" {
		return nil, output.Usage("--alfresco-compose and --alfresco-env cannot be combined", "Use --alfresco-compose for an Alfresco on Docker Compose, --alfresco-env for any other.")
	}
	if c.Alfresco != 1 || c.Nuxeo != 0 {
		return nil, output.Usage("--alfresco-compose works with --alfresco 1 --nuxeo 0 only", "An existing Alfresco replaces the bundled one, and it is tested without other sources.")
	}
	dir, err := filepath.Abs(opts.AlfrescoCompose)
	if err != nil {
		return nil, err
	}
	p, err := InspectAlfrescoProject(ctx, env, dir)
	if err != nil {
		return nil, err
	}
	if p.BrokerUser == "" || p.brokerPassword == "" {
		return nil, output.Usage("cannot find the broker login of "+dir, "The project sets neither ACTIVEMQ_ADMIN_LOGIN/PASSWORD on "+p.Broker+" nor messaging.broker.username/password in JAVA_OPTS.")
	}
	user := opts.AlfrescoUser
	if user == "" {
		user = "admin"
	}
	if opts.AlfrescoPassword == nil {
		return nil, output.Usage("the password of the Alfresco admin is missing", "Set OPENARCH_APP_ALFRESCO_PASSWORD, or use --alfresco-password-stdin.")
	}
	password, err := opts.AlfrescoPassword()
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, output.Usage("the password of the Alfresco admin is empty", "")
	}
	quoted := map[string]string{}
	for k, v := range alfrescoStackValues(p, user, password) {
		if strings.ContainsRune(v, '\'') || strings.ContainsFunc(v, unicode.IsControl) {
			return nil, output.Usage(fmt.Sprintf("the Alfresco setting %s has a single quote or a control character", k), "init writes the values in single quotes to "+EnvFile+".")
		}
		quoted[k] = "'" + v + "'"
	}
	c.AlfrescoCompose = dir
	c.ExternalAlfresco = "http://" + alfrescoAlias + ":8080"
	return quoted, nil
}

// EnsureSharedNetwork creates the network that an existing Alfresco project
// and the stack share, when it does not exist yet.
func EnsureSharedNetwork(ctx context.Context, env Env) error {
	if _, err := env.Run(ctx, "", nil, "docker", "network", "inspect", SharedNetwork); err == nil {
		return nil
	}
	if _, err := env.Run(ctx, "", nil, "docker", "network", "create", SharedNetwork); err != nil {
		return output.NewError(output.ExitPrerequisite, "cannot create the Docker network "+SharedNetwork+": "+err.Error(), "")
	}
	return nil
}
