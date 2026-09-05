# Development notes

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

The CLI default is `docker.io/atacandur/codegenbox:0.2.2`.
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

## Phase 5 private GitHub dependencies

Phase 5 deliberately does not forward `SSH_AUTH_SOCK`. Agent forwarding avoids
private-key files, but it cannot make an arbitrary SSH-agent key read-only or
limit the agent to Git dependency fetches: an agent can request signatures for
other SSH/GitHub operations and the key's server-side permissions determine the
result. A generic credential proxy has the same problem unless it terminates
and reimplements Git's HTTPS transport, which would be a much larger trusted
surface.

The initial mechanism is opt-in `CODEGENBOX_GITHUB_READ_TOKEN`. It must be a
separate fine-grained GitHub token limited to explicitly selected dependency
repositories with `Contents: Read-only` and no other permissions. The resolver
accepts only the `github_pat_` fine-grained-token prefix and a conservative
token character set, and never reads `$HOME`,
`~/.gitconfig`, `~/.ssh`, the host credential helper, or `gh` state. An absent
variable returns disabled authorization; an invalid value fails before Docker
starts.

`BuildRunInvocation` passes the token to the Docker client through that
client's per-process environment and uses `--env CODEGENBOX_GITHUB_READ_TOKEN`
without a value. Consequently the token is absent from the Docker argument
vector, metadata, mounts, and persistent files. Static in-container Git
environment configuration disables system/global config, installs a
`credential.https://github.com.helper`, and rewrites only the two standard
GitHub SSH URL forms to HTTPS. Those settings and the secret exist only for the
disposable agent process. They have no effect on trusted post-exit Git, push,
compare, or PR operations; those retain the Phase 4 host environment
scrubbing and never consume this token.

This is capability containment, not secrecy from an agent: a compromised agent
can read the token from its own environment and send it over the permitted
network. The required server-side fine-grained, selected-repository,
read-only scope limits the blast radius. Codegenbox cannot verify that remote
scope locally. GitHub Enterprise and non-GitHub private dependency hosts are
not supported. Do not add SSH forwarding, a broad token, arbitrary credential
helpers, token files, or a general-purpose mount in an attempt to cover them.

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

## Phase 6 hardening

The final Docker argv is audited after construction and before `docker run`.
It rejects privileged/host namespace settings, Docker socket and legacy-volume
syntax, unexpected mounts, and missing `--rm`, capability-drop, or
no-new-privileges settings. Optional PID, memory, and CPU limits are parsed
only from Codegenbox environment settings; agent input never supplies Docker
options.

`doctor` runs only Git/Docker version and info commands, image inspect, and a
private storage write probe. It never starts an agent or resolves agent state.
Image compatibility uses `io.codegenbox.compatibility=1`; unlabelled Phase 3
0.1 images remain supported, while a present incompatible label is rejected.

Running metadata records the owner PID plus a unique Docker container name.
At the next start/resume, a dead/absent PID is recovered only after Docker
confirms that recorded container is stopped or explicitly absent. A live,
running, or uninspectable container is never touched; legacy running records are retained
for manual inspection. Dirty clones remain preserved after abrupt host failure.
Resume and recovery acquire a per-session PID lock before changing metadata, so
simultaneous processes cannot start or finalize the same clone concurrently.

## Phase 4 host GitHub workflow

After every normal session return, the CLI reads the original source repository
on the host and prints its recorded base, reserved branch, imported commit,
new commit subjects, and `git diff --numstat` totals. Summary collection never
uses the agent-writable session clone.

`codegenbox push <id>` is deliberately separate from running an agent. It
loads validated metadata, reads the fixed `origin` push URL only from the
recorded source repository, and invokes Git directly (no shell) with exactly:

```text
git ... push --porcelain --no-verify -- <source-origin-push-url> \
  refs/heads/codegenbox/<id>:refs/heads/codegenbox/<id>
```

There is no force option, configured remote refspec, agent-provided branch,
clone remote, hook, or string-interpolated shell command in that path. The
source repository is validated as its own Git root before remote lookup. A
failed push leaves the generated local session branch and metadata intact.

`codegenbox compare <id>` recognizes only standard `github.com` HTTPS, SSH, or
SCP-style origin URLs and opens the generated compare page on the host. It is
an explicit action. `codegenbox pr <id>` uses a host-installed `gh` only after
an explicit request, passing fixed `--repo`, `--base`, `--head`, and `--fill`
arguments derived from validated metadata. Users must push first; Codegenbox
does not automatically merge or push as part of PR creation.

Do not loosen the Docker mount builder for this feature. Neither host Git
configuration, GitHub credentials, SSH private keys, nor writable GitHub/`gh`
state belongs in the container. The existing post-container lifecycle boundary
is mandatory for every host-side GitHub operation.

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

For a manual private-dependency acceptance test, first obtain explicit approval
to use a disposable repository and a dedicated read-only test token. Configure
a dependency via either HTTPS or a conventional `git@github.com:`/`ssh://git@
github.com/` URL, run one Codegenbox session with the environment variable, and
verify the fetch succeeds. Then inspect the Docker invocation through the
structural tests rather than printing the token, unset the variable, and verify
the same private fetch fails without authorization. Never test this with host
`gh` credentials, a personal SSH key, or a token with write access.

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
before announcing a later tag. The CLI publishing workflow then renders the
Homebrew formula from its published checksums and atomically updates
`atacan/homebrew-tap`. It requires the `HOMEBREW_TAP_GITHUB_TOKEN` Actions
secret: a fine-grained token restricted to that repository with only Contents
read/write permission.

When changing an OS, toolchain, or agent version, update the pinned build
input, compatible image tag, and documentation together. Rebuild both
architectures, repeat the direct-command smoke tests and mount-boundary
regressions, rerun the Go checks above, and record the immutable tag or digest
in release evidence. Never move the CLI default to an unverified image.
