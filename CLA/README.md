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
bot posts a comment asking you to agree. Reply on the pull request with a
comment containing exactly:

> I have read the CLA Document and I hereby sign the CLA

Your agreement is recorded against your GitHub account (login and immutable
numeric user id) for all future contributions, and the `cla` status check
re-runs and turns green. If you have already signed and the check is still red,
comment `recheck`. Contributing on behalf of a company uses
[`corporate.md`](corporate.md) — see the corporate flow below.

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

## Corporate flow (contributing for an employer)

The entity signature is recorded **out-of-band**: an authorized representative
of the company completes [`corporate.md`](corporate.md) (including the
signatory and authorized-contributor addendum) and sends it to the maintainer,
who records the entity — and the GitHub accounts authorized to contribute on
its behalf — in the private signature store. Any pull request whose author is
mapped to a signed entity then passes the `cla` check without commenting the
individual phrase. To add or remove authorized contributors, the company sends
an updated list.

## Operator setup (one-time, maintainer)

The gate is the first-party workflow
[`.github/workflows/cla.yml`](../.github/workflows/cla.yml) — no third-party
actions; it talks to the GitHub API directly (#164). It needs two things it
cannot create itself. **Until both exist, the check logs a loud "CLA gate NOT
ENFORCED" warning and passes (fail-soft) so development is never blocked; the
moment both exist it enforces (fail-closed) with no workflow edit.**

1. **Create the private signature store repo** `nightgauge/.cla-signatures`.
   Signatures are stored **there**, not in this source tree, so contributor
   identities never become part of the public codebase. Seed it with:

   `README.md`:

   ```markdown
   # nightgauge CLA signatures

   Private signature store for the nightgauge Contributor License Agreement
   (see CLA/ in nightgauge/nightgauge). `signatures/version1/cla.json` is
   written by the CLA workflow via the CLA_SIGNATURES_TOKEN fine-grained PAT —
   do not edit it by hand except to correct an entry.
   `signatures/version1/corporate.json` is maintained by hand from out-of-band
   corporate agreements (CLA/corporate.md). This repo stays PRIVATE: it maps
   GitHub identities to legal agreements.
   ```

   `signatures/version1/cla.json` (the workflow appends entries; each entry
   records login, immutable numeric user id, PR number, agreement-comment URL,
   UTC timestamp, and the CLA document version + commit SHA agreed to):

   ```json
   { "signedContributors": [] }
   ```

   `signatures/version1/corporate.json` (maintained by hand; `authorized_ids`
   is authoritative, `authorized_logins` is a convenience fallback):

   ```json
   {
     "entities": [
       {
         "name": "Example Corp",
         "cla_document": "CLA/corporate.md",
         "cla_version": "HA-CLA-E v1.0 (Option Five)",
         "signed_at": "2026-01-01T00:00:00Z",
         "signatory": "Jane Doe, CTO",
         "agreement_ref": "where the signed agreement is archived",
         "authorized_ids": [123456],
         "authorized_logins": ["example-employee"]
       }
     ]
   }
   ```

2. **Mint a fine-grained PAT** that can write to the store repo: resource owner
   **nightgauge**, repository access limited to **only**
   `nightgauge/.cla-signatures`, repository permission **Contents: read and
   write** (Metadata: read is implied). No other repos, no other permissions.
   (Org settings must allow fine-grained PATs.) Store it as the
   `nightgauge/nightgauge` Actions secret **`CLA_SIGNATURES_TOKEN`**, e.g.
   `gh secret set CLA_SIGNATURES_TOKEN --repo nightgauge/nightgauge`. When the
   token expires or is rotated, update the secret — an invalid token fails the
   check closed with a pointer here.
3. **After the public flip, cut a test PR** from a second account (or ask an
   outside collaborator) and verify end-to-end: the `cla` check fails and the
   bot comments; commenting the agreement phrase commits a signature to
   `nightgauge/.cla-signatures` and the check re-runs green; a second PR from
   the same account passes with no new comment. The check-run name is exactly
   **`cla`** (the job id) — the name the branch ruleset requires (#137).

Bots are allow-listed in the workflow and do not sign. The sole maintainer signs
once like anyone else (the flow is idempotent) or merges under the ruleset's
owner bypass; branch protection handles the solo-maintainer self-review case
(#137).

Adopt exactly one instrument. This repository uses the CLA; there is deliberately
no parallel DCO.
