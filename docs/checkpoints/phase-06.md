# Phase 06 checkpoint — Hardening

## Design and threat model

Phase 6 adds host-side defense in depth while retaining the existing boundary:
the agent receives only a self-contained clone and selected agent state in an
unprivileged disposable container. Agent input cannot select Docker options,
mounts, host Git operations, or recovery targets.

## Security properties

- `codegenbox doctor` checks Git, Docker CLI/daemon, compatible image, and
  private storage without starting an agent or reading credentials.
- Optional PID, memory, and CPU limits are host configuration only.
- Final Docker argv auditing rejects privileged/host namespace settings, socket
  and legacy volumes, unexpected mounts, and missing required hardening.
- Image labels are checked before sessions. Legacy unlabelled 0.1 images work;
  a present incompatible label fails closed.
- Running records contain an owner PID and unique Docker container name. A dead
  PID is reconciled only after Docker confirms the named container is stopped
  or explicitly absent; live, running, uninspectable, and legacy records are preserved.
- Resume and recovery use a per-session host lock before changing lifecycle
  metadata, preventing concurrent containers or cleanup for one clone.
- Cancellation still runs post-exit status/import with a non-cancelled context.

## Tests

Local tests cover limits, image compatibility, doctor with a fake executor,
final command auditing, dead-PID dirty-orphan recovery, cancellation, hostile
mounts, and session Git import. No real credentials are used.

## Manual verification requiring approval

Use a temporary local repository and disposable compatible image to exercise
doctor, limits, interruption, and abrupt CLI termination/recovery. No Docker,
GitHub, SSH, private repository, or credential live acceptance was run here.

## Limitations

Outbound networking and Docker/Colima VM isolation are unchanged. PID reuse is
inherently platform-dependent, and unlabelled compatibility is a legacy
allowance; validation/import failures preserve the clone.
