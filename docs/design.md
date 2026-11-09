# Design

`openarch-app` installs and operates App for OpenArch on one machine with Docker Compose. App for OpenArch adds Alfresco and Nuxeo ingesters, a RAG service and a UI to Hyland OpenArch. It runs with local models on [Docker Model Runner](https://docs.docker.com/ai/model-runner/).

It is one Go binary. `openarch app ...` runs the `openarch-app` binary from `PATH`, through the plugin hook of the [CLI for OpenArch](https://github.com/AlfrescoLabs/openarch-cli). `openarch-app ...` works the same way on its own. App for OpenArch is a proof of concept, and so is this plugin.

Upstream is [AlfrescoLabs/content-lake-app-deployment](https://github.com/AlfrescoLabs/content-lake-app-deployment). Its repositories and variables use the name `content-lake`, and it names the Hyland OpenArch service `hxpr-app` (its clients use `HXPR_*` variables).

Every command of the first version is built. The rest of this document is the design for the commands that come next. Flags of the built commands are in `openarch app <command> --help`.

## Goals and non-goals

Goals of the first version:

- The `alfresco`, `nuxeo` and `full` profiles, and the `alfresco-2` extra (see Profiles).
- One setup per machine.
- Every user gets the upstream commit that the plugin was tested with. A newer upstream commit comes with a plugin update.

Not in the first version:

- Choosing another upstream commit, or changing the commit of an existing setup.
- HTTPS. Every port binds to `127.0.0.1` by default.
- The other upstream profiles and extras: `demo`, `platform`, `debug`, `observability`, `connector`, `sharepoint-mock`, `transform-extras`.
- More than two Alfresco instances or more than one Nuxeo.
- An Alfresco that the user already runs, instead of the bundled one.
- Admin commands for a running stack (connectors, sources, RAG questions, reindex).

## Rules

1. **Wrap upstream, do not copy it.** The plugin fetches the upstream repository at a pinned commit and runs its `compose.yaml`. Copied templates would need to be kept in sync with upstream by hand. The plugin runs `docker compose` itself and does not use the upstream Makefile.
2. **Pinned source repositories.** The upstream repository and the remote build contexts are pinned to full commit SHAs, and Git checks their content. Images and models are pulled by tag (see Limits).
3. **Secure by default, every security feature optional.** Ports bind to `127.0.0.1`, the secrets that the stack sets itself are random, and OpenArch has one admin user with a random password and no default users. A flag turns each of these off.
4. **Local models only.** Embeddings and chat run on Docker Model Runner.
5. **Same output contract as openarch.** Every command supports `--output json`. Results go to stdout, logs go to stderr. In JSON mode errors also go to stdout. The exit codes and the JSON error shape match openarch. Every prompt has a flag.
6. **No OpenArch password in arguments or variables.** The plugin passes the OpenArch password to `openarch` on stdin, never as an argument or a variable. Containers get their secrets as environment variables, as upstream expects. The upstream test suites get the Alfresco and Nuxeo logins as environment variables, because they read them that way.

## Decisions

| Decision | Rejected option | Trade-off |
| --- | --- | --- |
| A plugin of the CLI for OpenArch | Built into the CLI (`init --with ...`) | The core CLI keeps one product and one workspace layout. The plugin costs a second binary. |
| Wrap upstream at a pinned commit | Copy its Compose files | No copied files to keep in sync. Each new commit needs a test run, and setup needs the network. |
| Use the Hyland OpenArch that upstream bundles | Reuse the OpenArch of a CLI workspace | The upstream Compose files run unchanged. A user with a CLI workspace runs a second OpenArch, with its own MongoDB and OpenSearch. |
| A shallow Git fetch of the pinned commit | A tarball with a custom digest | Git checks the content, as BuildKit does for the build contexts. Git becomes a prerequisite. |
| Run the `docker compose` CLI | The Compose Go SDK | The CLI matches what the user runs by hand, and the plugin has no extra dependency. |

## Setup folder

`openarch app init [dir]` creates a setup folder (default `./openarch-app`). The plugin does not read or change `openarch.yaml`, but `init` adds a context to the CLI config (see Credentials).

```text
openarch-app/
  openarch-app.yaml       # plugin settings
  .env.local              # generated settings and secrets, mode 0600
  compose.override.yaml   # generated: OpenArch port and users file
  openarch-users.yml      # generated OpenArch users (bcrypt hashes), mode 0644
  upstream/               # the upstream repository at the pinned commit, never edited
  nuxeo-deployment/       # the user adds it, only for the nuxeo and full profiles
```

- `openarch-app.yaml` is the source of truth for settings: `schemaVersion: 1`, the profile, the extras, the models, the bind IP, the name of the `openarch` context, and the build context SHAs of the last build. It holds no secrets.
- `init` writes `openarch-app.yaml` last. If `init` fails, running it again keeps `upstream/` and `.env.local`. Generated files never go inside `upstream/`.
- `openarch-users.yml` holds only hashes. Its mode is 0644 so that the OpenArch container (user `openarch-user`, uid 33010) can read it through a bind mount on Linux.
- Upstream expects `nuxeo-deployment` next to its own repository (`../nuxeo-deployment`). The nuxeo-deployment project publishes Nuxeo on host port 8081. In the first version the user clones it at the commit that `doctor` prints, and starts it.

Every Compose call has this form:

```sh
docker compose -p openarch-app --project-directory upstream \
  --env-file upstream/.env --env-file .env.local \
  -f upstream/compose.yaml -f compose.override.yaml --profile <profile> ...
```

- `-p openarch-app` replaces the fixed upstream name `content-lake-app`. There is one setup per machine.
- `.env.local` comes after the upstream `.env`, so its values win.
- The `alfresco-2` extra adds `-f upstream/compose.alfresco-2.yaml`.
- Compose resolves relative paths in every file against the project directory, `upstream/`. So `compose.override.yaml` refers to its neighbors with `../` paths, for example `../openarch-users.yml`.

## Credentials

- `init` generates random values for the secrets where the stack sets both the server side and the client side: `POSTGRES_PASSWORD`, `SHARED_SECRET`, `ACTIVEMQ_PASSWORD`, `OPENSEARCH_ADMIN_PASSWORD` and `HXPR_PASSWORD`. One generator makes passwords that meet the OpenSearch strength rule and use only URL-safe characters.
- Every other secret keeps its upstream value, because its server side is set somewhere else: `ALFRESCO_ADMIN_PASSWORD` (Alfresco keeps its admin password in its database), `ALFRESCO_2_INTERNAL_PASSWORD`, and `NUXEO_PASSWORD` (set in nuxeo-deployment).
- The secrets are in `.env.local` only. `init` writes them once and never overwrites an existing `.env.local`, because the databases keep the passwords of their first start.
- The OpenArch admin password is in `openarch-users.yml` as a bcrypt hash. `compose.override.yaml` mounts the file into `hxpr-app` (`OPENARCH_USERS_FILE`) and publishes the OpenArch port on `${BIND_IP}:8090`. Port 8090 avoids 8080 (a CLI workspace) and 8081 (Nuxeo).
- The ingesters and the RAG service log in to OpenArch as the admin, with `HXPR_USERNAME` and `HXPR_PASSWORD`, so the plaintext password is also in `.env.local`.
- `init` pipes the password into `openarch config set --name <context> -s http://127.0.0.1:8090 -u admin --password-stdin`. A context is a named server URL and login in the CLI config. The default name is `app`. `init` fails if the name exists, unless `--force` is set. Then `openarch --context app documents ...` works on the bundled OpenArch.

## Upstream commit

- The plugin code holds one full commit SHA of upstream, and every setup uses it.
- `init` fetches it into `upstream/` with a shallow Git fetch of that SHA (`git fetch --depth 1 <url> <sha>`), checks it out, and checks that `HEAD` is that SHA.

## Remote build contexts

Upstream builds some images from remote Git contexts. `.env.local` sets each variable to a Git URL that ends in `#<full commit SHA>`:

| Variable | Repository |
| --- | --- |
| `CONTENT_LAKE_GIT_CONTEXT` | [AlfrescoLabs/content-lake-app](https://github.com/AlfrescoLabs/content-lake-app) |
| `CONTENT_LAKE_ACS_GIT_CONTEXT` | [AlfrescoLabs/content-lake-app](https://github.com/AlfrescoLabs/content-lake-app) |
| `CONTENT_LAKE_UI_GIT_CONTEXT` | [AlfrescoLabs/alfresco-content-lake-ui](https://github.com/AlfrescoLabs/alfresco-content-lake-ui) |
| `CONTENT_LAKE_APP_UI_CONTEXT` | [AlfrescoLabs/content-lake-app-ui](https://github.com/AlfrescoLabs/content-lake-app-ui) |

- The SHAs are pinned in the plugin code next to the upstream commit and change with it. BuildKit fetches each commit by its SHA.
- The built images have fixed tags (`content-lake-app/<service>:local`). So `up` passes `--build` when the pinned SHAs differ from the ones in `openarch-app.yaml`, and then records the new ones.

## Secure defaults

| Setting | Upstream | Plugin default | Change with |
| --- | --- | --- | --- |
| `BIND_IP` in `.env.local` | `0.0.0.0` | `127.0.0.1` | `init --bind-ip 0.0.0.0` |
| Secrets in `.env.local` | fixed values | random (see Credentials) | `init --default-secrets` |
| OpenArch users in `openarch-users.yml` | `admin:password` | one admin, random password | `init --default-users` |

`USE_HTTPS` is always `false` in `.env.local`. Upstream turns TLS on only when a certificate exists, and the ports bind to `127.0.0.1` by default. The proxy still publishes port 443.

## Commands

| Command | Built | What it does |
| --- | --- | --- |
| `version` | yes | Prints the version. |
| `doctor` | yes | Checks the prerequisites: Docker, Compose, Docker memory for the profile, Docker Model Runner and its URL, the models, free ports (80, 443 and 8090; 8082 and 8445 with `alfresco-2`), `nuxeo-deployment` for the Nuxeo profiles, and `openarch` 0.5.2 or later (the first release that removes `OPENARCH_PASSWORD` from the environment of plugins). `--dir` is the setup folder, which may not exist yet. |
| `init [dir]` | yes | Creates the setup folder, fetches upstream, writes the generated files, and adds the `openarch` context (with `--no-verify`, because the stack is not running yet). It prints the `docker compose` command that starts the stack. |
| `up`, `down`, `status`, `logs` | yes | Run `docker compose up -d --wait`, `down`, `ps` and `logs` with the profile and extras of `openarch-app.yaml`. `up` warns when a model is missing, fails early when nuxeo-deployment is missing, and prints the endpoints. `down` keeps the volumes. `status` exits with code 4 when a service is not healthy. In JSON mode `logs` returns the logs as one field and does not follow them. |
| `models pull` | yes | Pulls an `ai/` model with `docker model pull`. Builds the default chat model with the upstream `scripts/package-olmo3-local.sh`. Leaves any other model alone. |
| `smoke-test` | yes | Runs the upstream suite of the profile against the running stack: `test/test-alfresco.sh`, `test/test-nuxeo.sh`, or `test/smoke-test.sh` for `full` (it covers both sources). It runs from the setup folder, never from `upstream/`. |

Flags of `init`:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--profile` | `alfresco` | `alfresco`, `nuxeo` or `full` |
| `--extra` | none | `alfresco-2` |
| `--llm-model` | `local/olmo3-7b-instruct:Q4_K_M` | Chat model (the upstream default) |
| `--embedding-model` | `ai/mxbai-embed-large` | Embedding model |
| `--bind-ip` | `127.0.0.1` | Host address of every published port |
| `--context-name` | `app` | Name of the `openarch` context |
| `--default-secrets` | off | Keep the upstream secrets |
| `--default-users` | off | Keep the upstream OpenArch users (`admin:password`) and `HXPR_PASSWORD` |
| `--force` | off | Replace an existing context with the same name |

## Profiles

| Name | Kind | What it starts |
| --- | --- | --- |
| `alfresco` | profile | Hyland OpenArch, the bundled Alfresco and its ingesters, the RAG service, the Alfresco UI and the proxy |
| `nuxeo` | profile | Hyland OpenArch, the Nuxeo ingesters, the RAG service and the proxy. Needs nuxeo-deployment. |
| `full` | profile | `alfresco` and `nuxeo` together. Needs nuxeo-deployment. |
| `alfresco-2` | extra (an upstream override file) | A second Alfresco with its own ingesters, on host ports 8082 and 8445. Only with `alfresco` or `full`. Upstream limits the JVM memory of both instances to 13.5 GB in total. |

## Limits

- Images and models are pulled by tag, as upstream does. Base images and build-time packages inside the remote builds are not pinned. The Nuxeo source is a GitHub archive with no checksum.
- nuxeo-deployment is outside Rules 2 and 3 in the first version: the plugin does not fetch it, and its port 8081 does not follow `BIND_IP`.
- `ALFRESCO_ADMIN_PASSWORD`, `ALFRESCO_2_INTERNAL_PASSWORD` and `NUXEO_PASSWORD` keep their upstream values.
- The secrets are tied to the volumes. Losing `.env.local` means losing access to the data.
- Building the default chat model and running the test suites need `bash`, `curl`, and `jq` or `python3`. On Windows that means Git Bash or WSL.
- The plugin depends on the layout of upstream at the pinned commit. A new commit needs a test run before it becomes the default.
- Setup needs the network: the Git fetch, the image pulls, the remote builds and the models.
- The bundled OpenArch is separate from the one of a CLI workspace. Documents do not move between them.
