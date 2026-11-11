package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	// moduleTarget is where the repository loads the module JAR from. It is
	// the WEB-INF/lib of the official repository images.
	moduleTarget = "/usr/local/tomcat/webapps/alfresco/WEB-INF/lib/content-lake-repo-model.jar"
	// ProjectAssets is the folder in the Alfresco project that holds the files
	// the override mounts, so the project does not depend on the setup folder.
	ProjectAssets = "openarch-app"
	moduleFile    = "content-lake-repo-model.jar"
	prefixesFile  = "reindex.prefixes-file.json"
	// userACALinks is the link of an Alfresco source in an answer, for the
	// user's own ACA served at / (the ACA 8 search viewer route). The
	// upstream default opens the stack's ACA at /aca/.
	userACALinks = "/#/search/(viewer:view/{nodeId})?location=%2Frag"
	// noIngestionFolder is a folder id that no repository has. A configured
	// sync skips a folder that it cannot find, so 'POST /api/sync/configured'
	// syncs nothing instead of the upstream default -root-, the whole
	// repository. Folders are synced with 'POST /api/sync/batch' instead.
	noIngestionFolder = "openarch-app-no-default-folder"
)

// OverrideHeader starts every override file that the plugin writes into an
// existing Alfresco project, so the plugin knows its own file.
const OverrideHeader = "# Written by 'openarch app alfresco-prepare'. It connects this Alfresco to App for OpenArch.\n"

var tagChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

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

// renderProjectOverride returns the override file for project p. Its mounts
// point to the ProjectAssets folder of the project, relative to the project.
func renderProjectOverride(p AlfrescoProject) []byte {
	var b strings.Builder
	b.WriteString(OverrideHeader)
	b.WriteString("# Undo it with 'openarch app alfresco-prepare --undo'. Do not only delete it: nodes with cl: aspects\n")
	b.WriteString("# cannot be read once the module is gone.\n")
	fmt.Fprintf(&b, "networks:\n  %s:\n    external: true\n", SharedNetwork)
	b.WriteString("services:\n")
	// The repository keeps the image of the project, so a later change of its
	// tag still counts. The module JAR is mounted into it, and the module
	// properties are added. Its broker is the broker service of the project.
	opts := strings.TrimSpace(p.javaOpts) + fmt.Sprintf(
		" -Dcontent.lake.permission.sync.enabled=true -Dcontent.lake.permission.sync.broker-url=tcp://%s:61616"+
			" -Dcontent.lake.permission.sync.broker-user=%s -Dcontent.lake.permission.sync.broker-password=%s",
		p.Broker, p.BrokerUser, p.brokerPassword)
	// The long syntax for every mount, because a Windows path holds a colon.
	fmt.Fprintf(&b, "  %s:\n    environment:\n      JAVA_OPTS: %s\n    volumes:\n%s", p.Repository, quote(opts), bindMount("./"+ProjectAssets+"/"+moduleFile, moduleTarget))
	b.WriteString(p.networksBlock(p.Repository, alfrescoAlias))
	fmt.Fprintf(&b, "  %s:\n", p.Broker)
	b.WriteString(p.networksBlock(p.Broker, brokerAlias))
	if p.Search == "opensearch" {
		fmt.Fprintf(&b, "  %s:\n    environment:\n      ALFRESCO_REINDEX_PREFIXES_FILE: %s\n    volumes:\n%s",
			p.Indexer, quote("file:"+prefixesTarget), bindMount("./"+ProjectAssets+"/"+prefixesFile, prefixesTarget))
	}
	if p.ContentApp != "" {
		// The image has its own nginx, which forwards /alfresco/ to the
		// repository and the App for OpenArch paths to the stack proxy. It
		// looks the names up per request, so ACA runs while the stack is down.
		fmt.Fprintf(&b, "  %s:\n    image: %s\n    environment:\n      APP_CONFIG_ECM_HOST: %s\n      APP_CONFIG_RAG_URL: %s\n      APP_CONFIG_CONTENT_LAKE_URL: %s\n",
			p.ContentApp, quote(ContentAppImage(p)), quote("http://"+p.Repository+":8080"), quote("http://"+proxyAlias), quote("http://"+proxyAlias))
		b.WriteString(p.networksBlock(p.ContentApp, ""))
	}
	return []byte(b.String())
}

// bindMount returns a read-only bind mount entry of a service in the override.
func bindMount(source, target string) string {
	return fmt.Sprintf("      - type: bind\n        source: %s\n        target: %s\n        read_only: true\n", quote(source), quote(target))
}

// Prepared reports whether the repository of project p mounts the module.
func (p AlfrescoProject) Prepared() bool {
	return slices.Contains(p.repositoryMounts, moduleTarget)
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

// The file names that Compose finds by itself, in its order. It merges the
// first override file that exists, whatever the main file is called, and no
// override file at all while COMPOSE_FILE is set.
var (
	mainFiles     = []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"}
	overrideFiles = []string{"compose.override.yml", "compose.override.yaml", "docker-compose.override.yml", "docker-compose.override.yaml"}
)

// projectLayout is how Compose finds the files of a project.
type projectLayout struct {
	// main is the main file, override the override file that Compose merges
	// (or the one the plugin creates when there is none).
	main, override string
	// exists says whether override exists, ours whether the plugin wrote it.
	exists, ours bool
	// composeFile is COMPOSE_FILE of the environment or of the project's .env.
	composeFile string
}

// projectFiles returns the layout of the Compose project in dir.
func projectFiles(dir string) (projectLayout, error) {
	var l projectLayout
	for _, f := range mainFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			l.main = f
			break
		}
	}
	if l.main == "" {
		return l, output.Usage("no compose.yaml or docker-compose.yml in "+dir, "")
	}
	l.override = filepath.Join(dir, "compose.override.yaml")
	for _, f := range overrideFiles {
		if data, err := os.ReadFile(filepath.Join(dir, f)); err == nil {
			l.override, l.exists, l.ours = filepath.Join(dir, f), true, strings.HasPrefix(string(data), OverrideHeader)
			break
		}
	}
	l.composeFile = os.Getenv("COMPOSE_FILE")
	if data, err := os.ReadFile(filepath.Join(dir, ".env")); err == nil && l.composeFile == "" {
		l.composeFile = ParseEnv(data)["COMPOSE_FILE"]
	}
	return l, nil
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
	Project string `json:"project"`
	// Assets is the folder with the files that the override mounts, and
	// Override the override file, both in the setup folder.
	Assets   string   `json:"assets"`
	Override string   `json:"override"`
	Images   []string `json:"images,omitempty"`
	Applied  bool     `json:"applied"`
	Steps    []string `json:"steps,omitempty"`
}

// WriteText writes the human-readable form.
func (r PrepareResult) WriteText(w io.Writer) error {
	var b strings.Builder
	built := "the module JAR"
	if len(r.Images) > 0 {
		built += " and " + strings.Join(r.Images, " and ")
	}
	fmt.Fprintf(&b, "Built %s.\n", built)
	if r.Applied {
		fmt.Fprintf(&b, "Installed the changes in %s and recreated its changed services.\n", r.Project)
	} else {
		fmt.Fprintf(&b, "Wrote %s and %s. Nothing in %s changed.\n\nTo apply them:\n", r.Override, r.Assets, r.Project)
		for i, s := range r.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// acaVersion matches an ACA image tag that names a version, such as 8.0.0.
var acaVersion = regexp.MustCompile(`^[0-9]+\.[0-9]`)

// Prepare builds the module JAR and the ACA image for the existing Alfresco
// project of the setup, and writes the override file and the files it mounts.
// With opts.Apply it installs them in the project and recreates the changed
// services. The repository keeps the image of the project, with the module
// mounted; ACA is replaced by an ACA of the same version with the App for
// OpenArch extension.
func Prepare(ctx context.Context, env Env, s Setup, opts PrepareOptions, st Streams) (PrepareResult, error) {
	res := PrepareResult{Project: s.Config.AlfrescoCompose}
	if s.Config.AlfrescoCompose == "" {
		return res, output.Usage("the setup has no Alfresco Compose project", "Create the setup with 'openarch app init --alfresco-compose <dir>'.")
	}
	if err := s.CheckPins(); err != nil {
		return res, err
	}
	l, err := projectFiles(s.Config.AlfrescoCompose)
	if err != nil {
		return res, err
	}
	// --apply writes only where Compose reads it, and only its own file.
	if opts.Apply && l.composeFile != "" {
		return res, output.Usage("the project sets COMPOSE_FILE, so Compose merges no override file", "Run 'openarch app alfresco-prepare' without --apply: it prints the steps for this case.")
	}
	if opts.Apply && l.exists && !l.ours {
		return res, output.Usage(l.override+" exists and is not from openarch-app", "Run 'openarch app alfresco-prepare' without --apply: it prints how to merge the changes into that file.")
	}
	// Read the project without the plugin's own override, so a second run
	// starts from the user's files again.
	var files []string
	if l.ours && l.composeFile == "" {
		files = []string{l.main}
	}
	p, err := InspectAlfrescoProject(ctx, env, s.Config.AlfrescoCompose, files...)
	if err != nil {
		return res, err
	}
	// Check everything before the builds, which take minutes.
	if p.ContentApp != "" && !acaVersion.MatchString(p.ACAVersion) {
		return res, output.Usage(fmt.Sprintf("cannot tell the ACA version of %s in %s (tag %q)", p.ContentApp, p.Dir, p.ACAVersion),
			"Give its image a version tag, for example alfresco/alfresco-content-app:8.0.0.")
	}
	if p.Search == "opensearch" && p.indexerPrefixes != "" && p.indexerPrefixes != "file:"+prefixesTarget {
		return res, output.Usage(p.Indexer+" already sets ALFRESCO_REINDEX_PREFIXES_FILE ("+p.indexerPrefixes+")",
			"Add the cl prefix of upstream/acs/search/reindex.prefixes-file.json to that file by hand. The plugin does not merge prefix files.")
	}
	values, err := s.env()
	if err != nil {
		return res, err
	}
	work := filepath.Join(s.Dir, "alfresco-prepare")
	module, err := AlfrescoModule(ctx, env, s, filepath.Join(work, "build"), st)
	if err != nil {
		return res, err
	}
	res.Assets = filepath.Join(work, ProjectAssets)
	assets := map[string]string{moduleFile: module.JAR}
	if p.Search == "opensearch" {
		assets[prefixesFile] = filepath.Join(s.Dir, UpstreamDir, "acs", "search", prefixesFile)
	}
	for name, src := range assets {
		data, err := os.ReadFile(src)
		if err != nil {
			return res, err
		}
		if err := os.MkdirAll(res.Assets, 0o755); err != nil {
			return res, err
		}
		// Readable by the user that the repository and the indexer run as.
		if err := writeFile(filepath.Join(res.Assets, name), data, 0o644); err != nil {
			return res, err
		}
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
	if err := writeFile(res.Override, renderProjectOverride(p), 0o600); err != nil {
		return res, err
	}
	res.Steps = prepareSteps(res, p, l)
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
	if err := copyDir(res.Assets, filepath.Join(p.Dir, ProjectAssets)); err != nil {
		return res, err
	}
	data, err := os.ReadFile(res.Override)
	if err != nil {
		return res, err
	}
	if err := writeFile(l.override, data, 0o600); err != nil {
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
				fmt.Sprintf("See 'docker compose logs' in %s. Nothing is synced yet when this happens right after --apply: then remove %s and run 'docker compose up -d' there.", p.Dir, l.override))
		}
	}
	res.Applied = true
	res.Steps = nil
	return res, nil
}

// prepareSteps returns the steps that apply the files of res to project p by
// hand, for the layout l of the project.
func prepareSteps(res PrepareResult, p AlfrescoProject, l projectLayout) []string {
	steps := []string{
		"Back up the Alfresco database and content store.",
		"Copy the folder " + res.Assets + " to " + filepath.Join(p.Dir, ProjectAssets) + ".",
	}
	switch {
	case l.exists && !l.ours:
		steps = append(steps, "Merge "+res.Override+" into your "+l.override+" by hand. Do not replace that file. 'openarch app alfresco-prepare --undo' then removes the cl: aspects, and you remove the merged lines.")
	default:
		steps = append(steps, "Copy "+res.Override+" to "+l.override+".")
	}
	if l.composeFile != "" {
		steps = append(steps, "Compose merges no override file while COMPOSE_FILE is set ("+l.composeFile+"): add "+filepath.Base(l.override)+" to COMPOSE_FILE.")
	}
	return append(steps,
		// The override file declares the shared network as external, so Compose refuses
		// to start the project until it exists. Only --apply and 'up' create it.
		"Create the shared network once: 'docker network create "+SharedNetwork+"' (skip it if 'docker network ls' lists it).",
		"Run 'docker compose up -d' in "+p.Dir+". Compose recreates only the changed services.",
		"Run 'openarch app up'.")
}

// copyDir copies the files of the folder src into the folder dst.
func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// composeArgsOf returns 'compose' with -f for each file.
func composeArgsOf(files []string) []string {
	args := []string{"compose"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return args
}
