# Phase 03 checkpoint — Production Development Image

**Status:** implementation complete and `0.1.0` published; native AMD64 CI,
workflow deployment, and credential-backed persistence acceptance remain.

## Release evidence

- Published `docker.io/atacandur/codegenbox:0.1.0` on 2026-09-05 with OCI
  index digest
  `sha256:11b94307b51a73b485d97f826cc50a31958157bd564a1a2899f719a68ade6170`.
- Registry inspection reports `linux/amd64` manifest
  `sha256:1f09aa18d815930354b38b3a28f2cb19838515572c70884e21e58c66c1efe3a8`
  and `linux/arm64` manifest
  `sha256:2bf655b1dde3868171b776d3f725188015f0e36be795b18e33298912fdf9fc25`,
  with an SBOM/provenance attestation for each platform.
- Both explicit platform pulls succeeded, and an empty Docker configuration
  could inspect the index anonymously, confirming public registry access.
  Native ARM64 image checks passed for all documented tools and
  compiler/runtime smoke programs. The AMD64 build passed its
  install-time architecture and checksum checks; Python, Go, Rust, Swift, and
  all three agent executables also ran under emulation. Node later hit a QEMU
  segmentation fault, so native AMD64 CI remains an explicit release gate.
- With empty disposable state, the published ARM64 image reached the Claude
  first-run UI, Codex sign-in UI, and OpenCode main UI through Codegenbox. No
  real credentials were mounted or inspected.
- A mapped `501:20` runtime wrote the host-owned workspace, selected Codex
  state, language caches, and user package directories while retaining host
  ownership. The Docker socket and unselected Claude state were absent.
- Exact resolved Ubuntu packages are recorded in
  [`phase-03-packages.txt`](phase-03-packages.txt). Explicit non-APT versions:
  Node 22.18.0, npm 10.9.3, pnpm 10.14.0, Python 3.12.11, uv 0.7.20,
  Go 1.24.6, Rust/cargo 1.89.0, rustup 1.28.2, Swift 6.1.2, Claude Code
  2.1.261, Codex 0.153.4, and OpenCode 1.18.29.

## Implementation checklist

- [x] Build a production image from Ubuntu 24.04 LTS for `linux/arm64` and
  `linux/amd64`.
- [x] Include the documented common CLI tools, build tooling, language
  toolchains, and installed Claude Code, Codex, and OpenCode binaries.
- [x] Pin or otherwise explicitly control OS, toolchain, and agent versions;
  record the selected inputs in the Dockerfile/build configuration.
- [x] Keep agent startup offline from package registries: adapters invoke
  `claude`, `codex --dangerously-bypass-approvals-and-sandbox`, and `opencode`
  directly, with no `npx` fallback.
- [x] Keep `docker.io/atacandur/codegenbox:0.1.0` as the CLI default and retain
  `CODEGENBOX_IMAGE` as an explicit compatible-image override.

## Verification checklist

- [x] Build or pull each architecture and confirm the expected OS, installed
  toolchains, and all three agent executables are present.
- [x] Run a pull/startup smoke test for Claude, Codex, and OpenCode using the
  production image. Confirm each adapter executes its installed direct command,
  not a package-manager download. Do not use or record real credentials.
- [x] Confirm Codegenbox's trusted `--user <host-uid>:<host-gid>` mapping lets
  the non-root process write the host-owned session clone and only the selected
  state mount, without a container-startup ownership/permission change.
- [x] Inspect the pushed image with `docker buildx imagetools inspect` (or an
  equivalent registry inspection) and confirm both `linux/arm64` and
  `linux/amd64` variants are attached to every release tag.
- [x] Rerun mount-boundary regression tests: selected state only; no host home,
  repository parent, Docker socket, privileged mode, or GitHub write
  credentials. Preserve dropped capabilities and `no-new-privileges`.
- [x] Rerun normal/race Go tests and `git diff --check` after image and adapter
  changes.

## Publishing checklist

- [x] Provision the intended Docker Hub namespace and publish to
  `atacandur/codegenbox`.
- [ ] Configure the GitHub Actions variable `DOCKERHUB_IMAGE` with the intended
  repository (the planned value is `atacandur/codegenbox`) and provide
  `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` as repository secrets. Never put
  registry credentials in the repository, image, logs, or checkpoint.
- [ ] Verify that a version tag such as `v0.1.0` produces only the immutable
  `0.1.0` tag after a successful authenticated workflow run, and that a retry
  refuses to overwrite that existing tag.
- [ ] Run the pull-request or non-publishing manual-dispatch validation path
  and verify it builds both architectures without Docker Hub credentials or an
  image push.
- [x] Verify pull access and the multi-architecture manifests from a clean
  Docker client before documenting a release as available.

## Reproducibility and version updates

- [x] Treat changes to the Ubuntu base, package inputs, language toolchains, or
  agent versions as a new image-release input; update the pinned configuration,
  image tag/default compatibility, and user/developer documentation together.
- [x] Rebuild and inspect both architectures, repeat direct-command and
  security regression checks, and record the immutable tag or digest in the
  release evidence.

## Unresolved manual acceptance

- [ ] On native AMD64 CI, pull the published image and run the full executable
  smoke suite. Native Apple Silicon pull and direct-agent startup are complete;
  native AMD64 remains pending.
- [ ] On native Apple Silicon and an AMD64-capable Docker host or runner, pull
  the published image and launch each agent through Codegenbox; verify native
  manifest selection and interactive startup without exposing credentials in
  output.
- [ ] Repeat the per-agent persistence/resume check from Phase 2 with the
  production image, including dirty-clone retention and clean-clone cleanup.
- [ ] Configure the repository Actions variable/secrets, run both validation
  matrix jobs, and review the actual workflow logs before marking all Phase 3
  acceptance complete. Registry publication and inspection are complete.
