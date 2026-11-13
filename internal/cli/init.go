package cli

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AlfrescoLabs/openarch-app/internal/output"
	"github.com/AlfrescoLabs/openarch-app/internal/setup"
	"github.com/AlfrescoLabs/openarch-app/internal/stack"
	"github.com/AlfrescoLabs/openarch-app/internal/term"
)

func newInitCmd(app *App) *cobra.Command {
	var sf sourceFlags
	var cmisStdin, alfrescoStdin bool
	var modelURL string
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

--cmis-env adds a CMIS 1.1 repository as a source, with the settings in a
file of KEY=value lines: CMIS_URL (as the containers reach it, for example
http://host.docker.internal:8080/...), CMIS_USERNAME, and any other CMIS_*,
RAG_SECURITY_CMIS_* or CONNECTOR_ROOTS key. init copies them to .env.local and
refuses other keys and CMIS_ACL_FALLBACK=public. The password is CMIS_PASSWORD
in the file, or OPENARCH_APP_CMIS_PASSWORD, or the first line of stdin with
--cmis-password-stdin, or a prompt. The readers of each document come from its
CMIS permissions: users and everyone, not groups. 'openarch app sync' reads
the repository.

A setup has at most one of --filesystem and --cmis-env. Neither works with
--alfresco 2 --sample-ui: that sample UI does not find their documents.

--alfresco-env uses an Alfresco that you already run (25.x or later) instead
of the bundled one, with --alfresco 1 --nuxeo 0. The file holds KEY=value
lines: ALFRESCO_URL (without /alfresco, as the containers reach it, for
example http://host.docker.internal:8180), ALFRESCO_EVENT_BROKER_URL (its
ActiveMQ, for example tcp://host.docker.internal:61616), ALFRESCO_ADMIN_USER
and ALFRESCO_ADMIN_PASSWORD (an administrator), ACTIVEMQ_USER and
ACTIVEMQ_PASSWORD, and optionally CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL and
ALFRESCO_PROXY_UPSTREAM. init never changes that Alfresco: install the module
of 'openarch app alfresco-module' there first. up checks it.

--alfresco-compose does the same for an Alfresco that runs with Docker
Compose: give the folder where you run 'docker compose up' for it. init reads
that project and its broker login, and asks for the password of --alfresco-user
(or reads OPENARCH_APP_ALFRESCO_PASSWORD, or stdin with
--alfresco-password-stdin). 'openarch app alfresco-prepare' then shows the
changes for that project, and makes them with --apply.`,
		Example: `  openarch app init                            # one Alfresco
  openarch app init --alfresco 2               # two Alfresco instances
  openarch app init --alfresco 1 --nuxeo 1     # one Alfresco and one Nuxeo
  openarch app init --alfresco 0 --nuxeo 1     # one Nuxeo
  openarch app init --alfresco 2 --sample-ui   # two Alfresco and the sample UI
  openarch app init --alfresco 2 --sample-ui \
    --alfresco-name "Northgate Branch" --alfresco-name "Northgate Head Office"
  openarch app init --filesystem ~/Documents/shared \
    --filesystem-reader jim --filesystem-reader dwight
  openarch app init --alfresco-env alfresco.env   # an Alfresco you already run
  openarch app init --alfresco-compose ~/alfresco  # the same, on Docker Compose
  openarch app init ./my-app --llm-model ai/qwen2.5 --output json
  openarch app init --model-url http://localhost:11434 \
    --embedding-model mxbai-embed-large --llm-model qwen2.5   # Ollama`,
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
			if modelURL != "" && strings.TrimSuffix(modelURL, "/") != stack.DefaultModelURL {
				if err := stack.ValidateModelURL(modelURL); err != nil {
					return err
				}
				// The default model names are Docker Model Runner names.
				if !cmd.Flags().Changed("llm-model") || !cmd.Flags().Changed("embedding-model") {
					return output.Usage("--model-url needs --llm-model and --embedding-model",
						"Name the models as the server knows them, for example --embedding-model mxbai-embed-large --llm-model qwen2.5 for Ollama. "+
							"The embedding model must make vectors of 1024 dimensions.")
				}
				opts.Settings.ModelURL = strings.TrimSuffix(modelURL, "/")
			}
			if net.ParseIP(opts.Settings.BindIP) == nil {
				return output.Usage("invalid --bind-ip "+opts.Settings.BindIP, "Use an IP address, such as 127.0.0.1 or 0.0.0.0.")
			}
			if cmisStdin && opts.CMISEnv == "" {
				return output.Usage("--cmis-password-stdin needs --cmis-env", "")
			}
			if alfrescoStdin && opts.AlfrescoCompose == "" {
				return output.Usage("--alfresco-password-stdin needs --alfresco-compose", "")
			}
			if cmisStdin && alfrescoStdin {
				return output.Usage("--cmis-password-stdin and --alfresco-password-stdin cannot be combined", "Set one of the passwords with its variable instead.")
			}
			opts.CMISPassword = app.secret(cmisStdin, envCMISPassword, "CMIS password: ")
			opts.AlfrescoPassword = app.secret(alfrescoStdin, envAlfrescoPassword, "Alfresco admin password: ")
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
	f.StringVar(&s.LLMModel, "llm-model", s.LLMModel, "chat model, in Docker Model Runner or in the --model-url server")
	f.StringVar(&s.EmbeddingModel, "embedding-model", s.EmbeddingModel, "embedding model (1024 dimensions), in Docker Model Runner or in the --model-url server")
	f.StringVar(&modelURL, "model-url", "", "OpenAI-compatible model server instead of Docker Model Runner, such as Ollama at http://localhost:11434, as seen from this machine")
	f.StringVar(&s.BindIP, "bind-ip", s.BindIP, "host address of every published port")
	f.StringVar(&s.Context, "context-name", s.Context, "name of the openarch context for the bundled Hyland OpenArch")
	f.BoolVar(&s.SampleUI, "sample-ui", false, "add the sample UI, served by the proxy at /")
	f.StringArrayVar(&opts.AlfrescoNames, "alfresco-name", nil, "name of an Alfresco source in the sample UI (repeatable, in instance order; needs --alfresco 2)")
	f.StringVar(&s.Filesystem, "filesystem", "", "folder of this machine to add as a source, mounted read-only")
	f.StringArrayVar(&opts.FilesystemReaders, "filesystem-reader", nil, "user who can read the files of --filesystem (repeatable)")
	f.BoolVar(&opts.FilesystemEveryone, "filesystem-everyone", false, "let every user read the files of --filesystem")
	f.StringVar(&opts.CMISEnv, "cmis-env", "", "file with the settings of a CMIS source (CMIS_URL, CMIS_USERNAME, ...)")
	f.BoolVar(&cmisStdin, "cmis-password-stdin", false, "read the CMIS password from the first line of stdin")
	f.StringVar(&opts.AlfrescoEnv, "alfresco-env", "", "file with the URLs and logins of an existing Alfresco that replaces the bundled one")
	f.StringVar(&opts.AlfrescoCompose, "alfresco-compose", "", "folder of the Docker Compose project of an existing Alfresco that replaces the bundled one")
	f.StringVar(&opts.AlfrescoUser, "alfresco-user", "admin", "admin user of the existing Alfresco of --alfresco-compose")
	f.BoolVar(&alfrescoStdin, "alfresco-password-stdin", false, "read the password of --alfresco-user from the first line of stdin")
	f.BoolVar(&s.DefaultSecrets, "default-secrets", false, "keep the upstream secrets instead of random ones")
	f.BoolVar(&s.DefaultUsers, "default-users", false, "keep the upstream OpenArch users (admin:password)")
	f.BoolVar(&opts.Force, "force", false, "replace an openarch context with the same name")
	return cmd
}

// The variables that hold the passwords of init: the CMIS password for
// --cmis-env, and the Alfresco admin password for --alfresco-compose.
const (
	envCMISPassword     = "OPENARCH_APP_CMIS_PASSWORD"
	envAlfrescoPassword = "OPENARCH_APP_ALFRESCO_PASSWORD"
)

// secret returns how init gets a password: the first line of stdin, the
// variable envVar, or a prompt. It returns nil when there is no way.
func (a *App) secret(stdin bool, envVar, prompt string) func() (string, error) {
	switch {
	case stdin:
		return func() (string, error) {
			line, err := bufio.NewReader(a.Streams.In).ReadString('\n')
			if err != nil && !errors.Is(err, io.EOF) {
				return "", err
			}
			return strings.TrimRight(line, "\r\n"), nil
		}
	case os.Getenv(envVar) != "":
		return func() (string, error) { return os.Getenv(envVar), nil }
	case a.Interactive():
		return func() (string, error) { return term.ReadPassword(a.Streams.In, a.Streams.Err, prompt) }
	}
	return nil
}
