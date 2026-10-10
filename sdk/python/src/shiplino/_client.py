# Copyright 2026 The Shiplino Authors
# SPDX-License-Identifier: FSL-1.1-Apache-2.0

"""Shiplino client: report sessions, turns, tool calls and usage from a
custom agent to the local Shiplino daemon (POST /api/v1/ingest).

It never raises into the host agent. Events are queued and sent by a
background thread in batches; when the daemon isn't installed the client
is a no-op, and when it isn't reachable events wait in a bounded queue.
"""

from __future__ import annotations

import atexit
import collections
import itertools
import json
import logging
import os
import re
import secrets
import threading
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable, Deque, Dict, List, Optional, Set

__all__ = ["Shiplino", "Session", "ToolCall", "tool_kind"]

_log = logging.getLogger("shiplino")

_TOOL_KINDS = {
    "edit": "edit",
    "multiedit": "edit",
    "write": "write",
    "read": "read",
    "bash": "shell",
    "shell": "shell",
    "exec": "shell",
    "grep": "search",
    "glob": "search",
    "search": "search",
    "ls": "search",
    "fetch": "web",
    "webfetch": "web",
    "websearch": "web",
    "web": "web",
    "task": "task",
    "agent": "task",
}

_AGENT_NAME = re.compile(r"^[^:/\s]{1,64}$")


def tool_kind(name: str) -> str:
    """The universal tool kind for a tool name ("other" when unknown)."""
    n = name.lower()
    if n.startswith("mcp__") or n.startswith("mcp."):
        return "mcp"
    return _TOOL_KINDS.get(n, "other")


def _summarize(value: Any) -> Optional[str]:
    s: Optional[str] = None
    if isinstance(value, str):
        s = value
    elif isinstance(value, dict):
        for k in ("path", "file_path", "command", "cmd", "query", "pattern", "url"):
            if isinstance(value.get(k), str):
                s = value[k]
                break
        else:
            try:
                s = json.dumps(value, default=str)
            except (TypeError, ValueError):
                s = None
    elif value is not None:
        try:
            s = json.dumps(value, default=str)
        except (TypeError, ValueError):
            s = None
    if s is not None and len(s) > 200:
        s = s[:200] + "…"
    return s


def _read(path: Path) -> Optional[str]:
    try:
        return path.read_text(encoding="utf-8").strip() or None
    except OSError:
        return None


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def _compact(d: Dict[str, Any]) -> Dict[str, Any]:
    return {k: v for k, v in d.items() if v is not None}


class Shiplino:
    """A client for one agent. Create one per process and reuse it::

        shiplino = Shiplino(agent="release-bot")
        session = shiplino.session(title="Cut the release")
    """

    def __init__(
        self,
        agent: str,
        *,
        agent_version: Optional[str] = None,
        url: Optional[str] = None,
        token: Optional[str] = None,
        home: Optional[str] = None,
        flush_interval: float = 1.0,
        batch_size: int = 100,
        max_queue: int = 10_000,
        timeout: float = 5.0,
        on_warning: Optional[Callable[[str], None]] = None,
    ) -> None:
        """
        agent: your agent's name, up to 64 characters without ":", "/" or spaces.
        url: daemon address; default http://127.0.0.1:<port from the Shiplino home>.
        token: API token; default the ``token`` file in the Shiplino home.
        home: Shiplino home; default $SHIPLINO_HOME, else ~/.shiplino.
        flush_interval: seconds between background sends.
        batch_size: events per request; reaching it also triggers a send.
        max_queue: events kept while the daemon is unreachable; the oldest go first.
        on_warning: where warnings go (each kind once); default the "shiplino" logger.
        """
        self.agent = agent if isinstance(agent, str) else ""
        self._agent_version = agent_version
        self._warn = on_warning or _log.warning
        self._warned: Set[str] = set()
        self._batch_size = max(1, min(int(batch_size), 1000))
        self._max_queue = max(self._batch_size, int(max_queue))
        self._timeout = timeout
        self._interval = flush_interval
        self._queue: Deque[Dict[str, Any]] = collections.deque()
        self._lock = threading.Lock()  # guards the queue and counters
        self._send_lock = threading.Lock()  # one sender at a time
        self._wake = threading.Event()
        self._stop = threading.Event()
        self._thread: Optional[threading.Thread] = None
        self._retry_at = 0.0  # no size-triggered sends before this, after a failed send
        self._sent = self._duplicates = self._dropped = self._rejected = 0
        self._last_error: Optional[str] = None
        # Plain HTTP to 127.0.0.1: never through a proxy from the environment.
        self._opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        h = Path(home or os.environ.get("SHIPLINO_HOME") or Path.home() / ".shiplino")
        self._token = token or _read(h / "token")
        port = _read(h / "port") or "4777"
        self._url = (url or "http://127.0.0.1:" + port).rstrip("/")

        if not isinstance(agent, str) or not _AGENT_NAME.match(agent):
            self._warn_once("agent", 'invalid agent name %r: use up to 64 characters without ":", "/" or spaces. Not recording.' % (agent,))
            self.enabled = False
        elif not self._token:
            self._warn_once("token", "no API token in %s: is Shiplino installed? Not recording." % (h / "token"))
            self.enabled = False
        else:
            self.enabled = True
            self._thread = threading.Thread(target=self._run, name="shiplino-flush", daemon=True)
            self._thread.start()
            atexit.register(self.close)

    def session(
        self,
        title: Optional[str] = None,
        cwd: Optional[str] = None,
        *,
        model: Optional[str] = None,
        id: Optional[str] = None,
    ) -> "Session":
        """Starts a session (one task or run of your agent)."""
        if cwd is None:
            try:
                cwd = os.getcwd()
            except OSError:
                cwd = None
        root = _Root("%s:%s" % (self.agent, id or uuid.uuid4()), cwd)
        s = Session(self, root, root.session_id)
        s._emit("session.start", _compact({"title": title, "model": model}))
        return s

    @property
    def stats(self) -> Dict[str, Any]:
        """Counters: enabled, queued, sent, duplicates, dropped (queue full),
        rejected (refused by the daemon) and last_error."""
        with self._lock:
            return {
                "enabled": self.enabled,
                "queued": len(self._queue),
                "sent": self._sent,
                "duplicates": self._duplicates,
                "dropped": self._dropped,
                "rejected": self._rejected,
                "last_error": self._last_error,
            }

    def flush(self) -> None:
        """Sends everything queued now (blocking). Never raises."""
        if not self.enabled:
            return
        try:
            with self._send_lock:
                self._drain()
        except Exception as e:  # never raise into the agent
            self._fail("internal", "flush failed: %r" % (e,))

    def close(self) -> None:
        """Stops the background thread and flushes. Runs at exit too."""
        if not self.enabled:
            return
        self._stop.set()
        self._wake.set()
        t = self._thread
        if t is not None and t is not threading.current_thread():
            t.join(timeout=self._timeout + 1)
        self.flush()
        try:
            atexit.unregister(self.close)
        except Exception:
            pass

    # Internals

    def _enqueue(self, e: Dict[str, Any]) -> None:
        if not self.enabled:
            return
        e["agent"] = _compact({"name": self.agent, "version": self._agent_version})
        with self._lock:
            self._queue.append(e)
            self._trim()
            full = len(self._queue) >= self._batch_size
        if full and time.monotonic() >= self._retry_at:
            self._wake.set()
        if self._stop.is_set():  # closed: nothing will send it later
            self.flush()

    def _trim(self) -> None:  # with self._lock held
        over = len(self._queue) - self._max_queue
        if over > 0:
            for _ in range(over):
                self._queue.popleft()
            self._dropped += over
            self._warn_once("dropped", "queue full (%d events): dropping the oldest. See stats['dropped']." % self._max_queue)

    def _run(self) -> None:
        while not self._stop.is_set():
            self._wake.wait(self._interval)
            self._wake.clear()
            if not self._stop.is_set():
                self.flush()

    def _drain(self) -> None:
        while True:
            with self._lock:
                n = min(self._batch_size, len(self._queue))
                batch = [self._queue.popleft() for _ in range(n)]
            if not batch:
                return
            if not self._send(batch):
                self._retry_at = time.monotonic() + self._interval
                with self._lock:
                    self._queue.extendleft(reversed(batch))  # retry later, first
                    self._trim()
                return

    def _send(self, batch: List[Dict[str, Any]]) -> bool:
        """Sends one batch. False means "keep it and retry later"."""
        req = urllib.request.Request(
            self._url + "/api/v1/ingest",
            data=json.dumps(batch, default=str).encode("utf-8"),
            headers={"Authorization": "Bearer " + str(self._token), "Content-Type": "application/json"},
            method="POST",
        )
        status, body = 0, b""
        try:
            with self._opener.open(req, timeout=self._timeout) as resp:
                status, body = resp.status, resp.read()
        except urllib.error.HTTPError as e:
            with e:
                status, body = e.code, e.read() or b""
        except Exception as e:  # URLError, timeouts, resets: retry later
            reason = getattr(e, "reason", e)
            self._fail("unreachable", "daemon not reachable at %s (%s); keeping events and retrying." % (self._url, reason))
            return False
        try:
            res = json.loads(body.decode("utf-8")) if body else {}
            if not isinstance(res, dict):
                res = {}
        except ValueError:
            res = {}
        if status >= 500 or status == 429:
            self._fail("server", "daemon answered %d; keeping events and retrying." % status)
            return False
        if status != 200 and status != 400:
            # Bad token (401), too large (413) and the like: retrying won't help.
            with self._lock:
                self._rejected += len(batch)
            self._fail("status-%d" % status, ("daemon refused %d events: %d %s" % (len(batch), status, res.get("error", ""))).strip())
            return True
        errors = res.get("errors") or []
        with self._lock:
            self._sent += int(res.get("accepted") or 0)
            self._duplicates += int(res.get("duplicates") or 0)
            self._rejected += len(errors)
            if res.get("paused"):
                self._rejected += len(batch) - len(errors)
        if errors:
            self._fail("invalid", "daemon rejected an event: %s" % errors[0].get("error"))
        if res.get("paused"):
            self._fail("paused", "recording is paused in Shiplino; events are not stored.")
        return True

    def _fail(self, kind: str, message: str) -> None:
        with self._lock:
            self._last_error = message
        self._warn_once(kind, message)

    def _warn_once(self, kind: str, message: str) -> None:
        if kind in self._warned:
            return
        self._warned.add(kind)
        try:
            self._warn("shiplino: " + message)
        except Exception:
            pass  # a broken warning handler must not break the agent


class _Root:
    def __init__(self, session_id: str, cwd: Optional[str]) -> None:
        self.session_id = session_id
        # Random per Session object, so a resumed session id never reuses dedup keys.
        self.instance = secrets.token_hex(4)
        self.seq = itertools.count(1)
        self.cost = 0.0
        self.cost_lock = threading.Lock()
        self.cwd = cwd


class ToolCall:
    """A tool call in progress. Use as a context manager, or call end()."""

    def __init__(self, session: "Session", id: str, name: str) -> None:
        self._session = session
        self.id = id
        self._name = name
        self._started = time.monotonic()
        self._done = False

    def end(self, ok: bool = True, error: Optional[str] = None) -> None:
        """Records the end of the call; the duration is measured for you."""
        if self._done:
            return
        self._done = True
        self._session._emit(
            "tool.end",
            _compact(
                {
                    "tool_call_id": self.id,
                    "tool": tool_kind(self._name),
                    "tool_raw": self._name,
                    "ok": ok,
                    "duration_ms": int((time.monotonic() - self._started) * 1000),
                    "error": error,
                }
            ),
        )

    def __enter__(self) -> "ToolCall":
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        if exc is None:
            self.end()
        else:
            self.end(False, str(exc) or exc_type.__name__)


class Session:
    """A session, or a subagent of one. All methods are fire-and-forget."""

    def __init__(
        self,
        client: Shiplino,
        root: _Root,
        id: str,
        parent: Optional["Session"] = None,
        type: Optional[str] = None,
    ) -> None:
        self._client = client
        self._root = root
        self.id = id  # for subagents: "<session id>/sub:<id>"
        self._parent = parent
        self._type = type
        self._ended = False
        self._turn_open = False

    @property
    def session_id(self) -> str:
        """The top-level session id this one belongs to."""
        return self._root.session_id

    def turn(self, prompt: str) -> None:
        """Starts a turn (a prompt and the work it causes). Ends the previous turn."""
        if self._turn_open:
            self.end_turn()
        self._turn_open = True
        self._emit("turn.start", {"prompt": prompt})

    def end_turn(self, status: str = "ok", error: Optional[str] = None) -> None:
        """Ends the current turn. A session with file edits moves to Review, otherwise to Done."""
        self._turn_open = False
        self._emit("turn.end", _compact({"status": status, "error": error}))

    def tool(self, name: str, input: Any = None) -> ToolCall:
        """Starts a tool call. Call .end() on the result, or use it in a with block."""
        call = ToolCall(self, "t%s-%d" % (self._root.instance, next(self._root.seq)), name)
        self._emit(
            "tool.start",
            _compact({"tool_call_id": call.id, "tool": tool_kind(name), "tool_raw": name, "input_summary": _summarize(input)}),
        )
        return call

    def shell(self, command: str, exit_code: int, duration_ms: Optional[int] = None) -> None:
        """Records a shell command that ran."""
        self._emit("shell.exec", _compact({"command": command, "exit_code": exit_code, "duration_ms": duration_ms}))

    def file_edit(self, path: str, added: int = 0, removed: int = 0) -> None:
        """Records a file edit with the line counts your agent knows."""
        self._emit(
            "file.edit",
            {"path": path, "op": "edit", "tool": "edit", "lines_added": added, "lines_removed": removed, "lines_source": "reported"},
        )

    def usage(
        self,
        model: str,
        input_tokens: int = 0,
        output_tokens: int = 0,
        cache_read: int = 0,
        cache_write: int = 0,
        cost_usd: Optional[float] = None,
        message_id: Optional[str] = None,
    ) -> None:
        """Records one model response's token usage. Pass cost_usd if your agent
        knows it; otherwise the daemon prices the tokens itself."""
        data = _compact(
            {
                "model": model,
                "message_id": message_id,
                "input_tokens": input_tokens,
                "output_tokens": output_tokens,
                "cache_read_tokens": cache_read,
                "cache_write_tokens": cache_write,
            }
        )
        reported = isinstance(cost_usd, (int, float)) and not isinstance(cost_usd, bool) and cost_usd >= 0 and cost_usd != float("inf")
        if reported:
            data["cost_usd"] = float(cost_usd)  # type: ignore[arg-type]
            data["cost_source"] = "reported"
        self._emit("usage", data)
        if reported:
            # The agent's own running total, so the session shows its cost
            # as reported rather than computed.
            with self._root.cost_lock:
                self._root.cost += float(cost_usd)  # type: ignore[arg-type]
                total = self._root.cost
            self._emit(
                "usage",
                {
                    "report": True,
                    "cost_source": "reported",
                    "process": "sdk:%s:%s" % (self._root.session_id, self._root.instance),
                    "total_cost_usd": total,
                },
                at_root=True,
            )

    def waiting(self, message: str) -> None:
        """The agent is waiting for the user."""
        self._emit("waiting.start", {"reason": "input", "message": message})

    def resumed(self) -> None:
        """The user answered; the agent continues."""
        self._emit("waiting.end", {"resolution": "resumed"})

    def set_title(self, title: str) -> None:
        """Renames the session's card."""
        self._emit("session.update", {"title": title})

    def subagent(self, type: str) -> "Session":
        """Starts a subagent and returns it as a child session."""
        child_id = "%s/sub:%s" % (self.id, secrets.token_hex(6))
        self._emit("subagent.start", {"child_session_id": child_id, "agent_type": type})
        return Session(self._client, self._root, child_id, self, type)

    def end(self, status: str = "ok", error: Optional[str] = None) -> None:
        """Ends the session (or subagent). "error" marks it failed."""
        if self._ended:
            return
        if self._turn_open or status == "error":
            self.end_turn(status, error)
        if self._parent is not None:
            self._parent._emit(
                "subagent.end",
                {"child_session_id": self.id, "agent_type": self._type, "status": "done" if status == "ok" else status},
            )
        else:
            self._emit("session.end", {"status": status})
        self._ended = True

    def __enter__(self) -> "Session":
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        if exc is None:
            self.end()
        else:
            self.end("error", str(exc) or exc_type.__name__)

    def _emit(self, kind: str, data: Dict[str, Any], at_root: bool = False) -> None:
        if self._ended:
            return
        try:
            e: Dict[str, Any] = {
                "id": "%s-%d" % (self._root.instance, next(self._root.seq)),
                "v": 1,
                "ts": _now(),
                "kind": kind,
                "session_id": self._root.session_id,
                "data": data,
            }
            if self._root.cwd:
                e["project"] = {"cwd": self._root.cwd}
            if self._parent is not None and not at_root:
                e["actor_id"] = self.id
                e["parent_actor"] = self._parent.id
                e["actor_type"] = self._type
            self._client._enqueue(e)
        except Exception:
            pass  # never raise into the agent
