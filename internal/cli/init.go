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
	opts := setup.InitOptions{Settings: setup.Config{
		Profile:        stack.DefaultProfile,
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

The nuxeo and full profiles need nuxeo-deployment in the setup folder: run
'openarch app doctor --profile nuxeo --dir <dir>' for the steps.`,
		Example: `  openarch app init
  openarch app init ./my-app --profile full --extra alfresco-2
  openarch app init --llm-model ai/qwen2.5 --output json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Dir = defaultSetupDir
			if len(args) == 1 {
				opts.Dir = args[0]
			}
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
	f := cmd.Flags()
	s := &opts.Settings
	f.StringVar(&s.Profile, "profile", s.Profile, "upstream profile: alfresco, nuxeo or full")
	f.StringSliceVar(&s.Extras, "extra", nil, "opt-in addition: alfresco-2 (repeatable)")
	f.StringVar(&s.LLMModel, "llm-model", s.LLMModel, "chat model in Docker Model Runner")
	f.StringVar(&s.EmbeddingModel, "embedding-model", s.EmbeddingModel, "embedding model in Docker Model Runner")
	f.StringVar(&s.BindIP, "bind-ip", s.BindIP, "host address of every published port")
	f.StringVar(&s.Context, "context-name", s.Context, "name of the openarch context for the bundled Hyland OpenArch")
	f.BoolVar(&s.DefaultSecrets, "default-secrets", false, "keep the upstream secrets instead of random ones")
	f.BoolVar(&s.DefaultUsers, "default-users", false, "keep the upstream OpenArch users (admin:password)")
	f.BoolVar(&opts.Force, "force", false, "replace an openarch context with the same name")
	_ = cmd.RegisterFlagCompletionFunc("profile", cobra.FixedCompletions(stack.Profiles, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("extra", cobra.FixedCompletions(stack.Extras, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
