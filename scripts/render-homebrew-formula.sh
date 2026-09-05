#!/bin/sh
set -eu

if [ "$#" -ne 3 ]; then
    echo "usage: scripts/render-homebrew-formula.sh <version> <checksums-file> <output-file>" >&2
    exit 2
fi

formula_version=$1
formula_checksums=$2
formula_output=$3

if ! printf '%s\n' "$formula_version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "version must be stable SemVer: MAJOR.MINOR.PATCH" >&2
    exit 2
fi

if [ ! -f "$formula_checksums" ]; then
    echo "checksums file does not exist: $formula_checksums" >&2
    exit 2
fi

formula_checksum() {
    formula_archive=$1
    formula_value=$(awk -v archive="$formula_archive" '$2 == archive { print $1 }' "$formula_checksums")
    if ! printf '%s\n' "$formula_value" | grep -Eq '^[0-9a-f]{64}$'; then
        echo "missing or invalid SHA-256 for $formula_archive" >&2
        exit 2
    fi
    printf '%s\n' "$formula_value"
}

formula_darwin_arm64=$(formula_checksum "codegenbox_${formula_version}_darwin_arm64.tar.gz")
formula_darwin_amd64=$(formula_checksum "codegenbox_${formula_version}_darwin_amd64.tar.gz")
formula_linux_arm64=$(formula_checksum "codegenbox_${formula_version}_linux_arm64.tar.gz")
formula_linux_amd64=$(formula_checksum "codegenbox_${formula_version}_linux_amd64.tar.gz")

formula_stage=$(mktemp "${TMPDIR:-/tmp}/codegenbox-formula.XXXXXX")
trap 'rm -f "$formula_stage"' EXIT HUP INT TERM

cat >"$formula_stage" <<EOF
class Codegenbox < Formula
  desc "Run coding agents safely in disposable Docker containers"
  homepage "https://github.com/atacan/codegenbox"

  on_macos do
    on_arm do
      url "https://github.com/atacan/codegenbox/releases/download/v${formula_version}/codegenbox_${formula_version}_darwin_arm64.tar.gz"
      sha256 "${formula_darwin_arm64}"
    end

    on_intel do
      url "https://github.com/atacan/codegenbox/releases/download/v${formula_version}/codegenbox_${formula_version}_darwin_amd64.tar.gz"
      sha256 "${formula_darwin_amd64}"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/atacan/codegenbox/releases/download/v${formula_version}/codegenbox_${formula_version}_linux_arm64.tar.gz"
      sha256 "${formula_linux_arm64}"
    end

    on_intel do
      url "https://github.com/atacan/codegenbox/releases/download/v${formula_version}/codegenbox_${formula_version}_linux_amd64.tar.gz"
      sha256 "${formula_linux_amd64}"
    end
  end

  def install
    bin.install "codegenbox"
  end

  test do
    assert_match "codegenbox #{version}", shell_output("#{bin}/codegenbox --version")
  end
end
EOF

mkdir -p "$(dirname "$formula_output")"
mv "$formula_stage" "$formula_output"
