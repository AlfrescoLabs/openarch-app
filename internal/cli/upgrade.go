package cli

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/setup"
)

func newUpgradeCmd(app *App) *cobra.Command {
	var dir string
	var yes bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Move the setup to the upstream commit of this release",
		Long: `Move the setup folder to the upstream commit and the build contexts that this
release of openarch-app pins. After an update of openarch-app, up and
smoke-test refuse the setup until upgrade has moved it.

upgrade refuses a running stack: stop it with 'openarch app down' and back up
the volumes first. It fetches the new commit next to upstream/, rewrites the
build-context URLs in .env.local (no other value changes), replaces upstream/
and records the new commit in openarch-app.yaml. A failed fetch changes
nothing, and an upgrade that stopped halfway is completed by running it again.

upgrade does not build: the next 'openarch app up' builds the new images. There
is no way back to the old commit. upgrade asks for confirmation; --yes skips
the question, and non-interactive mode needs --yes.`,
		Example: `  openarch app down
  openarch app upgrade
  openarch app up`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			opts := setup.UpgradeOptions{Confirm: func(from, to string) error {
				if yes {
					return nil
				}
				if !app.Interactive() {
					return output.Usage("upgrade needs a confirmation", "Back up the volumes, then run it again with --yes.")
				}
				_, _ = fmt.Fprintf(app.Streams.Err, "This moves the setup from upstream commit %s to %s.\n"+
					"The next 'up' builds new images, and there is no way back. Back up the volumes first.\nContinue? [y/N] ", from, to)
				line, _ := bufio.NewReader(app.Streams.In).ReadString('\n')
				switch strings.ToLower(strings.TrimSpace(line)) {
				case "y", "yes":
					return nil
				}
				return output.NewError(output.ExitError, "upgrade cancelled", "")
			}}
			res, err := setup.Upgrade(cmd.Context(), app.setupEnv(), s, opts)
			if err != nil {
				return err
			}
			return app.Printer.Print(res)
		},
	}
	addDirFlag(cmd, &dir)
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
