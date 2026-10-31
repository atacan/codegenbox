"""Container hook overrides leave global files and shared state untouched."""
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "images/codegenbox-codex"
loader = importlib.machinery.SourceFileLoader("codegenbox_hooks", str(HELPER))
spec = importlib.util.spec_from_loader(loader.name, loader)
hooks = importlib.util.module_from_spec(spec)
loader.exec_module(hooks)


class CodexHooksTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get("CODEGENBOX_NATIVE_CODEX"), "requires a native Codex binary")
    def test_native_global_hook_disabled_and_project_hook_runs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            home, project = root / "home", root / "project"
            home.mkdir()
            local = project / ".codex"
            local.mkdir(parents=True)
            global_marker, project_marker = root / "global", root / "local"
            for folder, marker in [(home, global_marker), (local, project_marker)]:
                (folder / "hooks.json").write_text(json.dumps({"hooks": {"SessionStart": [
                    {"hooks": [{"type": "command", "command": f"touch {marker}"}]}]}}))
            (home / "config.toml").write_text(f'''model_provider = "test-local"
[projects.{json.dumps(str(project))}]
trust_level = "trusted"
[model_providers.test-local]
name = "Test local"
base_url = "http://127.0.0.1:1/v1"
wire_api = "responses"
request_max_retries = 0
stream_max_retries = 0
''')
            before = {p: p.read_bytes() for p in (home / "hooks.json", home / "config.toml")}
            try:
                subprocess.run([sys.executable, str(HELPER), os.environ["CODEGENBOX_NATIVE_CODEX"],
                                "--no-daemon", "--dangerously-bypass-hook-trust", "exec",
                                "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "hello"],
                               cwd=project, env={**os.environ, "CODEX_HOME": str(home), "HOME": str(home)},
                               capture_output=True, text=True, timeout=5)
            except subprocess.TimeoutExpired:
                pass  # Startup hooks finish before the deliberately unavailable model request.
            self.assertFalse(global_marker.exists())
            self.assertTrue(project_marker.exists())
            self.assertEqual(before, {p: p.read_bytes() for p in before})

    def test_selected_profile_and_unselected_invalid_profile(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory).resolve()
            (home / "unused.config.toml").write_text("invalid = [")
            (home / "linux.config.toml").write_text('''[[hooks.Stop]]
[[hooks.Stop.hooks]]
type = "command"
command = "/host/stop"
''')
            self.assertEqual(hooks.overrides(home), [])
            expected = ["-c", f'hooks.state={{"{home}/linux.config.toml:stop:0:0"={{enabled=false}}}}']
            for arguments in [("-p", "linux"), ("--profile=linux",), ("-plinux",)]:
                self.assertEqual(hooks.overrides(home, arguments), expected)
            self.assertEqual(hooks.overrides(home, ("--", "--profile=linux")), [])
            (home / "config.toml").write_text('profile = "linux"\n')
            self.assertEqual(hooks.overrides(home), expected)

    def test_global_json_and_inline_hooks_only(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory).resolve()
            (home / "hooks.json").write_text(json.dumps({"hooks": {"PreToolUse": [
                {"hooks": [{"type": "command", "command": "/host/dcg"},
                           {"type": "command", "command": "/host/other"}]}]}}))
            (home / "config.toml").write_text('''[hooks.state.existing]
trusted_hash = "preserved"
[[hooks.SessionStart]]
[[hooks.SessionStart.hooks]]
type = "command"
command = "/host/start"
''')
            project = home / "project/.codex"
            project.mkdir(parents=True)
            (project / "hooks.json").write_text('{"hooks":{"Stop":[]}}')
            before = {p: p.read_bytes() for p in home.rglob("*") if p.is_file()}
            flags = hooks.overrides(home)
            self.assertEqual(flags, ["-c", 'hooks.state={' +
                f'"{home}/hooks.json:pre_tool_use:0:0"={{enabled=false}},' +
                f'"{home}/hooks.json:pre_tool_use:0:1"={{enabled=false}},' +
                f'"{home}/config.toml:session_start:0:0"={{enabled=false}}' + '}'])
            self.assertEqual(before, {p: p.read_bytes() for p in before})

    def test_exec_preserves_state_environment_and_literal_arguments(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory).resolve()
            (home / "hooks.json").write_text('{"hooks":{"Stop":[{"hooks":[{}]}]}}')
            command = 'import json,os,sys; print(json.dumps([os.environ["CODEX_HOME"],sys.argv[1:]]))'
            result = subprocess.run([sys.executable, str(HELPER), sys.executable,
                                     "-c", command, "--", "literal -c value"],
                                    env={**os.environ, "CODEX_HOME": str(home)},
                                    capture_output=True, text=True, check=True)
            state, arguments = json.loads(result.stdout)
            self.assertEqual(state, str(home))
            self.assertEqual(arguments[-2:], ["--", "literal -c value"])
            self.assertIn(f'hooks.state={{"{home}/hooks.json:stop:0:0"={{enabled=false}}}}', arguments)

    def test_malformed_hooks_do_not_launch_codex(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory).resolve()
            (home / "hooks.json").write_text('{invalid')
            result = subprocess.run([sys.executable, str(HELPER), sys.executable, "-c", "print('LAUNCHED')"],
                                    env={**os.environ, "CODEX_HOME": str(home)}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertNotIn("LAUNCHED", result.stdout)
            self.assertIn("could not isolate", result.stderr)


if __name__ == "__main__":
    unittest.main()
