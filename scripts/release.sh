#!/bin/sh
set -eu

if [ "$#" -ne 1 ] || ! printf '%s\n' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "usage: scripts/release.sh MAJOR.MINOR.PATCH" >&2
    exit 2
fi

release_version=$1
release_minor=${release_version%.*}
release_files='README.md docs/development.md internal/config/config.go internal/config/config_test.go scripts/install.sh'
current_version=$(sed -n 's/^const DefaultImage = "docker.io\/atacandur\/codegenbox:\([0-9][0-9.]*\)"$/\1/p' internal/config/config.go)

if [ -z "$current_version" ] || ! printf '%s\n' "$current_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "could not read the current version from internal/config/config.go" >&2
    exit 1
fi
if [ "$release_version" = "$current_version" ]; then
    echo "already at version $release_version" >&2
    exit 1
fi
if [ "$(git branch --show-current)" != "main" ]; then
    echo "releases must be created from main" >&2
    exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
    echo "working tree must be clean before releasing" >&2
    exit 1
fi
if git show-ref --tags --verify --quiet "refs/tags/v$release_version"; then
    echo "tag v$release_version already exists locally" >&2
    exit 1
fi
remote_tag=$(git ls-remote --tags origin "refs/tags/v$release_version") || {
    echo "could not check release tags on origin" >&2
    exit 1
}
if [ -n "$remote_tag" ]; then
    echo "tag v$release_version already exists on origin" >&2
    exit 1
fi

perl -pi -e "s/\Q$current_version\E/$release_version/g" $release_files
perl -pi -e "s#// [0-9]+\.[0-9]+ Go CLI line#// $release_minor Go CLI line#" internal/config/config.go

git add $release_files
git commit -m "release: prepare v$release_version"
git tag -a "v$release_version" -m "Release v$release_version"
git push origin main "v$release_version"
