# Codegenbox (Phase 2)

Codegenbox runs a coding agent in a disposable Docker container against a
self-contained Git session clone. Committed work is imported by the trusted
host process only after the container exits; uncommitted work retains its clone
for a safe later resume.

## Use

```sh
go build ./cmd/codegenbox
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

The Phase 1 proof image remains `node:22-bookworm`. Until Phase 3 supplies a
production image, adapters execute `npx --yes @anthropic-ai/claude-code`,
`npx --yes @openai/codex --dangerously-bypass-approvals-and-sandbox`, and
`npx --yes opencode-ai`. Current Codex CLI replaces Phase 1's `--yolo` spelling
with this documented equivalent; it remains limited to the isolated container.

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
