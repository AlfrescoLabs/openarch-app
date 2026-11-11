package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
)

// cmisPasswordKey is the CMIS password, the one key of the CMIS file that
// may also come from somewhere else.
const cmisPasswordKey = "CMIS_PASSWORD"

// cmisKey reports whether init copies key from a CMIS file: the settings of
// the CMIS connector, the CMIS caller login of the RAG service, and the
// roots of a sync.
func cmisKey(key string) bool {
	return strings.HasPrefix(key, "CMIS_") || strings.HasPrefix(key, "RAG_SECURITY_CMIS_") || key == "CONNECTOR_ROOTS"
}

// cmisSource reads the CMIS file path and returns the .env.local values of
// the CMIS source. It sets c.CMIS to CMIS_URL. password gives the CMIS
// password when the file has none; nil means there is no other way.
func cmisSource(c *Config, path string, password func() (string, error)) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, output.Usage(fmt.Sprintf("--cmis-env %s does not exist", path), "")
	}
	if err != nil {
		return nil, err
	}
	values := ParseEnv(data)
	var other []string
	for k := range values {
		if !cmisKey(k) {
			other = append(other, k)
		}
	}
	if len(other) > 0 {
		slices.Sort(other)
		return nil, output.Usage(fmt.Sprintf("--cmis-env %s has keys that init does not copy: %s", path, strings.Join(other, ", ")),
			"The file can only hold CMIS_*, RAG_SECURITY_CMIS_* and CONNECTOR_ROOTS.")
	}
	if strings.EqualFold(strings.TrimSpace(values["CMIS_ACL_FALLBACK"]), "public") {
		return nil, output.Usage("CMIS_ACL_FALLBACK=public is refused",
			"With public, every user reads a document whose permissions the repository cannot report. Use fail-closed (the default) or sync-account.")
	}
	u, err := url.Parse(values["CMIS_URL"])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, output.Usage(fmt.Sprintf("--cmis-env %s needs CMIS_URL, an http or https URL", path),
			"Use the CMIS endpoint as the containers reach it, for example http://host.docker.internal:8080/alfresco/api/-default-/public/cmis/versions/1.1/browser.")
	}
	if u.User != nil {
		return nil, output.Usage("CMIS_URL holds a login", "Put the login in CMIS_USERNAME and CMIS_PASSWORD: init stores CMIS_URL in "+ConfigFile+".")
	}
	if h := u.Hostname(); h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1" {
		return nil, output.Usage("CMIS_URL points to "+h+", which is the container itself",
			"The connector runs in a container. For a repository on this machine, use host.docker.internal.")
	}
	if values["CMIS_USERNAME"] == "" {
		return nil, output.Usage(fmt.Sprintf("--cmis-env %s needs CMIS_USERNAME", path), "")
	}
	if values[cmisPasswordKey] == "" {
		if password == nil {
			return nil, output.Usage("the CMIS password is missing",
				"Put CMIS_PASSWORD in the --cmis-env file, set OPENARCH_APP_CMIS_PASSWORD, or use --cmis-password-stdin.")
		}
		p, err := password()
		if err != nil {
			return nil, err
		}
		if p == "" {
			return nil, output.Usage("the CMIS password is empty", "")
		}
		values[cmisPasswordKey] = p
	}
	quoted := make(map[string]string, len(values))
	for k, v := range values {
		// Single quotes keep $ and # as they are, and cannot hold a quote.
		if strings.ContainsRune(v, '\'') || strings.ContainsFunc(v, unicode.IsControl) {
			return nil, output.Usage(fmt.Sprintf("the CMIS setting %s has a single quote or a control character", k), "init writes the values in single quotes to "+EnvFile+".")
		}
		quoted[k] = "'" + v + "'"
	}
	c.CMIS = values["CMIS_URL"]
	return quoted, nil
}
