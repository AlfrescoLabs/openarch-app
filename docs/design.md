# Design

`openarch-app` installs and operates App for OpenArch on one machine with Docker Compose. App for OpenArch adds Alfresco and Nuxeo ingesters, a RAG service and a UI to Hyland OpenArch. It runs with local models on [Docker Model Runner](https://docs.docker.com/ai/model-runner/).

It is one Go binary. `openarch app ...` runs the `openarch-app` binary from `PATH`, through the plugin hook of the [CLI for OpenArch](https://github.com/AlfrescoLabs/openarch-cli). `openarch-app ...` works the same way on its own. App for OpenArch is a proof of concept, and so is this plugin.

Upstream is [AlfrescoLabs/content-lake-app-deployment](https://github.com/AlfrescoLabs/content-lake-app-deployment). Its repositories and variables use the name `content-lake`, and it names the Hyland OpenArch service `hxpr-app` (its clients use `HXPR_*` variables).

Every command of the first version is built. The rest of this document is the design for the commands that come next. Flags of the built commands are in `openarch app <command> --help`.

## Goals and non-goals

Goals of the first version:

- One or two Alfresco instances and zero or one Nuxeo, chosen with `--alfresco` and `--nuxeo` (see Sources).
- One setup per machine.
- Every user gets the upstream commit that the plugin was tested with. A newer upstream commit comes with a plugin update.

Not in the first version:

- Choosing another upstream commit (`--ref`). A setup moves only to the commit of a newer release (see Upgrades).
- HTTPS. Every port binds to `127.0.0.1` by default.
- The other upstream profiles and extras: `demo`, `debug`, `observability`, `sharepoint-mock`, `transform-extras`. The plugin uses `platform` only for the sample UI and `connector` only for the filesystem source.
- More than two Alfresco instances or more than one Nuxeo.
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
  compose.override.yaml   # generated: OpenArch port, users file and data volume
  openarch-users.yml      # generated OpenArch users (bcrypt hashes), mode 0644
  upstream/               # the upstream repository at the pinned commit, never edited
  nuxeo-deployment/       # nuxeo-deployment at its pinned commit, only with Nuxeo
```

- `openarch-app.yaml` is the source of truth for settings: `schemaVersion: 2`, the sources (`alfresco` and `nuxeo`, the number of instances), the models, the bind IP, the name of the `openarch` context, whether the plugin manages nuxeo-deployment (`managedNuxeo`), whether the setup runs the sample UI (`sampleUI`), the folder of the filesystem source (`filesystem`), the URL of the CMIS source (`cmis`), the URL of an existing Alfresco (`externalAlfresco`) and the folder of its Compose project (`alfrescoCompose`), and the commits of the last build. It holds no secrets.
- `up` writes `compose.override.yaml` again on every start, so a setup made by an older build gets the current file.
- `init` writes `openarch-app.yaml` last. If `init` fails, running it again keeps `upstream/` and `.env.local`. Generated files never go inside `upstream/`.
- `openarch-users.yml` holds only hashes. Its mode is 0644 so that the OpenArch container (user `openarch-user`, uid 33010) can read it through a bind mount on Linux.
- Upstream expects `nuxeo-deployment` next to its own repository (`../nuxeo-deployment`). With Nuxeo, `init` fetches it at its pinned commit, with the same shallow Git fetch as upstream, and records `managedNuxeo: true`. Every Compose call then adds the upstream `compose.nuxeo-deployment.yaml`, which runs Nuxeo and its database in the same project, on the stack network, with port 8081 on `BIND_IP`. So `up`, `down`, `status` and `logs` cover Nuxeo too.
- A setup made before 0.3.0 has a nuxeo-deployment that the user cloned and started as a second project. Its data is in that project's volumes, so the plugin does not take it over: `upgrade` records `managedNuxeo: false`, and that setup keeps working as before.

Every Compose call has this form:

```sh
docker compose -p openarch-app --project-directory upstream \
  --env-file upstream/.env --env-file .env.local \
  -f upstream/compose.yaml -f compose.override.yaml --profile <upstream profile> ...
```

- `-p openarch-app` replaces the fixed upstream name `content-lake-app`. There is one setup per machine.
- `.env.local` comes after the upstream `.env`, so its values win.
- The upstream profile and extras follow from the sources (see Sources). The second Alfresco adds `-f upstream/compose.alfresco-2.yaml` and `--profile alfresco-2`, a managed Nuxeo adds `-f upstream/compose.nuxeo-deployment.yaml`, a filesystem or CMIS source adds `--profile connector` (see Filesystem source and CMIS source), and the sample UI adds `--profile platform` (see Sample UI).
- Compose resolves relative paths in every file against the project directory, `upstream/`. So `compose.override.yaml` refers to its neighbors with `../` paths, for example `../openarch-users.yml`.
- Hyland OpenArch keeps its blobs, the embeddings included, in `/deployments/nxserver/data`, and upstream gives that folder no volume. A recreated container would lose them while MongoDB and OpenSearch keep the metadata, and the next permission change would then delete the embeddings. So `compose.override.yaml` adds the volume `hxpr-data`, and a one-shot service `hxpr-data-init` gives it to the OpenArch user (uid 33010) before OpenArch starts: OpenArch cannot write to a volume owned by root.

## Credentials

- `init` generates random values for the secrets where the stack sets both the server side and the client side: `POSTGRES_PASSWORD`, `SHARED_SECRET`, `ACTIVEMQ_PASSWORD`, `OPENSEARCH_ADMIN_PASSWORD` and `HXPR_PASSWORD`. One generator makes passwords that meet the OpenSearch strength rule and use only URL-safe characters.
- `init` also generates a random admin password for each Alfresco: `ALFRESCO_ADMIN_PASSWORD` and `ALFRESCO_2_INTERNAL_PASSWORD`, the passwords that the ingesters and `rag-service` use. Alfresco itself reads the admin password once, at the first boot of an empty database, from `ALFRESCO_ADMIN_PASSWORD_HASH` and `ALFRESCO_2_ADMIN_PASSWORD_HASH`. `init` writes those as the MD4 hash over the UTF-16LE password, the only form that upstream accepts there.
- A setup made before 0.2.0 keeps Alfresco's `admin`, because Alfresco keeps the password of its first start. `up` warns about it.
- `NUXEO_PASSWORD` keeps its upstream value: Nuxeo creates `Administrator` at its first boot, and nuxeo-deployment has no setting for it.
- The secrets are in `.env.local` only. `init` writes them once and never overwrites an existing `.env.local`, because the databases keep the passwords of their first start.
- The OpenArch admin password is in `openarch-users.yml` as a bcrypt hash. `compose.override.yaml` mounts the file into `hxpr-app` (`OPENARCH_USERS_FILE`) and publishes the OpenArch port on `${BIND_IP}:8090`. Port 8090 avoids 8080 (a CLI workspace) and 8081 (Nuxeo).
- The ingesters and the RAG service log in to OpenArch as the admin, with `HXPR_USERNAME` and `HXPR_PASSWORD`, so the plaintext password is also in `.env.local`.
- `init` pipes the password into `openarch config set --name <context> -s http://127.0.0.1:8090 -u admin --password-stdin`. A context is a named server URL and login in the CLI config. The default name is `app`. `init` fails if the name exists, unless `--force` is set. Then `openarch --context app documents ...` works on the bundled OpenArch.

## Upstream commit

- The plugin code holds one full commit SHA of upstream. Every new setup uses it, and `upgrade` moves a setup of an earlier release to it.
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
- The built images have fixed tags (`content-lake-app/<service>:local`). Some of them also build from files in `upstream/` itself (`dockerfiles/Dockerfile`, `acs/alfresco`). So `up` passes `--build` when the upstream commit or the pinned SHAs differ from the ones of the last build in `openarch-app.yaml`, and then records the new ones.

## Upgrades

A plugin release can pin a newer upstream commit and newer build contexts. `openarch app upgrade` moves a setup to the pins of the binary. It is a separate command, because a new commit can bring images that change the data in the volumes, and there is no way back.

- The plugin code lists the upstream commits of earlier releases. A setup at one of them can be upgraded. A setup at a commit that the binary does not know comes from a newer release, and every command that uses upstream refuses it with "update openarch-app".
- `up` and `smoke-test` refuse a setup whose upstream commit, or whose build-context URLs in `.env.local`, differ from the pins of the binary, with the hint `openarch app upgrade`. `down`, `status` and `logs` still work, so the user can stop the old stack first.
- `upgrade` does these steps in order:
  1. It refuses when a service of the stack is running: stop it with `down`, then back up the volumes.
  2. It asks for confirmation. `--yes` skips the question, and non-interactive mode needs `--yes`.
  3. It fetches the pinned commit into `upstream.partial` and checks its `HEAD`. A failed fetch changes nothing.
  4. It rewrites the four `*_CONTEXT` keys of `.env.local` in place. No other key changes.
  5. It swaps `upstream.partial` with `upstream/`. Files added inside `upstream/` are lost.
  6. It records the new commit in `openarch-app.yaml`.

  Each step can run again, so an upgrade that stops halfway is completed by running `upgrade` again.
- `upgrade` does not build. The next `up` sees that the upstream commit of the last build differs, builds the images from the new checkout and URLs, and records them.
- With Nuxeo, `upgrade` also moves a managed `nuxeo-deployment` to the commit of this release, in the same way as `upstream/`. For a nuxeo-deployment that the user manages, it warns and prints the Git commands.
- A release that adds a field to `openarch-app.yaml` bumps `schemaVersion`, and its `upgrade` migrates the file. `Load` refuses unknown fields and other versions, so an older binary refuses a newer file.
- There is no downgrade. The built images have fixed `:local` tags, so the old images are gone after the next `up`.

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
| `doctor` | yes | Checks the prerequisites: Docker, Compose, Docker memory for the sources, Docker Model Runner and its URL, the models, free ports (80, 443 and 8090; 8081 with Nuxeo; 8082 and 8445 with a second Alfresco; 4200 with `--sample-ui`; 9096 with `--filesystem` or `--cmis`, which also add memory), the `nuxeo-deployment` folder when it already exists, and `openarch` 0.5.2 or later (the first release that removes `OPENARCH_PASSWORD` from the environment of plugins). `--dir` is the setup folder, which may not exist yet. |
| `init [dir]` | yes | Creates the setup folder, fetches upstream (and nuxeo-deployment with Nuxeo), writes the generated files, and adds the `openarch` context (with `--no-verify`, because the stack is not running yet). It prints the next commands: `models pull` and `up`. |
| `up`, `down`, `status`, `logs` | yes | Run `docker compose up -d --wait`, `down`, `ps` and `logs` with the upstream profile and extras of the sources in `openarch-app.yaml`. `up` warns when a model is missing, fails early when nuxeo-deployment is missing, and prints the endpoints. `down` keeps the volumes. `status` exits with code 4 when a service is not healthy. In JSON mode `logs` returns the logs as one field and does not follow them. |
| `models pull` | yes | Pulls an `ai/` model with `docker model pull`. Builds the default chat model with the upstream `scripts/package-olmo3-local.sh`. Leaves any other model alone. |
| `upgrade` | yes | Moves the setup to the upstream commit and build contexts of the binary (see Upgrades). |
| `smoke-test` | yes | Runs the upstream suite of the sources against the running stack: `test/test-alfresco.sh`, `test/test-nuxeo.sh`, or `test/test-rag-full.sh` for `full` (it covers both sources). These are the suites of the upstream `test/run-tests.sh`; `test/smoke-test.sh` is a check for a demo host and wants the demo UI at `/`. It runs from the setup folder, never from `upstream/`. |
| `alfresco-prepare` | yes | Builds the images for the Compose project of an existing Alfresco and writes the override file that uses them. `--apply` installs it in the project and recreates the changed services, and `--undo` removes the `cl:` aspects and then the changes (see Existing Alfresco). |
| `alfresco-module` | yes | Builds the repository module JAR for an existing Alfresco from the pinned commit, and prints the steps to install it (see Existing Alfresco). |
| `sync` | yes | Starts a sync of the filesystem or CMIS source and waits for the job (see Filesystem source). Exit code 4 means the job failed, a file failed, or the job still runs after `--timeout`. |

Flags of `init`:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--alfresco` | `1` | Number of Alfresco instances, 0 to 2 |
| `--nuxeo` | `0` | Number of Nuxeo instances, 0 or 1 |
| `--llm-model` | `local/olmo3-7b-instruct:Q4_K_M` | Chat model (the upstream default) |
| `--embedding-model` | `ai/mxbai-embed-large` | Embedding model |
| `--bind-ip` | `127.0.0.1` | Host address of every published port |
| `--context-name` | `app` | Name of the `openarch` context |
| `--sample-ui` | off | Add the upstream sample UI, served by the proxy at `/` (see Sample UI) |
| `--alfresco-name` | `Alfresco 1`, `Alfresco 2` | Name of each Alfresco source in the sample UI, repeatable, in instance order. Needs `--alfresco 2` |
| `--filesystem` | none | Folder of this machine to add as a source (see Filesystem source) |
| `--filesystem-reader` | none | User who can read the files of `--filesystem`, repeatable |
| `--filesystem-everyone` | off | Every user can read the files of `--filesystem` |
| `--cmis-env` | none | File with the settings of a CMIS source (see CMIS source) |
| `--cmis-password-stdin` | off | Read the CMIS password from the first line of stdin |
| `--alfresco-env` | none | File with the URLs and logins of an existing Alfresco that replaces the bundled one (see Existing Alfresco) |
| `--alfresco-compose` | none | Folder of the Docker Compose project of an existing Alfresco that replaces the bundled one (see Existing Alfresco) |
| `--alfresco-user` | `admin` | Admin user of that Alfresco. Its password comes from `OPENARCH_APP_ALFRESCO_PASSWORD`, `--alfresco-password-stdin` or a prompt |
| `--default-secrets` | off | Keep the upstream secrets |
| `--default-users` | off | Keep the upstream OpenArch users (`admin:password`) and `HXPR_PASSWORD` |
| `--force` | off | Replace an existing context with the same name |

## Sources

A user chooses the sources, not upstream's profiles: `--alfresco N` and `--nuxeo N` on `init` and `doctor` give the number of instances of each. Every setup also runs Hyland OpenArch, the RAG service and the proxy. The plugin maps the counts to the upstream Compose profile and extras:

| `--alfresco` | `--nuxeo` | Upstream profile and extras | What it adds |
| --- | --- | --- | --- |
| 1 | 0 | `alfresco` | Alfresco and its ingesters, the Alfresco UI |
| 0 | 1 | `nuxeo` | Nuxeo (from nuxeo-deployment) and its ingesters |
| 1 | 1 | `full` | both |
| 2 | 0 or 1 | `alfresco` or `full`, plus `alfresco-2` | a second Alfresco with its own ingesters, on host ports 8082 and 8445. Upstream limits the JVM memory of both instances to 13.5 GB in total. |

A setup needs at least one source, and upstream supports at most two Alfresco instances and one Nuxeo. The upstream-shaped `--profile` and `--extra` of 0.1.x and 0.2.0 still work, with a warning, and cannot be mixed with the new flags.

With two Alfresco instances, `rag-service` lists both instances as sources, and it needs the repository id of each (`ALFRESCO_1_SOURCE_ID`, `ALFRESCO_2_SOURCE_ID`). The id is the repository UUID that Alfresco makes at its first boot and that the ingesters already write on every document, so the plugin reads it and never chooses it. `rag-service` refuses to start while it lists two Alfresco sources and an id is empty, and the other services wait for it. So when an id is empty, `up` first starts `alfresco` and `alfresco-2` alone (with their databases and brokers, and always with a wait), reads `repository.id` from `/alfresco/api/discovery` of each instance (with `curl` inside its container, the login on stdin, so it needs neither the proxy nor a published port), writes the ids to `.env.local`, and then starts the whole stack. After every `up --wait` it reads the ids again, writes them when they changed, and recreates only `rag-service`, so the ids also follow a repository that `down -v` replaced. `smoke-test` then also runs `test/test-two-alfresco.sh`.

## Sample UI

`--sample-ui` on `init` adds the upstream sample UI (`content-lake-app-ui`). It asks questions across every source of the setup, with one login per source, so it shows one answer from Alfresco and Nuxeo, or from two Alfresco instances. Without it, the proxy sends `/` to the Alfresco UI, which knows only its own repository.

Upstream runs the sample UI in the `demo` and `platform` profiles. `demo` is `full` plus the UI, so it would also start Nuxeo. `platform` holds only the UI and services that every profile already has, so the plugin adds `--profile platform` next to the profile of the sources. `init` also writes `NGINX_ROOT_DIRECTIVE='proxy_pass http://$demo_ui_upstream;'` to `.env.local`, so the proxy serves the UI at `/`. The upstream nginx template resolves `$demo_ui_upstream` on each request, so the proxy can start before the UI. The single quotes keep Compose from expanding the variable. The UI container also listens on port 4200, on `BIND_IP`.

With two Alfresco instances, `rag-service` lists the sources by name. `init` writes `ALFRESCO_1_DISPLAY_NAME` and `ALFRESCO_2_DISPLAY_NAME` to `.env.local`, from `--alfresco-name` or the defaults `Alfresco 1` and `Alfresco 2`, in single quotes. A name has 1 to 60 characters and no single quote. With one Alfresco, upstream has one source of each type and names them `Alfresco` and `Nuxeo`, so `--alfresco-name` is refused there.

`sampleUI` is an optional field of `openarch-app.yaml`, so `schemaVersion` stays 2. A file without it is the same as before; a file with it cannot be read by 0.3.0, which refuses unknown fields. The choice is made at `init` only.

## Filesystem source

`--filesystem <folder>` on `init` adds a folder of this machine as a source. Upstream reads it with its shipped filesystem connector, which runs in `plugin-batch-ingester` in the `connector` profile. The one-shot `connector-init` copies the connector jar, built from the pinned `content-lake-app` commit, into a volume before the ingester starts. So the plugin adds `--profile connector` to every Compose call and writes these keys to `.env.local`:

| Key | Value |
| --- | --- |
| `CONNECTOR_SOURCE_TYPE` | `filesystem` |
| `CONNECTOR_HOST_PATH` | the absolute folder path, in single quotes. Upstream mounts it read-only. |
| `FILESYSTEM_READ_PRINCIPALS` | the readers, comma separated, or empty for everyone |
| `CONNECTOR_SYNC_USERNAME`, `CONNECTOR_SYNC_PASSWORD` | `sync` and a random password, also with `--default-secrets`, because upstream has no default |
| `CONNECTOR_PORT` | `<bind IP>:9096`, so the sync API follows `BIND_IP` |

- The connector gives every file the readers of `FILESYSTEM_READ_PRINCIPALS`, and an empty list means everyone. So `init` needs `--filesystem-reader` or `--filesystem-everyone`, and an empty list is never a default.
- A reader is a user name. The RAG service takes the user name of the Alfresco or Nuxeo login for the filesystem source, but it looks up no groups there, so a group would match nobody. `init` refuses a reader that starts with `GROUP_`.
- With two Alfresco instances, the sample UI sends one login for each Alfresco source and none for the filesystem source, so the RAG service finds no files for it. `init` refuses `--filesystem` with `--alfresco 2 --sample-ui`.
- The ingester has no schedule. A sync runs only when its API is called (`POST /api/sync/configured` on port 9096, with the sync account). `openarch app sync` makes that call, then reads `GET /api/sync/status/<job>` until the job ends, and prints the counts. `up` does not sync.
- `filesystem` is an optional field of `openarch-app.yaml`, like `sampleUI`, so `schemaVersion` stays 2. The folder and the readers are chosen at `init` only. One setup has at most one filesystem source.

## CMIS source

`--cmis-env <file>` on `init` adds a CMIS 1.1 repository as a source. Upstream reads it with its shipped CMIS connector, in the same `plugin-batch-ingester` as the filesystem source. Upstream runs one connector in a setup, so `init` refuses `--cmis-env` together with `--filesystem`. The rest works as for the filesystem source: `--profile connector`, the sync account, port 9096 on `BIND_IP`, `openarch app sync`, and no sync without it.

- The file has `KEY=value` lines. `init` copies only the keys `CMIS_*` (the settings of the connector), `RAG_SECURITY_CMIS_*` (a caller login against the CMIS repository, which the RAG service tries after Alfresco and Nuxeo) and `CONNECTOR_ROOTS`. It refuses any other key, so the file cannot change a secret or a port of the stack. It writes each value to `.env.local` in single quotes, so Compose does not expand a `$`, and it refuses a value with a single quote.
- `CMIS_URL` and `CMIS_USERNAME` are required. `CMIS_URL` is resolved inside the stack, so `init` refuses `localhost` and `127.*`. It also refuses a URL with a login, because `openarch-app.yaml` stores the URL.
- `init` refuses `CMIS_ACL_FALLBACK=public`. With it, every user would read a document whose permissions the repository cannot report. The upstream default is `fail-closed`.
- The password is never a flag value (Rule 6). It is `CMIS_PASSWORD` in the file, or the variable `OPENARCH_APP_CMIS_PASSWORD`, or the first line of stdin with `--cmis-password-stdin`, or a prompt in an interactive terminal.
- The connector takes the readers of each document from its CMIS permissions, and maps the "anyone" principal of the repository to everyone. The RAG service looks up no groups for a CMIS source, so a document that only a group can read is found by nobody.
- `cmis` (the URL) is an optional field of `openarch-app.yaml`, so `schemaVersion` stays 2. The other settings are in `.env.local` only.

## Existing Alfresco

`--alfresco-env <file>` on `init` uses an Alfresco that the user already runs, instead of the bundled one. The plugin never changes that Alfresco: it builds the module, prints the steps, and checks the result. The user owns every change on the Alfresco side. It works with `--alfresco 1 --nuxeo 0`, the case that is tested; a second Alfresco or Nuxeo next to it is refused.

Upstream runs it with the `alfresco-external` profile: the Alfresco ingesters, `transform-core-aio`, ACA, Hyland OpenArch, `rag-service` and the proxy, and none of the bundled Alfresco services. So every Compose call uses `--profile alfresco-external` instead of `alfresco`.

### On the Alfresco side

1. Back up the Alfresco database and content store. The module adds content model aspects, and Alfresco keeps nodes with those aspects in the database.
2. `openarch app alfresco-module <dir>` builds `content-lake-repo-model-<version>.jar` from the pinned `content-lake-app` commit, with the upstream `model-jar` Dockerfile stage, so the host needs no Maven.
3. Copy the JAR into `webapps/alfresco/WEB-INF/lib/` of the repository, or into the image that runs it (alfresco-docker-installer: `alfresco/modules/jars/`).
4. Set the module properties: `content.lake.permission.sync.enabled=true`, `.broker-url`, `.broker-user` and `.broker-password`, with the broker as Alfresco reaches it.
5. Make the broker reachable from the stack. For example, alfresco-docker-installer publishes no broker port.
6. With Alfresco Search Community, mount `upstream/acs/search/reindex.prefixes-file.json` in the batch indexer and set `ALFRESCO_REINDEX_PREFIXES_FILE`, so the `cl` prefix is indexed.
7. Restart Alfresco.

### On Docker Compose

`--alfresco-compose <dir>` instead of `--alfresco-env` is for an Alfresco that runs with Docker Compose. The plugin then makes the changes of the steps above itself, or shows them, and the user chooses:

- `init` reads the project with `docker compose config`, so the files, `.env` and profiles count as for the user's own `docker compose up`. It finds the repository (an `alfresco-content-repository` image, or `-Ddb.url=` in `JAVA_OPTS`), the ActiveMQ broker and its login, the search indexer (Search Community or Solr) and ACA, and their versions. It refuses a project without a broker, with two repositories, or with Alfresco older than 25.
- The two projects meet on the Docker network `openarch-app-alfresco`. The repository and the broker join it with the aliases `openarch-alfresco` and `openarch-activemq`, and the stack proxy with `openarch-app-proxy`. So the stack reaches them by name, and no port of the project is published. `up` creates the network when it is missing.
- `alfresco-prepare` builds two images: the repository image of the project plus the module JAR (`openarch-app/<project>-<service>:module`), and ACA of the project's version with the extension, from the pinned `alfresco-content-lake-ui` commit (`openarch-app/aca-extension:<version>`). It writes `alfresco-compose.override.yaml` in the setup folder. That file replaces the image of the repository (with `build: !reset null`), adds the module properties to its full `JAVA_OPTS`, keeps the networks of each service and adds the shared one, mounts the prefixes file in a Search Community indexer, and replaces the image of ACA. The ACA image has its own nginx, which forwards `/alfresco/` to the repository and the App for OpenArch paths to the stack, so a proxy that sends `/` to ACA needs no change. That nginx resolves the stack proxy at startup, so the replaced ACA gets `restart: unless-stopped` and runs once the stack is up.
- Without `--apply`, nothing in the project changes, and the command prints the steps. With `--apply`, it asks for confirmation (`--yes` when non-interactive), installs the file as the override file of the project (`docker-compose.override.yml` next to `docker-compose.yml`, `compose.override.yaml` next to `compose.yaml`), waits for the repository, the broker and the indexer, and then starts the rest. It refuses an override file that it did not write. A later run reads the project without its own file, so it starts from the user's files again.
- `--undo` removes it again. Once the module is gone, Alfresco cannot read or update a node that keeps a `cl:` aspect: `GET` and `PUT` of that node answer 500, "A namespace prefix is not registered for uri http://www.alfresco.org/model/contentlake/1.0" (listing, download and search still work, and putting the module back repairs it). So `--undo` keeps the module until the aspects are gone: it stops the two ingesters of the stack, so none adds an aspect again, keeps the stack proxy to reach the repository, searches for `cl:indexed`, `cl:syncStatus` and `cl:fileScope` page by page, removes those aspects from each node, and searches again until the index lists no node that still has one. Then it removes the override file, recreates the services of the project with the user's files, and stops the stack. Tested: the nodes answer 200 again, and the repository and ACA run the images of the project.
- The file holds the broker password of the project, as the project's own files do, so it has mode 0600.

### What the plugin does

- The file has `KEY=value` lines with the keys of the upstream profile: `ALFRESCO_URL`, `ALFRESCO_EVENT_BROKER_URL`, `ALFRESCO_ADMIN_USER`, `ALFRESCO_ADMIN_PASSWORD`, `ACTIVEMQ_USER` and `ACTIVEMQ_PASSWORD`, and optionally `CONTENT_LAKE_PERMISSION_SYNC_BROKER_URL` (default: the Event2 broker) and `ALFRESCO_PROXY_UPSTREAM` (default: host and port of `ALFRESCO_URL`). `init` refuses other keys. The passwords are in the file, as with `--cmis-env`, never in a flag value.
- The URLs are the ones that the containers reach, so `init` refuses `localhost` and `127.*`, a login in a URL, and a path in `ALFRESCO_URL` (the clients add `/alfresco`). `ALFRESCO_URL` must be `http`: the proxy forwards `/alfresco/` over plain HTTP.
- `init` writes the keys to `.env.local` in single quotes, and `TRANSFORM_ACTIVEMQ_URL=false`: there is no bundled broker, and the transform engine works over HTTP. It writes no `ALFRESCO_ADMIN_PASSWORD_HASH`, because there is no bundled Alfresco to bootstrap. The logins of the file replace the random secrets of the bundled Alfresco.
- After every `up` with a wait, the plugin checks the Alfresco through the proxy of the stack, which forwards `/alfresco/` to it: the login works, the version is 25 or later (from the discovery API), and the aspect `cl:indexed` exists. A missing module fails with the steps above.
- `smoke-test` runs `test/test-alfresco.sh`. It creates test users, groups and folders in that Alfresco and removes them at the end, so it needs `--yes`.
- `externalAlfresco` (the URL) is an optional field of `openarch-app.yaml`, so `schemaVersion` stays 2.

### Answers

| Question | Answer |
| --- | --- |
| How the module is installed | By the user. The plugin builds the JAR from the pinned commit and prints the steps. It never copies files to, or restarts, a system it does not own. |
| Alfresco versions | 25.x and later, as the content-lake-app README says (upstream builds the module against 25.2). Tested with Community 26.2 from alfresco-docker-installer. Enterprise is not tested. |
| Backup and dry run | The backup is step 1, by the user. There is no dry run: `up` checks the Alfresco right after the stack starts, and the ingesters only sync folders that have the `cl:indexed` aspect of the module. |
| Uninstall | Remove the `cl:` aspects from every node first, while the module is installed, then the JAR and the properties, then restart. A node that keeps a `cl:` aspect cannot be read or updated without the module (500). On Docker Compose, `alfresco-prepare --undo` does it. Otherwise, search for the three aspects and remove them with the REST API. Version history and the trashcan are not checked. |
| Permissions on the Alfresco system | One account in `GROUP_ALFRESCO_ADMINISTRATORS`. The ingesters read every node with its permissions, add `cl:indexed` to root folders and write `cl:syncStatus`, and `rag-service` reads the groups of each caller. Plus a broker account that reads the topic `alfresco.repo.event2` and reads and writes the queue `contentlake.acl.changed`. |

## Limits

- Images and models are pulled by tag, as upstream does. Base images and build-time packages inside the remote builds are not pinned. The Nuxeo source is a GitHub archive with no checksum.
- `NUXEO_PASSWORD` stays `Administrator` until nuxeo-deployment can set its admin password at the first boot. A nuxeo-deployment that the user manages is outside Rules 2 and 3: its port 8081 does not follow `BIND_IP`.
- The first `up` with Nuxeo builds Nuxeo from source, which takes long.
- `NUXEO_PASSWORD` keeps its upstream value, and setups made before 0.2.0 keep the upstream Alfresco admin password.
- The secrets are tied to the volumes. Losing `.env.local` means losing access to the data.
- Building the default chat model and running the test suites need `bash`, `curl`, and `jq` or `python3`. On Windows that means Git Bash or WSL.
- The plugin depends on the layout of upstream at the pinned commit. A new commit needs a test run before it becomes the default.
- Setup needs the network: the Git fetch, the image pulls, the remote builds and the models.
- The bundled OpenArch is separate from the one of a CLI workspace. Documents do not move between them.
