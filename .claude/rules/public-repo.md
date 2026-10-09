# This repository is public

Everything committed here is published under FSL-1.1-Apache-2.0 (see `LICENSE`), including code comments, docs, commit messages, issue templates and this `.claude/` folder.

## OK to write here

- Source code, tests, fixtures (redacted), benchmarks, schemas
- User and contributor docs: how it works, event format, adapters, CLI usage, privacy behavior, configuration
- "Planned" feature lists **without dates or estimates**
- Neutral, factual descriptions of supported agents

## Never write here

- Roadmap dates, milestones with estimates, internal priorities or launch plans
- Pricing, paid tiers, revenue, business strategy, competitor analysis
- Details of the hosted service beyond the public sync protocol. The sync **client** (`internal/sync`) and its protocol are public on purpose. The server side isn't documented here.
- References to private repositories, private docs or internal hostnames. (The UI inspiration is credited openly in README "Acknowledgments" and `NOTICE`; keep that credit, and keep our UI code our own.)
- Customer, company or personal names (other than authors in git metadata and credited upstream authors), emails, private links
- Secrets of any kind (CI runs gitleaks). Hook payload fixtures must be redacted: no real prompts, paths with real usernames, or tokens.
- Committed Claude Code settings that enable data-sharing plugins (e.g. Supermemory). Personal choices go in `.claude/settings.local.json`.

## Third-party code

- Keep license headers. New Go files start with:
  ```go
  // Copyright 2026 The Shiplino Authors
  // SPDX-License-Identifier: FSL-1.1-Apache-2.0
  ```
- Code copied from third-party projects keeps its copyright notice (MIT and Apache require it). Add it to `NOTICE` the first time. Prefer writing our own code with the shadcn CLI over copying files from other projects.
- Only use dependencies with permissive licenses (MIT, BSD, Apache-2.0, ISC). Ask before adding anything else.
