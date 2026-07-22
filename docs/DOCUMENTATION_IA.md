# Documentation Publication Boundary

This repository is intended for public use. Documentation must help a user,
contributor, security reviewer, or integrator understand the open-source
product.

## Content that belongs here

- Installation, configuration, operation, troubleshooting, and security
- Public architecture and extension contracts
- Contributor workflows and governance
- Stable design decisions about the open-source implementation
- Synthetic examples and reusable templates
- Release notes and migration instructions for shipped behavior

## Content that does not belong here

- Company positioning, competitive analysis, or commercial plans
- Pricing models, margins, sales plans, or launch sequencing
- Private service architecture, deployment topology, or operational runbooks
- Cross-repository plans involving private repositories
- Unreleased product roadmaps and internal prioritization
- Real customer, credential, telemetry, or incident data
- Raw company research, spike artifacts, and epic execution summaries

Those materials belong in the private `nightgauge-internal` repository. Public
documents may state an integration contract or current user-visible behavior,
but must not explain a private implementation behind that contract.

## Generated workflow artifacts

Nightgauge supports repository-local artifacts such as `docs/spikes/` and
`docs/epics/`. Their contents are owned by the repository in which a user runs
the pipeline. In this project, generated artifacts require an explicit
publication review before being committed.

## Enforcement

`.github/publication-boundary.yaml` is fail-closed: every tracked path must be
classified. It also assigns stricter content rules to paths likely to contain
planning or private implementation details. Passing the automated check is
necessary but does not replace human publication review.

When uncertain, document the public contract here and keep the business or
private implementation rationale internal.
