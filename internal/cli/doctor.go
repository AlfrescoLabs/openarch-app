package cli

import (
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/doctor"
	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
)

// envBin is set by openarch to its own path when it runs a plugin.
const envBin = "OPENARCH_BIN"

func newDoctorCmd(app *App) *cobra.Command {
	var sf sourceFlags
	var sampleUI, filesystem, cmis bool
	opts := doctor.Options{
		LLMModel:       stack.DefaultLLMModel,
		EmbeddingModel: stack.DefaultEmbeddingModel,
		ModelURL:       stack.DefaultModelURL,
		Dir:            defaultSetupDir,
	}
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the prerequisites to run App for OpenArch",
		Long: `Check the prerequisites to run App for OpenArch: Docker, Docker Compose,
Docker memory for the sources, Git, Docker Model Runner and its models, free
ports, the nuxeo-deployment folder when it exists, and the CLI for OpenArch.

Each check reports ok, warn, fail or skip, and a fix when something is wrong.
Exit code 3 means at least one check failed. Warnings do not change the exit code.

Checks: docker.cli, docker.daemon, docker.compose, docker.memory, git.cli,
model.runner, model.url, model.embedding, model.chat, port.<n> (one for each
port), nuxeo.deployment (with --nuxeo 1), openarch.cli.

The default ports are ` + ports(stack.HostPorts("alfresco", nil, false, false)) + ` (the proxy and Hyland OpenArch), plus
` + ports([]int{stack.NuxeoPort}) + ` (Nuxeo) with --nuxeo 1,
` + ports([]int{stack.Alfresco2Port, stack.Alfresco2HTTPSPort}) + ` (the second Alfresco) with --alfresco 2, and
` + ports([]int{stack.SampleUIPort}) + ` (the sample UI) with --sample-ui, and
` + ports([]int{stack.ConnectorPort}) + ` (the sync API of a connector source) with --filesystem or --cmis.
The memory thresholds are estimates.`,
		Example: `  openarch app doctor
  openarch app doctor --alfresco 2 --nuxeo 1 --dir ./my-app
  openarch app doctor --llm-model ai/qwen2.5 --output json
  openarch app doctor --check model.runner --check model.chat`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sources, err := sf.sources(cmd, app.Printer)
			if err != nil {
				return err
			}
			opts.Sources = sources
			opts.Connector = filesystem || cmis
			if len(opts.Ports) == 0 {
				opts.Ports = stack.HostPorts(sources.Profile(), sources.Extras(), sampleUI, filesystem || cmis)
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
	addSourceFlags(cmd, &sf)
	f := cmd.Flags()
	f.StringVar(&opts.LLMModel, "llm-model", opts.LLMModel, "chat model in Docker Model Runner")
	f.StringVar(&opts.EmbeddingModel, "embedding-model", opts.EmbeddingModel, "embedding model in Docker Model Runner")
	f.StringVar(&opts.ModelURL, "model-url", opts.ModelURL, "Docker Model Runner API, as seen from this machine")
	f.BoolVar(&sampleUI, "sample-ui", false, "check the port of the sample UI too")
	f.BoolVar(&filesystem, "filesystem", false, "check the port and memory of the filesystem source too")
	f.BoolVar(&cmis, "cmis", false, "check the port and memory of the CMIS source too")
	f.StringVar(&opts.Dir, "dir", opts.Dir, "setup folder (nuxeo-deployment goes inside it)")
	f.IntSliceVar(&opts.Ports, "port", nil, "host port that must be free (repeatable, replaces the default ports)")
	f.StringSliceVar(&opts.Only, "check", nil, "run only this check (repeatable)")
	return cmd
}

// ports joins port numbers with commas.
func ports(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ", ")
}
