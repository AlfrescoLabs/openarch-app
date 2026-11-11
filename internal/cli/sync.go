package cli

import (
	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/setup"
)

func newSyncCmd(app *App) *cobra.Command {
	var dir string
	opts := setup.SyncOptions{Timeout: 1800}
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Read the folder of the filesystem source into App for OpenArch",
		Long: `Start a sync of the filesystem source (init --filesystem) and wait until it
ends. The sync reads every file of the folder, and the stack makes the
embeddings of new and changed files. Nothing syncs by itself: run sync again
after the files change.

sync calls the sync API of the running stack on port 9096, with the account in
.env.local. It prints the counts of the job. Exit code 4 means the job failed,
a file failed, or the job still runs after --timeout seconds.`,
		Example: `  openarch app sync
  openarch app sync --dir ./my-app --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			res, err := setup.Sync(cmd.Context(), app.setupEnv(), s, opts)
			if err != nil {
				return err
			}
			if err := app.Printer.Print(res); err != nil {
				return err
			}
			if !res.OK {
				return output.Exit(output.ExitUnhealthy, "the sync failed")
			}
			return nil
		},
	}
	addDirFlag(cmd, &dir)
	cmd.Flags().IntVar(&opts.Timeout, "timeout", opts.Timeout, "seconds to wait for the sync job")
	return cmd
}
