"""Exercise the real launcher through a PTY, without an API or npm network access."""
import json
import os
from pathlib import Path
import pty
import select
import subprocess
import tempfile
import time
import unittest


LAUNCHER = Path(__file__).resolve().parents[2] / "images/codegenbox-session"
CODEX = '''#!/usr/bin/env python3
import json, os, signal, sys, time
from pathlib import Path
if "--version" in sys.argv:
    print("codex-cli test")
    sys.exit(0)
with open(os.environ["CALL_LOG"], "a") as f:
    f.write(json.dumps({"binary": sys.argv[0], "args": sys.argv[1:], "cwd": os.getcwd()}) + "\\n")
Path("workspace.txt").write_text("preserved work")
if os.environ.get("WAIT_FOR_SIGNAL"):
    def stopped(*args):
        Path(os.environ["SIGNAL_LOG"]).write_text("terminated")
        sys.exit(0)
    signal.signal(signal.SIGTERM, stopped)
    print("CHILD_READY", flush=True)
    while True: time.sleep(0.05)
sys.exit(int(os.environ.get("CODEX_STATUS", "0")))
'''
NPM = '''#!/usr/bin/env python3
import os, sys
from pathlib import Path
if os.environ.get("NPM_FAIL"):
    sys.exit(1)
prefix = Path(sys.argv[sys.argv.index("--prefix") + 1])
(prefix / "bin").mkdir(parents=True)
binary = prefix / "bin/codex"
binary.write_text(Path(os.environ["MOCK_CODEX"]).read_text())
binary.chmod(0o755)
'''


class LauncherTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="codegenbox-launcher-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        for name, contents in [("codex", CODEX), ("npm", NPM)]:
            p = self.bin / name
            p.write_text(contents)
            p.chmod(0o755)
        self.env = dict(os.environ, HOME=str(self.root), CALL_LOG=str(self.root / "calls"),
                        MOCK_CODEX=str(self.bin / "codex"), SIGNAL_LOG=str(self.root / "signal"))
        self.env["PATH"] = str(self.bin) + os.pathsep + self.env["PATH"]

    def start(self, **environment):
        self.master, slave = pty.openpty()
        self.addCleanup(os.close, self.master)
        self.process = subprocess.Popen(["bash", str(LAUNCHER), "codex", "--dangerously-bypass-approvals-and-sandbox"],
                                        cwd=self.root, env={**self.env, **environment}, stdin=slave,
                                        stdout=slave, stderr=slave, start_new_session=True)
        os.close(slave)
        self.addCleanup(self.stop)
        self.output = b""

    def stop(self):
        if self.process.poll() is None:
            self.process.kill()
            self.process.wait()

    def expect(self, needle):
        needle = needle.encode()
        end = time.monotonic() + 10
        while time.monotonic() < end:
            if needle in self.output:
                before, self.output = self.output.split(needle, 1)
                return before
            if select.select([self.master], [], [], 0.1)[0]:
                try:
                    self.output += os.read(self.master, 65536)
                except OSError:
                    break
        self.fail(f"Did not see {needle!r}: {self.output!r}")

    def calls(self):
        return [json.loads(line) for line in (self.root / "calls").read_text().splitlines()]

    def test_update_and_resume_preserves_workspace_and_arguments(self):
        self.start()
        self.expect("[r] Resume:")
        os.write(self.master, b"u\n")
        self.expect("[r] Resume:")
        os.write(self.master, b"\n")
        self.assertEqual(self.process.wait(timeout=5), 0)
        first, resumed = self.calls()
        self.assertNotEqual(first["binary"], resumed["binary"])
        self.assertIn("/.local/share/codegenbox/codex.", resumed["binary"])
        self.assertEqual(resumed["args"], ["resume", "--dangerously-bypass-approvals-and-sandbox"])
        self.assertEqual(first["cwd"], resumed["cwd"])
        self.assertEqual((self.root / "workspace.txt").read_text(), "preserved work")

    def test_failed_update_keeps_current_binary_and_allows_resume(self):
        self.start(NPM_FAIL="1")
        self.expect("[r] Resume:")
        os.write(self.master, b"u\n")
        self.expect("Codex update failed")
        self.expect("[r] Resume:")
        os.write(self.master, b"r\n")
        self.expect("[r] Resume:")
        os.write(self.master, b"\n")
        self.assertEqual(self.process.wait(timeout=5), 0)
        first, resumed = self.calls()
        self.assertEqual(first["binary"], resumed["binary"])

    def test_finish_preserves_codex_failure_status(self):
        self.start(CODEX_STATUS="7")
        self.expect("[r] Resume:")
        os.write(self.master, b"\n")
        self.assertEqual(self.process.wait(timeout=5), 7)

    def test_stdin_eof_finishes_session(self):
        self.start()
        self.expect("[r] Resume:")
        os.write(self.master, b"\x04")
        self.assertEqual(self.process.wait(timeout=5), 0)

    def test_sigterm_reaches_child_before_launcher_exits(self):
        self.start(WAIT_FOR_SIGNAL="1")
        self.expect("CHILD_READY")
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=5), 143)
        self.assertEqual((self.root / "signal").read_text(), "terminated")

    def test_non_interactive_codex_and_other_commands_pass_through(self):
        for args in [("codex",), ("bash", "-c", "exit 9")]:
            result = subprocess.run(["bash", str(LAUNCHER), *args], cwd=self.root,
                                    env=self.env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0 if args[0] == "codex" else 9)
            self.assertNotIn("Finish session", result.stdout)


if __name__ == "__main__":
    unittest.main()
