# Contributor License Agreement

Nightgauge requires every contributor to agree to a Contributor License
Agreement (CLA) before their contribution can be merged. This directory holds
the agreements and explains how they work.

| File                             | For                                           |
| -------------------------------- | --------------------------------------------- |
| [`individual.md`](individual.md) | Anyone contributing on their own account.     |
| [`corporate.md`](corporate.md)   | Anyone contributing on behalf of an employer. |

Both are the **Harmony** contributor agreements (HA-CLA-I / HA-CLA-E, v1.0) with
outbound-license **Option Five** selected — see "Why these are safe to adopt"
below.

## Why a CLA (and not a DCO)

Nightgauge is open-core: the code here is Apache-2.0 and free, and a separate
hosted platform is commercial. A **DCO** only certifies that a contributor had
the right to submit their code — it grants Edibu, LLC nothing beyond Apache-2.0's
inbound=outbound. Under a DCO, the first external contribution would lock that
code to Apache-2.0 forever, removing any ability to offer it under different
terms later.

The **CLA** keeps copyright with the contributor but grants Edibu a broad,
any-license grant (Section 2.3, Option Five). That is what keeps the license
decision reversible: Apache-2.0 ships today, and a different posture (e.g.
dual-license or commercial) remains possible tomorrow. Crucially, Option Five
comes with a **promise back** — Edibu must _also_ keep every contribution
available under the project's open-source license, so a contribution can never
be pulled _out_ of the open version. Contributors get that assurance; the project
keeps its optionality.

## Why these are safe to adopt without bespoke legal counsel

These are not hand-written clauses. They are the **Harmony Agreements** — a set
of standardized contributor agreements drafted by a committee of open-source
lawyers (led from Canonical) specifically so projects do **not** each have to
commission and pay for their own CLA. They are used across the industry and are
themselves published under [CC-BY 3.0](https://www.harmonyagreements.org/) for
exactly this reuse.

What we filled in is limited and non-substantive:

- **Section 2.3 → Option Five** (any license, with the promise-back) — the one
  business choice, and the one that matches the relicensing optionality this
  project wants. The narrower options (One–Four) are in the Harmony template if
  you ever want to tighten the grant.
- **"We/Us" = Edibu, LLC**, steward of Nightgauge (matches `NOTICE`).
- **Governing law = South Dakota** (Edibu, LLC's state of formation).
- **Media license = CC BY 4.0**; submission instructions point to the CLA
  Assistant flow below.

If you later want a lawyer's eyes on it, a fixed-fee review of a standard Harmony
agreement is inexpensive and entirely optional — it is not a launch blocker,
because the CLA only governs the **first external contribution**, not the repo
going public.

## How agreeing works (for contributors)

You don't sign a file or fill in a form. On your **first pull request**, the CLA
gate posts a comment with a short agreement phrase. Reply with the exact
phrase and your agreement is recorded against your GitHub account for all future
contributions — the `cla` status check then turns green. Contributing on behalf
of a company uses [`corporate.md`](corporate.md).

The exact phrase is:

> I have read the CLA Document and I hereby sign the CLA

## Submitting work you do not fully own

If a contribution includes work whose copyright you do **not** entirely own (for
example, code copied from another project, or work your employer owns):

- **Do not** submit it as your own. In the pull request, identify the source and
  its license, and mark the borrowed portion clearly (e.g. "Portions from
  `<project>`, licensed under `<license>`").
- If **your employer** owns rights in what you write, either have an authorized
  representative agree to [`corporate.md`](corporate.md), or obtain your
  employer's written permission/waiver before contributing.
- If you're unsure, open an issue and ask before submitting.

## Operator setup (one-time, maintainer)

The first-party [`.github/workflows/cla.yml`](../.github/workflows/cla.yml)
workflow records agreements without executing code from contributor forks.
Before the check can pass, a maintainer must:

1. **Create a private signatures repository** you control — e.g.
   `nightgauge/.cla-signatures`. Signatures are stored **there**, not in this
   source tree, so contributor identities never become part of the public
   codebase. (The workflow's `remote-organization-name` /
   `remote-repository-name` point at it.)
2. **Create a token** that can write to that signatures repo — a fine-grained PAT
   scoped to only `nightgauge/.cla-signatures` with **Contents: read and write**
   is preferred over a classic `repo`-scoped PAT. Store it as the repository (or
   org) secret **`CLA_SIGNATURES_TOKEN`**.
3. **Cut a test PR** from a second account (or ask an outside collaborator) and
   confirm the `cla` status appears and gates merge.

Bots are allow-listed in the workflow and do not sign. The sole maintainer's own
PRs are covered by the maintainer's owner authority; branch protection still
enforces the configured review policy.

Adopt exactly one instrument. This repository uses the CLA; there is deliberately
no parallel DCO.
