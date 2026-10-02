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

## A history rewrite does not redact

Anything pushed to a public GitHub repository is disclosed from that moment,
and a history rewrite does not redact it. A force push, a rebase or squash over
the commit, deleting its branch or tag, or starting a fresh root makes the
commit unreachable. It does not remove it from GitHub:

- GitHub keeps unreachable objects and serves them by SHA, without
  authentication: the commit, its tree and its file contents, through the API
  and the web view. The SHA is already out, in the clones, forks, CI logs,
  links and public event feed of the time when the commit was reachable.
- A commit in a pull request's final head stays referenced by that pull
  request after its branch is deleted, and no push removes that reference.
- Only GitHub Support garbage-collects such objects, on request, and only for
  sensitive data it judges rotation cannot mitigate. Nothing reaches the forks,
  clones and mirrors made while the content was public.

Removal from `main` is therefore cosmetic: the repository looks clean and the
content is still served. The checks here cannot tell the difference. The
publication guard reads the working tree and the credential scan reads the
history a clone can reach, so both stay green while old objects remain
fetchable. Never describe content as removed because a rewrite ran.

When sensitive material is found in public history, act in this order:

1. **Record the exposure.** Write down every affected commit SHA and what it
   carried before anything changes. A rewrite done first destroys that
   evidence, and with it the list of what to rotate.
2. **Rotate.** A credential that was ever pushed public is revoked and
   replaced. Any other private material is treated as already published, and
   its owner decides what follows on that basis.
3. **Decide in writing whether to purge or accept.** Accepting is defensible
   when the content was meant to be public anyway; it is not when the content
   is what the cleanup set out to remove. A purge is a Support request naming
   every recorded SHA, made after a rewrite has removed each branch and tag
   that points at them; Support removes the pull-request references itself.

A rewrite on its own changes what the repository shows, not what has been
disclosed. GitHub documents its side of this in
[Removing sensitive data from a repository](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository).

### Verify the effect, not the execution

A control must verify its effect, not its execution. "The rewrite ran", "the
new root was pushed" and "the guard is green on `main`" each record that a step
was performed, and none of them shows the exposure is gone. A control that
passes because its step happened can pass while the risk it exists for is
untouched.

The effect check for history is an unauthenticated request for each commit the
cleanup was meant to remove, made as a stranger would make it:

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  "https://api.github.com/repos/<owner>/<repo>/commits/<sha>"
```

`200` means GitHub still serves the commit; `422` (`No commit found for SHA`)
means it does not. Anything else, such as a `403` from the unauthenticated rate
limit, answers neither way: retry later. Record the result for every SHA, and
run the same probe again after any purge: a purge is done when the probe says
so, not when the ticket closes.

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
the value the checker names. See `AGENTS.md` § _Security and publication boundary_.
