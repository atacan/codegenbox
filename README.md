# Codegenbox

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
defaults to release `0.2.1`; set `CODEGENBOX_VERSION` to install a different
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
codegenbox push <session-id>
codegenbox compare <session-id>
codegenbox pr <session-id>
codegenbox doctor
```

`codegenbox <agent>` and `codegenbox run <agent>` are equivalent. `sessions`
prints stable ID/agent/state/workspace/branch records without reading or
printing credentials. `resume` requires an explicit ID; it never accepts a
workspace path or silently chooses a session.

## Host GitHub workflow

Once a container has stopped, Codegenbox prints a host-side summary from the
original repository: repository, recorded base branch and commit, reserved
session branch, imported commit, commit subjects, and changed-file totals.
The summary is informational; it never pushes, opens a browser, creates a pull
request, or merges anything automatically.

After reviewing the summary, push only the generated branch explicitly:

```sh
codegenbox push <session-id>
```

The command reads only `origin`'s push URL from the original source repository
and pushes the fixed refspec
`refs/heads/codegenbox/<session-id>:refs/heads/codegenbox/<session-id>` without
force mode. It does not use the retained session clone, update another branch
or tag, or merge into your current branch. A local/non-GitHub `origin` can be
pushed, but browser and PR helpers require a standard `github.com` remote.

For a GitHub source remote, open the compare page after pushing:

```sh
codegenbox compare <session-id>
```

This prints the exact compare URL and asks the host desktop to open it. To
create a PR through an authenticated host `gh` installation, push first, then
run:

```sh
codegenbox pr <session-id>
```

`pr` invokes `gh pr create --repo owner/repo --base <recorded-base> --head
codegenbox/<session-id> --fill`; it never runs in the container and does not
ask `gh` to push. If `gh` is missing, unauthenticated, or rejects the request,
the source branch and Codegenbox metadata remain unchanged.

## Production image

The default image is `docker.io/atacandur/codegenbox:0.2.1`. It contains the
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
pull the native ARM64 variant. Each release image is immutable and published
under its matching CLI version.

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

## Private Git dependencies

Private GitHub dependencies are opt-in. For a session that needs them, provide
a separate GitHub **fine-grained** personal access token restricted to the
specific dependency repositories, with only repository `Contents: Read-only`.
Do not reuse the credential used by host Git or `gh`, and do not grant any
write, administration, workflow, or organization permission. Codegenbox
accepts only fine-grained-token-shaped values beginning `github_pat_`; it
rejects classic and OAuth-style tokens.

```sh
export CODEGENBOX_GITHUB_READ_TOKEN='github_pat_...'
codegenbox codex
unset CODEGENBOX_GITHUB_READ_TOKEN
```

The token is forwarded only to the disposable Docker process, not written to a
file, Docker command argument, Codegenbox metadata, agent state, or Git
configuration. Git is configured in that container process only to supply the
token for HTTPS `github.com` fetches; conventional GitHub SSH dependency URLs
are rewritten to HTTPS. No SSH agent, SSH directory/key, host Git config,
`gh` state, or credential helper is mounted. With the variable absent, this
feature is disabled and the ordinary Phase 1–4 mount boundary is unchanged.

The container can read and exfiltrate this token, so it is not a defense
against a malicious agent; the token's GitHub-enforced repository selection and
read-only permission are the containment. Codegenbox validates only its safe
syntax, not its server-side scope. GitHub Enterprise and non-GitHub private
hosts are intentionally unsupported in this initial implementation.

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

GitHub credentials, SSH private keys, host Git configuration, and writable
GitHub authentication state are never mounted into the container. The only
exception is the explicitly supplied, separate, read-only private-dependency
token described above; it is not a host GitHub credential and never enables
GitHub writes. Host Git and `gh` operations occur only through the explicit
commands above, after the agent container has terminated.

## Hardening and diagnostics

`codegenbox doctor` checks Git, the Docker CLI and daemon, the configured
image compatibility marker, and Codegenbox storage writability. It does not
start a container or inspect agent state/credentials.

Optional container limits use `CODEGENBOX_PIDS_LIMIT`,
`CODEGENBOX_MEMORY_LIMIT` (for example `2g`), and `CODEGENBOX_CPUS_LIMIT`
(for example `1.5`). Unset values retain Docker/Colima defaults.

The final Docker command is audited before execution: it must retain its fixed
workspace/selected-state mounts, `--rm`, dropped capabilities, and
no-new-privileges; privileged mode, host network/PID namespaces, Docker
socket, and legacy volume flags are rejected. New images carry
`io.codegenbox.compatibility=1`; existing unlabelled 0.1 images remain
supported, while a present incompatible marker is rejected. Dead `running`
session records are recovered only after the recorded named container is
confirmed stopped or explicitly reported absent; Docker inspection failures
leave the clone untouched. Dirty clones are retained.

## Manual real-agent persistence check

Use a temporary Git repository and, one agent at a time, run the corresponding
Codegenbox command, complete its ordinary login (if needed), create a short
conversation, exit with an uncommitted edit, and run `codegenbox resume <id>`.
Verify that its own CLI offers/continues the prior conversation and login, then
commit or remove the edit and exit. Repeat for Claude, Codex, and OpenCode.
Check `codegenbox sessions` between runs; it must show no credentials. Do not
copy or print state files. The structural tests cover mount isolation; this
manual check is required because CI intentionally has no real credentials.
