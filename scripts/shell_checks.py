"""Drive the interactive shell through a real pseudo-terminal.

Starts a scripted fake model server, launches kalonide in a pty and types
into it like a person: the /model picker, permission prompts, Shift+Tab,
multi-line input, history, Ctrl-C, bracketed paste and more.

    python3 -I scripts/shell_checks.py ./kalonide      (or: make shell-check)
"""
import http.server
import json
import os
import pty
import re
import select
import socketserver
import struct
import sys
import tempfile
import threading
import time
import fcntl
import termios


class FakeModel(http.server.BaseHTTPRequestHandler):
    """Scripted OpenAI-compatible server. Decides its reply from the chat."""

    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path != "/v1/models":
            self.send_response(404)
            self.end_headers()
            return
        body = json.dumps({"data": [
            {"id": "fake-coder", "capabilities": ["completion", "tools"]},
            {"id": "fake-small", "capabilities": ["completion", "tools"]},
        ]}).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        last = req["messages"][-1]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()

        def send(obj):
            try:
                self.wfile.write(("data: " + json.dumps(obj) + "\n\n").encode())
                self.wfile.flush()
            except (BrokenPipeError, ConnectionResetError):
                pass  # the client cancelled (Ctrl-C test)

        if last["role"] == "tool":
            send({"choices": [{"delta": {"content": "RESULT: " + last["content"][:70]}, "finish_reason": "stop"}]})
        elif "MAKEFILE" in last["content"]:
            args = json.dumps({"path": "notes.txt", "content": "hi\n"})
            send({"choices": [{"delta": {"tool_calls": [{"index": 0, "id": "t1", "type": "function",
                                                          "function": {"name": "write_file", "arguments": args}}]}}]})
        elif "MARKDOWN" in last["content"]:
            send({"choices": [{"delta": {"content": "## Plan\n- first **bold** step\n- uses `go test`\n"}, "finish_reason": "stop"}]})
        elif "SLOW" in last["content"]:
            for i in range(100):
                send({"choices": [{"delta": {"content": "tick%d " % i}}]})
                time.sleep(0.1)
        else:
            text = "echo: " + last["content"].replace("\n", "<NL>") + " | model=" + req["model"]
            send({"choices": [{"delta": {"content": text}, "finish_reason": "stop"}]})
        try:
            self.wfile.write(b"data: [DONE]\n\n")
        except (BrokenPipeError, ConnectionResetError):
            pass


ANSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*\x07|\r")


class Shell:
    def __init__(self, binary, port):
        self.home = tempfile.mkdtemp()
        self.work = tempfile.mkdtemp()
        env = dict(os.environ, KALONIDE_HOME=self.home, TERM="xterm-256color",
                   KALONIDE_BASE_URL="http://127.0.0.1:%d/v1" % port)
        for k in ("KALONIDE_MODEL", "KALONIDE_PROVIDER", "NO_COLOR"):
            env.pop(k, None)
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            os.chdir(self.work)
            os.execve(binary, [binary], env)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 120, 0, 0))
        self.raw = b""

    def screen(self):
        return ANSI.sub("", self.raw.decode("utf-8", "replace"))

    def read(self, seconds=0.3):
        end = time.time() + seconds
        while time.time() < end:
            if select.select([self.fd], [], [], 0.05)[0]:
                try:
                    self.raw += os.read(self.fd, 65536)
                except OSError:
                    return

    def expect(self, text, timeout=8):
        end = time.time() + timeout
        while time.time() < end:
            if text in self.screen():
                return
            self.read(0.1)
        os.kill(self.pid, 9)
        sys.exit("FAIL: never saw %r\n--- screen tail ---\n%s" % (text, self.screen()[-1500:]))

    def send(self, keys, settle=0.15):
        os.write(self.fd, keys.encode())
        self.read(settle)

    def clear(self):
        self.raw = b""


def main():
    binary = os.path.abspath(sys.argv[1] if len(sys.argv) > 1 else "./kalonide")
    socketserver.ThreadingTCPServer.allow_reuse_address = True
    srv = socketserver.ThreadingTCPServer(("127.0.0.1", 0), FakeModel)
    srv.daemon_threads = True
    threading.Thread(target=srv.serve_forever, daemon=True).start()

    sh = Shell(binary, srv.server_address[1])
    notes = os.path.join(sh.work, "notes.txt")
    passed = []

    def ok(name):
        passed.append(name)
        print("ok  ", name)

    sh.expect("❯")
    sh.expect("fake-coder")
    ok("banner, prompt, model auto-picked")
    sh.expect("fake-coder · kalon")
    sh.expect("● ask")
    ok("rule names the model; status line shows the mode")
    sh.clear(); sh.send("/help\r"); sh.expect("Shift+Tab"); ok("/help")
    sh.clear(); sh.send("/mo"); sh.send("\t"); sh.send("\r"); sh.expect("mode ask"); ok("Tab completes /mo to /mode")
    sh.clear(); sh.send("/model\r"); sh.expect("fake-small"); sh.send("\x1b[B"); sh.send("\r")
    sh.expect("model is now fake-small"); ok("/model live picker with arrow keys")
    sh.clear(); sh.send("hello\r"); sh.expect("echo: hello | model=fake-small"); ok("streamed reply uses the picked model")
    sh.clear(); sh.send("first line\\\r"); sh.send("second\r"); sh.expect("echo: first line<NL>second"); ok("multi-line input")
    sh.clear(); sh.send("MARKDOWN\r"); sh.expect("• first bold step"); sh.expect("• uses go test")
    ok("replies are formatted (headings, bullets, bold, code)")
    sh.clear(); sh.send("MAKEFILE\r"); sh.expect("+    1 │ hi"); sh.expect("Allow write_file: notes.txt"); sh.send("n")
    sh.expect("RESULT: The user did not allow")
    assert not os.path.exists(notes), "file written after denial"
    ok("permission prompt shows the diff first, then: no")
    sh.clear(); sh.send("MAKEFILE\r"); sh.expect("Allow write_file: notes.txt"); sh.send("y")
    sh.expect("RESULT: Created notes.txt")
    assert open(notes).read() == "hi\n"
    os.remove(notes)
    ok("permission prompt: yes writes the file")
    sh.clear(); sh.send("\x1b[Z"); sh.send("x"); sh.expect("● auto-edit"); sh.send("\x7f"); ok("Shift+Tab switches to auto-edit")
    sh.clear(); sh.send("MAKEFILE\r"); sh.expect("RESULT: Created notes.txt")
    assert "Allow write_file:" not in sh.screen()
    ok("auto-edit writes without asking")
    sh.clear(); sh.send("\x1b[A"); sh.expect("MAKEFILE"); sh.send("\x15"); ok("history with Up, Ctrl-U clears")
    sh.clear(); sh.send("SLOW\r"); sh.expect("tick3"); sh.send("\x03"); sh.expect("(stopped)", 5); ok("Ctrl-C stops a streaming reply")
    sh.clear(); sh.send("after stop\r"); sh.expect("echo: after stop"); ok("shell works after a cancel")
    sh.clear(); sh.send("!echo shell-escape-$((40+2))\r"); sh.expect("shell-escape-42"); ok("! shell escape")
    sh.clear(); sh.send("/context\r"); sh.expect("tokens"); ok("/context")
    sh.clear(); sh.send("/sessions\r"); sh.expect("hello"); ok("/sessions lists this session")
    sh.clear(); sh.send("/new\r"); sh.expect("New conversation"); ok("/new")
    sh.clear(); sh.send("/resume\r"); sh.expect("Resume"); sh.send("\r"); sh.expect("resumed"); ok("/resume picker")
    sh.clear(); sh.send("\x1b[200~pasted\nblock\x1b[201~"); sh.send("\r"); sh.expect("echo: pasted<NL>block")
    ok("bracketed paste keeps newlines")
    sh.clear(); sh.send("\x03"); sh.expect("again to exit"); sh.send("\x03", 1.0)
    _, status = os.waitpid(sh.pid, 0)
    assert os.WEXITSTATUS(status) == 0, status
    ok("double Ctrl-C exits cleanly")
    print("\nALL %d SHELL CHECKS PASSED" % len(passed))


if __name__ == "__main__":
    main()
