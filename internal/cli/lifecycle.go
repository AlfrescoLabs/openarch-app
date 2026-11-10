package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/setup"
)

// addDirFlag adds --dir to cmd. The default is the current folder when it is
// a setup folder, else ./openarch-app (the folder that 'init' creates).
func addDirFlag(cmd *cobra.Command, dir *string) {
	cmd.Flags().StringVar(dir, "dir", "", "setup folder (default: the current folder if it is a setup, else ./"+defaultSetupDir+")")
}

func (a *App) openSetup(dir string) (setup.Setup, error) {
	return setup.Open(setupDir(dir))
}

// openSetupForUpgrade is openSetup for 'upgrade', which also reads an older
// version of openarch-app.yaml.
func (a *App) openSetupForUpgrade(dir string) (setup.Setup, error) {
	return setup.OpenForUpgrade(setupDir(dir))
}

// setupDir returns the folder of --dir: the current folder when it is a setup,
// else ./openarch-app.
func setupDir(dir string) string {
	if dir == "" {
		dir = defaultSetupDir
		if _, err := os.Stat(setup.ConfigFile); err == nil {
			dir = "."
		}
	}
	return dir
}

func (a *App) streams() setup.Streams {
	return setup.Streams{Out: a.Streams.Out, Err: a.Streams.Err}
}

func newUpCmd(app *App) *cobra.Command {
	var dir string
	opts := setup.UpOptions{Wait: true, WaitTimeout: 900}
	var noWait bool
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the App for OpenArch stack",
		Long: `Start the stack of the setup folder with 'docker compose up -d', with the
profile and extras of openarch-app.yaml. Compose writes its progress to stderr.

up builds the images from the pinned build contexts on the first start, and
again after 'openarch app upgrade'. The first build takes several minutes. Then
up waits until every service is running or healthy (--no-wait skips this) and
prints the endpoints.

After an update of openarch-app that pins another upstream commit, up refuses
the setup until 'openarch app upgrade' has moved it.

With the alfresco-2 extra, rag-service needs the repository id of each Alfresco
(ALFRESCO_1_SOURCE_ID and ALFRESCO_2_SOURCE_ID in .env.local). The first up
starts the two Alfresco instances alone, waits for them even with --no-wait,
records their ids, and then starts the rest. Every later up checks the ids
again after the wait, so they follow a repository that 'down -v' replaced.

For the nuxeo and full profiles, Nuxeo runs in the same Compose project, from
the nuxeo-deployment folder that init fetched. Its first start builds Nuxeo from
source, which takes long. A setup made before 0.3.0 keeps its nuxeo-deployment
as a project of its own: start that one first.`,
		Example: `  openarch app up
  openarch app up --dir ./my-app --wait-timeout 1800`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			if err := s.CheckNuxeo(); err != nil {
				return err
			}
			if err := s.CheckPins(); err != nil {
				return err
			}
			opts.Wait = !noWait
			if noWait && slices.Contains(s.Config.Extras, "alfresco-2") {
				app.Printer.Warn("With --no-wait, up does not check that the RAG source ids still match the two Alfresco repositories. After a 'down -v', run 'openarch app up' without it.")
			}
			env := app.setupEnv()
			if s.UsesUpstreamAlfrescoAdmin() {
				app.Printer.Warn("Alfresco still uses the upstream admin password 'admin'. New setups get a random one. " +
					"Alfresco keeps the password of its first start: change it in Alfresco first, then ALFRESCO_ADMIN_PASSWORD in " + setup.EnvFile + ".")
			}
			if missing := s.MissingModels(cmd.Context(), env); len(missing) > 0 {
				app.Printer.Warn("Docker Model Runner does not have " + strings.Join(missing, ", ") + ". Run 'openarch app models pull'.")
			}
			res, err := setup.Up(cmd.Context(), env, s, opts, app.streams())
			if err != nil {
				return err
			}
			return app.Printer.Print(res)
		},
	}
	addDirFlag(cmd, &dir)
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "do not wait for the services to be healthy")
	cmd.Flags().IntVar(&opts.WaitTimeout, "wait-timeout", opts.WaitTimeout, "seconds to wait for the services to be healthy")
	return cmd
}

func newDownCmd(app *App) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Stop the App for OpenArch stack and keep its data",
		Long: `Stop and remove the containers of the setup folder with 'docker compose down'.
The volumes stay, so 'openarch app up' starts again with the same data. This
includes Nuxeo; only a nuxeo-deployment of a setup made before 0.3.0 is a
project of its own, which you stop in its folder.`,
		Example: `  openarch app down
  openarch app down --dir ./my-app`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			if err := setup.Down(cmd.Context(), app.setupEnv(), s, app.streams()); err != nil {
				return err
			}
			return app.Printer.Print(textResult{Message: "Stopped the stack in " + s.Dir + ". The volumes are kept."})
		},
	}
	addDirFlag(cmd, &dir)
	return cmd
}

func newStatusCmd(app *App) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the services of the App for OpenArch stack",
		Long: `Show the state and health of every service of the setup folder, from
'docker compose ps'. Exit code 4 means a service is not running, is unhealthy,
or ended with an error. A one-shot job that ended with exit code 0 is fine.`,
		Example: `  openarch app status
  openarch app status --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			res, err := setup.Status(cmd.Context(), app.setupEnv(), s)
			if err != nil {
				return err
			}
			if err := app.Printer.Print(res); err != nil {
				return err
			}
			if !res.Healthy {
				return output.Exit(output.ExitUnhealthy, "the stack is not healthy")
			}
			return nil
		},
	}
	addDirFlag(cmd, &dir)
	return cmd
}

func newLogsCmd(app *App) *cobra.Command {
	var dir string
	var opts setup.LogsOptions
	cmd := &cobra.Command{
		Use:   "logs [service...]",
		Short: "Show the logs of the App for OpenArch stack",
		Long: `Show the logs of the setup folder, from 'docker compose logs'. Without a
service name, it shows every service.

In JSON mode the logs are one string field, and --follow is not allowed.`,
		Example: `  openarch app logs rag-service --tail 100
  openarch app logs --follow
  openarch app logs hxpr-app --tail 50 --output json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			opts.Services = args
			if app.Printer.Format() == output.JSON {
				if opts.Follow {
					return output.Usage("--follow does not work with --output json", "Use --tail instead.")
				}
				res, err := setup.Logs(cmd.Context(), app.setupEnv(), s, opts)
				if err != nil {
					return err
				}
				return app.Printer.Print(res)
			}
			return setup.StreamLogs(cmd.Context(), app.setupEnv(), s, opts, app.streams())
		},
	}
	addDirFlag(cmd, &dir)
	cmd.Flags().BoolVarP(&opts.Follow, "follow", "f", false, "keep printing new log lines")
	cmd.Flags().IntVar(&opts.Tail, "tail", 0, "number of lines per service (default: all)")
	return cmd
}

func newModelsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Manage the models of Docker Model Runner",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	var dir string
	pull := &cobra.Command{
		Use:   "pull",
		Short: "Get the embedding and chat models of the setup",
		Long: `Make the embedding and chat models of openarch-app.yaml available in Docker
Model Runner. An ai/ model is pulled with 'docker model pull'. The default chat
model, ` + "local/olmo3-7b-instruct:Q4_K_M" + `, is a local package that no registry
serves: the upstream scripts/package-olmo3-local.sh builds it (about 4.2 GB,
needs bash, curl and python3). Any other model is left alone.`,
		Example: `  openarch app models pull`,
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			res, err := setup.ModelsPull(cmd.Context(), app.setupEnv(), s, app.streams())
			if err != nil {
				return err
			}
			return app.Printer.Print(res)
		},
	}
	addDirFlag(pull, &dir)
	cmd.AddCommand(pull)
	return cmd
}

func newSmokeTestCmd(app *App) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "smoke-test",
		Short: "Run the upstream test suite against the running stack",
		Long: `Run the upstream test suite of the profile against the running stack:
test/test-alfresco.sh (alfresco), test/test-nuxeo.sh (nuxeo) or
test/test-rag-full.sh (full), the suites of the upstream test/run-tests.sh.
With the alfresco-2 extra it also runs test/test-two-alfresco.sh. The suites create test documents and remove them.
They need bash, curl and jq.

In text mode the suite output goes to stdout; in JSON mode it goes to stderr,
and stdout holds the counts. Exit code 4 means a check failed.`,
		Example: `  openarch app smoke-test
  openarch app smoke-test --output json`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := app.openSetup(dir)
			if err != nil {
				return err
			}
			out := app.Streams.Out
			if app.Printer.Format() == output.JSON {
				out = app.Streams.Err
			}
			res, err := setup.SmokeTest(cmd.Context(), app.setupEnv(), s, out, app.streams())
			if err != nil {
				return err
			}
			if err := app.Printer.Print(res); err != nil {
				return err
			}
			if !res.OK {
				return output.Exit(output.ExitUnhealthy, "the smoke test failed")
			}
			return nil
		},
	}
	addDirFlag(cmd, &dir)
	return cmd
}

// textResult is the result of a command that only reports a message.
type textResult struct {
	Message string `json:"message"`
}

func (r textResult) WriteText(w io.Writer) error {
	_, err := fmt.Fprintln(w, r.Message)
	return err
}
