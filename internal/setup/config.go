// Package setup creates and reads an App for OpenArch setup folder: the
// plugin settings, the generated files and the upstream checkout.
package setup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// DefaultDir is the setup folder that 'init' creates, and that the other
// commands use, when no folder is given.
const DefaultDir = "openarch-app"

// Names of the files and folders in a setup folder.
const (
	ConfigFile   = "openarch-app.yaml"
	EnvFile      = ".env.local"
	OverrideFile = "compose.override.yaml"
	UsersFile    = "openarch-users.yml"
	UpstreamDir  = "upstream"
)

// SchemaVersion is the version of openarch-app.yaml that this build writes.
// Version 2 replaced profile and extras by the source counts, and added
// managedNuxeo. 'upgrade' migrates a file of an older version.
const SchemaVersion = 2

// Config is openarch-app.yaml: the settings of a setup. It holds no secrets.
type Config struct {
	SchemaVersion int    `yaml:"schemaVersion" json:"schemaVersion"`
	Upstream      string `yaml:"upstream" json:"upstream"`
	// Sources are the Alfresco and Nuxeo instances. Profile() and Extras() give
	// the upstream profile and extras that run them.
	stack.Sources  `yaml:",inline"`
	LLMModel       string `yaml:"llmModel" json:"llmModel"`
	EmbeddingModel string `yaml:"embeddingModel" json:"embeddingModel"`
	BindIP         string `yaml:"bindIP" json:"bindIP"`
	Context        string `yaml:"context" json:"context"`
	DefaultSecrets bool   `yaml:"defaultSecrets,omitempty" json:"defaultSecrets,omitempty"`
	DefaultUsers   bool   `yaml:"defaultUsers,omitempty" json:"defaultUsers,omitempty"`
	// ManagedNuxeo means that init fetched nuxeo-deployment, so the plugin runs
	// it in the same Compose project. A setup made before 0.3.0 has a
	// nuxeo-deployment that the user started as a project of its own.
	ManagedNuxeo bool `yaml:"managedNuxeo,omitempty" json:"managedNuxeo,omitempty"`
	// SampleUI runs the upstream sample UI, which the proxy serves at /. It is
	// optional, so a file without it keeps schemaVersion 2.
	SampleUI bool `yaml:"sampleUI,omitempty" json:"sampleUI,omitempty"`
	// Filesystem is the absolute path of the folder that the filesystem source
	// reads, or empty for no filesystem source. It is optional too.
	Filesystem string `yaml:"filesystem,omitempty" json:"filesystem,omitempty"`
	// CMIS is the URL of the CMIS source (CMIS_URL), or empty for no CMIS
	// source. It is optional too. Its other settings are in .env.local.
	CMIS string `yaml:"cmis,omitempty" json:"cmis,omitempty"`
	// BuiltContexts maps each build context variable, and "upstream", to the
	// commit of the last build, so that 'up' knows when to rebuild.
	BuiltContexts map[string]string `yaml:"builtContexts,omitempty" json:"builtContexts,omitempty"`
}

// Connector returns the upstream connector of the setup: "filesystem",
// "cmis", or empty for none. plugin-batch-ingester runs it.
func (c Config) Connector() string {
	switch {
	case c.Filesystem != "":
		return "filesystem"
	case c.CMIS != "":
		return "cmis"
	}
	return ""
}

// Load reads openarch-app.yaml from the setup folder dir. A file of an older
// version needs 'upgrade' first.
func Load(dir string) (Config, error) {
	c, err := LoadForUpgrade(dir)
	if err == nil && c.SchemaVersion < SchemaVersion {
		return c, output.Usage(fmt.Sprintf("%s has schemaVersion %d, this build reads %d", ConfigFile, c.SchemaVersion, SchemaVersion), upgradeHint)
	}
	return c, err
}

// LoadForUpgrade reads openarch-app.yaml like Load, and also accepts an older
// version, which 'upgrade' migrates.
func LoadForUpgrade(dir string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, output.Usage(fmt.Sprintf("%s has no %s", dir, ConfigFile), "Run 'openarch app init' first, or use --dir.")
	}
	if err != nil {
		return c, err
	}
	var version struct {
		SchemaVersion int `yaml:"schemaVersion"`
	}
	if err := yaml.Unmarshal(data, &version); err != nil {
		return c, output.Usage(fmt.Sprintf("cannot read %s: %v", ConfigFile, err), "")
	}
	if version.SchemaVersion == 1 {
		return loadVersion1(data)
	}
	if err := decodeStrict(data, &c); err != nil {
		return c, err
	}
	if c.SchemaVersion < 1 || c.SchemaVersion > SchemaVersion {
		return c, output.Usage(fmt.Sprintf("%s has schemaVersion %d, this build reads %d", ConfigFile, c.SchemaVersion, SchemaVersion), "Update openarch-app.")
	}
	return c, nil
}

// decodeStrict decodes data into v and refuses unknown fields.
func decodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return output.Usage(fmt.Sprintf("cannot read %s: %v", ConfigFile, err), "")
	}
	return nil
}

// configVersion1 is openarch-app.yaml of 0.1.0 to 0.2.0: the upstream profile
// and extras instead of the source counts.
type configVersion1 struct {
	SchemaVersion  int               `yaml:"schemaVersion"`
	Upstream       string            `yaml:"upstream"`
	Profile        string            `yaml:"profile"`
	Extras         []string          `yaml:"extras,omitempty"`
	LLMModel       string            `yaml:"llmModel"`
	EmbeddingModel string            `yaml:"embeddingModel"`
	BindIP         string            `yaml:"bindIP"`
	Context        string            `yaml:"context"`
	DefaultSecrets bool              `yaml:"defaultSecrets,omitempty"`
	DefaultUsers   bool              `yaml:"defaultUsers,omitempty"`
	BuiltContexts  map[string]string `yaml:"builtContexts,omitempty"`
}

// loadVersion1 reads a version 1 file into a Config that still has
// SchemaVersion 1, so that only 'upgrade' accepts it. Its nuxeo-deployment is
// the user's, so ManagedNuxeo stays false.
func loadVersion1(data []byte) (Config, error) {
	var v configVersion1
	if err := decodeStrict(data, &v); err != nil {
		return Config{}, err
	}
	sources, err := stack.SourcesOf(v.Profile, v.Extras)
	if err != nil {
		return Config{}, output.Usage(fmt.Sprintf("cannot read %s: %v", ConfigFile, err), "")
	}
	return Config{SchemaVersion: 1, Upstream: v.Upstream, Sources: sources, LLMModel: v.LLMModel,
		EmbeddingModel: v.EmbeddingModel, BindIP: v.BindIP, Context: v.Context, DefaultSecrets: v.DefaultSecrets,
		DefaultUsers: v.DefaultUsers, BuiltContexts: v.BuiltContexts}, nil
}

// Save writes openarch-app.yaml into the setup folder dir.
func Save(dir string, c Config) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# App for OpenArch setup, written by 'openarch app init'. It holds no secrets.\n"
	return writeFile(filepath.Join(dir, ConfigFile), append([]byte(header), data...), 0o644)
}

// writeFile writes data through a temporary file, so a reader never sees a
// half-written file.
func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	// After the rename the temporary name no longer exists, so this only
	// cleans up after a failure.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
