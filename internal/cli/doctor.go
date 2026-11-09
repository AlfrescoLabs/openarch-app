package cli

import (
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/doctor"
	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// envBin is set by openarch to its own path when it runs a plugin.
const envBin = "OPENARCH_BIN"

func newDoctorCmd(app *App) *cobra.Command {
	opts := doctor.Options{
		Profile:        stack.DefaultProfile,
		LLMModel:       stack.DefaultLLMModel,
		EmbeddingModel: stack.DefaultEmbeddingModel,
		ModelURL:       stack.DefaultModelURL,
		Dir:            defaultSetupDir,
	}
	var https bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the prerequisites to run App for OpenArch",
		Long: `Check the prerequisites to run App for OpenArch: Docker, Docker Compose,
Docker memory for the profile, Docker Model Runner and its models, free ports,
nuxeo-deployment for the profiles with Nuxeo, and the CLI for OpenArch.

Each check reports ok, warn, fail or skip, and a fix when something is wrong.
Exit code 3 means at least one check failed. Warnings do not change the exit code.

Checks: docker.cli, docker.daemon, docker.compose, docker.memory, model.runner,
model.url, model.embedding, model.chat, port.<n> (one for each port),
nuxeo.deployment (profiles nuxeo, full and demo), openarch.cli.

The default ports are ` + strconv.Itoa(stack.ProxyPort) + ` (the proxy, ` + strconv.Itoa(stack.HTTPSPort) + ` with --https) and ` + strconv.Itoa(stack.OpenArchPort) + ` (Hyland OpenArch).
The memory thresholds are estimates.`,
		Example: `  openarch app doctor
  openarch app doctor --profile full --dir ./my-app
  openarch app doctor --llm-model ai/qwen2.5 --output json
  openarch app doctor --check model.runner --check model.chat`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := stack.ValidProfile(opts.Profile); err != nil {
				return err
			}
			if len(opts.Ports) == 0 {
				proxy := stack.ProxyPort
				if https {
					proxy = stack.HTTPSPort
				}
				opts.Ports = []int{proxy, stack.OpenArchPort}
			}
			opts.OpenArchBin = os.Getenv(envBin)
			report, err := doctor.Run(cmd.Context(), app.doctorEnv(), opts)
			if err != nil {
				return err
			}
			if err := app.Printer.Print(report); err != nil {
				return err
			}
			if report.Failed() {
				return output.Exit(output.ExitPrerequisite, "prerequisite check failed")
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.Profile, "profile", opts.Profile, "upstream profile: alfresco, nuxeo, full, demo or platform")
	f.StringVar(&opts.LLMModel, "llm-model", opts.LLMModel, "chat model in Docker Model Runner")
	f.StringVar(&opts.EmbeddingModel, "embedding-model", opts.EmbeddingModel, "embedding model in Docker Model Runner")
	f.StringVar(&opts.ModelURL, "model-url", opts.ModelURL, "Docker Model Runner API, as seen from this machine")
	f.StringVar(&opts.Dir, "dir", opts.Dir, "setup folder (nuxeo-deployment goes inside it)")
	f.BoolVar(&https, "https", false, "check the HTTPS port of the proxy instead of the HTTP port")
	f.IntSliceVar(&opts.Ports, "port", nil, "host port that must be free (repeatable, replaces the default ports)")
	f.StringSliceVar(&opts.Only, "check", nil, "run only this check (repeatable)")
	_ = cmd.RegisterFlagCompletionFunc("profile", cobra.FixedCompletions(stack.Profiles, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
