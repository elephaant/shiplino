# Copyright 2026 The Shiplino Authors
# SPDX-License-Identifier: FSL-1.1-ALv2

"""Against a real daemon: set SHIPLINO_BIN to a built `shiplino` binary
(go build -o bin/shiplino ./cmd/shiplino). It runs with a temp home."""

import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

from shiplino import Shiplino  # noqa: E402

BIN = os.environ.get("SHIPLINO_BIN")
NO_PROXY = urllib.request.build_opener(urllib.request.ProxyHandler({}))


def wait_for(f, what):
    for _ in range(200):
        try:
            v = f()
        except Exception:
            v = None
        if v:
            return v
        time.sleep(0.05)
    raise AssertionError("timed out waiting for " + what)


@unittest.skipUnless(BIN, "set SHIPLINO_BIN to run")
class DaemonTest(unittest.TestCase):
    def test_session_reaches_the_daemon(self):
        with tempfile.TemporaryDirectory() as home:
            shome = os.path.join(home, ".shiplino")
            env = dict(os.environ, SHIPLINO_HOME=shome, HOME=home, USERPROFILE=home)
            daemon = subprocess.Popen([BIN, "daemon"], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            try:
                self._run(home, shome)
            finally:
                daemon.terminate()
                daemon.wait(timeout=10)

    def _run(self, home, shome):
        port = wait_for(lambda: Path(shome, "port").read_text().strip(), "the daemon to listen")
        token = Path(shome, "token").read_text().strip()
        base = "http://127.0.0.1:" + port
        wait_for(lambda: NO_PROXY.open(base + "/api/v1/health", timeout=2).status == 200, "health")

        def get(path):
            req = urllib.request.Request(base + path, headers={"Authorization": "Bearer " + token})
            with NO_PROXY.open(req, timeout=5) as r:
                return json.loads(r.read())

        client = Shiplino("sdk-e2e", home=shome)
        s = client.session(title="SDK end to end", cwd=home)
        s.turn("Check the SDK")
        s.tool("Read", {"file_path": os.path.join(home, "a.txt")}).end()
        s.file_edit(os.path.join(home, "a.txt"), 3, 1)
        s.usage("claude-opus-5-5", input_tokens=1000, output_tokens=100, cost_usd=0.05)
        sub = s.subagent("reviewer")
        sub.usage("claude-opus-5-5", input_tokens=10, output_tokens=10, cost_usd=0.01)
        sub.end()
        s.end()
        # Without a cost from the agent, the daemon prices the tokens.
        s2 = client.session(title="Priced by the daemon", cwd=home)
        s2.usage("claude-opus-5-5", input_tokens=1000, output_tokens=100)
        s2.end()
        client.close()
        self.assertEqual(client.stats["rejected"], 0, client.stats["last_error"])
        self.assertGreater(client.stats["sent"], 0)

        def find():
            for x in get("/api/v1/sessions")["sessions"]:
                if x["id"] == s.session_id and x["status"] != "running":
                    return x
            return None

        session = wait_for(find, "the session")
        self.assertEqual(session["agent"], "sdk-e2e")
        self.assertEqual(session["title"], "SDK end to end")
        self.assertEqual(session["status"], "review")  # a file was edited
        self.assertEqual(session["lines_added"], 3)
        self.assertEqual(session["input_tokens"], 1000)
        self.assertEqual(session["cost_source"], "reported")
        self.assertAlmostEqual(session["best_cost_usd"], 0.06)

        child = get("/api/v1/sessions/" + urllib.parse.quote(sub.id, safe=""))
        self.assertEqual((child["parent_id"], child["actor_type"], child["status"]), (s.session_id, "reviewer", "done"))

        priced = get("/api/v1/sessions/" + urllib.parse.quote(s2.session_id, safe=""))
        self.assertEqual(priced["cost_source"], "computed")
        self.assertGreater(priced["best_cost_usd"], 0)


if __name__ == "__main__":
    unittest.main()
