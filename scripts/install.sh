#!/bin/sh
set -eu

install_version=${CODEGENBOX_VERSION:-0.1.0}
install_repository=${CODEGENBOX_GITHUB_REPOSITORY:-atacan/codegenbox}

if ! printf '%s\n' "$install_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "CODEGENBOX_VERSION must be stable SemVer: MAJOR.MINOR.PATCH" >&2
    exit 1
fi

case $(uname -s) in
    Darwin) install_os=darwin ;;
    Linux) install_os=linux ;;
    *)
        echo "codegenbox supports macOS and Linux hosts" >&2
        exit 1
        ;;
esac

case $(uname -m) in
    arm64 | aarch64) install_arch=arm64 ;;
    x86_64 | amd64) install_arch=amd64 ;;
    *)
        echo "codegenbox supports ARM64 and AMD64 hosts" >&2
        exit 1
        ;;
esac

if [ -z "${HOME:-}" ]; then
    echo "HOME is required to select the default installation directory" >&2
    exit 1
fi

install_dir=${CODEGENBOX_INSTALL_DIR:-"$HOME/.local/bin"}
install_archive="codegenbox_${install_version}_${install_os}_${install_arch}.tar.gz"
install_default_url="https://github.com/${install_repository}/releases/download/v${install_version}"
install_base_url=${CODEGENBOX_DOWNLOAD_BASE_URL:-$install_default_url}
install_tmp=$(mktemp -d "${TMPDIR:-/tmp}/codegenbox-install.XXXXXX")
trap 'rm -rf "$install_tmp"' EXIT HUP INT TERM

curl --fail --location --silent --show-error \
    "$install_base_url/$install_archive" -o "$install_tmp/$install_archive"
curl --fail --location --silent --show-error \
    "$install_base_url/checksums.txt" -o "$install_tmp/checksums.txt"

install_expected=$(awk -v archive="$install_archive" '$2 == archive { print $1 }' "$install_tmp/checksums.txt")
if [ -z "$install_expected" ]; then
    echo "release checksum is missing for $install_archive" >&2
    exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
    install_actual=$(sha256sum "$install_tmp/$install_archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    install_actual=$(shasum -a 256 "$install_tmp/$install_archive" | awk '{ print $1 }')
else
    echo "sha256sum or shasum is required" >&2
    exit 1
fi

if [ "$install_actual" != "$install_expected" ]; then
    echo "checksum verification failed for $install_archive" >&2
    exit 1
fi

tar -C "$install_tmp" -xzf "$install_tmp/$install_archive"
mkdir -p "$install_dir"
install -m 0755 "$install_tmp/codegenbox" "$install_dir/codegenbox"

echo "Installed codegenbox $install_version to $install_dir/codegenbox"
case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *) echo "Add $install_dir to PATH before running codegenbox." ;;
esac
echo "Next: verify Docker is running, then run 'codegenbox claude'."
