# web

The Shiplino web app (Next.js static export, React, TypeScript). See [docs/how-it-works.md](../docs/how-it-works.md).

- `packages/ui`: `@shiplino/ui`, shared components (project overview, per-project board, sprints, session timeline, virtual office, insights). Also used by the hosted app.
- `apps/local`: the app that is built and embedded into the `shiplino` binary.
- `e2e`: browser tests ([Playwright](https://playwright.dev), Chromium) against the real binary.

## Browser tests

`make e2e` builds `bin/shiplino`, then runs `npm run e2e`. The global setup starts `shiplino daemon` with a temp `HOME` and `SHIPLINO_HOME` (never your real data), seeds sessions by piping Claude Code hook payloads through `shiplino hook`, and stops the daemon at the end. Tests fail on any console error or page error.

```bash
cd web && npx playwright install chromium   # once
make e2e                                     # build + test
cd web/e2e && npx playwright test --ui       # debug, against an existing bin/shiplino
```

Set `SHIPLINO_BIN` to test another binary. On failure, the daemon log is in `e2e/daemon.log` and traces are in `e2e/test-results/` (`npx playwright show-trace <trace.zip>`). CI uploads both, plus the HTML report.
