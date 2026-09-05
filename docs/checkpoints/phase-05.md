# Phase 5 checkpoint — private dependencies

## Delivered

- Added opt-in private GitHub dependency authorization through
  `CODEGENBOX_GITHUB_READ_TOKEN`.
- The resolver accepts no authorization by default and rejects malformed token
  input and non-fine-grained (`github_pat_`) token forms before Docker starts.
  It neither reads nor derives a value from host
  Git, `gh`, SSH, `$HOME`, or credential-helper state.
- Docker receives only `--env CODEGENBOX_GITHUB_READ_TOKEN` (without its
  value); the Go Docker child receives the value in its per-process
  environment. The value is absent from Docker arguments, mounts, metadata,
  session storage, and persistent agent state.
- In the disposable container process, Git disables system/global config,
  supplies the dedicated token only for `https://github.com`, and rewrites
  standard GitHub SSH dependency URL forms to HTTPS. Existing private GitHub
  submodules and language dependency tools that delegate to Git can therefore
  fetch with the same limited authorization.

## Design decision and threat model

Phase 5 evaluated SSH-agent forwarding, a restricted GitHub token, and a host
credential proxy. SSH-agent forwarding met the no-key-file requirement but not
the required read-only boundary: Codegenbox cannot restrict a generic agent
socket to `git-upload-pack`, and a key can have arbitrary GitHub/SSH server
permissions. A credential proxy capable of enforcing request-level Git access
would need to become a substantially larger HTTPS/Git transport trusted
component.

The selected initial design forwards a separate fine-grained GitHub token. Its
server-side scope must be explicitly selected dependency repositories and
`Contents: Read-only`, with no write, workflow, administration, or unrelated
permission. This limits the exposed capability to dependency reads; it is not
the host Git/`gh` credential and cannot perform Phase 4 push/PR actions.

The coding agent can read its own process environment, so it can exfiltrate the
dedicated read token. This design relies on the token's GitHub-enforced scope,
not secrecy from hostile code running inside the allowed networked container.
Codegenbox syntactically validates the token but cannot inspect its remote
scope. A user must revoke the token if a session is untrusted.

## Security properties

- No SSH private key, `SSH_AUTH_SOCK`, host `$HOME`, host `.gitconfig`, host
  credential helper, host `gh` state, Docker socket, or general host path is
  mounted for private dependency support.
- No privileged container or capabilities are added. The only ordinary mounts
  remain the session clone and selected agent state.
- The token cannot appear in Docker process arguments or persisted metadata.
  It is passed to Docker from a per-process environment and exists only during
  the disposable container run.
- Static Git settings apply only inside the agent container. Agent-controlled
  clone paths, Git config, hooks, refs, environment changes, and commands do
  not influence trusted host import, push, compare, or PR operations.
- Phase 4 push and PR behavior has no private-dependency token input and
  remains host-only after the agent terminates.

## Automated verification

- Resolver tests cover absent, valid fake, malformed, control-character, and
  oversized token inputs without real credentials.
- Docker construction tests prove a fake token is absent from arguments, that
  no SSH or credential mounts are added, and that invalid/multiple
  authorizations are rejected.
- A local Git fixture runs `git credential fill` against the generated
  in-container configuration and confirms it returns the fake token for
  `github.com`, plus both standard GitHub SSH-to-HTTPS rewrites.
- Lifecycle tests prove an unavailable resolver never starts the agent and
  preserves source-branch/session cleanup safety; a successful fake resolver
  proves there are no extra mounts and no token is persisted in metadata.
- Existing container regression tests continue to cover host-home aliases,
  Git/agent state collisions, Docker socket rejection, no privileged flag, and
  selected-adapter mount isolation.

## Manual verification and limitations

Live acceptance passed on 2026-09-05 against the user-provided disposable
`atacan/TestingCodegenboxGithubIntegration` private repository. A dedicated
fine-grained read-only PAT was available only through
`CODEGENBOX_GITHUB_READ_TOKEN`. The test used the actual Codegenbox lifecycle,
Docker invocation, selected temporary Codex state, and production `0.1.0`
image, while a fixed host-side test shim replaced the agent executable with two
read-only `git ls-remote` probes. Both the HTTPS repository URL and its
conventional `ssh://git@github.com/...` form succeeded; no remote write, push,
PR, SSH agent, private-key mount, host Git/`gh` credential, or agent state was
used. The generated session clone was cleanly removed; the temporary local
fixtures contain no token and are retained pending safe cleanup.

The first live attempt exposed a corrupt local Colima/containerd image cache:
containerd reported I/O failures reading its metadata database and the image
blob. After explicit approval, Colima was restarted and only the dead test
container plus the local `atacandur/codegenbox:0.1.0` image were removed. The
same published digest was pulled again before the successful rerun. This was a
local Docker-storage fault, not a Codegenbox authorization failure.

The shim validates the complete forwarding and container-Git path without
starting a real coding-agent conversation. A normal interactive agent/build
acceptance remains optional, and should use the same disposable repository and
read-only selected-repository token. Rerun with the token unset to verify the
expected private-fetch failure.

GitHub Enterprise, non-GitHub hosts, SSH-only hosts, and credential proxying
are deferred. The implementation is intentionally not a network allowlist or
a defense against token exfiltration by a malicious agent.
