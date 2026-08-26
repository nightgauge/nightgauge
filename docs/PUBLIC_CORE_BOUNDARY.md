# Public Core Boundary

Nightgauge uses an open-core model. This repository contains the Apache-2.0
local product: the Go CLI, VS Code extension, TypeScript SDK, portable skills,
Claude plugin, and public integration contracts.

## Which repositories are public

Three, and each is public for a different reason:

| Repository                                                              | Why it is public                                                                                                                                                                                                                          |
| ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| [`nightgauge/nightgauge`](https://github.com/nightgauge/nightgauge)     | The Apache-2.0 product itself — CLI, extension, SDK, skills. This is the open core.                                                                                                                                                       |
| [`nightgauge/.github`](https://github.com/nightgauge/.github)           | Organization community-health defaults: security policy, code of conduct, support and contribution guidance. GitHub serves these for any repository without its own, so they must be readable by anyone who might report a vulnerability. |
| [`nightgauge/homebrew-tap`](https://github.com/nightgauge/homebrew-tap) | A distribution channel. `brew install` fetches from it, so users must be able to inspect what they are installing and verify its provenance.                                                                                              |

Everything else in the organization is private, and this document's _What stays
private_ section describes the kind of material that lives there. That
distinction is about **content class, not secrecy for its own sake**: the open
core is the part you can run entirely on your own machine with your own model
credentials, and it is complete on its own terms.

The boundary is enforced mechanically rather than by convention — see
_Intake and enforcement_ below.

## What belongs here

- Features that run locally with credentials and model subscriptions controlled
  by the user.
- Reliability, security, accessibility, documentation, and developer-experience
  improvements to the public components.
- Provider-neutral interfaces and public contracts for optional services.
- Reproducible bugs and public roadmap proposals that can be discussed without
  private operational context.

## What stays private

- Hosted-service implementation, infrastructure, deployment topology, and
  incident response.
- Pricing, packaging strategy, commercial forecasts, customer information, and
  internal product research.
- Private repository names combined with issue numbers, internal project-board
  state, company operations, credentials, or unpublished partner plans.
- Raw spikes, epics, estimates, decision logs, and generated agent memory unless
  deliberately rewritten as stable public documentation.

## Intake and enforcement

External issues are always human-triaged. Checking a box or adding text to an
issue never authorizes autonomous execution. Only a maintainer may apply an
automation label after reviewing the content and confirming this boundary.

Every public feature request and pull request must pass the boundary checklist.
The publication manifest and CI reject known internal artifact classes, and the
certified release export is built from an immutable reviewed commit.

When a proposal spans public and private surfaces, create a public issue only
for the local capability or public contract. Track private implementation and
commercial work separately; never link private issue numbers from the public
repository.
