# Phase 01 checkpoint — Core Proof of Architecture

**Status:** revised, implemented, and reviewed on 2026-09-05. No later phase has been started.

## Implemented

Phase 1 establishes the Go proof of architecture for an isolated coding-agent session with a self-contained Git workspace:

- `codegenbox codex` and `codegenbox run codex` discover the enclosing Git repository, capture its base branch/commit, and create a standalone no-local session clone below Codegenbox-owned storage outside the source repository.
- The session clone has an ordinary `.git` directory inside the mounted workspace. It has no object alternates or hardlinks to the source object database, and its temporary source remote is removed before Docker starts.
- A single, deliberately ephemeral Codex adapter starts `npx --yes @openai/codex --yolo` in a disposable Docker container. The terminal UI remains interactive; `--yolo` bypasses its per-action permission prompts.
- The Docker invocation is interactive and mounts only the session clone at `/workspace`; it uses `--rm`, `--cap-drop ALL`, and `--security-opt no-new-privileges=true`.
- JSON metadata is persisted before execution and updated after it finishes, recording identity, source/workspace paths, agent, base branch/commit, session branch, imported commit, timestamps, lifecycle state, and final error where applicable.
- On every normal Docker-command return, including a failed command or context cancellation, Codegenbox checks clone Git porcelain status and imports only the fixed `codegenbox/<session-id>` clone ref. The imported commit must equal the recorded base or descend from it; trusted host code atomically creates only that session branch in the original repository. Dirty, invalid, and import-failed clones are preserved; clean clones are removed only after verified import and metadata update.

## Important design decision

The original linked-worktree proof could not meet the one-mount security invariant: its `.git` gitfile pointed into the source repository's Git administrative directory, which a correctly isolated container could not access. A live Codex attempt exposed this when Git inside the container reported a missing gitdir.

The approved correction uses a self-contained clone plus host-side validated import. It keeps the source repository's `.git`, parent directory, home, Docker socket, credentials, and agent state out of the container. The tradeoff is that commits enter the original Git object database during trusted post-exit reconciliation rather than at the instant the agent makes them. The implementation plan now records this approved Phase 1 clarification in section 5.0.

## Design decisions

- Phase 1 intentionally supports only `codex`. It mounts no host agent state, credentials, home directory, cache, SSH material, repository parent, or Docker socket. Agent authentication and history persistence are deferred to Phase 2.
- The requested `--yolo` mode is deliberately limited to the agent inside the disposable container. It allows Codex to modify the isolated session clone without per-action approval; it does not broaden Docker mounts or host access.
- The default proof image is `node:22-bookworm`; it uses `npx` to acquire the Codex CLI inside the disposable container. `CODEGENBOX_IMAGE` permits an explicit image override, but the CLI neither builds nor persistently caches images or packages.
- Storage defaults to an XDG-style Codegenbox-owned host directory (`~/.local/share/codegenbox`, or `$XDG_DATA_HOME/codegenbox`) and can be overridden with `CODEGENBOX_DATA_DIR`. The storage root is validated not to resolve inside the source repository and must be shared with the selected Docker/Colima VM. Standard Colima shares the default home location; macOS `/tmp` resolves to `/private/tmp` and is not shared by default.
- Git and Docker interactions are isolated behind small packages/interfaces so Docker lifecycle behavior can be tested without a daemon.

## Files added or changed

- `go.mod`
- `cmd/codegenbox/main.go`
- `internal/agent/agent.go`, `internal/agent/agent_test.go`
- `internal/cli/cli.go`
- `internal/config/config.go`, `internal/config/config_test.go`
- `internal/container/docker.go`, `internal/container/docker_test.go`
- `internal/git/git.go`
- `internal/session/lifecycle.go`, `internal/session/lifecycle_test.go`
- `internal/session/metadata.go`, `internal/session/metadata_test.go`
- `README.md`
- `docs/development.md`
- `docs/implementation-plan.md`

## Review and verification

- Inspected the complete new source and tests, with particular review of clone independence, config quarantine, fixed-ref bundle import, descendant validation, atomic source-ref creation, metadata persistence, Docker command construction, and cleanup ordering.
- `gofmt -d` on all Go sources/tests: no output.
- `go test ./...`: passed. Temporary-repository integration tests prove that a self-contained clone supports ordinary `git add`/`commit`; imported commits survive clean clone removal on only the reserved branch; committed work imports while dirty files are retained; Docker failures/cancellation preserve dirty clones; malformed clone config is quarantined; and non-descendant session refs cannot alter main, tags, or unrelated refs.
- `go test -race ./...`: passed.
- `go vet ./...`: passed.
- `go build -o /private/tmp/codegenbox-phase1-clone-review ./cmd/codegenbox`: passed.
- The Go commands used disposable caches under `/private/tmp` because this environment disallows creation of its default host Go build-cache directory.
- Static invocation review confirms one `--mount` with the generated session-clone source and `/workspace` destination, `--rm`, interactive TTY, `--cap-drop ALL`, and `no-new-privileges=true`; no `--privileged`, Docker socket, host-home, SSH, Codex-state, source-Git-metadata, or repository-parent mount construction exists.
- The original live Docker attempt was the diagnostic that exposed linked-worktree Git failure. The post-repair live Docker/Colima smoke test passed on 2026-09-05: Codex ran `git status`, created and committed `hello2.txt` as `da700ae` on `codegenbox/codegenbox-smoke-20260905-125501-7390`; after exit, Codegenbox removed the clean session clone, and host-side `git log`/`git show` verified the commit and file on that branch while `main` remained at `ec3d60a`.
- The repository remains on `main`. The implementation files are intentionally uncommitted pending user approval of this checkpoint.

## Deviations from the implementation plan

- **Approved material clarification:** Phase 1 uses self-contained session clones and host-side validated import instead of linked Git worktrees sharing the original object database during agent execution. This is documented in plan section 5.0 and preserves the stricter one-workspace-mount security invariant.
- The source-repository `codegenbox/<session-id>` branch is created only after the agent exits and validated import succeeds. The clone uses the same reserved branch name while it runs.
- Phase 1 intentionally supports only Codex; the plan permits one adapter without prescribing which.

## Unresolved issues / intentional Phase 1 limits

- No Claude/OpenCode adapters, host agent-state/auth/history mount, `resume`, session-listing command, or reconciliation retry command. A retained clone and imported OID provide durable context for later resume/recovery work.
- No production development image, version compatibility check, Docker/Colima doctor command, resource limits, private-dependency read-auth strategy, host push/PR workflow, or full orphan-session recovery.
- Signal cancellation is structured to inspect/import/preserve the clone after the Docker command returns. Broader crash/orphan recovery remains Phase 6 work.
- The import policy deliberately rejects rebased or otherwise non-descendant session histories. Broader reconciliation policy is deferred.

## Next phase

Phase 2 must add separately scoped Claude, Codex, and OpenCode adapters; explicitly validated per-agent authentication/history mounts; session discovery and `resume`; and integration verification that each agent can discover and resume preserved history without exposing another agent's state, the host home directory, or source Git metadata. Resume/recovery must operate on retained session clones and reconcile any recorded imported commit safely.
