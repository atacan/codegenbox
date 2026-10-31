"""Exercise release sequencing with real local Git remotes and a Docker stub."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "release.sh"
DIGEST = "sha256:" + "a" * 64
DOCKER = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
state = Path(os.environ["STUB_STATE"])
mode = os.environ.get("STUB_MODE", "success")
with Path(os.environ["STUB_LOG"]).open("a") as f:
    f.write(json.dumps(args) + "\n")
digest = "sha256:" + "a" * 64
if args[0] == "info":
    print("x86_64" if mode == "native-amd64" else "aarch64")
elif args[:2] == ["buildx", "version"]:
    print("buildx test")
elif args[:2] == ["buildx", "inspect"]:
    sys.exit(0 if mode == "reuse-builder" else 1)
elif args[:2] == ["buildx", "create"]:
    print("codegenbox-publish")
elif args[:2] == ["buildx", "build"]:
    if mode == "build-failure":
        sys.exit(1)
    if "--load" in args:
        if mode == "load-failure":
            sys.exit(1)
    else:
        state.write_text(args[args.index("--platform") + 1])
        metadata = Path(args[args.index("--metadata-file") + 1])
        metadata.write_text(json.dumps({"containerimage.digest": digest}))
elif args[:2] == ["image", "inspect"]:
    print("sha256:" + ("0" if mode == "loaded-mismatch" else "d" if mode == "containerd-store" else "f") * 64)
elif args[:3] == ["buildx", "imagetools", "inspect"]:
    if not state.exists():
        if mode == "existing":
            print("Digest: " + digest)
        else:
            print("connection refused" if mode == "network-failure" else "manifest unknown: not found", file=sys.stderr)
            sys.exit(1)
    elif "--raw" in args:
        if args[-1].endswith("@sha256:" + "d" * 64) or args[-1].endswith("@sha256:" + "e" * 64):
            print(json.dumps({"config": {"digest": "sha256:" + "f" * 64}}))
            sys.exit(0)
        platforms = ["linux/amd64"] if mode == "wrong-platform" else state.read_text().split(",")
        print(json.dumps({"manifests": [
            {"platform": {"os": p.split("/")[0], "architecture": p.split("/")[1]},
             "digest": "sha256:" + ("d" if p.endswith("arm64") else "e") * 64}
            for p in platforms
        ] + [{"platform": {"os": "unknown", "architecture": "unknown"}}]}))
    else:
        print("Digest: " + ("sha256:" + "b" * 64 if mode == "digest-mismatch" else digest))
else:
    sys.exit("Unexpected Docker arguments: " + repr(args))
'''


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="codegenbox-release-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        self.env.update(GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1")
        self.env.update(STUB_STATE=str(self.root / "pushed"), STUB_LOG=str(self.root / "docker.log"))
        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        (bin_dir / "docker").write_text(DOCKER)
        (bin_dir / "docker").chmod(0o755)
        self.env["PATH"] = str(bin_dir) + os.pathsep + self.env["PATH"]
        files = {
            "README.md": "Release 0.6.3\n",
            "docs/development.md": "Release 0.6.3\n",
            "internal/config/config.go": 'const DefaultImage = "docker.io/atacandur/codegenbox:0.6.3"\n',
            "internal/config/config_test.go": "0.6.3\n",
            "scripts/install.sh": "0.6.3\n",
            "release/image-digests.txt": "# Version OCI-index-digest\n0.6.3 sha256:" + "c" * 64 + "\n",
        }
        for name, contents in files.items():
            p = self.repo / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text(contents)
        shutil.copyfile(SCRIPT, self.repo / "scripts/release.sh")
        self.git("init", "-b", "main")
        self.git("config", "user.name", "Release test")
        self.git("config", "user.email", "release@example.invalid")
        self.git("add", ".")
        self.git("commit", "-m", "initial")
        self.git("init", "--bare", str(self.root / "remote.git"))
        self.git("remote", "add", "origin", str(self.root / "remote.git"))
        self.before = self.git("rev-parse", "HEAD")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.repo), *args], env=self.env, stderr=subprocess.PIPE, text=True).strip()

    def release(self, *args, mode="success"):
        return subprocess.run(["sh", "scripts/release.sh", "0.6.4", *args], cwd=self.repo,
                              env={**self.env, "STUB_MODE": mode}, capture_output=True, text=True)

    def calls(self):
        log = self.root / "docker.log"
        return [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []

    def assert_not_released(self):
        self.assertEqual(self.git("rev-parse", "HEAD"), self.before)
        self.assertEqual(self.git("tag", "--list", "v0.6.4"), "")
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_existing_github_release_path_does_not_use_docker(self):
        result = self.release()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls(), [])
        self.assertTrue(self.git("ls-remote", "--tags", "origin", "refs/tags/v0.6.4"))

    def test_local_native_image_is_pushed_and_digest_committed_in_tag(self):
        result = self.release("--local-image")
        self.assertEqual(result.returncode, 0, result.stderr)
        build = next(c for c in self.calls() if c[:2] == ["buildx", "build"])
        self.assertEqual(build[build.index("--platform") + 1], "linux/arm64")
        self.assertIn("--push", build)
        self.assertIn("--sbom=true", build)
        load = next(c for c in self.calls() if "--load" in c)
        self.assertEqual(load[load.index("--platform") + 1], "linux/arm64")
        self.assertNotIn("--push", load)
        self.assertIn("--provenance=false", load)
        refresh = build[build.index("--build-arg") + 1]
        self.assertTrue(refresh.startswith("CODEX_REFRESH=0.6.4-"))
        self.assertEqual(load[load.index("--build-arg") + 1], refresh)
        self.assertEqual(build[build.index("--tag") + 1], "docker.io/atacandur/codegenbox:0.6.4")
        records = self.git("show", "v0.6.4:release/image-digests.txt")
        self.assertIn("0.6.4 " + DIGEST, records)
        self.assertIn("0.6.3 sha256:" + "c" * 64, records)
        self.assertTrue(self.git("ls-remote", "--tags", "origin", "refs/tags/v0.6.4"))
        self.assertEqual(self.git("status", "--porcelain"), "")

    def test_explicit_multi_platform_build(self):
        result = self.release("--local-image", "--platforms", "linux/amd64,linux/arm64")
        self.assertEqual(result.returncode, 0, result.stderr)
        build = next(c for c in self.calls() if c[:2] == ["buildx", "build"])
        self.assertEqual(build[build.index("--platform") + 1], "linux/amd64,linux/arm64")
        load = next(c for c in self.calls() if "--load" in c)
        self.assertEqual(load[load.index("--platform") + 1], "linux/arm64")

    def test_containerd_store_manifest_image_identity(self):
        result = self.release("--local-image", mode="containerd-store")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_non_native_only_build_does_not_load(self):
        result = self.release("--local-image", "--platforms", "linux/amd64")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("--load" in c for c in self.calls()))

    def test_native_amd64_daemon(self):
        result = self.release("--local-image", mode="native-amd64")
        self.assertEqual(result.returncode, 0, result.stderr)
        build = next(c for c in self.calls() if c[:2] == ["buildx", "build"])
        self.assertEqual(build[build.index("--platform") + 1], "linux/amd64")

    def test_reuses_existing_builder_without_switching_selected_builder(self):
        result = self.release("--local-image", mode="reuse-builder")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any(c[:2] == ["buildx", "create"] for c in self.calls()))
        build = next(c for c in self.calls() if c[:2] == ["buildx", "build"])
        self.assertEqual(build[build.index("--builder") + 1], "codegenbox-publish")
        self.assertFalse(any("--use" in c for c in self.calls()))

    def test_image_failures_do_not_create_release_or_change_source(self):
        for mode in ["existing", "network-failure", "build-failure", "digest-mismatch", "wrong-platform", "load-failure", "loaded-mismatch"]:
            with self.subTest(mode=mode):
                state = self.root / "pushed"
                if state.exists():
                    state.unlink()
                result = self.release("--local-image", mode=mode)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assert_not_released()

    def test_invalid_options_fail_before_docker_or_release(self):
        for args in [("--platforms", "linux/arm64"), ("--local-image", "--platforms", "linux/386"),
                     ("--local-image", "--platforms"), ("--local-image", "--local-image"), ("--unknown",)]:
            with self.subTest(args=args):
                result = self.release(*args)
                self.assertNotEqual(result.returncode, 0)
                self.assert_not_released()
                self.assertEqual(self.calls(), [])


if __name__ == "__main__":
    unittest.main()
