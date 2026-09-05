# Codegenbox (Phase 3)

Codegenbox runs a coding agent in a disposable Docker container against a
self-contained Git session clone. Committed work is imported by the trusted
host process only after the container exits; uncommitted work retains its clone
for a safe later resume.

## Install

Codegenbox provides checksum-verified release binaries for macOS and Linux on
ARM64 and AMD64. Docker (or Colima on macOS) and Git must already be installed.

With Homebrew:

```sh
brew install atacan/tap/codegenbox
codegenbox version
```

Alternatively, install the release binary directly:

```sh
curl -fsSLO https://raw.githubusercontent.com/atacan/codegenbox/main/scripts/install.sh
less install.sh
sh install.sh
export PATH="$HOME/.local/bin:$PATH"
codegenbox version
```

Set `CODEGENBOX_INSTALL_DIR` to choose another binary directory. The installer
defaults to release `0.1.0`; set `CODEGENBOX_VERSION` to install a different
published version. The first agent run pulls the matching public development
image automatically.

## Use

```sh
cd /path/to/a/git/repository
codegenbox claude
codegenbox codex
codegenbox opencode
codegenbox sessions
codegenbox resume <session-id>
```

`codegenbox <agent>` and `codegenbox run <agent>` are equivalent. `sessions`
prints stable ID/agent/state/workspace/branch records without reading or
printing credentials. `resume` requires an explicit ID; it never accepts a
workspace path or silently chooses a session.

## Production image

The default image is `docker.io/atacandur/codegenbox:0.1.0`. It contains the
production development environment and installed agent CLIs, so adapters use
fixed direct commands instead of downloading an agent with `npx` at session
startup:

| Agent | Container command |
| --- | --- |
| Claude Code | `claude` |
| Codex | `codex --dangerously-bypass-approvals-and-sandbox` |
| OpenCode | `opencode` |

Codex's bypass flag applies only inside Codegenbox's existing container
boundary; it does not add host mounts or privileges. The published image index
contains `linux/arm64` and `linux/amd64` variants, allowing Apple Silicon to
pull the native ARM64 variant. The immutable `0.1.0` index digest is
`sha256:11b94307b51a73b485d97f826cc50a31958157bd564a1a2899f719a68ade6170`.

Set `CODEGENBOX_IMAGE` to use a compatible local, private, or test image:

```sh
CODEGENBOX_IMAGE=registry.example/codegenbox:test codegenbox codex
```

The default tag is publicly pullable from Docker Hub. Codegenbox does not
build or publish images during a normal session.

## Persistent agent state

Codegenbox gives each run a synthetic container home, `/home/agent`; it never
mounts host `$HOME`. State is mounted read-write because all three CLIs can
write login refresh data, settings, or conversation history.

| Agent | Host path assumption | Container path/environment | Override |
| --- | --- | --- | --- |
| Claude Code | `~/.claude` | `/home/agent/.claude`, `HOME=/home/agent` | `CODEGENBOX_CLAUDE_STATE_DIR` |
| Codex | `~/.codex` | `/home/agent/.codex`, `HOME`, `CODEX_HOME` | `CODEGENBOX_CODEX_STATE_DIR` |
| OpenCode | `$XDG_CONFIG_HOME/opencode` (or `~/.config/opencode`) and `$XDG_DATA_HOME/opencode` (or `~/.local/share/opencode`) | matching paths under `/home/agent`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME` | `CODEGENBOX_OPENCODE_CONFIG_DIR`, `CODEGENBOX_OPENCODE_DATA_DIR` |

Only the selected adapter's paths are created/mounted. An override must name
that direct agent-state directory; Codegenbox rejects host home, generic
config/state parents, known cross-agent defaults, commas, and mount collisions.
It has no user-controlled arbitrary mount flags.

The default Codegenbox storage root is `~/.local/share/codegenbox` (or
`$XDG_DATA_HOME/codegenbox`); set `CODEGENBOX_DATA_DIR` to override it. This
location and all state paths must be shared with Docker/Colima. Standard Colima
shares the home directory; macOS `/tmp` resolves to `/private/tmp`, which is
not shared by default.

## Security and lifecycle

The ordinary source mount is exactly the independent session clone at
`/workspace`, including its own `.git`. Selected agent state is the only
additional mount. Docker is run with `--rm`, interactive TTY, `--cap-drop ALL`,
and `no-new-privileges=true`; it has no Docker socket, `--privileged`, host
home, source-repository parent, source Git metadata, SSH, or GitHub write
credentials.

After Docker returns, Codegenbox validates and imports only the reserved
`codegenbox/<session-id>` branch if it is the recorded base commit or a
descendant. On resume, that branch is atomically advanced only from the
recorded imported commit. Main and unrelated refs are untouched. Dirty and
failed clones are never auto-deleted; committed work is still imported when a
clone remains dirty.

## Manual real-agent persistence check

Use a temporary Git repository and, one agent at a time, run the corresponding
Codegenbox command, complete its ordinary login (if needed), create a short
conversation, exit with an uncommitted edit, and run `codegenbox resume <id>`.
Verify that its own CLI offers/continues the prior conversation and login, then
commit or remove the edit and exit. Repeat for Claude, Codex, and OpenCode.
Check `codegenbox sessions` between runs; it must show no credentials. Do not
copy or print state files. The structural tests cover mount isolation; this
manual check is required because CI intentionally has no real credentials.
