#!/bin/sh
set -eu

usage() {
    echo "usage: scripts/release.sh MAJOR.MINOR.PATCH [--local-image] [--platforms linux/arm64|linux/amd64|linux/amd64,linux/arm64]" >&2
    exit 2
}

[ "$#" -ge 1 ] || usage
release_version=$1
shift
printf '%s\n' "$release_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || usage
local_image=false
image_platforms=
while [ "$#" -gt 0 ]; do
    case "$1" in
        --local-image)
            [ "$local_image" = false ] || usage
            local_image=true
            shift
            ;;
        --platforms)
            [ -z "$image_platforms" ] && [ "$#" -ge 2 ] || usage
            case "$2" in
                linux/arm64 | linux/amd64 | linux/amd64,linux/arm64 | linux/arm64,linux/amd64) ;;
                *) usage ;;
            esac
            image_platforms=$2
            shift 2
            ;;
        *) usage ;;
    esac
done
if [ -n "$image_platforms" ] && [ "$local_image" = false ]; then
    echo "--platforms requires --local-image" >&2
    exit 2
fi

release_minor=${release_version%.*}
set -- README.md docs/development.md internal/config/config.go internal/config/config_test.go scripts/install.sh
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

if [ "$local_image" = true ]; then
    image="docker.io/atacandur/codegenbox:$release_version"
    image_check_dir=$(mktemp -d "${TMPDIR:-/tmp}/codegenbox-release-image.XXXXXX")
    cleanup_image_check() {
        rm -f "$image_check_dir/existing" "$image_check_dir/error" \
            "$image_check_dir/metadata.json" "$image_check_dir/published" "$image_check_dir/manifest.json"
        rmdir "$image_check_dir"
    }
    trap cleanup_image_check EXIT
    trap 'exit 1' HUP INT TERM
    daemon_arch=$(docker info --format '{{.Architecture}}')
    case "$daemon_arch" in
        arm64 | aarch64) native_platform=linux/arm64 ;;
        amd64 | x86_64) native_platform=linux/amd64 ;;
        *) echo "unsupported Docker architecture: $daemon_arch" >&2; exit 1 ;;
    esac
    image_platforms=${image_platforms:-$native_platform}
    docker buildx version >/dev/null
    command -v jq >/dev/null
    if docker buildx imagetools inspect "$image" >"$image_check_dir/existing" 2>"$image_check_dir/error"; then
        echo "refusing to overwrite existing image $image" >&2
        exit 1
    elif ! grep -Eiq 'not found|manifest unknown|name unknown' "$image_check_dir/error"; then
        cat "$image_check_dir/error" >&2
        echo "could not establish that $image is absent; no release was created" >&2
        exit 1
    fi
    if awk -v version="$release_version" '$1 == version { found=1 } END { exit !found }' release/image-digests.txt; then
        echo "image digest already recorded for $release_version" >&2
        exit 1
    fi

    # A container builder supports registry pushes with attestations even when
    # the default Docker driver cannot. Reuse its cache without switching the
    # user's selected builder.
    image_builder=${CODEGENBOX_RELEASE_BUILDER:-codegenbox-publish}
    if ! docker buildx inspect "$image_builder" >/dev/null 2>&1; then
        docker buildx create --name "$image_builder" --driver docker-container
    fi
    echo "Building and publishing $image locally for $image_platforms"
    docker buildx build --builder "$image_builder" --platform "$image_platforms" \
        --provenance=mode=max --sbom=true --push --tag "$image" \
        --metadata-file "$image_check_dir/metadata.json" .
    docker buildx imagetools inspect "$image" >"$image_check_dir/published"
    image_digest=$(awk '$1 == "Digest:" { print $2; exit }' "$image_check_dir/published")
    if ! printf '%s\n' "$image_digest" | grep -Eq '^sha256:[0-9a-f]{64}$'; then
        echo "could not verify published image digest; no release tag was created" >&2
        exit 1
    fi
    built_digest=$(jq -er '."containerimage.digest"' "$image_check_dir/metadata.json")
    if [ "$image_digest" != "$built_digest" ]; then
        echo "published image digest differs from local build; no release tag was created" >&2
        exit 1
    fi
    docker buildx imagetools inspect --raw "$image@$image_digest" >"$image_check_dir/manifest.json"
    image_architectures=$(jq -er '[.manifests[].platform | select(.os == "linux") | .architecture] | sort | join(",")' "$image_check_dir/manifest.json")
    expected_architectures=$(printf '%s\n' "$image_platforms" | tr ',' '\n' | sed 's#^linux/##' | sort | paste -sd, -)
    if [ "$image_architectures" != "$expected_architectures" ]; then
        echo "published platforms do not match $image_platforms; no release tag was created" >&2
        exit 1
    fi
    case ",$image_platforms," in
        *",$native_platform,"*)
            # Export the native platform from the same cached build into the
            # local daemon. Classic image stores cannot load a multi-platform
            # index or attestations, so keep those on the registry export.
            docker buildx build --builder "$image_builder" --platform "$native_platform" \
                --provenance=false --sbom=false --load --tag "$image" .
            native_arch=${native_platform#linux/}
            native_digest=$(jq -er --arg arch "$native_arch" '.manifests[] | select(.platform.os == "linux" and .platform.architecture == $arch) | .digest' "$image_check_dir/manifest.json")
            remote_config=$(docker buildx imagetools inspect --raw "$image@$native_digest" | jq -er '.config.digest')
            local_image_id=$(docker image inspect --format '{{.Id}}' "$image")
            # Classic stores identify images by config; containerd stores may
            # identify them by their platform manifest instead.
            if [ "$local_image_id" != "$remote_config" ] && [ "$local_image_id" != "$native_digest" ]; then
                echo "loaded image differs from published native image; no release tag was created" >&2
                exit 1
            fi
            echo "$image is available in the local Docker image store."
            ;;
        *) echo "No native platform selected; skipping local image load." ;;
    esac
    printf '%s %s\n' "$release_version" "$image_digest" >> release/image-digests.txt
    echo "Verified $image@$image_digest; GitHub will reuse this recorded image."
fi

perl -pi -e "s/\Q$current_version\E/$release_version/g" "$@"
perl -pi -e "s#// [0-9]+\.[0-9]+ Go CLI line#// $release_minor Go CLI line#" internal/config/config.go

if [ "$local_image" = true ]; then
    set -- "$@" release/image-digests.txt
fi
git add "$@"
git commit -m "release: prepare v$release_version"
git tag -a "v$release_version" -m "Release v$release_version"
git push origin main "v$release_version"
