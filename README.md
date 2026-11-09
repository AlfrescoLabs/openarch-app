# openarch-app

> [!IMPORTANT]
> **Not an official Hyland product.** openarch-app, App for OpenArch and the CLI for OpenArch are not Hyland OpenArch. They are in the [AlfrescoLabs](https://github.com/AlfrescoLabs) organization on GitHub because Hyland does not officially support them. Hyland OpenArch itself is at [github.com/hyland/OpenArch](https://github.com/hyland/OpenArch).

A plugin for the [CLI for OpenArch](https://github.com/AlfrescoLabs/openarch-cli) to install and operate App for OpenArch with Docker Compose. App for OpenArch adds Alfresco and Nuxeo ingesters, a RAG service and a UI to Hyland OpenArch. It runs with local models on Docker Model Runner.

The plugin wraps the upstream [content-lake-app-deployment](https://github.com/AlfrescoLabs/content-lake-app-deployment) repository at a pinned commit.

## Status

In development. Only `version` works.

## Run it

Put `openarch-app` on `PATH`. The CLI for OpenArch then runs it as a plugin:

```sh
openarch app version
openarch plugin list
```

It also runs on its own as `openarch-app`.

## Build

```sh
make build        # bin/openarch-app
go test ./...
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
