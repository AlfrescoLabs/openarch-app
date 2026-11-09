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
const SchemaVersion = 1

// Config is openarch-app.yaml: the settings of a setup. It holds no secrets.
type Config struct {
	SchemaVersion  int      `yaml:"schemaVersion" json:"schemaVersion"`
	Upstream       string   `yaml:"upstream" json:"upstream"`
	Profile        string   `yaml:"profile" json:"profile"`
	Extras         []string `yaml:"extras,omitempty" json:"extras,omitempty"`
	LLMModel       string   `yaml:"llmModel" json:"llmModel"`
	EmbeddingModel string   `yaml:"embeddingModel" json:"embeddingModel"`
	BindIP         string   `yaml:"bindIP" json:"bindIP"`
	Context        string   `yaml:"context" json:"context"`
	DefaultSecrets bool     `yaml:"defaultSecrets,omitempty" json:"defaultSecrets,omitempty"`
	DefaultUsers   bool     `yaml:"defaultUsers,omitempty" json:"defaultUsers,omitempty"`
	// BuiltContexts maps each build context variable to the commit of the last
	// build, so that 'up' knows when to rebuild.
	BuiltContexts map[string]string `yaml:"builtContexts,omitempty" json:"builtContexts,omitempty"`
}

// Load reads openarch-app.yaml from the setup folder dir.
func Load(dir string) (Config, error) {
	var c Config
	data, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return c, output.Usage(fmt.Sprintf("%s has no %s", dir, ConfigFile), "Run 'openarch app init' first, or use --dir.")
	}
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, output.Usage(fmt.Sprintf("cannot read %s: %v", ConfigFile, err), "")
	}
	if c.SchemaVersion != SchemaVersion {
		return c, output.Usage(fmt.Sprintf("%s has schemaVersion %d, this build reads %d", ConfigFile, c.SchemaVersion, SchemaVersion), "Update openarch-app.")
	}
	return c, nil
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
