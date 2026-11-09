# openarch-app

> [!IMPORTANT]
> **Not an official Hyland product.** openarch-app, App for OpenArch and the CLI for OpenArch are not Hyland OpenArch. They are in the [AlfrescoLabs](https://github.com/AlfrescoLabs) organization on GitHub because Hyland does not officially support them. Hyland OpenArch itself is at [github.com/hyland/OpenArch](https://github.com/hyland/OpenArch).

A plugin for the [CLI for OpenArch](https://github.com/AlfrescoLabs/openarch-cli) to install and operate App for OpenArch with Docker Compose. App for OpenArch adds Alfresco and Nuxeo ingesters, a RAG service and a UI to Hyland OpenArch. It runs with local models on Docker Model Runner.

The plugin wraps the upstream [content-lake-app-deployment](https://github.com/AlfrescoLabs/content-lake-app-deployment) repository at a pinned commit.

## Status

In development. `doctor`, `init` and `version` work.

## Run it

Put `openarch-app` on `PATH`. The CLI for OpenArch then runs it as a plugin:

```sh
openarch app doctor
openarch app init
openarch app version
openarch plugin list
```

`doctor` checks Docker, Docker Compose, Docker memory for the profile, Git, Docker Model Runner and its models, free ports, `nuxeo-deployment` for the profiles with Nuxeo, and the CLI for OpenArch 0.5.2 or later.

`init` creates the setup folder `./openarch-app`: it fetches upstream at the pinned commit with Git, writes random secrets to `.env.local`, and adds an `openarch` context named `app` for the bundled Hyland OpenArch. It prints the `docker compose` command that starts the stack.

It also runs on its own as `openarch-app`.

## Build

```sh
make build        # bin/openarch-app
go test ./...
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
