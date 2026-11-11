package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
		// The long syntax, because a Windows path holds a colon.
		fmt.Fprintf(&b, "  %s:\n    environment:\n      ALFRESCO_REINDEX_PREFIXES_FILE: %s\n    volumes:\n      - type: bind\n        source: %s\n        target: %s\n        read_only: true\n",
			p.Indexer, quote("file:"+prefixesTarget), quote(prefixes), quote(prefixesTarget))
	}
	if p.ContentApp != "" {
		// The image has its own nginx, which forwards /alfresco/ to the
		// repository and the App for OpenArch paths to the stack proxy. nginx
		// resolves those names at startup and exits while the stack is down,
		// so the container restarts until the stack proxy is there.
		fmt.Fprintf(&b, "  %s:\n    image: %s\n    restart: unless-stopped\n    environment:\n      APP_CONFIG_ECM_HOST: %s\n      APP_CONFIG_RAG_URL: %s\n      APP_CONFIG_CONTENT_LAKE_URL: %s\n",
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

// composeFiles are the file names that Compose finds by itself, in its order,
// each with the override file that Compose merges into it.
var composeFiles = []struct{ base, override string }{
	{"compose.yaml", "compose.override.yaml"},
	{"compose.yml", "compose.override.yml"},
	{"docker-compose.yaml", "docker-compose.override.yaml"},
	{"docker-compose.yml", "docker-compose.override.yml"},
}

// projectFiles returns the main file of the project in dir, the path of its
// override file, and whether that override file is the plugin's own.
func projectFiles(dir string) (base, override string, ours bool, err error) {
	for _, f := range composeFiles {
		if _, err := os.Stat(filepath.Join(dir, f.base)); err == nil {
			override = filepath.Join(dir, f.override)
			data, err := os.ReadFile(override)
			return f.base, override, err == nil && strings.HasPrefix(string(data), OverrideHeader), nil
		}
	}
	return "", "", false, output.Usage("no compose.yaml or docker-compose.yml in "+dir, "")
}

// PrepareOptions select what Prepare does.
type PrepareOptions struct {
	// Apply installs the override file in the project and recreates the
	// changed services. Without it, Prepare changes nothing of the project.
	Apply bool
	// Confirm is asked before Apply changes the project.
	Confirm func(project string) error
}

// PrepareResult is the outcome of Prepare.
type PrepareResult struct {
	Project  string   `json:"project"`
	Override string   `json:"override"`
	Images   []string `json:"images"`
	Applied  bool     `json:"applied"`
	Steps    []string `json:"steps,omitempty"`
}

// WriteText writes the human-readable form.
func (r PrepareResult) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Built %s.\n", strings.Join(r.Images, " and "))
	if r.Applied {
		fmt.Fprintf(&b, "Installed %s and recreated the changed services of %s.\n", r.Override, r.Project)
	} else {
		fmt.Fprintf(&b, "Wrote %s. Nothing in %s changed.\n\nTo apply it (or run 'openarch app alfresco-prepare --apply'):\n", r.Override, r.Project)
		for i, s := range r.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Prepare builds the images for the existing Alfresco project of the setup and
// writes the override file that uses them. With opts.Apply it also installs
// the file in the project and recreates the changed services. It replaces the
// repository with the project image plus the module, and ACA with an ACA that
// has the App for OpenArch extension.
func Prepare(ctx context.Context, env Env, s Setup, opts PrepareOptions, st Streams) (PrepareResult, error) {
	res := PrepareResult{Project: s.Config.AlfrescoCompose}
	if s.Config.AlfrescoCompose == "" {
		return res, output.Usage("the setup has no Alfresco Compose project", "Create the setup with 'openarch app init --alfresco-compose <dir>'.")
	}
	if err := s.CheckPins(); err != nil {
		return res, err
	}
	base, override, ours, err := projectFiles(s.Config.AlfrescoCompose)
	if err != nil {
		return res, err
	}
	if opts.Apply && !ours {
		if _, err := os.Stat(override); err == nil {
			return res, output.Usage(override+" exists and is not from openarch-app", "Merge the changes by hand: run 'openarch app alfresco-prepare' without --apply.")
		}
	}
	// Read the project without the plugin's own override, so a second run
	// starts from the user's files again.
	var files []string
	if ours {
		files = []string{base}
	}
	p, err := InspectAlfrescoProject(ctx, env, s.Config.AlfrescoCompose, files...)
	if err != nil {
		return res, err
	}
	values, err := s.env()
	if err != nil {
		return res, err
	}
	work := filepath.Join(s.Dir, "alfresco-prepare")
	module, err := AlfrescoModule(ctx, env, s, work, st)
	if err != nil {
		return res, err
	}
	// The project image must exist; Compose builds it for a build service.
	if _, err := env.Run(ctx, "", nil, "docker", "image", "inspect", p.RepositoryImage); err != nil {
		if err := env.Stream(ctx, p.Dir, nil, st.Err, st.Err, "docker", append(composeArgsOf(files), "build", p.Repository)...); err != nil {
			return res, output.NewError(output.ExitError, "cannot build "+p.Repository+" of "+p.Dir+": "+err.Error(), "")
		}
	}
	dockerfile := fmt.Sprintf("FROM %s\nUSER root\nCOPY %s /usr/local/tomcat/webapps/alfresco/WEB-INF/lib/\nUSER alfresco\n", p.RepositoryImage, filepath.Base(module.JAR))
	if err := writeFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return res, err
	}
	if err := env.Stream(ctx, work, nil, st.Err, st.Err, "docker", "build", "-t", RepositoryModuleImage(p), "."); err != nil {
		return res, output.NewError(output.ExitError, "cannot build "+RepositoryModuleImage(p)+": "+err.Error(), "")
	}
	res.Images = append(res.Images, RepositoryModuleImage(p))
	if p.ContentApp != "" && p.ACAVersion == "" {
		return res, output.Usage("cannot tell the ACA version of "+p.ContentApp+" in "+p.Dir, "Give its image a version tag, for example alfresco/alfresco-content-app:8.0.0.")
	}
	if p.ContentApp != "" {
		// The upstream Dockerfile of the extension clones ACA at ACA_TAG and
		// builds it with the extension.
		ui := values["CONTENT_LAKE_UI_GIT_CONTEXT"]
		if err := env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", "build", "--build-arg", "ACA_TAG="+p.ACAVersion,
			"-t", ContentAppImage(p), "-f", "docker/Dockerfile", ui); err != nil {
			return res, output.NewError(output.ExitError, fmt.Sprintf("cannot build ACA %s with the App for OpenArch extension: %v", p.ACAVersion, err),
				"The extension may not support this ACA version yet.")
		}
		res.Images = append(res.Images, ContentAppImage(p))
	}
	// The file holds the broker password of the project, as its own files do.
	res.Override = filepath.Join(s.Dir, "alfresco-compose.override.yaml")
	if err := writeFile(res.Override, renderProjectOverride(p, s.Dir), 0o600); err != nil {
		return res, err
	}
	res.Steps = []string{
		"Back up the Alfresco database and content store.",
		"Copy " + res.Override + " to " + override + ".",
		"Run 'docker compose up -d' in " + p.Dir + ". Compose recreates only the changed services.",
		"Run 'openarch app up'.",
	}
	if !opts.Apply {
		return res, nil
	}
	if opts.Confirm != nil {
		if err := opts.Confirm(p.Dir); err != nil {
			return res, err
		}
	}
	if err := EnsureSharedNetwork(ctx, env); err != nil {
		return res, err
	}
	data, err := os.ReadFile(res.Override)
	if err != nil {
		return res, err
	}
	if err := writeFile(override, data, 0o600); err != nil {
		return res, err
	}
	// Wait for the services that the stack needs. ACA starts after them
	// without a wait: it runs once the stack proxy is up.
	wait := []string{"compose", "up", "-d", "--wait", p.Repository, p.Broker}
	if p.Search == "opensearch" {
		wait = append(wait, p.Indexer)
	}
	for _, args := range [][]string{wait, {"compose", "up", "-d"}} {
		if err := env.Stream(ctx, p.Dir, nil, st.Err, st.Err, "docker", args...); err != nil {
			return res, output.NewError(output.ExitUnhealthy, "the Alfresco project did not start again: "+err.Error(),
				"See 'docker compose logs' in "+p.Dir+". To undo, remove "+override+" and run 'docker compose up -d' there.")
		}
	}
	res.Applied = true
	res.Steps = nil
	return res, nil
}

// composeArgsOf returns 'compose' with -f for each file.
func composeArgsOf(files []string) []string {
	args := []string{"compose"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return args
}
