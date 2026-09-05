# Phase 1 development notes

## Lifecycle

The CLI accepts exactly these forms:

```text
codegenbox codex
codegenbox run codex
```

It discovers the Git top-level from the current directory, captures the base
branch and commit, and creates an independent clone below the Codegenbox data
root. The clone starts on `codegenbox/<session-id>` at the captured commit; the
source-repository branch is not created until Docker has returned and trusted
host import succeeds. The data root is deliberately outside the source
repository. JSON metadata is written before Docker runs and is updated after it
returns.

`CODEGENBOX_DATA_DIR` must be shared with the selected Docker or Colima VM: the
session clone beneath it is the one host path bind-mounted at `/workspace`.
The default home-directory data root works with standard Colima configuration.
On macOS, `/tmp` resolves to `/private/tmp`, which Colima does not share by
default; users should not choose it unless they have explicitly configured that
VM mount.

The clone uses `git clone --no-local --no-hardlinks --no-checkout --no-tags`,
then removes its temporary source remote. Its ordinary `.git` directory is
entirely inside the clone, with no object alternates. Codegenbox configures a
clone-local `Codegenbox <codegenbox@localhost>` identity so ordinary agent
`git commit` commands work without exposing the host Git configuration. This
identity is limited to the disposable clone and an agent can change it there.

The Phase 1 `codex` adapter runs `npx --yes @openai/codex --yolo`. `--yolo`
intentionally bypasses Codex permission prompts while keeping the Codex
terminal UI interactive within the isolated, disposable container. It grants
no host filesystem access beyond the session-clone mount. The default
`node:22-bookworm` image makes that command available via `npx`; a user can
override the image through `CODEGENBOX_IMAGE`. The image and package download
are intentionally disposable. No Docker image is built by Codegenbox.

There are deliberately no agent state, authentication, history, cache, SSH,
GitHub credential, home-directory, repository-parent, or Docker-socket mounts.
Adding any such mount is out of scope until Phase 2 (or later) and must be
explicitly designed and tested.

## Docker invocation contract

The command builder emits the equivalent of:

```text
docker run --rm --interactive --tty --workdir /workspace \
  --mount type=bind,src=<temporary-session-clone>,dst=/workspace \
  --cap-drop ALL --security-opt no-new-privileges=true \
  <image> npx --yes @openai/codex --yolo
```

The builder has no general-purpose mount option. Its only bind source is the
session clone it just created. Tests inspect the exact invocation without requiring
a Docker daemon.

## State transitions

`running` is recorded before Docker begins. When Docker returns, even with an
error, clone Git status is checked first. Codegenbox then reads only the fixed
clone `refs/heads/codegenbox/<session-id>` ref, never clone `HEAD` or a
clone-selected ref. It accepts only the recorded base commit or a descendant,
exports that one ref to a temporary bundle, verifies it again in the source
repository, and atomically creates the reserved source branch only if absent.
No clone remote/configuration, tags, arbitrary refs, hooks, or source-branch
checkout participates in this import.

| Git status / import | Docker result | Metadata state | Session clone |
| --- | --- | --- | --- |
| dirty / valid import | success or error | `dirty` | preserved |
| clean / valid import | success | `completed` | removed |
| clean / valid import | error/interruption | `interrupted` | removed |
| clean / import failure | any | `interrupted` | preserved |
| clean / removal fails | any | `clean` | preserved |
| status cannot be inspected | any | `interrupted` | preserved |

Metadata contains the ID, repository, session workspace path, agent, base branch and commit,
session branch, imported commit (after a successful source import), start/finish
times, state, and any final error. The imported OID permits later reconciliation
work if a process stops around metadata or clone cleanup. Dirty metadata is
resumable context for a later phase, but Phase 1 intentionally supplies no
`resume` command.

## Testing

Run `go test ./...`. The unit tests assert the ID/metadata behavior and the
security-sensitive Docker argument contract. Git integration tests construct
temporary repositories locally; they verify a self-contained clone can commit,
its imports leave main/tags/unrelated refs unchanged, dirty clones survive, and
invalid ancestry is rejected. They never invoke a real Docker daemon or pull an
image.
