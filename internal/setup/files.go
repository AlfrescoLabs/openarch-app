package setup

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/md4" //nolint:staticcheck // Alfresco reads only this hash at bootstrap.

	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// RandomSecrets are the upstream secrets that init replaces. For each one the
// stack sets both the server side and the client side. The Alfresco admin
// passwords are random too (see alfrescoAdmins). NUXEO_PASSWORD keeps its
// upstream value, because Nuxeo creates its admin at the first boot and
// nuxeo-deployment has no setting for it.
var RandomSecrets = []string{"POSTGRES_PASSWORD", "SHARED_SECRET", "ACTIVEMQ_PASSWORD", "OPENSEARCH_ADMIN_PASSWORD"}

// alfrescoAdmins maps the password that the clients of each Alfresco use to
// the upstream variable of its bootstrap hash. Alfresco reads the hash only
// at the first boot of an empty database.
var alfrescoAdmins = []struct{ Password, Hash string }{
	{"ALFRESCO_ADMIN_PASSWORD", "ALFRESCO_ADMIN_PASSWORD_HASH"},
	{"ALFRESCO_2_INTERNAL_PASSWORD", "ALFRESCO_2_ADMIN_PASSWORD_HASH"},
}

// UpstreamAlfrescoPassword is the Alfresco admin password of upstream and of
// setups made with --default-secrets or before 0.2.0.
const UpstreamAlfrescoPassword = "admin"

// Keys of the OpenArch login that the ingesters and the RAG service use.
const (
	adminUserKey     = "HXPR_USERNAME"
	adminPasswordKey = "HXPR_PASSWORD"
	adminUser        = "admin"
)

const (
	passwordLetters = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	passwordSymbols = "-_"
	passwordLength  = 24
)

// NewPassword returns a random password with upper and lower case letters,
// digits and a symbol, as the OpenSearch password rule asks. It uses only
// characters that need no quoting in an env file or escaping in a URL.
func NewPassword() (string, error) {
	classes := []string{"abcdefghijkmnopqrstuvwxyz", "ABCDEFGHJKLMNPQRSTUVWXYZ", "23456789", passwordSymbols}
	b := make([]byte, 0, passwordLength)
	for _, c := range classes {
		ch, err := pick(c)
		if err != nil {
			return "", err
		}
		b = append(b, ch)
	}
	for len(b) < passwordLength {
		ch, err := pick(passwordLetters)
		if err != nil {
			return "", err
		}
		b = append(b, ch)
	}
	// Shuffle, so the classes are not always in the first four places.
	for i := len(b) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		b[i], b[j.Int64()] = b[j.Int64()], b[i]
	}
	return string(b), nil
}

func pick(chars string) (byte, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
	if err != nil {
		return 0, err
	}
	return chars[n.Int64()], nil
}

// ParseEnv reads KEY=VALUE lines. It skips blank lines and comments, and
// removes one pair of surrounding quotes from a value.
func ParseEnv(data []byte) map[string]string {
	env := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env
}

// alfrescoNameKeys are the upstream variables of the Alfresco source names,
// in instance order.
var alfrescoNameKeys = []string{"ALFRESCO_1_DISPLAY_NAME", "ALFRESCO_2_DISPLAY_NAME"}

// envValues returns the settings and secrets that init writes to .env.local,
// with names as the names of the Alfresco sources and source as the values of
// the settings of the connector source and of an existing Alfresco.
func envValues(c Config, names []string, source map[string]string) (map[string]string, error) {
	v := map[string]string{
		"BIND_IP":         c.BindIP,
		"USE_HTTPS":       "false",
		"LLM_MODEL":       c.LLMModel,
		"EMBEDDING_MODEL": c.EmbeddingModel,
	}
	if !c.UsesModelRunner() {
		// The services reach a server on this machine at host.docker.internal.
		v["MODEL_RUNNER_URL"] = stack.ContainerModelURL(c.ModelURL)
	}
	for _, g := range stack.GitContexts {
		v[g.Var] = g.URL()
	}
	for i, n := range names {
		// Single quotes keep spaces and any $ as they are.
		v[alfrescoNameKeys[i]] = "'" + n + "'"
	}
	if c.SampleUI {
		// Single quotes, so Compose does not expand $demo_ui_upstream.
		v["NGINX_ROOT_DIRECTIVE"] = "'" + stack.SampleUIRoot + "'"
	}
	if c.Connector() != "" {
		cv, err := connectorValues(c)
		if err != nil {
			return nil, err
		}
		for k, x := range cv {
			v[k] = x
		}
	}
	if !c.DefaultSecrets {
		for _, k := range RandomSecrets {
			p, err := NewPassword()
			if err != nil {
				return nil, err
			}
			v[k] = p
		}
		// An existing Alfresco has its own admin password, from source.
		for _, a := range alfrescoAdmins {
			if c.ExternalAlfresco != "" {
				break
			}
			p, err := NewPassword()
			if err != nil {
				return nil, err
			}
			v[a.Password] = p
			v[a.Hash] = AlfrescoHash(p)
		}
	}
	if !c.DefaultUsers {
		p, err := NewPassword()
		if err != nil {
			return nil, err
		}
		v[adminUserKey] = adminUser
		v[adminPasswordKey] = p
	}
	// The values of the sources win, so the logins of an existing Alfresco
	// replace the random secrets of the bundled one.
	for k, x := range source {
		v[k] = x
	}
	return v, nil
}

// renderEnv writes the values of .env.local in a stable order.
func renderEnv(v map[string]string) []byte {
	var b bytes.Buffer
	b.WriteString("# Written by 'openarch app init'. These values override upstream/.env.\n")
	b.WriteString("# The databases keep the passwords of their first start: keep this file, and keep it private.\n")
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, v[k])
	}
	return b.Bytes()
}

// renderUsers writes the OpenArch users file with one admin user. The
// format is the one of the CLI for OpenArch.
func renderUsers(password string) ([]byte, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 3)
	for i := range ids {
		if ids[i], err = newUUID(); err != nil {
			return nil, err
		}
	}
	var b strings.Builder
	b.WriteString("# Hyland OpenArch users, written by 'openarch app init'. Passwords are bcrypt hashes.\n")
	b.WriteString("# OpenArch reads this file at startup. The admin password is HXPR_PASSWORD in .env.local.\n")
	b.WriteString("administratorsGroups:\n  - administrators\n")
	fmt.Fprintf(&b, "groups:\n  - id: %s\n    name: administrators\n  - id: %s\n    name: members\n", ids[0], ids[1])
	fmt.Fprintf(&b, "users:\n  - id: %s\n    username: %s\n    password: %s\n", ids[2], adminUser, strconv.Quote("{bcrypt}"+string(hash)))
	b.WriteString("    firstName: Admin\n    lastName: User\n    email: admin@example.com\n    groups:\n      - administrators\n      - members\n")
	return []byte(b.String()), nil
}

// renderOverride writes compose.override.yaml. Compose resolves its relative
// paths against the project directory, upstream/, so its neighbors are "../".
//
// Hyland OpenArch keeps its blobs, the embeddings included, in
// /deployments/nxserver/data. Upstream gives that folder no volume, so a
// recreated container loses them while MongoDB and OpenSearch keep the
// metadata. The override adds the volume, and a one-shot service gives it to
// the OpenArch user (uid 33010): OpenArch cannot write to a volume owned by root.
func renderOverride(c Config) []byte {
	var b strings.Builder
	b.WriteString("# Written by 'openarch app init' and 'up'. Relative paths start at upstream/, the Compose project directory.\n")
	b.WriteString("services:\n  hxpr-app:\n    ports:\n")
	fmt.Fprintf(&b, "      - \"${BIND_IP:-127.0.0.1}:%d:8080\"\n", stack.OpenArchPort)
	if !c.DefaultUsers {
		b.WriteString("    environment:\n      OPENARCH_USERS_FILE: file:/etc/openarch/users.yml\n")
	}
	b.WriteString("    volumes:\n      - " + openArchVolume + ":/deployments/nxserver/data\n")
	if !c.DefaultUsers {
		fmt.Fprintf(&b, "      - ../%s:/etc/openarch/users.yml:ro\n", UsersFile)
	}
	b.WriteString("    depends_on:\n      " + openArchVolumeInit + ":\n        condition: service_completed_successfully\n")
	fmt.Fprintf(&b, "  %s:\n    image: %s\n    command: [\"chown\", \"33010:1000\", \"/data\"]\n", openArchVolumeInit, initImage)
	b.WriteString("    network_mode: none\n    volumes:\n      - " + openArchVolume + ":/data\n")
	if c.ExternalAlfresco != "" {
		// The clients of an existing Alfresco project reach it, and its ACA
		// reaches the proxy, on the shared network. These values are here,
		// not in .env.local, so 'up' gives them to setups of older releases.
		var networks, linkTemplate string
		if c.AlfrescoCompose != "" {
			networks = fmt.Sprintf("    networks:\n      %s: {}\n", SharedNetwork)
			linkTemplate = fmt.Sprintf("    environment:\n      RAG_SOURCE_LINKS_ALFRESCO_TEMPLATE: %s\n", quote(userACALinks))
		}
		fmt.Fprintf(&b, "  batch-ingester:\n    environment:\n      INGESTION_FOLDER_1: %s\n%s", quote(noIngestionFolder), networks)
		if c.AlfrescoCompose != "" {
			fmt.Fprintf(&b, "  live-ingester:\n%s  rag-service:\n%s%s", networks, linkTemplate, networks)
			fmt.Fprintf(&b, "  proxy:\n    networks:\n      %s:\n        aliases: [%s]\n", SharedNetwork, proxyAlias)
		}
	}
	b.WriteString("volumes:\n  " + openArchVolume + ":\n")
	if c.AlfrescoCompose != "" {
		fmt.Fprintf(&b, "networks:\n  %s:\n    external: true\n", SharedNetwork)
	}
	return []byte(b.String())
}

// The OpenArch data volume and the service that sets its owner.
const (
	openArchVolume     = "hxpr-data"
	openArchVolumeInit = "hxpr-data-init"
	initImage          = "alpine:3.24"
)

// WriteOverride writes compose.override.yaml for setup c into dir. 'up' calls
// it too, so a setup made by an older build gets the current file.
func WriteOverride(dir string, c Config) error {
	return writeFile(filepath.Join(dir, OverrideFile), renderOverride(c), 0o644)
}

// AlfrescoHash returns the form of password that Alfresco reads from
// alfresco_user_store.adminpassword: MD4 over the UTF-16LE bytes, in hex.
func AlfrescoHash(password string) string {
	h := md4.New() //nolint:staticcheck // Alfresco reads only this hash at bootstrap.
	for _, u := range utf16.Encode([]rune(password)) {
		h.Write([]byte{byte(u), byte(u >> 8)})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func newUUID() (string, error) {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return "", err
	}
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}
