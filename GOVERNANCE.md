# Governance

## Roles

- **Users**: anyone using Shiplino.
- **Contributors**: anyone who has had a contribution merged.
- **Maintainers**: people with merge rights, listed in [MAINTAINERS.md](MAINTAINERS.md). They review PRs, triage issues, cut releases and enforce the code of conduct.
- **Adapter owners**: contributors responsible for a specific agent adapter (listed in `.github/CODEOWNERS` once set up). They are asked to review changes to that adapter.

## Decisions

- Day-to-day changes: one maintainer approval and green CI.
- Larger changes (new event fields, schema versions, capture/privacy changes, new dependencies, licensing): open an issue labeled `proposal`, leave it open for at least 7 days for feedback, and get two maintainer approvals.
- If maintainers disagree, the project lead decides after discussion.

## Becoming a maintainer

Contributors with a track record of high-quality contributions and reviews can be nominated by a maintainer. Approval needs a majority of current maintainers.

## Open core

The code in this repository is licensed under FSL-1.1-ALv2 (free for internal and development use, converting to Apache-2.0 after two years). The hosted Shiplino Cloud (team sync, team workspace) is a separate commercial service. Features merged here won't be removed later to move them behind a paywall.

## Releases

- Semantic versioning. Changes are recorded in [CHANGELOG.md](CHANGELOG.md).
- Release binaries are built by CI from tagged commits, with checksums and signatures.
