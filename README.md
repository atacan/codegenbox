# Codegenbox (Phase 1)

Codegenbox is a local Git-clone sandbox for coding agents. This first proof of
architecture creates a self-contained temporary session clone outside the
source repository, then starts one disposable Docker container against that
clone.

## Try it

Build the CLI with Go 1.22 or later:

```sh
go build ./cmd/codegenbox
cd /path/to/a/git/repository
/path/to/codegenbox codex
```

`codegenbox codex` is shorthand for `codegenbox run codex`. The sole Phase 1
adapter runs this command in Docker:

```text
npx --yes @openai/codex --yolo
```

`--yolo` intentionally bypasses Codex permission prompts, while its terminal
UI remains interactive inside the disposable container. Codex can modify the
isolated session clone without per-action approval; the Docker mount boundary is
unchanged.

The default image is `node:22-bookworm`, which Docker will pull if needed. Set
`CODEGENBOX_IMAGE` to use an image that already contains the command instead.
Codegenbox does not build images. Set `CODEGENBOX_DATA_DIR` to choose a
Codegenbox-owned host storage root; the default is
`~/.local/share/codegenbox` (or `$XDG_DATA_HOME/codegenbox`).

`CODEGENBOX_DATA_DIR` must be a host path shared with the selected Docker or
Colima VM because each session clone is bind-mounted into the container. The
default home-directory location is appropriate for standard Colima
configuration. On macOS, `/tmp` resolves to `/private/tmp`, which is not shared
by default; do not use it for `CODEGENBOX_DATA_DIR` unless that VM mount has
been explicitly configured.

Run the test suite with:

```sh
go test ./...
```

## Phase 1 security model

The Docker command is structurally restricted to one ordinary host bind mount:
the self-contained session clone at `/workspace`. Its `.git` directory lives
inside that mount, so Git commands such as `status`, `add`, and `commit` work
normally in the container without mounting source-repository metadata. It uses a disposable container,
interactive TTY, `/workspace` workdir, `--cap-drop ALL`, and
`no-new-privileges=true`.

Codegenbox never mounts the host home directory, repository parent directory,
Docker socket, or any agent state/credentials in this phase. It never uses
`--privileged`. There are no host build-cache mounts.

The clone is made with Git's no-local/no-hardlink strategy, has no Git
alternates, and has its source remote removed before Docker starts. The source
repository, its parent, host home, Docker socket, and all agent state or
credentials remain unmounted.

After Docker exits—successfully, unsuccessfully, or because its process was
interrupted—Codegenbox reads clone Git porcelain status and imports only the
reserved `codegenbox/<session-id>` clone branch. The imported commit must be
the recorded base commit or a descendant; trusted host code atomically creates
only that reserved branch in the original repository. Main, tags, and unrelated
branches are not modified. Committed work is imported even when uncommitted
changes remain. A clone is removed only after a clean status, verified import,
and metadata update; dirty or validation-failed clones are retained.

## Current limits

Phase 1 has only the `codex` adapter, which uses `--yolo` to bypass Codex
permission prompts inside its isolated disposable container. Its authentication
and history are ephemeral because no host state is mounted. `resume`, session listing, agent
persistence, production development images, private dependency access, push/PR
workflow, resource limits, and full crash/orphan reconciliation are not yet
implemented. See [docs/development.md](docs/development.md) for details.
