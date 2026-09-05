#!/bin/sh
set -eu

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
    echo "usage: scripts/build-release.sh <version> [goos/goarch]" >&2
    exit 2
fi

release_version=$1
if ! printf '%s\n' "$release_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "version must be stable SemVer: MAJOR.MINOR.PATCH" >&2
    exit 2
fi

if [ "$#" -eq 2 ]; then
    release_targets=$2
else
    release_targets="darwin/arm64 darwin/amd64 linux/arm64 linux/amd64"
fi

mkdir -p dist

for release_target in $release_targets; do
    release_os=${release_target%/*}
    release_arch=${release_target#*/}
    case "$release_target" in
        darwin/arm64 | darwin/amd64 | linux/arm64 | linux/amd64) ;;
        *)
            echo "unsupported release target: $release_target" >&2
            exit 2
            ;;
    esac

    release_name="codegenbox_${release_version}_${release_os}_${release_arch}"
    release_stage=$(mktemp -d "${TMPDIR:-/tmp}/codegenbox-release.XXXXXX")
    trap 'rm -rf "$release_stage"' EXIT HUP INT TERM

    CGO_ENABLED=0 GOOS=$release_os GOARCH=$release_arch \
        go build -trimpath \
        -ldflags="-s -w -X github.com/codegenbox/codegenbox/internal/version.Version=${release_version}" \
        -o "$release_stage/codegenbox" ./cmd/codegenbox
    tar -C "$release_stage" -czf "dist/${release_name}.tar.gz" codegenbox

    rm -rf "$release_stage"
    trap - EXIT HUP INT TERM
    echo "built dist/${release_name}.tar.gz"
done
