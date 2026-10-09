# Contributing to Shiplino

Thanks for helping! This guide covers how to propose changes and the rules that keep Shiplino fast, private and zero-token.

## Ways to contribute

- **Add or fix an agent adapter.** This is the most valuable contribution (see below).
- Report bugs with real (redacted!) hook payloads or transcript lines.
- Improve docs, the web UI, or the virtual office art.
- Review open pull requests.

## Before you start

- For anything bigger than a small fix, **open an issue first** so we can agree on the approach.
- Look for issues labeled `good first issue` or `adapter`.

## Development setup

```bash
git clone https://github.com/elephaant/shiplino
cd shiplino
make build && make test
```

## The non-negotiable rules

These protect users. Pull requests that break them won't be merged.

1. **Zero tokens.** The hook path (`shiplino hook`) must never write to stdout or stderr, must always exit 0, and must never return JSON that changes agent behavior. Never add instructions to `CLAUDE.md`, `AGENTS.md` or rules files. See [docs/how-it-works.md](docs/how-it-works.md).
2. **Fast hooks.** The hook path does no network calls, no database access and no config parsing. Target: < 5 ms p99.
3. **Observe only.** Shiplino never blocks, edits or redirects agent actions.
4. **Privacy.** Nothing leaves the machine unless the user enabled sync or an integration. New captured fields must respect capture levels and redaction.
5. **Safe config edits.** Edit agent config files only with real parsers, atomically, with backups. Never touch a file that fails to parse.

## Adding an agent adapter

1. Create `pkg/adapters/<agent>/` with `detect.go`, `install.go`, `parse.go`, and optionally `transcript.go`.
2. Add real, **redacted** fixtures to `pkg/adapters/<agent>/testdata/<agent-version>/` and golden expected events.
3. Map native tool names to normalized ones (docs/event-format.md).
4. Support subagents if the agent reports them.
5. Add `doctor` checks and update the supported agents table in README.md.

Full guide: [docs/adding-an-adapter.md](docs/adding-an-adapter.md).

## Commit and PR guidelines

- **Sign off every commit** (Developer Certificate of Origin): `git commit -s`. This certifies that you have the right to submit the code under the project's license.
- Keep PRs focused. One logical change per PR.
- Write clear commit messages: `adapter/codex: parse SubagentStart payloads`.
- Add or update tests. CI must be green.
- Run `make lint test` before pushing.

## Never commit

- Secrets of any kind (API keys, tokens, `.env` files, private keys). CI runs a secret scanner.
- Real, unredacted prompts, transcripts or code from private projects in fixtures.
- Your local Shiplino data (`~/.shiplino/`), databases, or build output.
- Third-party art or code with an incompatible or unknown license.

## Reviews and releases

Maintainers review PRs, usually within a week. Releases follow [semantic versioning](https://semver.org/) and are listed in [CHANGELOG.md](CHANGELOG.md). See [GOVERNANCE.md](GOVERNANCE.md) for how decisions are made.
