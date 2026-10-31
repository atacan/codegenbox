"""Real Codex startup smoke; enable with CODEGENBOX_TEST_IMAGE=<local image>."""
import os
import fcntl
import pty
import select
import subprocess
import struct
import termios
import time
import unittest
import uuid


@unittest.skipUnless(os.environ.get("CODEGENBOX_TEST_IMAGE"), "requires a locally built image")
class CodexContainerTests(unittest.TestCase):
    def test_foreground_startup_with_incomplete_daemon_install(self):
        name = "codegenbox-startup-test-" + uuid.uuid4().hex[:12]
        master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
        setup = '''mkdir -p "$CODEX_HOME/packages/app-server-daemon"
ln -s /host-only/codex/daemon "$CODEX_HOME/packages/app-server-daemon/current"
printf '{"OPENAI_API_KEY":"codegenbox-test-key"}' > "$CODEX_HOME/auth.json"
exec /usr/local/bin/codegenbox-session codex --no-alt-screen --dangerously-bypass-approvals-and-sandbox
'''
        process = subprocess.Popen([
            "docker", "run", "--rm", "-it", "--name", name, "--user", "12345:12345",
            "--tmpfs", "/workspace:mode=1777", "--env", "TERM=xterm-256color",
            "--env", "OPENAI_BASE_URL=http://127.0.0.1:1/v1",
            os.environ["CODEGENBOX_TEST_IMAGE"], "bash", "-euc", setup,
        ], stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
        os.close(slave)
        output = b""
        try:
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                if select.select([master], [], [], 0.2)[0]:
                    try:
                        chunk = os.read(master, 65536)
                    except OSError:
                        break
                    output += chunk
                    if b"\x1b[6n" in chunk:
                        os.write(master, b"\x1b[1;1R")
                    if b"\x1b[c" in chunk or b"\x1b[0c" in chunk:
                        os.write(master, b"\x1b[?1;2c")
                    if b"\x1b[?u" in chunk:
                        os.write(master, b"\x1b[?0u")
                    self.assertNotIn(b"daemon executable not found", output)
                    if any(marker in output for marker in [b"Sign in", b"sign in", b"Welcome to Codex", b"OpenAI Codex", b"model:"]):
                        self.assertIsNone(process.poll())
                        return
                if process.poll() is not None:
                    break
            self.fail("Codex did not reach the unauthenticated TUI: " + output.decode(errors="replace"))
        finally:
            subprocess.run(["docker", "stop", "--time", "2", name],
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            os.close(master)


if __name__ == "__main__":
    unittest.main()
