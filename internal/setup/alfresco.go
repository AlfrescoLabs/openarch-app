package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// Keys of an --alfresco-env file: the ones of the upstream alfresco-external
// profile. The broker of the ACL queue and the proxy target have defaults.
var (
	alfrescoRequiredKeys = []string{"ALFRESCO_URL", "ALFRESCO_EVENT_BROKER_URL", "ALFRESCO_ADMIN_USER", "ALFRESCO_ADMIN_PASSWORD", "ACTIVEMQ_USER", "ACTIVEMQ_PASSWORD"}
	alfrescoOptionalKeys = []string{"CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL", "ALFRESCO_PROXY_UPSTREAM"}
)

// MinAlfrescoVersion is the oldest major version of an existing Alfresco:
// content-lake-app builds its repository module for 25.x and later.
const MinAlfrescoVersion = 25

// moduleHint tells how to prepare an existing Alfresco.
const moduleHint = "Run 'openarch app alfresco-module <dir>', install the JAR and its properties in that Alfresco, and restart it (see 'openarch app alfresco-module --help')."

// alfrescoSource reads the --alfresco-env file path and returns the
// .env.local values of the existing Alfresco. It sets c.ExternalAlfresco to
// ALFRESCO_URL. The URLs are the ones that the containers reach.
func alfrescoSource(c *Config, path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	if c.Alfresco != 1 || c.Nuxeo != 0 {
		return nil, output.Usage("--alfresco-env works with --alfresco 1 --nuxeo 0 only", "An existing Alfresco replaces the bundled one, and it is tested without other sources.")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, output.Usage(fmt.Sprintf("--alfresco-env %s does not exist", path), "")
	}
	if err != nil {
		return nil, err
	}
	values := ParseEnv(data)
	var other, missing []string
	for k := range values {
		if !slices.Contains(alfrescoRequiredKeys, k) && !slices.Contains(alfrescoOptionalKeys, k) {
			other = append(other, k)
		}
	}
	for _, k := range alfrescoRequiredKeys {
		if values[k] == "" {
			missing = append(missing, k)
		}
	}
	slices.Sort(other)
	switch {
	case len(other) > 0:
		return nil, output.Usage(fmt.Sprintf("--alfresco-env %s has keys that init does not copy: %s", path, strings.Join(other, ", ")),
			"The file can only hold "+strings.Join(append(slices.Clone(alfrescoRequiredKeys), alfrescoOptionalKeys...), ", ")+".")
	case len(missing) > 0:
		return nil, output.Usage(fmt.Sprintf("--alfresco-env %s needs %s", path, strings.Join(missing, ", ")), "")
	}
	u, err := containerURL("ALFRESCO_URL", values["ALFRESCO_URL"], "http")
	if err != nil {
		return nil, err
	}
	if p := strings.TrimSuffix(u.Path, "/"); p != "" {
		return nil, output.Usage("ALFRESCO_URL has the path "+u.Path, "Give the address without /alfresco, for example http://alfresco.example.com:8080: the clients add the paths.")
	}
	if values["CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL"] == "" {
		// The repository module and Event2 use one broker in a normal Alfresco.
		values["CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL"] = values["ALFRESCO_EVENT_BROKER_URL"]
	}
	for _, k := range []string{"ALFRESCO_EVENT_BROKER_URL", "CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL"} {
		if _, err := containerURL(k, values[k], "tcp", "nio", "ssl", "failover"); err != nil {
			return nil, err
		}
	}
	if values["ALFRESCO_PROXY_UPSTREAM"] == "" {
		values["ALFRESCO_PROXY_UPSTREAM"] = hostPort(u)
	}
	quoted := map[string]string{
		// The stack has no bundled broker, so the transform engine works over
		// HTTP only.
		"TRANSFORM_ACTIVEMQ_URL": "false",
	}
	for k, v := range values {
		if strings.ContainsRune(v, '\'') || strings.ContainsFunc(v, unicode.IsControl) {
			return nil, output.Usage(fmt.Sprintf("the Alfresco setting %s has a single quote or a control character", k), "init writes the values in single quotes to "+EnvFile+".")
		}
		quoted[k] = "'" + v + "'"
	}
	c.ExternalAlfresco = strings.TrimSuffix(values["ALFRESCO_URL"], "/")
	return quoted, nil
}

// containerURL parses the URL in key for a container of the stack. It refuses
// another scheme, a login in the URL, and the loopback address, which is the
// container itself.
func containerURL(key, raw string, schemes ...string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !slices.Contains(schemes, u.Scheme) || u.Host == "" {
		// A failover URL has no host of its own: failover:(tcp://a:61616,...).
		if err == nil && u.Scheme == "failover" && slices.Contains(schemes, "failover") {
			return u, nil
		}
		return nil, output.Usage(fmt.Sprintf("%s=%s is not a %s URL", key, raw, strings.Join(schemes, ", ")), "")
	}
	if u.User != nil {
		return nil, output.Usage(key+" holds a login", "Put the login in the user and password keys.")
	}
	if h := u.Hostname(); h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1" {
		return nil, output.Usage(key+" points to "+h+", which is the container itself",
			"The stack runs in containers. For an Alfresco on this machine, use host.docker.internal.")
	}
	return u, nil
}

// hostPort returns host:port of u, with the default port of its scheme.
func hostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	return net.JoinHostPort(u.Hostname(), "80")
}

// alfrescoModuleSteps are the steps on the existing Alfresco side, after the
// JAR is in dir.
func alfrescoModuleSteps(jar string) []string {
	return []string{
		"Back up the Alfresco database and content store.",
		"Copy " + jar + " into webapps/alfresco/WEB-INF/lib/ of the repository, or into its image (alfresco-docker-installer: alfresco/modules/jars/).",
		"Add to alfresco-global.properties (or JAVA_OPTS): content.lake.permission.sync.enabled=true, content.lake.permission.sync.broker-url=<its broker, as Alfresco reaches it>, content.lake.permission.sync.broker-user=<user>, content.lake.permission.sync.broker-password=<password>.",
		"Make the broker reachable from the stack, at ALFRESCO_EVENT_BROKER_URL of the --alfresco-env file.",
		"With Alfresco Search Community (OpenSearch), mount upstream/acs/search/reindex.prefixes-file.json in its batch indexer and set ALFRESCO_REINDEX_PREFIXES_FILE=file:<path>.",
		"Restart Alfresco.",
	}
}

// ModuleResult is the outcome of AlfrescoModule.
type ModuleResult struct {
	JAR   string   `json:"jar"`
	Steps []string `json:"steps"`
}

// WriteText writes the human-readable form.
func (r ModuleResult) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Wrote %s.\n\nNext, on the existing Alfresco:\n", r.JAR)
	for i, s := range r.Steps {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// AlfrescoModule builds the repository module JAR of the pinned content-lake-app
// commit into the folder out, with the upstream model-jar stage. It needs no
// Maven on the host. It never changes an Alfresco.
func AlfrescoModule(ctx context.Context, env Env, s Setup, out string, st Streams) (ModuleResult, error) {
	var res ModuleResult
	if err := s.CheckPins(); err != nil {
		return res, err
	}
	values, err := s.env()
	if err != nil {
		return res, err
	}
	dir, err := filepath.Abs(out)
	if err != nil {
		return res, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, err
	}
	tag := values["ALFRESCO_CE_TAG"]
	if tag == "" {
		tag = "26.2.0"
	}
	// The stage itself does not use the tag, but BuildKit parses every FROM.
	args := []string{"build", "--target", "model-jar", "--build-arg", "ALFRESCO_TAG=" + tag,
		"--build-context", "content_lake_app=" + values["CONTENT_LAKE_ACS_GIT_CONTEXT"],
		"--output", "type=local,dest=" + dir, filepath.Join(UpstreamDir, "acs", "alfresco")}
	if err := env.Stream(ctx, s.Dir, nil, st.Err, st.Err, "docker", args...); err != nil {
		return res, output.NewError(output.ExitError, "cannot build the module JAR: "+err.Error(), "Check that Docker runs and can reach GitHub.")
	}
	jars, _ := filepath.Glob(filepath.Join(dir, "content-lake-repo-model-*.jar"))
	if len(jars) == 0 {
		return res, fmt.Errorf("the build wrote no content-lake-repo-model JAR to %s", dir)
	}
	res.JAR = jars[0]
	res.Steps = alfrescoModuleSteps(res.JAR)
	return res, nil
}

// CheckExternalAlfresco checks the existing Alfresco through the proxy of the
// running stack, which forwards /alfresco/ to it: the login works, the version
// is supported, and the content-lake-repo-model module is installed.
func CheckExternalAlfresco(ctx context.Context, env Env, s Setup) error {
	values, err := s.env()
	if err != nil {
		return err
	}
	base := "http://" + contextHost(s.Config.BindIP) + "/alfresco/api"
	get := func(path string, v any) (int, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return 0, err
		}
		req.SetBasicAuth(values["ALFRESCO_ADMIN_USER"], values["ALFRESCO_ADMIN_PASSWORD"])
		resp, err := env.HTTP.Do(req)
		if err != nil {
			return 0, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK && v != nil {
			return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
		}
		return resp.StatusCode, nil
	}
	var discovery struct {
		Entry struct {
			Repository struct {
				Version struct {
					Major   string `json:"major"`
					Display string `json:"display"`
				} `json:"version"`
			} `json:"repository"`
		} `json:"entry"`
	}
	code, err := get("/discovery", &discovery)
	switch {
	case err != nil:
		return output.NewError(output.ExitUnhealthy, "the existing Alfresco does not answer through the proxy: "+err.Error(), "Check ALFRESCO_PROXY_UPSTREAM in "+EnvFile+".")
	case code == http.StatusUnauthorized:
		return output.NewError(output.ExitAuth, "the existing Alfresco refused ALFRESCO_ADMIN_USER", "Check ALFRESCO_ADMIN_USER and ALFRESCO_ADMIN_PASSWORD in "+EnvFile+".")
	case code != http.StatusOK:
		return output.NewError(output.ExitUnhealthy, fmt.Sprintf("the existing Alfresco answered %d to /alfresco/api/discovery through the proxy", code), "Check ALFRESCO_PROXY_UPSTREAM in "+EnvFile+".")
	}
	v := discovery.Entry.Repository.Version
	var major int
	if _, err := fmt.Sscan(v.Major, &major); err != nil || major < MinAlfrescoVersion {
		return output.NewError(output.ExitPrerequisite, fmt.Sprintf("the existing Alfresco is version %s; App for OpenArch needs %d or later", v.Display, MinAlfrescoVersion), "")
	}
	code, err = get("/-default-/public/alfresco/versions/1/aspects/cl:indexed", nil)
	switch {
	case err != nil:
		return output.NewError(output.ExitUnhealthy, "cannot check the content model of the existing Alfresco: "+err.Error(), "")
	case code == http.StatusNotFound:
		return output.NewError(output.ExitPrerequisite, "the existing Alfresco has no content-lake-repo-model module (aspect cl:indexed is unknown)", moduleHint)
	case code != http.StatusOK:
		return output.NewError(output.ExitUnhealthy, fmt.Sprintf("the existing Alfresco answered %d to the cl:indexed aspect", code), "")
	}
	return nil
}

// externalAlfrescoProfile is the upstream profile of a setup with an existing
// Alfresco instead of the bundled one.
const externalAlfrescoProfile = "alfresco-external"
