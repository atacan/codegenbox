# Codegenbox

[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/atacan/codegenbox)

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
defaults to release `0.7.2`; set `CODEGENBOX_VERSION` to install a different
published version. The first agent run pulls the matching public development
image automatically.

To prepare and publish a release, run `scripts/release.sh MAJOR.MINOR.PATCH` from a clean `main` checkout.

To build and push the image on your own Docker daemon before publishing the
release, use `scripts/release.sh MAJOR.MINOR.PATCH --local-image`. This defaults
to the daemon's native architecture (ARM64 on Apple Silicon). Use
`--platforms linux/amd64,linux/arm64` for both architectures. A native-only
release image runs only on that architecture. Docker Hub login, Buildx, and
`jq` are required. The script records the verified image digest in the release
commit so GitHub reuses it rather than rebuilding the published image.
It also loads the native image from the build cache into your local Docker
daemon under the release tag, so Codegenbox can use it without downloading it.

## Use

```sh
cd /path/to/a/git/repository
codegenbox claude
codegenbox codex
codegenbox codex --base main
codegenbox codex --open-pr
codegenbox opencode
codegenbox ori claude
codegenbox ori codex --model openai/gpt-5.2
codegenbox ori opencode --open-pr
codegenbox sessions
codegenbox resume <session-id>
codegenbox continue <session-id>
codegenbox push <session-id>
codegenbox compare <session-id>
codegenbox pr <session-id>
codegenbox doctor
```

Use `--base <local-branch>` to start a new session from that local branch's
committed tip without changing your current checkout or its files. For example,
`codegenbox codex --base main` starts from local `main` even when your folder is
on another branch. The option also works with `run` and Ori harnesses and can
be combined with `--open-pr`. It does not fetch remote updates or accept tags,
commit IDs, or remote-tracking refs. Without `--base`, sessions start from the
current `HEAD`. Uncommitted source changes are not copied into the session.

`codegenbox <agent>` and `codegenbox run <agent>` are equivalent. `sessions`
prints stable ID/agent/state/workspace/branch records without reading or
printing credentials. `resume` requires an explicit ID; it never accepts a
workspace path or silently chooses a session. Use `resume <id>` for a retained
dirty or interrupted workspace. Use `continue <id>` only after a clean
workspace was removed: it reconstructs that exact workspace from the trusted
local `codegenbox/<id>` tip and records another run on the same session branch.
This version always uses the agent originally recorded for the session. Ori
sessions also retain an explicitly selected model.

### Ori harnesses

Ori wraps an installed coding harness with OpenRouter configuration. Install
Ori on the host and complete its global login once before starting an Ori
session:

```sh
curl -fsSL https://openrouter.ai/labs/ori/install.sh | bash
ori login
```

Do not use `ori login --local`: it writes credentials into the current
repository. Codegenbox seeds Ori from selected global authentication/settings
files and keeps its runtime state private to the session.
The host Ori executable is used only for login; Codegenbox runs its pinned Ori
binary inside the container.

The supported composed commands are `codegenbox ori claude`, `codegenbox ori
codex`, and `codegenbox ori opencode`, with equivalent `codegenbox run ori ...`
forms. Add `--model <model-id>` to select an OpenRouter model for that session;
Codegenbox records the model and reuses it on `resume` and `continue`. Arbitrary
Ori or child-harness arguments are not accepted.

## Host GitHub workflow

Once a container has stopped, Codegenbox prints a host-side summary from the
original repository: repository, recorded base branch and commit, reserved
session branch, imported commit, commit subjects, and changed-file totals.
By default the summary is informational; it never pushes, opens a browser,
creates a pull request, or merges anything automatically.

To opt into a convenient host-side handoff when starting a session, add
`--open-pr`:

```sh
codegenbox codex --open-pr
```

Codegenbox saves that request with the session metadata. After a successful,
clean run that imports a new commit, the trusted host process verifies a
standard `github.com` `origin`, pushes only the reserved session branch with
the same non-force fixed refspec, and opens its GitHub compare page. The
container never receives host GitHub write credentials, and Codegenbox never
creates or merges a pull request automatically.

Dirty, interrupted, failed, cleanup-incomplete, and no-change sessions do not
run the automatic handoff. If the push or browser opening fails, the imported
work and metadata remain safe, the compare URL is printed when available, and
the usual `push` and `compare` commands remain available for recovery. A
non-GitHub remote is not auto-pushed. `resume` and `continue` preserve the
saved preference: each later clean run with new imported work updates the same
reserved remote branch and opens the same compare URL, without creating another
PR.

After reviewing the summary, push only the generated branch explicitly:

```sh
codegenbox push <session-id>
```

After a continuation, run the same explicit push again to update an existing
PR. Continuation never fetches or adopts a remote branch: if the local reserved
session ref was moved, rewound, or advanced outside Codegenbox, it fails and
leaves recoverable work untouched.

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

The default image is `docker.io/atacandur/codegenbox:0.7.2`. It contains the
production development environment and installed agent CLIs, so adapters use
fixed direct commands instead of downloading an agent with `npx` at session
startup:

| Agent | Container command |
| --- | --- |
| Claude Code | `claude` |
| Codex | `codex --dangerously-bypass-approvals-and-sandbox` |
| OpenCode | `opencode` |
| Ori + Claude Code | `ori claude [--model <model-id>]` |
| Ori + Codex | `ori codex [--model <model-id>] --dangerously-bypass-approvals-and-sandbox` |
| Ori + OpenCode | `ori opencode [--model <model-id>]` |

The image bundles the `srcmv` CLI and its complete upstream skill globally for
Claude Code, Codex, and OpenCode, including their Ori harnesses. The skill is
pinned to the CLI's source revision and remains available alongside mounted
agent state; no manual skill installation is required inside the container.

Codex is installed from npm's `latest` tag when the image is built. Each
release refreshes its install layer; the installed version is recorded in
`/usr/local/share/codegenbox/codex-version`. Existing images retain the version
installed when they were built.

For a direct interactive `codegenbox codex` session, exit Codex to open the
container's session menu:

- **u — Update and resume:** install the latest Codex as the container user,
  then select the saved conversation in Codex's resume picker.
- **r — Resume:** choose a saved conversation with the current Codex.
- **Enter — Finish session:** end the container and let Codegenbox import work.

Updating/resuming keeps the same workspace, state mounts, and permissions.
The launcher uses `--no-daemon` for startup and resume, keeping Codex in the
container's foreground lifecycle.
A failed update leaves the working Codex available so you can retry or resume.
The update is container-local and disappears when that container ends; it does
not change the published image or the host Codex installation. There is no
startup network check. Noninteractive commands, Claude, OpenCode, and Ori
harnesses retain their existing direct execution behavior.

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

Every agent uses private state for each Codegenbox session, under
`<Codegenbox data root>/agent-state/<session-id>/`. Containers use the synthetic
home `/home/agent`. Host agent directories and host `$HOME` are never mounted.
The host CLI opens selected seed files read-only and creates separate copies;
databases, conversations, plugin installations, and caches stay private.

| Agent | Selected host seeds | Private directory / container destination | Source override |
| --- | --- | --- | --- |
| Claude Code | `~/.claude/.credentials.json`, portable `settings.json`, `CLAUDE.md`, authored `skills/`, `commands/`, `agents/`, `rules/` | `claude` → `/home/agent/.claude` | `CODEGENBOX_CLAUDE_STATE_DIR` |
| Codex | `$CODEX_HOME` or `~/.codex`: `auth.json`, portable `config.toml` and `*.config.toml` profiles, `AGENTS.md` | `codex` → `/home/agent/.codex` | `CODEGENBOX_CODEX_STATE_DIR` |
| OpenCode config | `$XDG_CONFIG_HOME/opencode` or `~/.config/opencode`: portable `opencode.json(c)`, `tui.json(c)`, authored skills/agents/commands/rules | `opencode-config` → `/home/agent/.config/opencode` | `CODEGENBOX_OPENCODE_CONFIG_DIR` |
| OpenCode authentication | `$XDG_DATA_HOME/opencode/auth.json` or `~/.local/share/opencode/auth.json` | `opencode-data` → `/home/agent/.local/share/opencode` | `CODEGENBOX_OPENCODE_DATA_DIR` |
| Ori | `~/.ori/credentials.json`, `config.json` login mode; legacy `~/.openrouter/credentials.json` fallback | `ori` → `/home/agent/.ori` | `CODEGENBOX_ORI_STATE_DIR` |
| Shared skills (Codex/OpenCode) | `~/.agents/skills`; Codex also includes legacy `$CODEX_HOME/skills` | `skills` → `/home/agent/.agents/skills` | `CODEGENBOX_AGENT_SKILLS_DIR` |

All overrides select **seed sources only**, never directories to mount. Claude
also honors host `CLAUDE_CONFIG_DIR`; its explicit Codegenbox source override
wins. Ori compositions receive private Ori state plus only their selected
harness's private state. Known host agent paths, custom seed sources, their
aliases/parents/children, source repositories, and generic home/config parents
cannot become state mounts or the Codegenbox data root. There are no arbitrary
mount flags. This isolation works with existing compatible Codegenbox images.

Each private location is initialized once, with a marker. Resume and continue
reuse it without reseeding, preserving refreshes, edits, and deliberate logout
or file deletion. A new session ID creates new state. Existing pre-isolation
sessions acquire private homes without importing their former shared history;
already-private Codex credentials/config are preserved during migration.
Private state survives clean workspace removal and container disposal. Remove
`agent-state/<session-id>` only after stopping its container when you want to
discard that session's credentials, settings, skills, and history. To rebuild a
snapshot from current host settings, start a new Codegenbox session.

Directories use mode `0700`; credentials/settings use `0600`; skill scripts
retain executable permission as `0700`. Containers run as the invoking non-root
UID/GID so refreshes write only to private copies. Source authentication is
never mounted or written back. Missing default files, including keychain-only
logins, leave that private location ready for independent login. An explicitly
selected missing auth file reports an error. `CODEGENBOX_CLAUDE_AUTH_FILE`,
`CODEGENBOX_CODEX_AUTH_FILE`, `CODEGENBOX_OPENCODE_AUTH_FILE`, and
`CODEGENBOX_ORI_AUTH_FILE` select exact auth seed files; `none` disables that
auth-file seed without deleting private credentials or suppressing credentials
already present in copied settings. These choices apply at initialization.

Portable settings preserve preferences such as models, reasoning, appearance,
permissions, provider configuration, and remote MCP endpoints. Host hooks,
notification/status commands, credential helper scripts, local MCP processes,
plugin/cache references, per-project trust, and machine-specific persistence or
external instruction paths are omitted. Codex uses file-backed credentials and
private SQLite/log paths; Claude receives an explicit private
`CLAUDE_CONFIG_DIR`. Ori carries only its verified `loginMode` setting. JSONC
comments and TOML formatting are normalized. Host-only integrations need
container-specific setup; repository configuration continues to load normally.

Only authored skill/definition trees are snapshotted. Top-level skill symlinks
are dereferenced into independent files; nested links are followed only inside
that resolved skill root. Broken, escaping, cyclic links, special files,
hidden entries, known runtime/cache directories, and database/history files
are skipped. Snapshots are bounded to 10,000 files, 128 MiB per authored tree,
and 16 MiB per file. The image's bundled `srcmv` remains available through a
fixed container-internal fallback link when no user snapshot provides it.

Authentication details follow the agents' supported storage:

- [Claude](https://code.claude.com/docs/en/authentication#credential-management)
  stores macOS logins in Keychain, which Codegenbox never exports. If no portable
  `.credentials.json` exists, use `/login` inside the private container session.
- [Codex](https://learn.chatgpt.com/docs/auth) supports copying its file-backed
  auth cache. A keyring-only host can configure file-backed storage and sign in
  again, or use **Sign in with Device Code** in the container when enabled.
  [Copied OAuth grants can still contend during refresh](https://learn.chatgpt.com/docs/auth/ci-cd-auth);
  independent login avoids sharing that grant.
- [OpenCode](https://opencode.ai/docs/providers/) stores connected-provider
  credentials in `auth.json`; use `/connect` when no reusable file is available.
- Ori uses global `credentials.json`; run its global login before creating a
  new Codegenbox session when needed. The legacy `.openrouter` fallback is used
  only without a selected Ori source override and when `.ori/credentials.json`
  is absent.

The image's Codex wrapper still filters user-global hooks inside its private
home; repository/plugin hooks retain normal discovery and trust behavior. A
host skill or settings change is picked up by new sessions, not live-linked
into an existing session.

The default Codegenbox data root is `~/.local/share/codegenbox` (or
`$XDG_DATA_HOME/codegenbox`); set `CODEGENBOX_DATA_DIR` to override it. Only this
private storage and session workspaces must be shared with Docker/Colima.
Standard Colima shares the home directory; macOS `/tmp` resolves to
`/private/tmp`, which is not shared by default.

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

New session workspaces contain a depth-1 snapshot of the selected local
branch's captured commit (current `HEAD` by default), without other branches,
tags, or older history. The source checkout and its shallow/partial-clone
settings stay unchanged. If a partial source is missing files needed by that
snapshot, startup downloads those objects through its existing promisor remote;
older blobs are not hydrated. Network access and host authentication are needed
only when required snapshot objects are missing. Continue reconstructs a depth-1
workspace at the previously imported session tip.

After Docker returns, Codegenbox validates and imports only the reserved
`codegenbox/<session-id>` branch if it is the recorded base commit or a
descendant. On resume or continue, that branch is atomically advanced only
from the recorded imported commit. Continue first requires its workspace path
to be absent and the local source branch to match that exact trusted tip; it
does not fetch, adopt, or reconcile remote state. Main and unrelated refs are
untouched. Dirty and failed clones are never auto-deleted; committed work is
still imported when a clone remains dirty.

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
`io.codegenbox.compatibility=2`; older, unlabelled, or otherwise incompatible
images are rejected before agent state is mounted. Dead `running`
session records are recovered only after the recorded named container is
confirmed stopped or explicitly reported absent; Docker inspection failures
leave the clone untouched. Dirty clones are retained.

## Manual real-agent persistence check

Use a temporary Git repository and, one agent at a time, run the corresponding
Codegenbox command, complete its ordinary login (if needed), create a short
conversation, exit with an uncommitted edit, and run `codegenbox resume <id>`.
Verify that its own CLI offers/continues the prior conversation and login, then
commit or remove the edit and exit. Repeat for Claude, Codex, and OpenCode,
then for each supported Ori composition after completing global `ori login`.
For an Ori run, verify that both OpenRouter authentication and the child
harness's history persist and that an explicit model survives resume.
Check `codegenbox sessions` between runs; it must show no credentials. Do not
copy or print state files. The structural tests cover mount isolation; this
manual check is required because CI intentionally has no real credentials.
