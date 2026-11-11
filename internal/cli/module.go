package cli

import (
	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/setup"
)

func newAlfrescoModuleCmd(app *App) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "alfresco-module <out-dir>",
		Short: "Build the repository module for an existing Alfresco",
		Long: `Build content-lake-repo-model, the repository module that an existing
Alfresco (init --alfresco-env) needs, from the pinned content-lake-app commit,
and write the JAR to out-dir. It runs 'docker build' on the upstream model-jar
stage, so it needs no Maven on the host. It never changes an Alfresco: it
prints the steps to install the module there.

The module adds the cl: content model (cl:indexed, cl:fileScope with its
property cl:excludeFromLake, cl:syncStatus) and sends permission changes to the
broker. Back up Alfresco first. To remove the module later, run
'openarch app alfresco-prepare --undo' first, while it is still installed:
nodes that keep a cl: aspect cannot be read once it is gone.`,
		Example: `  openarch app alfresco-module ./alfresco-module`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			res, err := setup.AlfrescoModule(cmd.Context(), app.setupEnv(), s, args[0], app.streams())
			if err != nil {
				return err
			}
			return app.Printer.Print(res)
		},
	}
	addDirFlag(cmd, &dir)
	return cmd
}
