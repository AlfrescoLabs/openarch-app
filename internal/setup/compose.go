package setup

import (
	"path/filepath"

	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// ComposeArgs returns the 'docker compose' arguments for setup c, before the
// subcommand. They are relative to the setup folder, so the command runs there.
func ComposeArgs(c Config) []string {
	args := []string{"compose", "-p", stack.ProjectName, "--project-directory", UpstreamDir,
		"--env-file", filepath.Join(UpstreamDir, ".env"), "--env-file", EnvFile,
		"-f", filepath.Join(UpstreamDir, "compose.yaml")}
	if c.Alfresco >= 2 {
		args = append(args, "-f", filepath.Join(UpstreamDir, "compose.alfresco-2.yaml"))
	}
	if c.ManagedNuxeo && c.Nuxeo > 0 {
		// Runs ../nuxeo-deployment (the folder of the setup) in this project.
		args = append(args, "-f", filepath.Join(UpstreamDir, stack.NuxeoDeploymentFile))
	}
	args = append(args, "-f", OverrideFile, "--profile", c.Profile())
	for _, e := range c.Extras() {
		args = append(args, "--profile", e)
	}
	if c.Filesystem != "" {
		args = append(args, "--profile", stack.ConnectorProfile)
	}
	if c.SampleUI {
		args = append(args, "--profile", stack.SampleUIProfile)
	}
	return args
}
