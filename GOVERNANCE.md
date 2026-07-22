# Governance

This document describes how Nightgauge is governed: who makes decisions, how
someone becomes a maintainer, what happens if maintainers become unavailable,
and the boundary of the open-source project.

## Stewardship and decision model

Nightgauge is stewarded by **Edibu, LLC** and currently operates under a
**single-maintainer (BDFL) model**. The lead maintainer has final say on
technical direction, what is merged, and releases. This is deliberate for a
project at this stage: it keeps direction coherent and decisions fast.

In practice, decisions are made in the open — through issues, pull requests, and
discussions — and the maintainer's role is to review contributions, keep quality
high, and say no when something doesn't fit. Disagreement is welcome; it is
resolved by discussion, and the lead maintainer breaks ties.

As the contributor base grows, this model is expected to evolve toward a small
maintainer team with shared merge rights (below).

## Becoming a maintainer

There is no application form. Maintainers are invited based on a sustained track
record:

- A history of high-quality, merged contributions.
- Sound review comments on others' pull requests.
- Good judgment about scope, security, and the open-core boundary.
- Reliability and constructive conduct, consistent with the
  [Code of Conduct](CODE_OF_CONDUCT.md).

When someone demonstrates this over time, the lead maintainer invites them to the
`@nightgauge/maintainers` team, which carries review and merge responsibility.
Maintainer status can be stepped down at any time, and may be removed for
inactivity or conduct.

## Continuity and succession

Because Edibu, LLC is the corporate steward, project continuity does not depend
on any single individual's account. If the lead maintainer becomes unavailable:

- Edibu, LLC retains ownership of the repositories, the `nightgauge` GitHub
  organization, the trademarks, and the release/publishing credentials.
- Remaining members of `@nightgauge/maintainers` (once the team is larger than
  one) continue review and release duties.
- If no active maintainer remains, Edibu, LLC is responsible for appointing one
  or for clearly communicating the project's status.

## License commitment

The open-source components of Nightgauge — the VSCode extension, SDK, skills,
Claude plugin, and Go binary — are licensed under the **Apache License, Version
2.0**, and we intend to keep it that way. Contributions are accepted under the
[Contributor License Agreement](CLA/README.md); the CLA preserves the
flexibility to adjust licensing **only if needed to protect the project's
long-term viability**. It is a safeguard, not a signal of any intent to close
the open core.

## Project boundary

The Apache-2.0 project includes the local pipeline, VS Code extension, SDK,
skills, Claude plugin, and Go binary. Contributions should implement or document
the public product, its local operation, or a public integration contract.

Private service implementations, company strategy, commercial plans, and
internal operations are not governed in this repository. If a proposed change
depends on one of those areas, discuss the public contract in an issue before
implementing it. See [VISION.md](VISION.md) and
[docs/DOCUMENTATION_IA.md](docs/DOCUMENTATION_IA.md). The operational intake and
classification rules are defined in
[docs/PUBLIC_CORE_BOUNDARY.md](docs/PUBLIC_CORE_BOUNDARY.md).

## Related documents

- [MAINTAINERS.md](MAINTAINERS.md) — who maintains the project.
- [CONTRIBUTING.md](CONTRIBUTING.md) — how to contribute.
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — expected conduct.
- [SECURITY.md](SECURITY.md) — reporting vulnerabilities.
- [CLA/README.md](CLA/README.md) — the Contributor License Agreement.
