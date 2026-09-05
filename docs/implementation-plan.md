# Codegenbox — Technical Implementation Plan

## 1. Purpose

Codegenbox is a local sandbox for running interactive coding agents such as:

- Claude Code
- OpenAI Codex
- OpenCode

The primary goals are:

1. Prevent coding agents from reading arbitrary files from the host Mac.
2. Give agents a normal Linux development environment with shell, `/tmp`, compilers, package managers, and build tooling.
3. Make build artifacts, caches, temporary files, dependencies, and experiments disposable.
4. Preserve intentional development work through Git.
5. Preserve coding-agent authentication, settings, and session history across runs.
6. Keep GitHub write credentials outside the sandbox.
7. Make sandbox usage nearly as convenient as running the agent directly.
8. Use Colima + Docker locally rather than a remote VM/VPS.
9. Publish the development image so normal usage never requires rebuilding it locally.

The normal user experience should eventually be:

```bash
cd ~/src/my-project
codegenbox claude
```

or:

```bash
codegenbox codex
codegenbox opencode
```

---

# 2. High-Level Architecture

Codegenbox consists of two independent deliverables.

## 2.1 Host CLI

A native Go CLI installed on macOS:

```text
codegenbox
```

The host CLI is responsible for:

- validating that the current directory belongs to a Git repository;
- creating Codegenbox session branches;
- creating temporary Git worktrees;
- starting Docker containers;
- constructing safe bind mounts;
- forwarding the interactive terminal;
- handling signals and crashes;
- inspecting the worktree after the agent exits;
- cleaning disposable worktrees;
- preserving dirty worktrees;
- resuming interrupted sessions;
- optionally pushing completed branches from the host;
- optionally opening a GitHub pull-request page.

The CLI runs outside the sandbox and is part of the trusted computing base.

## 2.2 Development Image

A Linux Docker image containing:

- common shell utilities;
- compilers and build tools;
- programming language toolchains;
- Claude Code;
- Codex;
- OpenCode.

Example image:

```text
docker.io/<namespace>/codegenbox:<version>
```

The image is:

- immutable;
- versioned;
- published automatically;
- reusable across all repositories;
- never repository-specific;
- never credential-specific.

Containers created from it are disposable.

---

# 3. Security Model

## 3.1 Primary Threat

A coding agent or a subprocess started by a coding agent may attempt to read files that are unrelated to the repository in which the user started the coding session.

Examples include:

```text
~/.ssh
~/.aws
~/.gnupg
~/Documents
~/Desktop
other source repositories
browser profiles
shell history
credential stores
application configuration
```

Codegenbox must prevent these files from being visible inside the container unless an individual path has been deliberately exposed.

## 3.2 Security Boundary

The basic rule is:

> If a host path was not explicitly mounted into the container, the coding agent must not be able to access it.

The only ordinary host filesystem mounts should be:

1. the temporary Codegenbox Git worktree;
2. explicitly configured state required by the selected coding agent.

No general home-directory mount is permitted.

## 3.3 Security Invariants

The implementation must preserve these invariants.

1. Never mount the host `$HOME` into an agent container.
2. Never mount the repository's parent directory merely for convenience.
3. Expose only the temporary worktree as `/workspace`.
4. Mount agent state explicitly and per agent.
5. Never expose `/var/run/docker.sock`.
6. Never run agent containers with `--privileged`.
7. Drop Linux capabilities unless a demonstrated toolchain requirement needs one.
8. Use `no-new-privileges`.
9. GitHub write credentials are not available inside the container by default.
10. Containers are always disposable.
11. Build caches are disposable by default.
12. Never automatically delete a dirty worktree.
13. A process crash or Ctrl-C must not cause uncommitted source changes to be deleted.
14. Host-side Git/PR actions happen only after the coding-agent process has terminated.
15. Authentication mounts for one coding agent should not automatically expose another agent's state.

---

# 4. Persistence Model

Codegenbox deliberately separates data into three lifetimes.

## 4.1 Permanent Host State

Persistent indefinitely:

```text
original Git repository
Git object database
Codegenbox session branches
Claude authentication/history/settings
Codex authentication/history/settings
OpenCode authentication/history/settings
host Git credentials
host GitHub credentials
```

## 4.2 Session Worktree

Persistent only while necessary:

```text
source checkout
source modifications
uncommitted files
project-local dependencies
node_modules
.venv
.build
target
dist
generated artifacts
```

A worktree may be deleted only when Codegenbox can prove that deleting it will not destroy uncommitted work.

## 4.3 Container State

Always disposable:

```text
/tmp
/var/tmp
/home/agent ephemeral state
system package caches
compiler caches
download caches
agent experiments
temporary scripts
container writable layer
```

When the container exits, this state disappears.

---

# 5. Git Worktree Model

## 5.0 Approved Phase 1 clarification — self-contained session clones

The linked-worktree design below cannot satisfy both normal in-container Git
commands and the security invariant that only the session workspace is mounted:
a linked worktree's `.git` file points to administrative data in the source
repository's Git directory, which is correctly unavailable in the container.

Therefore, Phase 1 uses a self-contained temporary **session clone** rather
than a linked Git worktree. The clone, including its ordinary `.git` directory,
lives entirely below Codegenbox-owned storage and is the sole bind mount at
`/workspace`. It is created without local-clone hardlinks or object alternates,
and its temporary source remote is removed before the agent starts.

The clone creates `codegenbox/<session-id>` locally at the recorded base
commit. After the agent/container has terminated, the trusted host process
validates and imports only that fixed ref into the original repository. The
commit must equal the recorded base or descend from it, and the host atomically
creates only the reserved session branch. Main, tags, and unrelated refs are
never updated by this reconciliation. A clean clone is removable only after a
verified import; a dirty or import-failed clone is always retained.

This clarification supersedes references to a *temporary worktree* in the
Phase 1 lifecycle, persistence, testing, and success criteria. The metadata
field named `worktree` remains a compatibility-oriented path field but denotes
the session workspace. Linked worktrees remain a possible future design only
if their Git-metadata exposure can be reconciled with the security model.

Codegenbox must not let the coding agent operate directly on the user's normal working tree.

Suppose the user runs:

```bash
cd ~/src/foo
codegenbox claude
```

and the current branch is:

```text
main -> A
```

Codegenbox creates a temporary session branch:

```text
codegenbox/20260903-193012-a82f
```

and an associated worktree.

Conceptually:

```text
Shared local Git repository
          │
    ┌─────┴─────────────┐
    │                   │
~/src/foo         temporary worktree
main              codegenbox/... branch
```

The temporary worktree is mounted into Docker as:

```text
/workspace
```

The agent can:

```bash
git status
git diff
git add
git commit
```

normally.

Commits made from the temporary worktree are stored in the same local Git object database as the original repository.

They therefore survive deletion of the temporary worktree without requiring any push to GitHub.

---

# 6. Worktree Location

Codegenbox should maintain its own host-side data directory.

Proposed default:

```text
~/.local/share/codegenbox/
```

with:

```text
~/.local/share/codegenbox/
├── sessions/
├── worktrees/
└── metadata/
```

Example:

```text
~/.local/share/codegenbox/worktrees/
└── foo-a82f/
```

The exact path should follow appropriate macOS/XDG conventions where practical.

Worktree locations must never be placed inside the user's source repository.

---

# 7. Session Lifecycle

## 7.1 Start

```text
codegenbox claude
      │
      ▼
validate Docker/Colima
      │
      ▼
identify Git repository
      │
      ▼
capture current HEAD/base branch
      │
      ▼
create Codegenbox branch
      │
      ▼
create temporary worktree
      │
      ▼
write session metadata
      │
      ▼
start disposable Docker container
      │
      ▼
run Claude interactively
```

## 7.2 Agent Running

The terminal is attached directly to:

```text
codegenbox
   └── docker run
          └── claude/codex/opencode
```

The user's normal agent TUI remains interactive.

`codegenbox` simply waits for `docker run` to terminate.

## 7.3 Exit

When the agent exits:

```text
coding agent exits
      │
      ▼
container exits
      │
      ▼
docker run returns
      │
      ▼
host Codegenbox process resumes
      │
      ▼
inspect Git worktree
```

## 7.4 Clean Worktree

If:

```bash
git status --porcelain
```

is empty:

- destroy the temporary worktree;
- remove the container via `--rm`;
- preserve the Codegenbox branch if it contains commits;
- summarize what happened.

Example:

```text
Codegenbox session complete.

Branch:
  codegenbox/auth-fix-a82f

Commits:
  3

Temporary workspace:
  removed

Container:
  removed
```

## 7.5 Dirty Worktree

If uncommitted changes remain:

- do not delete the worktree;
- do not attempt automatic cleanup;
- record the session as resumable;
- show the user its location.

Example:

```text
Codegenbox session stopped with uncommitted changes.

Workspace preserved:
~/.local/share/codegenbox/worktrees/foo-a82f

Resume:
codegenbox resume foo-a82f
```

This invariant applies even after:

- Ctrl-C;
- coding-agent crash;
- Docker failure;
- Codegenbox error.

---

# 8. Session Metadata

Every session should have a small metadata record.

JSON is sufficient initially.

Example:

```json
{
  "id": "foo-a82f",
  "repository": "/Users/user/src/foo",
  "worktree": "/Users/user/.local/share/codegenbox/worktrees/foo-a82f",
  "agent": "claude",
  "base_branch": "main",
  "base_commit": "abc123",
  "session_branch": "codegenbox/foo-a82f",
  "started_at": "2026-09-03T19:30:12+02:00",
  "state": "running"
}
```

Possible states:

```text
running
clean
dirty
completed
interrupted
orphaned
```

SQLite is unnecessary for V1.

---

# 9. Resume

Codegenbox must support interrupted sessions.

```bash
codegenbox resume foo-a82f
```

The command:

1. loads session metadata;
2. verifies the worktree still exists;
3. verifies Git metadata is intact;
4. determines the original agent;
5. starts a new disposable container;
6. mounts the same worktree;
7. mounts the same agent state;
8. launches the selected agent.

The old container is irrelevant.

The worktree is the persistent development state.

---

# 10. Coding-Agent Authentication and History

Codegenbox will intentionally reuse selected coding-agent state from macOS.

This is necessary to preserve:

- browser/OAuth authentication;
- refresh credentials;
- settings;
- previous conversations;
- session-resume metadata.

## 10.1 Per-Agent Adapters

Each agent gets a configuration module specifying:

```text
binary name
command
required state mounts
environment variables
optional callback ports
history/state paths
version check
```

Example conceptual definition:

```text
Claude:
    host state A -> container state A
    host state B -> container state B

Codex:
    host state C -> container state C

OpenCode:
    host state D -> container state D
```

Exact paths must be verified against current versions during implementation.

No adapter may simply mount `$HOME`.

## 10.2 Read/Write State

Authentication/history state should be mounted read-write only where required.

Where an agent can operate correctly with read-only credential material, prefer read-only.

However, OAuth token refresh frequently requires writes, so read-write access may be necessary for selected files/directories.

## 10.3 Agent History

History must survive container deletion because the relevant state lives on the host.

Session-resume behavior must be integration-tested for all three agents:

```text
Claude Code
Codex
OpenCode
```

The tests should verify not merely that history files survive, but that each CLI can actually discover and resume earlier sessions.

---

# 11. Docker Execution

Typical invocation:

```bash
docker run \
  --rm \
  -it \
  --workdir /workspace \
  --mount type=bind,src=<worktree>,dst=/workspace \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  <agent-specific mounts> \
  <image> \
  <agent>
```

Additional hardening should be evaluated without compromising ordinary development.

Potential options include:

```text
--pids-limit
--memory
--cpus
```

Resource limits should initially be optional because Colima itself already provides a VM-wide resource boundary.

---

# 12. Temporary Storage and Disk Cleanup

The disposable container is the primary cleanup mechanism.

We do not attempt to maintain a giant list of paths to clean.

Instead:

> Anything that does not need persistence should live either in the disposable container or disposable worktree.

Examples:

```text
/tmp
/var/tmp
npm cache
pnpm store
pip cache
uv cache
Cargo cache
Go build cache
Swift build cache
temporary agent scripts
```

The temporary worktree handles project-local garbage that tools insist on creating under the source tree:

```text
node_modules
.venv
.build
target
dist
coverage
```

Once the worktree is safe to delete, all of those disappear together.

---

# 13. Container Image

## 13.1 Base

Initial candidate:

```text
Ubuntu 24.04 LTS
```

The image must support:

```text
linux/arm64
linux/amd64
```

Apple Silicon machines should therefore run the ARM64 image without CPU emulation.

## 13.2 Common Utilities

Initial set:

```text
bash
zsh
git
git-lfs
openssh-client
curl
wget
ca-certificates
jq
yq
ripgrep
fd
tree
less
vim
nano
tmux
rsync
zip
unzip
tar
```

## 13.3 Build Tooling

```text
build-essential
clang
LLVM
cmake
ninja
pkg-config
```

## 13.4 Languages

V1:

```text
Node.js
npm
pnpm

Python
uv

Go

Rust
cargo

Swift for Linux
```

Versions should be explicit rather than implicitly tracking whatever package repository happens to contain during a rebuild.

## 13.5 Coding Agents

```text
Claude Code
Codex
OpenCode
```

Their versions should also be explicit or controlled through build arguments.

---

# 14. Image Versioning

Publish immutable version tags:

```text
codegenbox:0.1.0
codegenbox:0.2.0
```

Optionally:

```text
codegenbox:latest
```

The CLI should preferably use a specific compatible image version rather than blindly using `latest`.

For example:

```text
Codegenbox CLI 0.1.x
     ↓
default image codegenbox:0.1
```

Users should be able to override it:

```bash
CODEGENBOX_IMAGE=my/image:test codegenbox claude
```

---

# 15. Docker Hub Publishing

The repository will contain a GitHub Actions workflow that:

1. checks out the repository;
2. configures Docker Buildx;
3. authenticates to Docker Hub;
4. builds ARM64 and AMD64 images;
5. creates a multi-platform manifest;
6. pushes versioned tags;
7. optionally pushes `latest`.

Publishing should happen on Git tags:

```text
v0.1.0
v0.2.0
```

Credentials are stored as GitHub Actions secrets.

Normal users therefore execute:

```bash
docker pull <namespace>/codegenbox:0.1.0
```

rather than building locally.

---

# 16. CLI Design

Initial command hierarchy:

```text
codegenbox <agent>
codegenbox run <agent>

codegenbox resume [session]
codegenbox sessions
codegenbox inspect <session>
codegenbox clean
codegenbox doctor
codegenbox version
```

`codegenbox claude` should be an alias for:

```text
codegenbox run claude
```

Likewise:

```text
codegenbox codex
codegenbox opencode
```

---

# 17. `codegenbox doctor`

A diagnostic command should validate:

```text
Git installed
Docker CLI installed
Docker daemon reachable
Colima/Docker running
Git repository usable
configured image available
host architecture
coding-agent state paths
worktree storage writable
```

Example:

```text
$ codegenbox doctor

✓ Git
✓ Docker
✓ Colima
✓ linux/arm64 image
✓ Claude state
✓ Codex state
✓ OpenCode state
✓ worktree directory

Codegenbox is ready.
```

---

# 18. GitHub and Private Repositories

## 18.1 Current Repository

No GitHub credentials are necessary for the current repository because Codegenbox creates a worktree from the local Git repository.

## 18.2 GitHub Push

The coding agent receives no GitHub write credentials by default.

After the container exits, Codegenbox may offer:

```text
Push branch and open pull request? [y/N]
```

If accepted, the host Codegenbox process executes the push using the host's normal Git authentication.

The coding agent has already terminated at this point.

Security boundary:

```text
agent container
     │
     X terminated

host Codegenbox
     │
     ├── git push
     └── open/create PR
```

## 18.3 Pull Requests

V1 can simply push and open the GitHub compare page in the default macOS browser.

Later versions may use the host `gh` CLI:

```bash
gh pr create
```

This remains a host-side operation.

## 18.4 Private Dependencies

Projects may depend on other private repositories.

Examples:

```text
private Git submodules
private Swift packages
private npm Git dependencies
private Cargo Git dependencies
```

This requires separate read authentication.

The preferred design is authentication forwarding rather than mounting host SSH private keys.

Potential implementations include:

```text
SSH agent forwarding
restricted GitHub token
host credential proxy
```

This is an explicit implementation item and must be threat-modeled independently from GitHub push.

No host `~/.ssh` directory should be mounted wholesale.

---

# 19. Networking

V1 will permit ordinary outbound networking.

This is required for:

```text
LLM APIs
npm
PyPI
crates.io
Go modules
Swift packages
GitHub
other Git hosts
development APIs
```

This means Codegenbox V1 protects files outside the sandbox but does not prevent the agent from transmitting repository contents.

The threat model must document this clearly.

Future versions may add:

```text
network allowlists
HTTP/SOCKS proxy
DNS policy
per-session network disable
```

Possible future interface:

```bash
codegenbox claude --network=default
codegenbox claude --network=none
codegenbox claude --network=restricted
```

Network policy is explicitly out of scope for V1.

---

# 20. Signal Handling

The host CLI must correctly handle:

```text
SIGINT
SIGTERM
terminal close
Docker failure
agent crash
```

The priority order is:

1. terminate/stop the container;
2. inspect/preserve the worktree;
3. update session metadata;
4. never destroy dirty work;
5. return a meaningful exit status.

Signal handling is one of the reasons Codegenbox should be implemented in Go rather than growing into a complex shell script.

---

# 21. Go Implementation

Proposed package structure:

```text
cmd/
└── codegenbox/
    └── main.go

internal/
├── agent/
│   ├── agent.go
│   ├── claude.go
│   ├── codex.go
│   └── opencode.go
│
├── config/
│   └── config.go
│
├── container/
│   └── docker.go
│
├── git/
│   ├── repository.go
│   ├── branch.go
│   └── worktree.go
│
├── session/
│   ├── session.go
│   ├── metadata.go
│   └── lifecycle.go
│
├── host/
│   ├── browser.go
│   └── github.go
│
└── cli/
    └── ...
```

Prefer standard-library functionality where practical.

External dependencies should remain minimal.

---

# 22. Repository Layout

Proposed repository:

```text
codegenbox/
├── README.md
├── LICENSE
├── Dockerfile
├── .dockerignore
├── go.mod
├── go.sum
│
├── cmd/
│   └── codegenbox/
│
├── internal/
│   ├── agent/
│   ├── cli/
│   ├── config/
│   ├── container/
│   ├── git/
│   ├── host/
│   └── session/
│
├── docs/
│   ├── architecture.md
│   ├── security.md
│   └── development.md
│
├── scripts/
│   ├── install.sh
│   └── release.sh
│
├── tests/
│   └── fixtures/
│
└── .github/
    └── workflows/
        ├── test.yml
        ├── image.yml
        └── release.yml
```

---

# 23. Configuration

V1 should work without requiring a configuration file.

Optional user configuration:

```text
~/.config/codegenbox/config.yaml
```

Possible settings:

```yaml
image: docker.io/example/codegenbox:0.1.0

worktree_root: ~/.local/share/codegenbox/worktrees

default_agent: claude

cleanup:
  clean_worktrees: true

github:
  offer_push: true
  offer_pr: true
```

Security-sensitive mount configuration should be explicit and validated rather than permitting arbitrary implicit host mounts.

---

# 24. Image Updates

Codegenbox should not automatically rebuild anything locally.

Possible commands:

```bash
codegenbox image pull
codegenbox image version
```

Later:

```bash
codegenbox update
```

could update both CLI and compatible container image.

---

# 25. Testing Strategy

## 25.1 Unit Tests

Test:

```text
session ID generation
metadata serialization
branch naming
Git-state detection
command construction
mount construction
configuration
cleanup decisions
```

## 25.2 Git Integration Tests

Create temporary repositories and verify:

1. session branch is created;
2. worktree is created;
3. commits from worktree appear in main repository;
4. deleting worktree does not delete commits;
5. dirty worktree is preserved;
6. clean worktree is removed;
7. resume restores the correct worktree.

## 25.3 Docker Integration Tests

Verify:

```text
/workspace exists
host repository is writable
host home is invisible
/tmp is writable
container disappears after exit
generated files outside /workspace disappear
```

A security regression test should explicitly attempt:

```bash
cat ~/.ssh/...
cat ~/Documents/...
ls other host repository
```

and verify those locations are unavailable.

## 25.4 Agent Integration Tests

For each:

```text
Claude
Codex
OpenCode
```

verify:

1. CLI launches successfully;
2. authentication from host state works;
3. browser auth still works if reauthentication is required;
4. history survives container replacement;
5. previous sessions can be resumed;
6. terminal UI behaves correctly;
7. quitting returns control to Codegenbox.

## 25.5 Architecture Tests

CI should fail if Docker invocation accidentally gains:

```text
$HOME mount
Docker socket
--privileged
unexpected writable host directories
```

---

# 26. Initial Implementation Phases

## Phase 1 — Core Proof of Architecture

Implement:

```text
Go CLI skeleton
Git repository discovery
session branch creation in a self-contained session clone
temporary session-clone creation
Docker execution
interactive TTY
clean/dirty detection
safe cleanup
basic session metadata
one agent adapter
```

Success criterion:

> An agent can edit and commit code in a self-contained disposable session clone, exit, and leave only the validated Git commits on its Codegenbox branch in the original repository.

---

## Phase 2 — Agent Persistence

Add:

```text
Claude adapter
Codex adapter
OpenCode adapter
authentication mounts
history mounts
session-resume verification
codegenbox resume
codegenbox sessions
```

Success criterion:

> Containers can be deleted without losing agent authentication or conversation history.

---

## Phase 3 — Development Image

Build the production toolchain:

```text
Ubuntu
Node
pnpm
Python
uv
Go
Rust
Swift
common compilers
common CLI tools
all three coding agents
```

Publish:

```text
linux/arm64
linux/amd64
```

Success criterion:

> A fresh Mac/Colima installation can pull the image and start Codegenbox without building anything locally.

### Phase 3 delivery contract

Phase 3 replaces the Node proof image, not the isolation model implemented in
Phases 1 and 2. The image must be general-purpose and repository-independent;
it must not contain credentials, source code, session data, or a mechanism for
reaching the host Docker daemon.

The release image must meet all of the following requirements:

1. Use Ubuntu 24.04 LTS and publish one manifest covering `linux/arm64` and
   `linux/amd64`.
2. Provide the utilities, compilers, and language toolchains listed in sections
   13.2–13.4. Each ecosystem must have a practical smoke check, rather than
   only an installation step.
3. Expose the selected agent CLIs as installed commands named `claude`,
   `codex`, and `opencode`. The Phase 2 adapters must invoke those commands
   directly: agents must never download themselves with `npx` during a normal
   session.
4. Make all externally downloaded language/runtime and agent versions explicit
   Docker build arguments with conservative defaults. A release record must
   state the resolved values; changing one is an image release, not an
   untracked rebuild.
5. Create an unprivileged `agent` account with `/home/agent` as its default
   home. Codegenbox must launch the container as the validated UID:GID of its
   host process so host-owned workspace and selected state mounts remain
   writable without container-startup `chown` or `chmod`. The synthetic home
   must accommodate that mapped non-root user and remain compatible with the
   fixed Phase 2 state destinations. Image construction must not add a generic
   home mount, Docker socket, privilege, capability, or credential mechanism.
6. Keep the build context deliberately small. In particular, `.git`, local
   environment files, editor settings, credentials, build outputs, and
   Codegenbox session data must not enter an image layer.
7. Make a concrete, immutable release image the CLI default for the compatible
   0.1 CLI line. `CODEGENBOX_IMAGE` remains the deliberate escape hatch for
   local testing, private registries, and later compatible releases.

The image build must fail early when an installed tool is unavailable. The
release workflow must build a multi-platform manifest and must not permit a
pull-request build to push an image. Docker Hub namespace and authentication
are deployment configuration, not source-controlled secrets.

### Phase 3 work packages

The work is intentionally divided into non-overlapping deliverables:

| Package | Owned surface | Required result |
| --- | --- | --- |
| Production image | `Dockerfile`, `.dockerignore` | Pinned/toolchain-configurable Ubuntu image, non-root default runtime, direct installed agent commands, and small build context. |
| CLI adoption | `internal/agent`, `internal/config`, `internal/container` and their tests | Direct agent commands, a compatible immutable default image, and trusted host-UID runtime mapping with the existing mount/state boundary unchanged. |
| Publishing | `.github/workflows/image.yml` | Tag-gated, secret-backed Buildx release of an ARM64/AMD64 manifest and version tags; non-release runs never push. |
| Documentation and acceptance | README, development notes, Phase 3 checkpoint | Accurate image/runtime usage and an honest manual-release and security-verification checklist. |

Integration order is image → direct CLI adapters → workflow/documentation,
followed by a clean-tree review and the verification matrix below. An image is
not considered released merely because its Dockerfile builds locally.

### Phase 3 verification matrix

Before recording Phase 3 as complete, verify the published manifest and both
platform images. Perform the agent checks with empty, disposable state
directories; never print, copy, or commit real authentication material.

| Concern | Required evidence |
| --- | --- |
| Manifest | Registry inspection shows `linux/arm64` and `linux/amd64` for the immutable release tag. |
| Image contents | `claude --version`, `codex --version`, `opencode --version`, `node --version`, `pnpm --version`, `python3 --version`, `uv --version`, `go version`, `cargo --version`, `swift --version`, and representative compiler/tool commands succeed in a disposable container. |
| CLI integration | A fresh local repository reaches each direct agent command through Codegenbox with the production image; no `npx` download occurs. |
| Runtime identity | The image defaults to unprivileged `agent`; Codegenbox supplies only the validated invoking host UID:GID through Docker's `--user` option, with `HOME=/home/agent`. The process can write its owned source/state mounts without modifying their host ownership; `/workspace` is still supplied only by Codegenbox at run time. |
| Boundary regression | Docker command construction still has no host-home mount, Docker socket, `--privileged`, or unexpected writable mount. Agent-state mounts remain selected, explicit, and adapter-owned. |
| Release behavior | A `vX.Y.Z` tag produces only immutable `X.Y.Z`; the workflow refuses to overwrite an existing version. Pull requests and manual validation cannot publish unless the dispatcher explicitly enables publishing. |
| Pull-first experience | On a fresh Colima installation, `docker pull <release-image>` followed by `codegenbox <agent>` requires no local image build. |

---

## Phase 4 — Host GitHub Workflow

Implement:

```text
session summary
commit comparison
optional host-side push
open GitHub PR/compare page
optional host gh integration
```

Success criterion:

> The agent itself never needs GitHub write credentials.

---

## Phase 5 — Private Dependencies

Implement safe read access for private Git dependencies.

Evaluate:

```text
SSH-agent forwarding
GitHub token forwarding
credential proxy
```

Success criterion:

> Builds can fetch private dependencies without mounting SSH private-key files.

---

## Phase 6 — Hardening

Add:

```text
codegenbox doctor
resource limits
security regression tests
mount auditing
container command auditing
orphan-session recovery
better crash handling
image compatibility checking
```

---

# 27. Explicitly Out of Scope for V1

The following are intentionally deferred:

```text
macOS/iOS/Xcode sandboxing
Seatbelt backend
network allowlisting
remote agents
Kubernetes
multi-host execution
persistent dependency caches
automatic uncommitted-change commits
automatic merges into user's branch
Docker-in-Docker
host Docker socket access
GUI development tools
```

---

# 28. Definition of Done for V1

V1 is complete when this workflow is reliable:

```bash
cd ~/src/private-project
codegenbox claude
```

Codegenbox:

1. creates a session branch;
2. creates a temporary Git worktree;
3. starts a disposable Linux container;
4. exposes only that worktree plus deliberate Claude state;
5. allows Claude to work interactively;
6. preserves Claude authentication/history;
7. allows normal Git commits;
8. deletes container-local temporary data after exit;
9. deletes a clean worktree;
10. refuses to delete a dirty worktree;
11. leaves committed work safely accessible as a local branch;
12. optionally lets the trusted host process push/open a PR.

Equivalent workflows must work for:

```bash
codegenbox codex
codegenbox opencode
```

---

# 29. Core Design Principle

Codegenbox should stay small enough that its security model can be understood by reading the source.

The project should prefer structural isolation over cleanup heuristics:

> Persist only what we explicitly need. Make everything else disposable.

The three important persistence boundaries are therefore:

```text
Git
    → preserves development work

Agent host state
    → preserves authentication and conversation history

Everything else
    → disposable
```

That is the fundamental architecture around which the implementation should remain organized.
