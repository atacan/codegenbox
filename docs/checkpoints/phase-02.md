# Phase 02 checkpoint — Agent Persistence

**Status:** implementation, automated verification, and independent security review completed on 2026-09-05. Live persistence verification has passed for Codex and OpenCode; Claude remains to be checked. No later phase has been started.

## Implemented

- Added fixed adapters for Claude Code, Codex, and OpenCode. Each has a fixed `npx` command, a synthetic container home (`/home/agent`), and a narrow, explicit state-mount contract:
  - Claude: `~/.claude` to `/home/agent/.claude`.
  - Codex: `~/.codex` to `/home/agent/.codex`, with `CODEX_HOME` set accordingly.
  - OpenCode: only its `opencode` children under the XDG config and data roots, mapped to the matching synthetic-home XDG locations.
- Added `codegenbox sessions`, which deterministically lists validated metadata records without displaying credentials or state-file contents.
- Added `codegenbox resume <session-id>`. It requires an explicit branch-safe ID and resumes only the retained self-contained clone named by validated metadata.
- Extended metadata with resume time/count and retained imported commit information. Each resumed exit revalidates the clone, imports only the fixed reserved ref, and uses the prior imported commit as the atomic compare-and-swap expectation.
- Kept all ordinary source work inside the self-contained session clone mounted at `/workspace`. Agent-state mounts are the only new Docker mounts.
- Added canonical path, symlink-alias, collision, nested-source, metadata-directory, and cleanup-race defenses around the new persistence paths.
- Updated the README and developer notes with supported commands, agent-state behavior, Colima sharing constraints, and a safe manual persistence procedure.

## Important design decisions

- The host home directory is never mounted. Each agent gets the same synthetic home, while only its narrowly scoped state directory/directories are mounted read-write so authentication refresh, settings, and history can persist.
- State mount destinations are a static per-adapter allowlist. The container layer independently validates them, so a future caller cannot convert adapter state into a general user-provided mount mechanism.
- `resume` deliberately does not support a workspace path, an implicit “latest” choice, or a different agent. It trusts only the recorded adapter after metadata, repository identity, storage containment, and clone-layout validation.
- Codex now uses its current documented `--dangerously-bypass-approvals-and-sandbox` spelling, replacing the earlier Phase 1 `--yolo` spelling. This affects only the agent inside the existing isolated container boundary.
- The source branch is never force-updated. First import creates `codegenbox/<session-id>` only if absent; subsequent imports advance it only if it still equals the metadata-recorded imported OID.

## Files added or changed

- `README.md`
- `docs/development.md`
- `internal/agent/agent.go`, `internal/agent/agent_test.go`
- `internal/cli/cli.go`, `internal/cli/cli_test.go`
- `internal/container/docker.go`, `internal/container/docker_test.go`
- `internal/git/git.go`
- `internal/session/lifecycle.go`, `internal/session/lifecycle_test.go`
- `internal/session/metadata.go`, `internal/session/metadata_test.go`

## Review and verification

- A fresh `gpt-5.6-terra` implementation agent (high reasoning, no inherited conversation) implemented Phase 2 only.
- A separate fresh `gpt-5.6-sol` reviewer performed a read-only security review. Its initial findings covered canonical-path alias handling, duplicate/nested state sources, metadata directory and listing path safety, and the cleanup status-check ordering. The implementation agent corrected those findings; the reviewer re-reviewed the changes and reported that all required findings were resolved.
- Coordinator review inspected the adapter definitions, Docker command builder, state-path resolution, metadata/listing validation, resume lifecycle, Git compare-and-swap import, tests, and documentation.
- `gofmt -d` on the changed Go files: no output.
- `git diff --check`: passed.
- `GOCACHE=/private/tmp/codegenbox-phase2-final-go-build GOMODCACHE=/private/tmp/codegenbox-phase2-final-go-mod go test -race ./...`: passed.
- `go test ./...`, `go vet ./...`, and `go build -o /private/tmp/codegenbox-phase2-final ./cmd/codegenbox` were also run with disposable Go caches during coordinator/reviewer verification: passed.
- The tests cover adapter selection and environments; selected-only mount generation; traversal/comma, socket, host-home, cross-agent, duplicate/nested, workspace, source-repository, and symlink-alias mount rejection; metadata symlink/escape rejection; explicit-ID resume; self-contained clone checks; imported-commit CAS behavior; dirty-clone retention; and cleanup when a clone becomes dirty just before removal.

## Deviations from the implementation plan

- No material scope deviation. The Phase 1 self-contained-clone clarification in implementation-plan section 5.0 remains the workspace model used by Phase 2.
- The proof image continues to obtain the three CLIs with `npx`; installing and publishing the production development image remains Phase 3.

## Unresolved issues / required manual acceptance

- CI and automated tests intentionally do not use real credentials or inspect agent state. The Codex live check passed on 2026-09-05: its existing authentication was available without a new login; its prior conversation was available through the Codex `/resume` control; the dirty retained clone resumed with the marker present; the marker was committed as `d82aa6e`; and Codegenbox imported it to the reserved branch before removing the clean clone. The Codex CLI reported hook exit status 127 during the session, but Git operations and session cleanup completed; this indicates an existing configured hook invoked a command absent from the deliberately minimal proof image, rather than a persistence or mount-boundary failure.
- The OpenCode live check passed on 2026-09-05: the resumed disposable container presented the prior session and its `opencode -s <session-id>` continuation command, then Codegenbox completed cleanup after the retained workspace was made clean.
- Before calling the Phase 2 success criterion fully verified, perform the corresponding Claude check: authenticate normally if needed, create a conversation, leave a small uncommitted edit so the clone is retained, obtain its exact ID from `codegenbox sessions`, then run `codegenbox resume <id>` and confirm that Claude offers the prior authentication/history. Commit or remove the edit afterward. Do not print, copy, or commit state files.
- Normal Docker/Colima sharing requirements still apply: default home-directory state/storage paths are normally shared; macOS `/tmp` resolves to `/private/tmp`, which is not shared by Colima by default.
- Production image construction, resource limits, doctor/auditing commands, orphan recovery, private-dependency access, and host GitHub workflow are intentionally deferred.

## Next phase

After Phase 2’s live persistence check is accepted and the checkpoint is approved/committed, Phase 3 will build and publish the multi-architecture production development image containing Ubuntu, language toolchains, common CLI tools, and all three agents. It must preserve the fixed adapter/mount and self-contained-clone boundaries established here.
