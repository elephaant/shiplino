import json
import re
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

SRC = str(Path(__file__).resolve().parent.parent / "src")
sys.path.insert(0, SRC)

from shiplino import Shiplino, tool_kind  # noqa: E402

TOKEN = "t" * 64


class FakeDaemon:
    """Stores events by dedup key like the real daemon, and can be told to fail."""

    def __init__(self):
        self.requests = []
        self.keys = set()
        self.status = 200  # or "lost": stored, then a 503
        daemon = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_POST(self):
                body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                if self.headers.get("Authorization") != "Bearer " + TOKEN:
                    self._reply(401, {"error": "unauthorized"})
                    return
                if isinstance(daemon.status, int) and daemon.status != 200:
                    self._reply(daemon.status, None)
                    return
                events = json.loads(body)
                daemon.requests.append(events)
                accepted = 0
                for e in events:
                    key = e["session_id"] + ":" + e["id"]
                    if key not in daemon.keys:
                        accepted += 1
                    daemon.keys.add(key)
                if daemon.status == "lost":
                    self._reply(503, None)
                    return
                self._reply(200, {"accepted": accepted, "duplicates": len(events) - accepted, "errors": []})

            def _reply(self, code, obj):
                b = json.dumps(obj).encode() if obj is not None else b""
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(b)))
                self.end_headers()
                self.wfile.write(b)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = "http://127.0.0.1:%d" % self.server.server_address[1]
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    @property
    def events(self):
        return [e for r in self.requests for e in r]


class ClientTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.daemon = FakeDaemon()

    @classmethod
    def tearDownClass(cls):
        cls.daemon.server.shutdown()
        cls.daemon.server.server_close()

    def setUp(self):
        self.daemon.requests = []
        self.daemon.keys = set()
        self.daemon.status = 200
        self.warnings = []

    def client(self, **kw):
        opts = dict(url=self.daemon.url, token=TOKEN, flush_interval=60, on_warning=self.warnings.append)
        opts.update(kw)
        c = Shiplino("test-bot", **opts)
        self.addCleanup(c.close)
        return c

    def test_whole_session(self):
        c = self.client()
        s = c.session(title="Fix the login bug", cwd="/home/dev/app")
        s.turn("Fix the login bug")
        with s.tool("Read", {"file_path": "/home/dev/app/login.py"}):
            pass
        s.tool("Bash", "pytest").end(False, "1 test failed")
        s.shell("pytest", 1, 1200)
        s.file_edit("/home/dev/app/login.py", 4, 2)
        s.usage("claude-opus-5-5", input_tokens=1200, output_tokens=300, cache_read=50, cache_write=10, cost_usd=0.02)
        s.usage("claude-opus-5-5", input_tokens=100, output_tokens=30)
        s.waiting("Deploy to staging?")
        s.resumed()
        sub = s.subagent("reviewer")
        sub.tool("Grep", {"pattern": "TODO"}).end()
        sub.usage("claude-opus-5-5", input_tokens=10, output_tokens=5, cost_usd=0.01)
        sub.end()
        s.end()
        s.turn("ignored after end")
        c.close()

        self.assertEqual(self.warnings, [])
        ev = self.daemon.events
        self.assertEqual(
            [e["kind"] for e in ev],
            [
                "session.start", "turn.start", "tool.start", "tool.end", "tool.start", "tool.end", "shell.exec", "file.edit",
                "usage", "usage", "usage", "waiting.start", "waiting.end", "subagent.start", "tool.start", "tool.end",
                "usage", "usage", "subagent.end", "turn.end", "session.end",
            ],
        )
        sid = ev[0]["session_id"]
        self.assertRegex(sid, r"^test-bot:[0-9a-f-]{36}$")
        for e in ev:
            self.assertEqual(e["v"], 1)
            self.assertEqual(e["agent"], {"name": "test-bot"})
            self.assertEqual(e["session_id"], sid)
            self.assertEqual(e["project"], {"cwd": "/home/dev/app"})
            self.assertRegex(e["ts"], r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$")
        self.assertEqual(len({e["id"] for e in ev}), len(ev), "dedup keys are unique")
        self.assertEqual(c.stats["sent"], len(ev))

        self.assertEqual(ev[0]["data"], {"title": "Fix the login bug"})
        self.assertEqual(ev[1]["data"], {"prompt": "Fix the login bug"})
        start, end = ev[2]["data"], ev[3]["data"]
        self.assertEqual(start["tool_call_id"], end["tool_call_id"])
        self.assertEqual(
            {k: v for k, v in start.items() if k != "tool_call_id"},
            {"tool": "read", "tool_raw": "Read", "input_summary": "/home/dev/app/login.py"},
        )
        self.assertIs(end["ok"], True)
        self.assertIsInstance(end["duration_ms"], int)
        self.assertEqual(ev[4]["data"]["input_summary"], "pytest")
        self.assertEqual((ev[5]["data"]["ok"], ev[5]["data"]["error"]), (False, "1 test failed"))
        self.assertEqual(ev[6]["data"], {"command": "pytest", "exit_code": 1, "duration_ms": 1200})
        self.assertEqual(
            ev[7]["data"],
            {"path": "/home/dev/app/login.py", "op": "edit", "tool": "edit", "lines_added": 4, "lines_removed": 2, "lines_source": "reported"},
        )
        self.assertEqual(
            ev[8]["data"],
            {
                "model": "claude-opus-5-5", "input_tokens": 1200, "output_tokens": 300, "cache_read_tokens": 50,
                "cache_write_tokens": 10, "cost_usd": 0.02, "cost_source": "reported",
            },
        )
        self.assertIs(ev[9]["data"]["report"], True)
        self.assertEqual(ev[9]["data"]["total_cost_usd"], 0.02)
        self.assertNotIn("cost_usd", ev[10]["data"], "no cost: the daemon prices it")
        self.assertEqual(ev[11]["data"], {"reason": "input", "message": "Deploy to staging?"})

        child = ev[13]["data"]["child_session_id"]
        self.assertRegex(child, "^" + re.escape(sid) + r"/sub:[0-9a-f]+$")
        self.assertEqual(ev[13]["data"]["agent_type"], "reviewer")
        for e in ev[14:17]:
            self.assertEqual((e["actor_id"], e["parent_actor"], e["actor_type"]), (child, sid, "reviewer"))
        self.assertNotIn("actor_id", ev[17])
        self.assertAlmostEqual(ev[17]["data"]["total_cost_usd"], 0.03)
        self.assertNotIn("actor_id", ev[18])
        self.assertEqual(ev[18]["data"], {"child_session_id": child, "agent_type": "reviewer", "status": "done"})
        self.assertEqual(ev[19]["data"], {"status": "ok"})
        self.assertEqual(ev[20]["data"], {"status": "ok"})

    def test_error_fails_the_session(self):
        c = self.client()
        try:
            with c.session(id="run-7"):
                raise RuntimeError("out of retries")
        except RuntimeError:
            pass
        c.close()
        ev = self.daemon.events
        self.assertEqual(ev[0]["session_id"], "test-bot:run-7")
        self.assertEqual(
            [(e["kind"], e["data"]) for e in ev[1:]],
            [("turn.end", {"status": "error", "error": "out of retries"}), ("session.end", {"status": "error"})],
        )

    def test_tool_context_manager_records_exceptions(self):
        c = self.client()
        s = c.session()
        with self.assertRaises(ValueError):
            with s.tool("deploy"):
                raise ValueError("bad config")
        c.close()
        end = self.daemon.events[-1]["data"]
        self.assertEqual((end["ok"], end["error"], end["tool"]), (False, "bad config", "other"))

    def test_batches_by_size(self):
        c = self.client(batch_size=100)
        s = c.session()
        for i in range(249):
            s.shell("echo %d" % i, 0)
        c.close()
        self.assertTrue(all(len(r) <= 100 for r in self.daemon.requests))
        self.assertEqual(len(self.daemon.events), 250)

    def test_flushes_on_the_interval(self):
        c = self.client(flush_interval=0.02)
        c.session()
        for _ in range(100):
            if self.daemon.events:
                break
            time.sleep(0.01)
        self.assertEqual(len(self.daemon.events), 1)

    def test_keeps_events_while_down_and_drops_oldest(self):
        c = self.client(max_queue=100, batch_size=100)
        self.daemon.status = 503
        s = c.session()
        for i in range(149):
            s.shell("echo %d" % i, 0)
        c.flush()
        self.assertEqual(c.stats["queued"], 100)
        self.assertEqual(c.stats["dropped"], 50)
        self.assertEqual(len(self.warnings), 2, self.warnings)  # once per kind, not per attempt
        self.daemon.status = 200
        c.close()
        self.assertEqual(len(self.daemon.events), 100)
        self.assertEqual(self.daemon.events[-1]["data"]["command"], "echo 148")
        self.assertEqual(c.stats["queued"], 0)

    def test_retries_are_idempotent(self):
        c = self.client()
        s = c.session()
        s.turn("hello")
        self.daemon.status = "lost"
        c.flush()
        self.assertEqual(c.stats["queued"], 2)
        self.daemon.status = 200
        c.close()
        r = self.daemon.requests
        self.assertEqual([e["id"] for e in r[0]], [e["id"] for e in r[1]])
        self.assertEqual(len(self.daemon.keys), 2)
        self.assertEqual((c.stats["sent"], c.stats["duplicates"]), (0, 2))

    def test_unreachable_daemon(self):
        c = Shiplino("test-bot", url="http://127.0.0.1:1", token=TOKEN, max_queue=100, flush_interval=60, on_warning=self.warnings.append)
        s = c.session()
        for i in range(300):
            s.turn("p%d" % i)
        c.flush()
        c.flush()
        self.assertEqual(c.stats["queued"], 100)
        self.assertGreaterEqual(c.stats["dropped"], 200)
        self.assertIn("not reachable", c.stats["last_error"])
        self.assertEqual(len([w for w in self.warnings if "not reachable" in w]), 1)
        c.close()

    def test_bad_token_is_not_retried(self):
        c = self.client(token="wrong")
        c.session()
        c.close()
        self.assertEqual((c.stats["queued"], c.stats["rejected"]), (0, 1))
        self.assertIn("401", self.warnings[0])

    def test_finds_token_and_port_in_home(self):
        with tempfile.TemporaryDirectory() as home:
            Path(home, "token").write_text(TOKEN + "\n")
            Path(home, "port").write_text(self.daemon.url.rsplit(":", 1)[1] + "\n")
            c = Shiplino("test-bot", home=home, flush_interval=60)
            c.session()
            c.close()
        self.assertEqual(len(self.daemon.events), 1)

    def test_not_installed_is_a_noop(self):
        with tempfile.TemporaryDirectory() as home:
            c = Shiplino("test-bot", home=home, on_warning=self.warnings.append)
            s = c.session()
            s.tool("Read", {}).end()
            s.subagent("x").end()
            s.end()
            c.close()
        self.assertFalse(c.enabled)
        self.assertEqual(len(self.warnings), 1)
        self.assertIn("no API token", self.warnings[0])

    def test_invalid_agent_name(self):
        for agent in ["has space", "a:b", "a/b", "", "x" * 65, None]:
            c = Shiplino(agent, url=self.daemon.url, token=TOKEN, on_warning=self.warnings.append)  # type: ignore[arg-type]
            c.session().end()
            c.close()
            self.assertFalse(c.enabled)
        self.assertEqual(len(self.warnings), 6)
        self.assertEqual(self.daemon.events, [])

    def test_throwing_warning_handler(self):
        def boom(msg):
            raise RuntimeError("boom")

        self.assertFalse(Shiplino("a b", on_warning=boom).enabled)

    def test_tool_kind(self):
        self.assertEqual(tool_kind("Bash"), "shell")
        self.assertEqual(tool_kind("edit"), "edit")
        self.assertEqual(tool_kind("WebFetch"), "web")
        self.assertEqual(tool_kind("mcp__github__create_issue"), "mcp")
        self.assertEqual(tool_kind("deploy"), "other")

    def test_flushes_at_exit(self):
        script = (
            "import sys\n"
            "sys.path.insert(0, %r)\n"
            "from shiplino import Shiplino\n"
            "c = Shiplino('exit-bot', url=sys.argv[1], token=sys.argv[2])\n"
            "c.session(title='short run').end()\n"
        ) % SRC
        run = lambda url: subprocess.run(  # noqa: E731
            [sys.executable, "-c", script, url, TOKEN], capture_output=True, text=True, timeout=30
        )
        r = run(self.daemon.url)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual([e["kind"] for e in self.daemon.events], ["session.start", "session.end"])

        # A daemon that's down must not hang the exit.
        r = run("http://127.0.0.1:1")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("not reachable", r.stderr)


if __name__ == "__main__":
    unittest.main()
