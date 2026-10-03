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
nothing done afterwards takes the content back. So a `pre-push` hook runs
`scripts/publication-push-guard.sh` before a push leaves the machine.

`npm install` installs it. After husky, `package.json`'s `prepare` script runs
`scripts/install-publication-push-hook.sh`, which points `core.hooksPath` at a
hook directory in the clone's shared git directory (`npm run setup-hooks` does
the same). Every worktree of the clone then runs the guard on every push,
including a worktree where `npm install` never ran, such as one the pipeline
creates. It runs whatever the worktree has checked out, and the guard it runs
is the copy installed beside the hook, not the worktree's own: an orphan
branch, another repository's history or an old commit has no guard or an older
one, and a guard being edited would judge its own pushes. Each `npm install`
installs the guard of the checkout it runs in, for the whole clone. The
worktree's own copy runs only when no copy is installed. husky's own hooks did
neither, because their path is relative to each worktree and husky skips a
hook the checked-out tree lacks. `.husky/pre-push` still runs the guard in a
clone where only husky is installed.

The hook examines a push only when its URL names this repository. It matches on
the URL's path, so any transport or SSH host alias counts. It asks the remote
which commits it already has, with `git ls-remote` on that URL, and fetches the
remote's `main` when the clone lacks it. Remote-tracking refs are not trusted:
they go stale when a branch is deleted upstream, and a push to a separate
`pushurl` writes them. For every branch or tag the push updates:

- A deletion publishes nothing and passes.
- **A history unrelated to `main` is refused.** Such a history has a root
  commit that `main` does not have. It comes from an orphan branch, from
  another repository's history, or from a merge made with
  `--allow-unrelated-histories`. There is no exception list. The scheduled
  discovery jobs' `discovery-state` branch is a separate root by design, so
  they push it from CI, where nothing installs the hook.
- **An allowlist change must stand alone**, as CI requires of a pull request
  (#1970). A new commit whose `.github/publication-boundary.yaml` differs from
  both its merge base with `main` and `main`'s own must change nothing else, by
  CI's own `scripts/check-boundary-allowlist-isolation.sh`. Loosening the
  allowlist and adding what it lets in is refused, in one commit or in two. A
  commit whose allowlist change `main` already has is not held to this, as on
  a stacked branch whose allowlist change was then merged on its own. This is
  stricter than CI, which judges a pull request's changes as a whole. A branch
  that changed the allowlist in an earlier commit together with other work is
  refused even after it takes `main`'s allowlist, unless `main` has that
  earlier change exactly. Squash its unpushed commits, as the refusal says.
- **The new commits are scanned** with `scripts/publication-boundary-check.py`,
  the check CI runs. A commit is new when nothing the remote has can reach it.
  One scan covers a ref: the ref merged into the remote's `main`, which is what
  CI's pull-request run checks, so `main`'s checker and manifest apply unless
  the ref changes them. A push publishes every commit it reaches, so every file
  version an earlier commit holds that the tip does not is merged into the same
  path for that scan, less the lines that commit's merge base already had there.
  Content that one commit adds and a later one rewrites or deletes is refused.
  A ref that does not merge cleanly is scanned as if each conflict were settled
  its way: `main`'s tree with the ref's version of every file it changed. So
  `main`'s checker and manifest still apply unless the ref changes them, a
  branch forked before `main` tightened its rules is held to the tighter ones,
  and files the ref never touched are not judged again. A commit with its own
  version of the checker, the manifest, the isolation script or a
  `.gitattributes` is also scanned on its own. Every file is scanned as stored:
  a `.gitattributes` in the pushed tree cannot re-encode or filter what the
  checker reads.
- When that scan fails, the commits are scanned one at a time to name the one
  at fault. The combined scan can fail where no commit does, for example on a
  count that the commits add up to. If every commit then passes by `main`'s
  rules, the push proceeds. If any commit was judged by rules of its own (its
  own checker, manifest or `.gitattributes`), the push is refused as
  unverified. A checker that cannot run blames no commit, so the push is
  refused at once.
- A push with nothing new, such as a release tag on `main`, scans nothing.
- **Anything the hook cannot verify is refused:**
  - a remote it cannot list, or one without a `main`;
  - a shallow history (`git fetch --unshallow`);
  - no `python3` with PyYAML;
  - a ref that is neither a branch nor a tag;
  - a checker that cannot run;
  - two paths in one commit that differ only in case or Unicode
    normalization, on a filesystem that folds them into one file. A rename by
    case alone is not refused: the commits are then scanned one at a time.

A refusal names the commit at fault. A later commit that removes the content
does not help, because the commit that adds it is published as well, so the
refusal gives a way to rewrite the unpushed commits that needs no force-push:
`git reset --soft` to the last commit the remote has, then commit again.

Each scan checks a synthetic commit out in a scratch repository that borrows
this repository's objects, so the checkout doing the push is never touched. A
scan of this repository costs about 13 CPU-seconds. Replaying real pull-request
branches of 6 to 8 commits on a 12-core machine at a load average of 15 to 35
took one scan and 5 to 7 seconds each, two of them branches that do not merge
cleanly. A branch 150 commits behind `main` that conflicts with it took one
scan and 6 seconds. A 17-commit branch that carried two versions of the
manifest took three scans and 15 seconds with the allowlist-isolation check set
aside; the check refuses that branch, as above. A push with nothing new costs
one `git ls-remote`, and a push to another remote costs nothing.
`scripts/test-publication-push-guard.sh` proves each case by installing the
hooks as `npm install` does, pushing for real into throwaway repositories, and
then reading the remote.

### What the hook cannot cover

A client-side hook narrows the window, but it does not close it:

- `git push --no-verify` skips the hook. `HUSKY=0` skips it only in a clone
  where the publication hook is not installed.
- A clone where `npm install` never ran has no hook at all. An `npm install` in
  a checkout older than the installer puts husky's relative hooks path back,
  which leaves only husky's coverage, until the next `npm install` in a current
  checkout or `npm run setup-hooks` restores it. Likewise, an `npm install` in
  a checkout with an older guard installs that guard for the whole clone. The
  hook directory is named by absolute path, so a clone moved elsewhere runs no
  hooks until one of those runs again.
- The hook runs only in a clone of this repository. A checkout of a different
  repository that pushes to this repository's URL runs that repository's hooks,
  or none. That is the most likely way for an unrelated history to be pushed.
- A push to a fork is not examined, because its URL names another owner, yet
  GitHub serves a fork's commits through this repository's network. Nor is a
  URL that reaches this repository only through a redirect, such as a renamed
  owner or repository.
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
A repository that sets its own `core.hooksPath`, as husky and this repository's
installer do, overrides the global one, so such a repository needs the hook in
its own hook directory.

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
