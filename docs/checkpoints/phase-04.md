# Phase 4 checkpoint — host GitHub workflow

## Delivered

- Every returned session now prints a host-side summary containing the source
  repository, recorded base branch and commit, reserved session branch,
  imported commit, added commit subjects/count, and changed file/insertion/
  deletion totals.
- `codegenbox push <session-id>` explicitly pushes only the validated generated
  branch. It is never automatic.
- `codegenbox compare <session-id>` reads the original source repository's
  `origin` push URL, recognizes standard `github.com` remotes, prints the
  compare URL, and opens it on the host.
- `codegenbox pr <session-id>` optionally invokes a host-installed `gh` with
  `pr create --repo owner/repo --base <base> --head <session> --fill`.

## Security properties and decisions

- The session clone is never opened for summary, remote detection, push,
  compare, or PR work. Remote information is read only from the recorded
  source repository after it has been validated as its own Git root.
- Metadata must have the reserved `codegenbox/<id>` branch shape, an absolute
  source root, safe base ref, and valid object IDs before host operations run.
- Push uses a direct source-origin push URL and the sole fixed refspec
  `refs/heads/codegenbox/<id>:refs/heads/codegenbox/<id>`, with `--no-verify`
  and no force option. It does not rely on a remote push refspec or clone
  configuration; Git hooks are disabled for Codegenbox host Git commands.
- Git and `gh` are called with argument arrays, never a shell. Host Git
  environment variables that could redirect repository/config state are
  removed. Normal host credentials remain host-only.
- A failed push or `gh` call returns an error but does not delete session
  metadata or alter the local session branch. Codegenbox never merges a user
  branch.

## Automated verification

- Unit tests cover GitHub HTTPS/SSH/SCP parsing, malformed remote rejection,
  compare URL validation, malformed session metadata, missing remotes,
  non-GitHub remotes, missing `gh`, `gh` failure, summary parsing, and fixed
  argument construction.
- Temporary Git repositories and local bare remotes prove that the exact
  session ref is pushed while `main` and a protected tag remain unchanged. A
  non-fast-forward remote session ref is rejected and remains untouched.
- Existing container tests continue to cover Docker command and mount
  boundaries; Phase 4 adds no container invocation or mount inputs.

## Manual verification and remaining limitations

- Live host acceptance passed on 2026-09-05 against the user-provided
  disposable `atacan/TestingCodegenboxGithubIntegration` repository. A single
  `codegenbox/live-20260905-acceptance-a1b2` commit (`008b694`) was pushed with
  `codegenbox push`; remote `main` remained at `46cb189`. `codegenbox compare`
  opened the expected compare URL, and `codegenbox pr` created
  [PR #1](https://github.com/atacan/TestingCodegenboxGithubIntegration/pull/1)
  through the host `gh` CLI.
- Remote recognition intentionally supports only public `github.com`, not
  GitHub Enterprise. A non-GitHub `origin` can still receive the fixed branch
  push, but cannot use compare or `gh` PR helpers.
