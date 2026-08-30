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

## How to write an issue reference

This tree was imported from a predecessor repository whose issue numbers came
with it, and the guard's `issue_references` rule exists because those numbers do
not name anything here. Three forms, and only three:

| Situation                               | Write                         |
| --------------------------------------- | ----------------------------- |
| An issue in this repository             | `#N`                          |
| An issue in another public repository   | `owner/repo#N`                |
| A number inherited from the predecessor | `legacy issue N` — **no `#`** |

`legacy issue N` is de-linked on purpose. The number is kept because it is real
provenance and someone with access can still look it up; the `#` is dropped
because that is the character that turns a note into a claim about _this_
repository's issue N. Rewriting such a reference to a live nightgauge number it
does not correspond to is worse than leaving it dead, and deleting it loses the
reasoning the citation was carrying. Neither of those is the fix.

`owner/repo#N` matters for the same reason in the other direction. A bare `#N`
in prose that has already named another repository still reads, to the forge and
to a human skimming, as a reference to _this_ repository's issue N — the
surrounding words are not part of the link. Qualifying it makes the same citation
correct and takes it out of the burn-down, because it now names the sequence it
belongs to. Three such references in `docs/spikes/` were qualified this way
rather than deleted; the prose already named the repository and only the link was
wrong.

This document cannot show a live example of a bad reference, because writing one
would be one — the guard rejects this file like any other, which is the intended
demonstration.

### Why the count alone cannot tell you how the sweep is going

`issue_references.tree_baseline` counts references above a ceiling that **rises
on its own** as this repository issues numbers. So a reference leaves the count
for two opposite reasons, and they are indistinguishable in the total:

- an edit **retired** it — progress; or
- the rising mark **crossed** it — the reference was a 404 and is now a
  confident live link to unrelated work. It got worse, and the number improved.

The checker reports both populations by name (`retired: <path> #N` and
`crossed (now resolves to unrelated work): <path>:<line> #N`) so the direction is
legible. A crossing is **reported, never gated**: the change that raised the mark
introduced nothing and cannot fix it by editing its own diff. The ratchet on the
count is the gate and is unchanged — and `tree_baseline` is still only lowered to
the value the checker names. See `AGENTS.md` § _Public Core Boundary_.
