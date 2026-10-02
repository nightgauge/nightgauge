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
certified release export is built from an immutable reviewed commit. The same
check runs before a push to this repository leaves the machine; see _Checked
before it is pushed_ below.

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
  and the web view. An abbreviated SHA, such as the seven characters a log
  prints, is enough. The SHA is already out, in the clones, forks, CI logs,
  links and public event feed of the time when the commit was reachable.
- One pushed branch or tag publishes every commit it reaches, not only its tip.
- The repositories in a fork network share their Git data, so a commit pushed
  to any fork is served through this repository too, even after that fork is
  deleted. A private repository that is made public publishes what it holds the
  same way, including commits pushed while it was private that no ref reaches.
- A commit in a pull request's final head stays referenced by that pull
  request after its branch is deleted, and no push removes that reference.
- Only GitHub Support garbage-collects such objects and removes their cached
  views, on request, and only for sensitive data it judges rotation cannot
  mitigate. Nothing reaches the forks, clones and mirrors made while the
  content was public.

Removal from `main` is therefore cosmetic: the repository looks clean and the
content is still served. The checks here cannot tell the difference. The
publication guard reads the working tree and the credential scan reads the
history a clone can reach, so both stay green while old objects remain
fetchable. Never describe content as removed because a rewrite ran.

When sensitive material is found in public history, act in this order:

1. **Record the exposure, privately.** Write down every affected commit SHA and
   what it carried before anything changes, in a maintainer's private record or
   a report made as [SECURITY.md](../SECURITY.md) describes. Never record it in
   a public issue, pull request, commit or discussion: a public record names
   the SHAs to fetch. Take the list from a clone that still holds the history
   (`git rev-list <tip>` for each branch or tag that was pushed), not from the
   refs you remember. A rewrite done first destroys that evidence, and with it
   the list of what to rotate.
2. **Rotate.** A credential that was ever pushed public is revoked and
   replaced. Any other private material is treated as already published, and
   its owner decides what follows on that basis.
3. **The owner decides in writing whether to purge or accept.** That decision,
   and any rewrite it needs, belongs to the repository owner and is made and
   verified outside this tree. Accepting is defensible when nothing in the
   recorded commits is sensitive once credentials are rotated; it is not when
   they carry private material that rotation cannot take back. A purge is a
   Support request naming every recorded SHA, made once no branch, tag or fork
   references them; Support removes the pull-request references itself.
   Support declines data it does not judge sensitive, so no purge is assured.

A rewrite on its own changes what the repository shows, not what has been
disclosed. GitHub documents its side of this in
[Removing sensitive data from a repository](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/removing-sensitive-data-from-a-repository)
and
[About permissions and visibility of forks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/working-with-forks/about-permissions-and-visibility-of-forks).

### Verify the effect, not the execution

A control must verify its effect, not its execution. "The rewrite ran", "the
new root was pushed" and "the guard is green on `main`" each record that a step
was performed, and none of them shows the exposure is gone. A control that
passes because its step happened can pass while the risk it exists for is
untouched.

The effect check for history is two unauthenticated requests for each commit
the cleanup was meant to remove, made as a stranger would make them:

```bash
curl -s -o /dev/null -w '%{http_code}\n' \
  "https://api.github.com/repos/<owner>/<repo>/commits/<sha>"
curl -s -o /dev/null -w '%{http_code}\n' \
  "https://github.com/<owner>/<repo>/commit/<sha>"
```

From the API, `200` means GitHub still serves the commit and `422` (`No commit
found for SHA`) means it does not. From the web view, `200` means served and
`404` means not. A commit is gone only when both say so, because GitHub
removes cached views in a step of its own. A `403`, `429` or `5xx` answers
neither way: retry later, and pace a long list, because the unauthenticated API
allows 60 requests an hour. A `404` from the API, or a `301` from either, means
the URL does not name a public repository (a typo, a private repository, a
rename): fix the URL, because retrying will not change the answer. Record the
result for every SHA, and run both probes again after any purge: a purge is
done when the probes say so, not when the ticket closes.

## Checked before it is pushed

CI runs the publication guard on pull requests and in the merge queue. That is
after GitHub has stored the pushed objects, and a branch or tag that never
becomes a pull request is not checked at all. As the section above explains,
nothing done afterwards takes the content back. So the repository's `pre-push`
hook, `.husky/pre-push`, runs `scripts/publication-push-guard.sh` before a push
leaves the machine. The hook examines a push only when its URL names this
repository. It matches on the URL's path, so any transport or SSH host alias
counts. For every branch or tag the push updates:

- A deletion publishes nothing and passes.
- **A history unrelated to `main` is refused.** Such a history has a root
  commit that `main` does not have. It comes from an orphan branch, from
  another repository's history, or from a merge made with
  `--allow-unrelated-histories`. There is no exception list. The scheduled
  discovery jobs' `discovery-state` branch is a separate root by design, so
  they push it from CI, where nothing installs the hook.
- **The new commits are scanned** with `scripts/publication-boundary-check.py`,
  the check CI runs. A commit is new when the remote does not have it yet:
  `origin/main`, the ref's old value and the remote's other remote-tracking
  refs cannot reach it. Each commit is scanned with its own checker and
  manifest, with `NG_BOUNDARY_DIFF_BASE` at its merge base with `origin/main`.
  The tip is always scanned. An earlier commit is scanned as well when it
  holds a file version that no scanned tree holds, because a push publishes
  every commit it reaches. So content that one commit adds and the next
  deletes is refused.
- A push with nothing new, such as a release tag on `main`, scans nothing.
- **Anything the hook cannot verify is refused:**
  - a missing `origin/main` (`git fetch origin main` fixes it);
  - a shallow history (`git fetch --unshallow`);
  - no `python3` with PyYAML;
  - a ref that is neither a branch nor a tag;
  - a checker that cannot run.

Each scan checks the commit out in a scratch repository that borrows this
repository's objects, so the checkout doing the push is never touched. A scan
of this repository costs about 13 CPU-seconds. On a 12-core machine at a load
average of 55 to 90, that came to 9 to 15 seconds of wall clock for each
scanned commit. A push with nothing new took about 0.25 seconds, and a push to
another remote about 0.05 seconds. `npm install` installs the hook along with
the other husky hooks. `scripts/test-publication-push-guard.sh` proves each case
by pushing for real into throwaway repositories and then reading the remote.

### What the hook cannot cover

A client-side hook narrows the window, but it does not close it:

- `git push --no-verify` skips the hook, and so does `HUSKY=0`. A checkout
  where `npm install` never ran has no hook at all.
- The hook runs only in a checkout of this repository. A checkout of a
  different repository that pushes to this repository's URL runs that
  repository's hooks, or none. That is the most likely way for an unrelated
  history to be pushed.
- The hook checks files. It does not read commit messages or tag messages.

Only a control that runs before GitHub accepts a push covers all of these, and
GitHub runs no custom checks at that point. Its server-side controls are
narrower:

- **Push protection** blocks credentials that secret scanning recognizes.
- **Rulesets** limit which identities may create or update branches and tags.
  That narrows who can make the mistake, but it does not check what they push.

A machine-wide `pre-push` hook covers a checkout of another repository on that
machine. Set it with `git config --global core.hooksPath`, and have it refuse a
push to this repository's URL unless the pushed history shares `main`'s root.
It is a client-side hook too, so it covers only the machine it is installed on.
A repository that sets its own `core.hooksPath`, as husky does, overrides the
global one, so such a repository needs the hook in its own hook directory.

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
