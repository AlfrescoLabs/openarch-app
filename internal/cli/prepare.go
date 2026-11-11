package cli

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/setup"
)

func newAlfrescoPrepareCmd(app *App) *cobra.Command {
	var dir string
	var yes bool
	var opts setup.PrepareOptions
	cmd := &cobra.Command{
		Use:   "alfresco-prepare",
		Short: "Prepare the existing Alfresco project for App for OpenArch",
		Long: `Prepare the Docker Compose project of an existing Alfresco (init
--alfresco-compose) for App for OpenArch. alfresco-prepare builds two images:

  the repository image of the project plus the content-lake-repo-model module
  ACA of the project's version with the App for OpenArch extension

Then it writes alfresco-compose.override.yaml in the setup folder. That file
replaces the repository and ACA services of the project with those images,
adds the module properties, the cl: prefix of the search indexer, and a Docker
network that the project and the stack share. Without --apply, nothing in the
project changes: the command prints the steps to apply the file.

With --apply, it installs the file as the override file of the project and
runs 'docker compose up -d --wait' there, which recreates only the changed
services. It asks for confirmation; --yes skips the question, and
non-interactive mode needs --yes. Back up the Alfresco database and content
store first. To undo, remove that override file and run 'docker compose up -d'
in the project. Nodes keep the cl: aspects.`,
		Example: `  openarch app alfresco-prepare
  openarch app alfresco-prepare --apply`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			opts.Confirm = func(project string) error {
				if yes {
					return nil
				}
				if !app.Interactive() {
					return output.Usage("alfresco-prepare --apply needs a confirmation", "Back up the Alfresco database and content store, then run it again with --yes.")
				}
				_, _ = fmt.Fprintf(app.Streams.Err, "This changes the Compose project %s and recreates its repository and ACA.\n"+
					"Back up the Alfresco database and content store first.\nContinue? [y/N] ", project)
				line, _ := bufio.NewReader(app.Streams.In).ReadString('\n')
				switch strings.ToLower(strings.TrimSpace(line)) {
				case "y", "yes":
					return nil
				}
				return output.NewError(output.ExitError, "alfresco-prepare cancelled", "")
			}
			res, err := setup.Prepare(cmd.Context(), app.setupEnv(), s, opts, app.streams())
			if err != nil {
				return err
			}
			return app.Printer.Print(res)
		},
	}
	addDirFlag(cmd, &dir)
	cmd.Flags().BoolVar(&opts.Apply, "apply", false, "install the override file in the project and recreate the changed services")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
