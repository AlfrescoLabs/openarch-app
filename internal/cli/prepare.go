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
	var yes, undo bool
	var opts setup.PrepareOptions
	cmd := &cobra.Command{
		Use:   "alfresco-prepare",
		Short: "Prepare the existing Alfresco project for App for OpenArch",
		Long: `Prepare the Docker Compose project of an existing Alfresco (init
--alfresco-compose) for App for OpenArch. alfresco-prepare builds the
content-lake-repo-model module JAR, and ACA of the project's version with the
App for OpenArch extension.

Then it writes alfresco-compose.override.yaml and the alfresco-prepare/openarch-app
folder in the setup folder. The override file mounts the JAR into the
repository of the project (which keeps its own image) and adds the module
properties, mounts the cl: prefix file into the search indexer, replaces the
ACA image, and adds a Docker network that the project and the stack share.
Without --apply, nothing in the project changes: the command prints the steps.

With --apply, it copies the folder into the project, installs the file as the
override file that Compose merges, and runs 'docker compose up -d' there,
which recreates only the changed services. It refuses a project that has an
override file of its own, or that sets COMPOSE_FILE: then run it without
--apply for the steps. It asks for confirmation; --yes skips the question, and
non-interactive mode needs --yes. Back up the Alfresco database and content
store first.

--undo removes it again. It stops the ingesters of the stack, removes the cl:
aspects from every node while the module is still installed, removes the
override file and the openarch-app folder, recreates the services of the
project, and stops the stack. With --alfresco-env, or with a merged override
file, it removes the aspects and prints what you remove yourself. Do
not only delete the override file: a node that keeps a cl: aspect cannot be
read or updated once the module is gone. --undo asks for confirmation too.`,
		Example: `  openarch app alfresco-prepare
  openarch app alfresco-prepare --apply
  openarch app alfresco-prepare --undo`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			if undo && opts.Apply {
				return output.Usage("--apply and --undo cannot be combined", "")
			}
			flag, what := "--apply", "This changes the Compose project %s and recreates its repository and ACA."
			if undo {
				flag, what = "--undo", "This removes the cl: aspects from every node of %s, then the module and the extension, and stops the stack."
			}
			opts.Confirm = func(project string) error {
				if yes {
					return nil
				}
				if !app.Interactive() {
					return output.Usage("alfresco-prepare "+flag+" needs a confirmation", "Back up the Alfresco database and content store, then run it again with --yes.")
				}
				_, _ = fmt.Fprintf(app.Streams.Err, what+"\n"+
					"Back up the Alfresco database and content store first.\nContinue? [y/N] ", project)
				line, _ := bufio.NewReader(app.Streams.In).ReadString('\n')
				switch strings.ToLower(strings.TrimSpace(line)) {
				case "y", "yes":
					return nil
				}
				return output.NewError(output.ExitError, "alfresco-prepare cancelled", "")
			}
			if undo {
				res, err := setup.Undo(cmd.Context(), app.setupEnv(), s, setup.UndoOptions{Confirm: opts.Confirm}, app.streams())
				if err != nil {
					return err
				}
				return app.Printer.Print(res)
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
	cmd.Flags().BoolVar(&undo, "undo", false, "remove the cl: aspects, then the changes of --apply, and stop the stack")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
