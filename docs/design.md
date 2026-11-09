# Design

`openarch-app` installs and operates App for OpenArch on one machine with Docker Compose. App for OpenArch adds Alfresco and Nuxeo ingesters, a RAG service and a UI to Hyland OpenArch. It runs with local models on Docker Model Runner.

It is one Go binary. It runs as the `openarch app` plugin of the CLI for OpenArch (github.com/AlfrescoLabs/openarch-cli), or on its own. App for OpenArch is a proof of concept, and so is this plugin.

Only `doctor` and `version` are built. The rest of this document is the design for the commands that come next. Flags are in `openarch app <command> --help`.

## Rules

1. **Wrap upstream, do not copy it.** The plugin downloads the upstream repository [content-lake-app-deployment](https://github.com/AlfrescoLabs/content-lake-app-deployment) at a pinned commit and runs its `compose.yaml`. The upstream stack changes often, and copied templates would drift from it. The plugin runs `docker compose` itself and does not use the upstream Makefile.
2. **Pinned and checked.** Everything the plugin downloads or builds is pinned to a full commit SHA: the upstream repository and the remote build contexts. The plugin checks the download before it uses it.
3. **Secure by default, every security feature optional.** Ports bind to `127.0.0.1`, every default secret is replaced with a random one, and OpenArch has no default users. Flags turn each of these off.
4. **Local models only.** Embeddings and chat run on Docker Model Runner.
5. **Same output contract as openarch.** Every command supports `--output json`. Results go to stdout, logs go to stderr. In JSON mode errors also go to stdout. The exit codes and the JSON error shape are the ones of openarch. Every prompt has a flag.
6. **No passwords in the environment of other tools.** The plugin passes the OpenArch password to `openarch` on stdin, never as an argument or a variable.

## Decisions

| Decision | Rejected option | Trade-off |
| --- | --- | --- |
| A plugin of the CLI for OpenArch | Built into the CLI (`init --with ...`) | The core CLI keeps one product and one workspace layout. The plugin costs a second binary. |
| Wrap upstream at a pinned commit | Copy its Compose files | No drift from upstream. Each bump of the commit needs a test run, and setup needs the network. |
| Use the Hyland OpenArch that upstream bundles | Reuse the OpenArch of a CLI workspace | The upstream stack runs as is. A second OpenArch runs when the user also has a CLI workspace. |
| A tree digest of the extracted files | A SHA-256 of the archive | GitHub does not promise stable archive bytes. The tree digest checks the content. |
| Run the `docker compose` CLI | The Compose Go SDK | The SDK is a large dependency. The CLI matches what the user runs by hand. |

## Setup folder

`openarch app init [dir]` creates a setup folder (default `./openarch-app`). The plugin does not read or change `openarch.yaml`.

```
openarch-app/
  openarch-app.yaml       # plugin config: the source of truth
  .env.local              # generated secrets and settings, mode 0600
  compose.override.yaml   # generated: OpenArch port and users file
  openarch-users.yml      # generated OpenArch users, mode 0600
  upstream/               # the upstream repository at the pinned commit, never edited
  nuxeo-deployment/       # only for the nuxeo, full and demo profiles
```

- `openarch-app.yaml` has `schemaVersion: 1`, the upstream commit, the profile, the extras, the models, the bind IP, the ports and the name of the `openarch` context.
- `upstream/` is replaced as a whole when the commit changes. Generated files never go inside it.
- Upstream expects `nuxeo-deployment` as a sibling folder of the repository (`../nuxeo-deployment`), and it publishes Nuxeo on host port 8081.

Every Compose call has this form:

```
docker compose -p <setup folder name> --project-directory upstream \
  --env-file upstream/.env --env-file .env.local \
  -f upstream/compose.yaml -f compose.override.yaml --profile <profile> [--profile <extra>...] ...
```

- `-p` overrides the fixed upstream name `content-lake-app`, so two setups do not share containers.
- `.env.local` comes after the upstream `.env`, so its values win.
- The `alfresco-2` extra adds `-f upstream/compose.alfresco-2.yaml`.

## Credentials

- The secrets of Alfresco, Nuxeo, OpenSearch, Postgres, ActiveMQ, the shared secret and the connector sync user live only in `.env.local` (mode 0600). `init` generates a random value for each default secret of the upstream `.env`. The connector sync user comes from a flag or a prompt.
- The OpenArch admin password is in `openarch-users.yml` as a bcrypt hash. `compose.override.yaml` mounts the file into `hxpr-app` (`OPENARCH_USERS_FILE`) and publishes the OpenArch port on `127.0.0.1:8090`. Port 8090 avoids 8080 (a CLI workspace) and 8081 (Nuxeo).
- The ingesters and the RAG service log in to OpenArch with `HXPR_USERNAME` and `HXPR_PASSWORD`, so the plaintext password is also in `.env.local`.
- `init` then pipes the password into `openarch config set --name <context> -s http://127.0.0.1:8090 -u admin --password-stdin`. From then on, `openarch --context <context> documents ...` works on the bundled OpenArch.

## Upstream commit and download check

- The ref is a full 40-character commit SHA. Tags and branches are refused, because they move.
- The plugin has a default commit in its code. `init --ref <sha>` picks another one.
- The plugin downloads `https://codeload.github.com/AlfrescoLabs/content-lake-app-deployment/tar.gz/<sha>` and strips the top folder.
- The check is a tree digest: SHA-256 over one line per file, sorted by path, `<mode> <sha256 of content> <path>`. Symbolic links use the hash of the link target. The plugin code pins the digest of the default commit.
- A `--ref` with no pinned digest gives a warning. With `--require-digest`, it is an error.

## Remote build contexts

Upstream builds some images from remote Git contexts. Each one is a variable that `.env.local` sets to a full commit SHA:

| Variable | Repository |
| --- | --- |
| `CONTENT_LAKE_GIT_CONTEXT` | content-lake-app |
| `CONTENT_LAKE_ACS_GIT_CONTEXT` | content-lake-app |
| `CONTENT_LAKE_UI_GIT_CONTEXT` | alfresco-content-lake-ui |
| `CONTENT_LAKE_APP_UI_CONTEXT` | content-lake-app-ui |

The SHAs are pinned in the plugin code next to the upstream commit and are bumped with it. BuildKit checks out the commit, so Git checks the content. The Nuxeo build is already pinned by SHA upstream.

## Secure defaults

`.env.local` overrides these upstream defaults:

| Variable | Upstream | Plugin default | Off with |
| --- | --- | --- | --- |
| `BIND_IP` | `0.0.0.0` | `127.0.0.1` | `--bind-ip` |
| `USE_HTTPS` | `true` | `false` | `--https` |
| Default secrets | fixed values | random | `--default-secrets` |
| OpenArch users | `admin:password` | one admin, random password | `--default-users` |

## Commands

| Command | What it does |
| --- | --- |
| `doctor` | Checks the prerequisites: Docker, Compose, Docker Model Runner and its URL, the models, free ports, Docker memory for the profile, `nuxeo-deployment` for the Nuxeo profiles, and `openarch` 0.5.2 or newer. |
| `init` | Creates the setup folder, downloads and checks upstream, writes the generated files, and registers the `openarch` context. |
| `up`, `down`, `status`, `logs` | Lifecycle, with profiles and extras. `up` prints the endpoints. |
| `models pull` | Pulls the embedding and chat models into Docker Model Runner. |
| `smoke-test` | Checks a running stack. |
| `connector`, `source`, `rag ask`, `reindex` | Admin commands for a running stack. |

## Profiles

Base profiles (one of them): `alfresco`, `nuxeo`, `full`, `demo`, `platform`. Extras (zero or more): `debug`, `observability`, `connector`, `sharepoint-mock`, `transform-extras`, `alfresco-2`. Each one has the meaning that it has upstream.

## Existing Alfresco

The plan is to connect the stack to an Alfresco the user already runs, instead of the bundled one. This changes a system the plugin does not own, so it needs its own design first: how the `content-lake-repo-model` module is installed, which Alfresco versions are supported, a backup and a dry run, how to uninstall it, and which permissions the plugin needs.

## Limits

- Images are pinned by tag, as upstream does, not by digest.
- The plugin depends on the layout of upstream at the pinned commit. A new commit needs a test run before it becomes the default.
- Setup needs the network: the download, the image pulls, the remote builds and the models.
- The bundled OpenArch is separate from the one of a CLI workspace. Documents do not move between them.
