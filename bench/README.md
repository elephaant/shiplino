# Benchmarks

End-to-end load tests for the daemon: hook payloads go through the real shim into the spool, the daemon ingests them, and a WebSocket client on the local API measures when each one reaches the live view.

```bash
go test ./bench           # short variant, a few seconds; runs in CI and checks correctness only
make bench                # full size; fails if a target is missed
```

| Test | What it measures | Target (full run) |
|------|------------------|-------------------|
| `TestLoad` | 50 sessions × 4 subagents × 10 tool calls/s (4,000 hooks/s) for 20 s: hook → `OnChange` and hook → WebSocket latency, per-commit time, board query time under load. Checks every tool call was counted once | hook → live view p95 < 500 ms |
| `TestIngestThroughput` | One daemon pass over a 200,000-line spool backlog | > 20,000 events/s |
| `TestBoardQueryWithHistory` | Board of a project with 2,000 past sessions × 4 subagents | p95 < 30 ms |
| `TestInsightsWithHistory` | Insights over 2,000 sessions × 40 tool calls, a third with failures and retry loops: the endpoint, and the failure report built from the sessions the engine folded | failure report p95 < 30 ms |

Notes:

- Set `SHIPLINO_BENCH_BIN=./bin/shiplino` to spawn `shiplino hook` for every payload instead of calling the shim in-process. At 4,000 hooks/s, process start-up then dominates the CPU of a laptop.
- Results depend on the disk: run with `TMPDIR=/dev/shm` to take it out and measure the code alone.
- Results are in [docs/how-it-works.md](../docs/how-it-works.md#performance).
