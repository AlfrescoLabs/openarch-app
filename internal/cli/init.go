package cli

import (
	"net"
	"os"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/setup"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

func newInitCmd(app *App) *cobra.Command {
	var sf sourceFlags
	opts := setup.InitOptions{Settings: setup.Config{
		LLMModel:       stack.DefaultLLMModel,
		EmbeddingModel: stack.DefaultEmbeddingModel,
		BindIP:         "127.0.0.1",
		Context:        "app",
	}}
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Create an App for OpenArch setup folder",
		Long: `Create an App for OpenArch setup folder (default: ./openarch-app).

init fetches the upstream content-lake-app-deployment repository at the commit
that this build pins, with Git. Then it writes:

  openarch-app.yaml      the settings of the setup (no secrets)
  .env.local             settings and random secrets, mode 0600
  openarch-users.yml     the Hyland OpenArch admin user (bcrypt hash)
  compose.override.yaml  the OpenArch port and users file

and adds an openarch context for the bundled Hyland OpenArch, with the admin
password on stdin. The ports bind to 127.0.0.1 unless --bind-ip says otherwise.

init writes .env.local once: the databases keep the passwords of their first
start. If init fails, run it again: it keeps upstream/ and .env.local.

--alfresco and --nuxeo say how many instances of each source the setup runs:
at most 2 Alfresco and 1 Nuxeo, and at least one source. With Nuxeo, init also
fetches nuxeo-deployment, and Nuxeo runs in the same stack.

--sample-ui adds the upstream sample UI, which asks questions across every
source of the setup. The proxy serves it at /, and its container listens on
port 4200 too. With two Alfresco instances, --alfresco-name names them, once
for each instance (default: "Alfresco 1" and "Alfresco 2").

--filesystem adds a folder of this machine as a source. The stack mounts it
read-only, and 'openarch app sync' reads it. --filesystem-reader names a user
who can read its files, once for each user; a user logs in to Alfresco or
Nuxeo, and the same user name counts for the files. Groups do not work for
this source. --filesystem-everyone lets every user read the files instead.
The sync API listens on port 9096 with a random account in .env.local.
--filesystem does not work with --alfresco 2 --sample-ui: that sample UI does
not find the files.`,
		Example: `  openarch app init                            # one Alfresco
  openarch app init --alfresco 2               # two Alfresco instances
  openarch app init --alfresco 1 --nuxeo 1     # one Alfresco and one Nuxeo
  openarch app init --alfresco 0 --nuxeo 1     # one Nuxeo
  openarch app init --alfresco 2 --sample-ui   # two Alfresco and the sample UI
  openarch app init --alfresco 2 --sample-ui \
    --alfresco-name "Northgate Branch" --alfresco-name "Northgate Head Office"
  openarch app init --filesystem ~/Documents/shared \
    --filesystem-reader jim --filesystem-reader dwight
  openarch app init ./my-app --llm-model ai/qwen2.5 --output json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Dir = defaultSetupDir
			if len(args) == 1 {
				opts.Dir = args[0]
			}
			sources, err := sf.sources(cmd, app.Printer)
			if err != nil {
				return err
			}
			opts.Settings.Sources = sources
			if net.ParseIP(opts.Settings.BindIP) == nil {
				return output.Usage("invalid --bind-ip "+opts.Settings.BindIP, "Use an IP address, such as 127.0.0.1 or 0.0.0.0.")
			}
			opts.OpenArchBin = os.Getenv(envBin)
			res, err := setup.Init(cmd.Context(), app.setupEnv(), opts)
			if err != nil {
				return err
			}
			return app.Printer.Print(res)
		},
	}
	addSourceFlags(cmd, &sf)
	f := cmd.Flags()
	s := &opts.Settings
	f.StringVar(&s.LLMModel, "llm-model", s.LLMModel, "chat model in Docker Model Runner")
	f.StringVar(&s.EmbeddingModel, "embedding-model", s.EmbeddingModel, "embedding model in Docker Model Runner")
	f.StringVar(&s.BindIP, "bind-ip", s.BindIP, "host address of every published port")
	f.StringVar(&s.Context, "context-name", s.Context, "name of the openarch context for the bundled Hyland OpenArch")
	f.BoolVar(&s.SampleUI, "sample-ui", false, "add the sample UI, served by the proxy at /")
	f.StringArrayVar(&opts.AlfrescoNames, "alfresco-name", nil, "name of an Alfresco source in the sample UI (repeatable, in instance order; needs --alfresco 2)")
	f.StringVar(&s.Filesystem, "filesystem", "", "folder of this machine to add as a source, mounted read-only")
	f.StringArrayVar(&opts.FilesystemReaders, "filesystem-reader", nil, "user who can read the files of --filesystem (repeatable)")
	f.BoolVar(&opts.FilesystemEveryone, "filesystem-everyone", false, "let every user read the files of --filesystem")
	f.BoolVar(&s.DefaultSecrets, "default-secrets", false, "keep the upstream secrets instead of random ones")
	f.BoolVar(&s.DefaultUsers, "default-users", false, "keep the upstream OpenArch users (admin:password)")
	f.BoolVar(&opts.Force, "force", false, "replace an openarch context with the same name")
	return cmd
}
