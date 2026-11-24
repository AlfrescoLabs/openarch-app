#!/usr/bin/env bash
# Push the Homebrew cask and the Scoop manifest of a published release to
# AlfrescoLabs/homebrew-tap and AlfrescoLabs/scoop-bucket. The release
# workflow makes both files (artifact "packages"), but GitHub refuses pushes
# to these repositories from GitHub-hosted runners, so this runs on a
# maintainer machine with gh and git access to both repositories.
#
# Usage: scripts/publish-packages.sh <tag>
# Example: scripts/publish-packages.sh v0.7.3
set -euo pipefail

if [ $# -ne 1 ]; then
	echo "usage: $0 <tag>" >&2
	exit 2
fi
tag=$1
version=${tag#v}
repo=AlfrescoLabs/openarch-app

# Users must not get a cask that points to files of a draft release.
if [ "$(gh release view "$tag" -R "$repo" --json isDraft -q .isDraft)" != false ]; then
	echo "release $tag is not published yet" >&2
	exit 1
fi

run=$(gh run list -R "$repo" --workflow release.yml --branch "$tag" --status success --limit 1 --json databaseId -q '.[0].databaseId')
if [ -z "$run" ]; then
	echo "no successful release run for $tag" >&2
	exit 1
fi

tmp=$(mktemp -d)
gh run download "$run" -R "$repo" -n packages -D "$tmp/packages"
cask=$tmp/packages/homebrew/Casks/openarch-app.rb
manifest=$tmp/packages/scoop/openarch-app.json
grep -q "version \"$version\"" "$cask" || { echo "$cask is not for $version" >&2; exit 1; }
grep -q "\"version\": \"$version\"" "$manifest" || { echo "$manifest is not for $version" >&2; exit 1; }

publish() {
	local name=$1 src=$2 dst=$3
	git clone -q "https://github.com/AlfrescoLabs/$name.git" "$tmp/$name"
	mkdir -p "$(dirname "$tmp/$name/$dst")"
	cp "$src" "$tmp/$name/$dst"
	git -C "$tmp/$name" add "$dst"
	if git -C "$tmp/$name" diff --cached --quiet; then
		echo "$name: $dst is already up to date"
		return
	fi
	git -C "$tmp/$name" commit -q -m "openarch-app $version"
	git -C "$tmp/$name" push -q origin HEAD:main
	echo "$name: pushed $dst for $version"
}

publish homebrew-tap "$cask" Casks/openarch-app.rb
publish scoop-bucket "$manifest" openarch-app.json
