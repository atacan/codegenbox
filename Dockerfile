# syntax=docker/dockerfile:1.7
#
# A multi-platform development image.  All downloads select TARGETARCH rather
# than the builder architecture, so buildx produces native amd64 and arm64
# images (rather than accidentally embedding an emulated host toolchain).

# Pin the official multi-platform Ubuntu 24.04 index so the same build inputs
# cannot silently select a different base layer. Refresh only as a deliberate
# image-release input after validating both architectures.
ARG UBUNTU_IMAGE=docker.io/library/ubuntu:24.04@sha256:33ceb71981b602c1a7443a53469e4dba065f7503eab3078a2d7a57a2ab987517
FROM ${UBUNTU_IMAGE}

ARG TARGETARCH
ARG BUILDARCH

ARG APT_PACKAGES="bash zsh git git-lfs openssh-client curl wget ca-certificates jq yq ripgrep fd-find tree less vim nano tmux rsync zip unzip tar xz-utils file gnupg2 build-essential clang llvm cmake ninja-build pkg-config binutils libc6-dev libcurl4-openssl-dev libedit2 libgcc-13-dev libncurses-dev libpython3-dev libsqlite3-0 libstdc++-13-dev libxml2-dev libz3-dev tzdata uuid-dev zlib1g-dev"

# Language tools are deliberately versioned build arguments.  Changing one of
# these invalidates only the layers which consume it.
ARG NODE_VERSION=22.18.0
ARG NPM_VERSION=10.9.3
ARG PNPM_VERSION=10.14.0
ARG PYTHON_VERSION=3.12.11
ARG UV_VERSION=0.7.20
ARG GO_VERSION=1.24.6
ARG RUST_VERSION=1.89.0
ARG RUSTUP_VERSION=1.28.2
ARG SWIFT_VERSION=6.1.2

# These are the vendor-published hashes for the architecture-specific Node and
# Go archives.  They are arguments so a version bump must also intentionally
# update its integrity value.
ARG NODE_SHA256_AMD64=c1bfeecf1d7404fa74728f9db72e697decbd8119ccc6f5a294d795756dfcfca7
ARG NODE_SHA256_ARM64=04fca1b9afecf375f26b41d65d52aa1703a621abea5a8948c7d1e351e85edade
ARG GO_SHA256_AMD64=bbca37cc395c974ffa4893ee35819ad23ebb27426df87af92e93a9ec66ef8712
ARG GO_SHA256_ARM64=124ea6033a8bf98aa9fbab53e58d134905262d45a022af3a90b73320f3c3afd5

# Agent CLIs are installed during the image build, never through npx at run
# time.  They can be refreshed independently with --build-arg.
ARG CLAUDE_CODE_VERSION=2.1.261
ARG CODEX_VERSION=0.153.4
ARG OPENCODE_VERSION=1.18.29

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    HOME=/home/agent \
    CODEX_HOME=/home/agent/.codex \
    XDG_CONFIG_HOME=/home/agent/.config \
    XDG_DATA_HOME=/home/agent/.local/share \
    CARGO_HOME=/opt/rust/cargo \
    RUSTUP_HOME=/opt/rust/rustup \
    UV_PYTHON_INSTALL_DIR=/opt/uv/python \
    PATH=/opt/swift/usr/bin:/opt/go/bin:/opt/rust/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# The base image is pinned above. Ubuntu's ARM64 ports archive does not expose
# the snapshot metadata needed for a portable APT snapshot pin, so this layer
# uses Canonical's signed current archive. Capture resolved package versions in
# release evidence before promoting a rebuilt image. fd-find is exposed as fd
# to match its normal command name elsewhere.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ${APT_PACKAGES} \
    && ln -s /usr/bin/fdfind /usr/local/bin/fd \
    && rm -rf /var/lib/apt/lists/*

# Install Node from nodejs.org and verify its architecture-specific SHA-256.
RUN case "${TARGETARCH}" in \
        amd64) node_arch=x64; node_sha256="${NODE_SHA256_AMD64}" ;; \
        arm64) node_arch=arm64; node_sha256="${NODE_SHA256_ARM64}" ;; \
        *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && node_archive="node-v${NODE_VERSION}-linux-${node_arch}.tar.xz" \
    && curl --fail --location --silent --show-error "https://nodejs.org/dist/v${NODE_VERSION}/${node_archive}" -o /tmp/node.tar.xz \
    && echo "${node_sha256}  /tmp/node.tar.xz" | sha256sum --check --strict \
    && tar -xJf /tmp/node.tar.xz -C /usr/local --strip-components=1 \
    && rm /tmp/node.tar.xz \
    && node --version | grep --fixed-strings --quiet "v${NODE_VERSION}"

# uv publishes prebuilt glibc binaries for both supported architectures.  It
# then installs the requested CPython release into /opt, not root's home.
RUN case "${TARGETARCH}" in \
        amd64) uv_arch=x86_64-unknown-linux-gnu ;; \
        arm64) uv_arch=aarch64-unknown-linux-gnu ;; \
        *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && uv_archive="uv-${uv_arch}.tar.gz" \
    && curl --fail --location --silent --show-error "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/${uv_archive}" -o /tmp/uv.tar.gz \
    && tar -xzf /tmp/uv.tar.gz -C /tmp \
    && install -m 0755 "/tmp/uv-${uv_arch}/uv" /usr/local/bin/uv \
    && install -m 0755 "/tmp/uv-${uv_arch}/uvx" /usr/local/bin/uvx \
    && rm -rf /tmp/uv.tar.gz "/tmp/uv-${uv_arch}" \
    && uv --version | grep --fixed-strings --quiet "${UV_VERSION}" \
    && uv python install "${PYTHON_VERSION}" \
    && python_binary="$(find /opt/uv/python -type f -name "python${PYTHON_VERSION%.*}" -print -quit)" \
    && test -n "${python_binary}" \
    && ln -s "${python_binary}" /usr/local/bin/python3 \
    && ln -s /usr/local/bin/python3 /usr/local/bin/python \
    && python3 --version | grep --fixed-strings --quiet "Python ${PYTHON_VERSION}"

# Go distributions are architecture-specific and authenticated with the
# upstream checksums published at go.dev/dl.
RUN case "${TARGETARCH}" in \
        amd64) go_arch=amd64; go_sha256="${GO_SHA256_AMD64}" ;; \
        arm64) go_arch=arm64; go_sha256="${GO_SHA256_ARM64}" ;; \
        *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && go_archive="go${GO_VERSION}.linux-${go_arch}.tar.gz" \
    && curl --fail --location --silent --show-error "https://go.dev/dl/${go_archive}" -o /tmp/go.tar.gz \
    && echo "${go_sha256}  /tmp/go.tar.gz" | sha256sum --check --strict \
    && tar -xzf /tmp/go.tar.gz -C /opt \
    && rm /tmp/go.tar.gz \
    && go version | grep --fixed-strings --quiet "go${GO_VERSION}"

# Install the pinned Rust toolchain using the official rust-lang bootstrap
# binary for the target architecture.  No shell installer is executed.
RUN case "${TARGETARCH}" in \
        amd64) rust_target=x86_64-unknown-linux-gnu ;; \
        arm64) rust_target=aarch64-unknown-linux-gnu ;; \
        *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && curl --fail --location --silent --show-error "https://static.rust-lang.org/rustup/archive/${RUSTUP_VERSION}/${rust_target}/rustup-init" -o /tmp/rustup-init \
    && chmod 0755 /tmp/rustup-init \
    && /tmp/rustup-init -y --profile minimal --default-toolchain "${RUST_VERSION}" --no-modify-path \
    && rm /tmp/rustup-init \
    && rustc --version | grep --fixed-strings --quiet "${RUST_VERSION}" \
    && cargo --version | grep --fixed-strings --quiet "${RUST_VERSION}"

# Swift's vendor tarballs are signed.  Import the project's published signing
# keys and verify the detached signature before putting the toolchain on PATH.
RUN case "${TARGETARCH}" in \
        amd64) swift_platform=ubuntu24.04; swift_directory=ubuntu2404 ;; \
        arm64) swift_platform=ubuntu24.04-aarch64; swift_directory=ubuntu2404-aarch64 ;; \
        *) echo "unsupported target architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
    && swift_release="swift-${SWIFT_VERSION}-RELEASE" \
    && swift_archive="${swift_release}-${swift_platform}.tar.gz" \
    && swift_url="https://download.swift.org/swift-${SWIFT_VERSION}-release/${swift_directory}/${swift_release}/${swift_archive}" \
    && curl --fail --location --silent --show-error "${swift_url}" -o /tmp/swift.tar.gz \
    && curl --fail --location --silent --show-error "${swift_url}.sig" -o /tmp/swift.tar.gz.sig \
    && curl --compressed --fail --location --silent --show-error https://www.swift.org/keys/all-keys.asc -o /tmp/swift-keys.asc \
    && export GNUPGHOME=/tmp/swift-gnupg \
    && install -d --mode=0700 "${GNUPGHOME}" \
    && gpg --batch --import /tmp/swift-keys.asc \
    && gpg --batch --verify /tmp/swift.tar.gz.sig /tmp/swift.tar.gz \
    && mkdir -p /opt/swift \
    && tar -xzf /tmp/swift.tar.gz -C /opt/swift --strip-components=1 \
    && rm -rf /tmp/swift.tar.gz /tmp/swift.tar.gz.sig /tmp/swift-keys.asc /tmp/swift-gnupg \
    && swift --version | grep --fixed-strings --quiet "Swift version ${SWIFT_VERSION}"

# Pin npm and pnpm before installing the agent packages.  npm runs the
# OpenCode package's required postinstall hook, selecting the matching native
# opencode binary while leaving all three launchers directly on PATH.
RUN npm install --global --no-audit --no-fund "npm@${NPM_VERSION}" "pnpm@${PNPM_VERSION}" \
    && npm --version | grep --fixed-strings --quiet "${NPM_VERSION}" \
    && pnpm --version | grep --fixed-strings --quiet "${PNPM_VERSION}" \
    && npm install --global --no-audit --no-fund "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" "@openai/codex@${CODEX_VERSION}" "opencode-ai@${OPENCODE_VERSION}" \
    && command -v claude \
    && command -v codex \
    && command -v opencode \
    && npm cache clean --force

# Ensure every tool is present for the target platform.  Full executable smoke
# tests run only on a native builder: QEMU can itself crash otherwise-valid
# target binaries, as it is an emulator rather than the release runtime. CI
# must exercise each target on a matching native runner before release.
RUN command -v claude \
    && command -v codex \
    && command -v opencode \
    && command -v node \
    && command -v python3 \
    && command -v go \
    && command -v rustc \
    && command -v cc \
    && command -v c++ \
    && command -v swiftc \
    && if [ "${TARGETARCH}" = "${BUILDARCH}" ]; then \
        claude --version \
        && codex --version \
        && opencode --version \
        && node --input-type=module --eval 'if (40 + 2 !== 42) process.exit(1)' \
        && python3 -c 'assert 40 + 2 == 42' \
        && install -d /tmp/codegenbox-smoke \
        && printf 'package main\nfunc main() {}\n' > /tmp/codegenbox-smoke/main.go \
        && go run /tmp/codegenbox-smoke/main.go \
        && printf 'fn main() {}\n' > /tmp/codegenbox-smoke/main.rs \
        && rustc /tmp/codegenbox-smoke/main.rs -o /tmp/codegenbox-smoke/rust-smoke \
        && /tmp/codegenbox-smoke/rust-smoke \
        && printf '#include <stdio.h>\nint main(void) { return 0; }\n' > /tmp/codegenbox-smoke/main.c \
        && cc /tmp/codegenbox-smoke/main.c -o /tmp/codegenbox-smoke/c-smoke \
        && /tmp/codegenbox-smoke/c-smoke \
        && printf '#include <iostream>\nint main() { return 0; }\n' > /tmp/codegenbox-smoke/main.cc \
        && c++ /tmp/codegenbox-smoke/main.cc -o /tmp/codegenbox-smoke/cpp-smoke \
        && /tmp/codegenbox-smoke/cpp-smoke \
        && printf 'print("ok")\n' > /tmp/codegenbox-smoke/main.swift \
        && swiftc /tmp/codegenbox-smoke/main.swift -o /tmp/codegenbox-smoke/swift-smoke \
        && /tmp/codegenbox-smoke/swift-smoke \
        && rm -rf /tmp/codegenbox-smoke; \
    else \
        echo "cross-build (${BUILDARCH} -> ${TARGETARCH}): executable smoke deferred to native CI"; \
    fi

# The CLI mounts only per-agent state below this synthetic home. Remove caches
# created by build-time smoke tests before constructing it: Codegenbox runs the
# image as the invoking host UID:GID, which deliberately differs from the
# image's fixed agent UID:GID. Writable synthetic-home directories therefore
# use sticky world-writable permissions. There is only one unprivileged user in
# a Codegenbox container, and the host state mounts retain their host ownership
# and permissions when mounted over these empty destinations.
RUN groupadd --gid 10001 agent \
    && useradd --uid 10001 --gid agent --create-home --home-dir /home/agent --shell /bin/bash agent \
    && rm -rf /home/agent \
    && install -d --owner=agent --group=agent --mode=0755 /workspace /home/agent/.claude /home/agent/.codex /home/agent/.config/opencode /home/agent/.local/share/opencode \
    && find /home/agent -type d -exec chmod 1777 {} +

# System toolchains stay immutable under /opt. Package-manager caches, user
# installs, and generated configuration belong to the mapped runtime user and
# therefore resolve below the writable synthetic home.
ENV CARGO_HOME=/home/agent/.cargo \
    UV_PYTHON_INSTALL_DIR=/home/agent/.local/share/uv/python \
    NPM_CONFIG_CACHE=/home/agent/.cache/npm \
    PNPM_HOME=/home/agent/.local/share/pnpm \
    PATH=/home/agent/.local/bin:/home/agent/.local/share/pnpm:/home/agent/.cargo/bin:/home/agent/go/bin:/opt/swift/usr/bin:/opt/go/bin:/opt/rust/cargo/bin:/usr/local/bin:/usr/local/sbin:/usr/sbin:/usr/bin:/sbin:/bin

USER agent
WORKDIR /workspace

CMD ["bash"]
