package cli

import (
	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/version"
)

func newVersionCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Example: `  openarch app version
  openarch-app version --output json`,
		Args: noArgs,
		RunE: func(*cobra.Command, []string) error {
			return app.Printer.Print(version.Get())
		},
	}
}
