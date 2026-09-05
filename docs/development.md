# Phase 3 development notes

## Adapter contract

Each adapter owns a fixed command, selected-only state locations, and a fixed
synthetic-home environment. The container builder accepts adapter-derived
mounts only and revalidates agent name, destination allowlist, duplicate and
`/workspace` collisions, comma/NUL injection, host-home use, workspace/source
collisions, and the Docker socket. It never accepts a user mount flag.

| Adapter | Production-image command | State mounts | Environment |
| --- | --- | --- | --- |
| Claude | `claude` | host `~/.claude` → `/home/agent/.claude` | `HOME=/home/agent` |
| Codex | `codex --dangerously-bypass-approvals-and-sandbox` | host `~/.codex` → `/home/agent/.codex` | `HOME=/home/agent`, `CODEX_HOME=/home/agent/.codex` |
| OpenCode | `opencode` | host XDG config/data `opencode` children → matching `/home/agent` XDG children | `HOME`, `XDG_CONFIG_HOME=/home/agent/.config`, `XDG_DATA_HOME=/home/agent/.local/share` |

The production image replaces the Node proof image. It contains Ubuntu 24.04
LTS, common interactive/source tools, C/C++ build tooling, Node/npm/pnpm,
Python/uv, Go, Rust/cargo, Swift for Linux, and installed Claude Code, Codex,
and OpenCode. Keep OS, toolchain, and agent versions explicit in Docker build
inputs. Normal agent startup must not run `npx`, install a package, or depend
on a package registry; the adapter commands above execute installed binaries.

The CLI default is `docker.io/atacandur/codegenbox:0.1.0`.
`CODEGENBOX_IMAGE` remains the only supported image override for a compatible
local, private, or test image; it must not alter the fixed command or mount
contract. The published OCI index contains `linux/arm64` and `linux/amd64`
manifests. Its release evidence and resolved Ubuntu package inventory are in
the Phase 3 checkpoint.

For an unusual installation, override only the required direct agent state
location with `CODEGENBOX_CLAUDE_STATE_DIR`, `CODEGENBOX_CODEX_STATE_DIR`,
`CODEGENBOX_OPENCODE_CONFIG_DIR`, or `CODEGENBOX_OPENCODE_DATA_DIR`. Overrides
do not permit host home, generic XDG parents, commas, or known another-agent
default state. Missing selected directories are created mode 0700; existing
directories are not chmodded or read by Codegenbox.

All state mounts are read-write: the CLIs can persist OAuth refresh state,
configuration, and history. The only ordinary source mount remains the
self-contained clone at `/workspace`. Never add host home, a repository parent,
the Docker socket, privileged mode, or GitHub write credentials while changing
the image; retain dropped capabilities and `no-new-privileges`.

The image defaults to an unprivileged `agent` account, but Codegenbox passes
Docker a validated `--user <host-uid>:<host-gid>` derived only from its own
process. This lets the non-root container process write the host-owned session
clone and selected state directories without changing their host ownership or
permissions. It is not user configuration and must not become an arbitrary
Docker user flag. Codegenbox rejects zero UID or GID, so do not run it with
`sudo`.

## Session lifecycle and resume

`codegenbox sessions` reads JSON metadata files in deterministic ID order and
prints only ID, agent, state, workspace presence, and reserved branch. Invalid
or corrupt metadata produces an error rather than being ignored.

`codegenbox resume <id>` requires an explicit branch-safe ID. It validates the
metadata ID/branch/repository, canonical data-root containment, exact expected
session path, ordinary self-contained `.git` layout (no alternates, linked
common directory, or symlinks), and recorded adapter before a new disposable
container starts. A missing, corrupt, or unsafe clone is never cleaned up.

After every run, including a resumed run, host Git inspects status only after
the runner returns. It imports the fixed reserved clone ref only if it descends
from the recorded base. First import atomically creates the branch; later
imports atomically advance that same branch only from `ImportedCommit`. A CAS
failure, source mismatch, or dirty clone preserves the session clone. Committed
work imports even with remaining dirty files.

## Colima and manual verification

`CODEGENBOX_DATA_DIR` and selected state paths must be bind-shareable by the
chosen Docker/Colima VM. The default home locations work with standard Colima;
macOS `/tmp` (`/private/tmp`) is not shared by default.

For each of Claude, Codex, and OpenCode, use a temporary Git repository, run
the adapter, authenticate normally if needed, create a conversation, leave a
small uncommitted edit, then use the exact ID from `codegenbox sessions` with
`codegenbox resume`. Confirm the agent discovers the previous history/auth,
then commit or remove the edit and exit. Inspect `sessions` only; never print,
copy, or add test tokens/state files to source control.

Run the validation suite with:

```sh
GOCACHE=/private/tmp/codegenbox-go-cache go test ./...
GOCACHE=/private/tmp/codegenbox-go-cache go test -race ./...
GOCACHE=/private/tmp/codegenbox-go-cache go vet ./...
GOCACHE=/private/tmp/codegenbox-go-cache go build ./cmd/codegenbox
```

## Image build, verification, and publishing

Build the production image for `linux/arm64` and `linux/amd64`, then run each
direct adapter command in its applicable image variant. A representative local
smoke test for one platform uses a compatible local tag (replace `arm64` with
`amd64` where appropriate):

```sh
docker buildx build --platform linux/arm64 --load -t codegenbox:dev .
CODEGENBOX_IMAGE=codegenbox:dev codegenbox claude
CODEGENBOX_IMAGE=codegenbox:dev codegenbox codex
CODEGENBOX_IMAGE=codegenbox:dev codegenbox opencode
```

The Dockerfile runs its executable compiler/runtime smoke suite only when the
target matches the builder architecture. A cross-build still verifies that the
target-platform launchers and tools are installed, but QEMU is not a reliable
substitute for a native runtime. The publishing workflow validates AMD64 on
`ubuntu-24.04` and ARM64 on `ubuntu-24.04-arm` before its registry-backed
multi-platform build can run.

During each smoke session, also confirm the mapped non-root process can create
and remove a disposable file in `/workspace` and update only its selected
state directory. Do not test this with real credentials.

Use a registry-backed `--push` build or the publishing workflow to create the
multi-platform manifest; `--load` is for a single-platform local image. After
a push, inspect the manifest with:

```sh
docker buildx imagetools inspect <image:tag>
```

The publishing workflow requires the `DOCKERHUB_IMAGE` Actions variable (set
to `atacandur/codegenbox`) plus `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN`
secrets. Pull requests and manual dispatches validate both architectures
without credentials or a push; manual publishing requires its explicit
`publish` input. A `v0.1.0` tag publishes only the immutable `0.1.0` tag. The
workflow skips a version only when the existing registry digest exactly
matches `release/image-digests.txt`; it refuses every other overwrite. Verify
the successful authenticated workflow run, pull access, and both manifests
before announcing a later tag.

When changing an OS, toolchain, or agent version, update the pinned build
input, compatible image tag, and documentation together. Rebuild both
architectures, repeat the direct-command smoke tests and mount-boundary
regressions, rerun the Go checks above, and record the immutable tag or digest
in release evidence. Never move the CLI default to an unverified image.
