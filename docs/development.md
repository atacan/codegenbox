# Phase 2 development notes

## Adapter contract

Each adapter owns a fixed command, selected-only state locations, and a fixed
synthetic-home environment. The container builder accepts adapter-derived
mounts only and revalidates agent name, destination allowlist, duplicate and
`/workspace` collisions, comma/NUL injection, host-home use, workspace/source
collisions, and the Docker socket. It never accepts a user mount flag.

| Adapter | Proof-image command | State mounts | Environment |
| --- | --- | --- | --- |
| Claude | `npx --yes @anthropic-ai/claude-code` | host `~/.claude` → `/home/agent/.claude` | `HOME=/home/agent` |
| Codex | `npx --yes @openai/codex --dangerously-bypass-approvals-and-sandbox` | host `~/.codex` → `/home/agent/.codex` | `HOME=/home/agent`, `CODEX_HOME=/home/agent/.codex` |
| OpenCode | `npx --yes opencode-ai` | host XDG config/data `opencode` children → matching `/home/agent` XDG children | `HOME`, `XDG_CONFIG_HOME=/home/agent/.config`, `XDG_DATA_HOME=/home/agent/.local/share` |

These assumptions were verified against local metadata on 2026-09-05: Codex
CLI 0.153.4 help identifies `~/.codex/config.toml` and `$CODEX_HOME`; Claude
Code 2.1.261 help identifies interactive resume; OpenCode 1.18.29 help
identifies interactive sessions. Current vendor documentation confirms Claude
interactive/resume behavior, Codex's `$CODEX_HOME` configuration model, and
OpenCode’s XDG configuration plus data model. No token or state-file content
was inspected.

For an unusual installation, override only the required direct agent state
location with `CODEGENBOX_CLAUDE_STATE_DIR`, `CODEGENBOX_CODEX_STATE_DIR`,
`CODEGENBOX_OPENCODE_CONFIG_DIR`, or `CODEGENBOX_OPENCODE_DATA_DIR`. Overrides
do not permit host home, generic XDG parents, commas, or known another-agent
default state. Missing selected directories are created mode 0700; existing
directories are not chmodded or read by Codegenbox.

All state mounts are read-write: the CLIs can persist OAuth refresh state,
configuration, and history. The only ordinary source mount remains the
self-contained clone at `/workspace`. The proof image is intentionally not a
production image; Phase 3 owns installing the commands rather than `npx`.

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
