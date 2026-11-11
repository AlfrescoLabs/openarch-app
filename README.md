# openarch-app

> [!IMPORTANT]
> **Not an official Hyland product.** openarch-app, App for OpenArch and the CLI for OpenArch are not Hyland OpenArch. They are in the [AlfrescoLabs](https://github.com/AlfrescoLabs) organization on GitHub because Hyland does not officially support them. Hyland OpenArch itself is at [github.com/hyland/OpenArch](https://github.com/hyland/OpenArch).

A plugin for the [CLI for OpenArch](https://github.com/AlfrescoLabs/openarch-cli) to install and operate App for OpenArch with Docker Compose. App for OpenArch adds Alfresco and Nuxeo ingesters, a RAG service and a UI to Hyland OpenArch. It runs with local models on Docker Model Runner.

The plugin wraps the upstream [content-lake-app-deployment](https://github.com/AlfrescoLabs/content-lake-app-deployment) repository at a pinned commit.

## Status

In development. Every command of the first version works: `doctor`, `init`, `models pull`, `up`, `status`, `logs`, `smoke-test`, `sync`, `down` and `version`. `upgrade` moves a setup to the upstream commit of a newer release.

## Install

Download the archive for your system from the [releases page](https://github.com/AlfrescoLabs/openarch-app/releases): Linux, macOS and Windows, on amd64 or arm64. Put `openarch-app` on your `PATH`, next to `openarch` (the CLI for OpenArch, 0.5.2 or later).

```sh
VERSION=0.4.0
OS=linux        # or darwin, windows
ARCH=amd64      # or arm64
curl -LO "https://github.com/AlfrescoLabs/openarch-app/releases/download/v$VERSION/openarch-app_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -LO "https://github.com/AlfrescoLabs/openarch-app/releases/download/v$VERSION/checksums.txt"
sha256sum --ignore-missing -c checksums.txt     # on macOS: shasum -a 256 --ignore-missing -c checksums.txt
tar xzf "openarch-app_${VERSION}_${OS}_${ARCH}.tar.gz"
sudo mv openarch-app /usr/local/bin/
openarch app version
```

On Windows, download the `.zip` archive and put `openarch-app.exe` in a folder on your `PATH`. The binaries are not signed with an Apple or Microsoft certificate: when a browser downloaded the file, run `xattr -d com.apple.quarantine openarch-app` once on macOS.

`checksums.txt` is signed with [cosign](https://docs.sigstore.dev/) by the release workflow (keyless, with the GitHub Actions identity), and each archive has an SBOM (`.sbom.json`, SPDX):

```sh
curl -LO "https://github.com/AlfrescoLabs/openarch-app/releases/download/v$VERSION/checksums.txt.sigstore.json"
cosign verify-blob checksums.txt \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/AlfrescoLabs/openarch-app/.github/workflows/release.yml@refs/tags/v$VERSION" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Run it

Put `openarch-app` on `PATH`. The CLI for OpenArch then runs it as a plugin:

```sh
openarch app doctor
openarch app init
cd openarch-app
openarch app models pull
openarch app up
openarch app smoke-test
openarch app down
```

`doctor` checks Docker, Docker Compose, Docker memory for the sources, Git, Docker Model Runner and its models, free ports, and the CLI for OpenArch 0.5.2 or later.

`init` creates the setup folder `./openarch-app`: it fetches upstream at the pinned commit with Git, writes random secrets to `.env.local`, and adds an `openarch` context named `app` for the bundled Hyland OpenArch. `up` builds the images on the first start, waits until every service is healthy, and prints the endpoints. `down` keeps the data.

`--alfresco` and `--nuxeo` say how many instances of each source the setup runs (default: one Alfresco). A setup has at most two Alfresco instances and one Nuxeo:

```sh
openarch app init                          # one Alfresco
openarch app init --alfresco 2             # two Alfresco instances
openarch app init --alfresco 1 --nuxeo 1   # one Alfresco and one Nuxeo
openarch app init --alfresco 0 --nuxeo 1   # one Nuxeo
```

`--sample-ui` adds the upstream sample UI. It asks questions across every source of the setup, with one login per source, and the proxy serves it at `http://127.0.0.1/`:

```sh
openarch app init --alfresco 1 --nuxeo 1 --sample-ui
openarch app init --alfresco 2 --sample-ui --alfresco-name "Branch" --alfresco-name "Head Office"
```

With two Alfresco instances, `--alfresco-name` gives each one the name that the sample UI shows (default: `Alfresco 1` and `Alfresco 2`).

`--filesystem` adds a folder of this machine as a source. The stack mounts it read-only. `--filesystem-reader` names a user who can read its files, once for each user, and `--filesystem-everyone` lets every user read them. A user logs in to Alfresco or Nuxeo, and the same user name counts for the files. Groups do not work for this source. Nothing syncs by itself: `sync` reads the folder, so run it again after the files change:

```sh
openarch app init --filesystem ~/Documents/shared --filesystem-reader jim --filesystem-reader dwight
openarch app up
openarch app sync
```

`--filesystem` does not work with `--alfresco 2 --sample-ui`, because that sample UI does not find the files.

`doctor` takes the same flags, and `--filesystem` there has no folder: it adds the port and memory of the filesystem source. With Nuxeo, `init` also fetches [nuxeo-deployment](https://github.com/aborroy/nuxeo-deployment), and Nuxeo runs in the same stack. Its first `up` builds Nuxeo from source, which takes long. The Nuxeo admin is still `Administrator`/`Administrator`.

After an update of `openarch-app` that pins a newer upstream commit, `up` refuses the setup until it is upgraded. There is no way back, so back up the volumes first:

```sh
openarch app down
openarch app upgrade
openarch app up
```

`upgrade` fetches the new commit and rewrites the build-context URLs in `.env.local`. The secrets stay. The next `up` builds the new images.

It also runs on its own as `openarch-app`.

## Build

```sh
make build        # bin/openarch-app
go test ./...
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
