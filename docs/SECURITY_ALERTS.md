# Dependabot Security Alert Response

> Producer contract and operator runbook for the dependency-security surface.
> For the authoring contract every Action Center producer follows, see
> [docs/ATTENTION_PRODUCERS.md](ATTENTION_PRODUCERS.md). For the store, CLI,
> and lifecycle operations (`ack` / `mute` / `resolve`), see
> [docs/GO_BINARY.md#attention-operations](GO_BINARY.md#attention-operations).

## Overview

Nightgauge reads open dependency-security advisories (GitHub calls them
Dependabot alerts) directly from the forge and turns each one into a standing
Action Center card. Before this surface existed, the pipeline's only signal
for dependency security was a `security` label on an issue or PR — so severity
was fabricated (a critical RCE and a low-severity ReDoS looked identical) and,
more importantly, an alert with **no** remediation pull request was reported by
nothing at all. The forge only opens a PR when it can reach a non-vulnerable
version through the manifest; when it cannot, there is no PR, no issue, and
every prior code path that keyed off one or the other stayed silent on exactly
the alerts that most needed a human.

## Ingestion

### A forge-neutral service

`forge.SecurityService` (`internal/forge/security.go`) is the one method every
forge adapter implements:

```go
type SecurityService interface {
    ListOpenAlerts(ctx context.Context, owner, repo string) (*forgetypes.SecurityAlerts, error)
}
```

It returns the advisory's **own** fields — severity, identifier, package,
manifest, vulnerable range, first patched version — never anything inferred
from a label. Every alert carries its own deep link
(`SecurityAlert.URL`); that link, not the public advisory database, is what an
operator is sent to when there is no remediation PR to route them to instead,
because the advisory URL names neither the repository nor the manifest.

The call is contractually single-request: it runs inside the attention sweep's
shared 30-second budget alongside every other producer, so a repository
holding more than `forge.MaxSecurityAlertsPerRequest` (100) open alerts gets
`SecurityAlerts.Truncated` set rather than a second round trip. **A truncated
answer is not the open set** — see [Truncation](#truncation-is-a-read-failure-not-a-smaller-set) below.

- **GitHub** (`internal/github/security.go`) — one GraphQL request answers
  coverage, the alert list, and the remediation state together, because
  GraphQL's `dependabotUpdate` field is the only place the remediation PR (or
  the forge's typed reason it could not open one) is exposed; REST's
  Dependabot-alerts endpoint carries neither. On the ambiguous case — scanning
  reports as on, but zero alerts came back — the adapter spends one additional
  REST request to confirm the emptiness is real rather than a permission
  filter (GraphQL answers an under-scoped token with an empty connection and
  HTTP 200; REST answers the same request with a loud 403). See the file's
  header comment for how that was verified against a live repository.
- **GitLab** is an explicit unsupported stub
  (`internal/gitlab/stubs.go`): `ListOpenAlerts` returns
  `forge.ErrUnsupported` wrapped with a tracking reference. GitLab's dependency
  scanning reports findings through a CI job artifact and the Vulnerability
  Report rather than an always-on per-repository alert feed, so mapping it onto
  the shared `SecurityAlerts` shape is real adapter work, not a rename. On a
  GitLab repo today, the `dependabot-alerts` producer's read fails with that
  sentinel; since it is neither `forge.ErrPermissionDenied`,
  `forge.ErrUnauthorized` nor `forge.ErrRateLimited`, the sweep does not treat
  it as repository-wide — it is recorded as this one producer's failed
  observation (see [Producer failures are visible](#producer-failures-are-visible-not-silent)), and every other
  repo-scoped producer still runs.

## The three outcomes, and the one that is a dangerous silence

A caller that only inspects the alert list has two apparent states: empty or
not. That collapses three genuinely different facts into two, and the
collapsed pair is exactly the dangerous one. `forgetypes.SecurityAlerts.Status`
exists so a caller never has to make that mistake:

| Outcome            | `Status`                          | What it means                                                                                                 | Is it an error?                                                   |
| ------------------ | --------------------------------- | ------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| **Enabled, clean** | `SecurityAlertsEnabled`, 0 alerts | Scanning is on; nothing is currently open.                                                                    | No — a positive assertion                                         |
| **Disabled**       | `SecurityAlertsDisabled`          | Scanning is off for this repository. The empty alert list means **nobody looked**, not that nothing is wrong. | No — a status, deliberately not an error (see below)              |
| **Unreadable**     | _(no result)_                     | A genuine failure to observe: the credential lacks the scope, or the forge rejected the request outright.     | Yes — wraps `forge.ErrPermissionDenied` / `forge.ErrUnauthorized` |

**Disabled is the dangerous middle case**, not the error case. "Enabled with
zero alerts" and "scanning disabled" are both _successful_ observations — one
says the repository is clean, the other says it is unmeasured — and they read
identically to any code that only checks `len(Alerts) == 0`. Modelling
"disabled" as an error would fix that confusion by trading it for a worse one:
it would drag a completely normal repository setting through the same
machinery the sweep uses to decide "do not trust this cycle's observations",
making a routine coverage answer indistinguishable from a broken scan at every
call site. The design instead puts the fact on `SecurityAlerts.Status` where a
caller reads it directly — `SecurityAlerts.Enabled()` — with no error branch
required for either of the two successful outcomes. Only the third outcome, a
forge-confirmed denial, is allowed to be an `error`, and only because the
sweep's fatal/non-fatal split (`isSweepFatal`) genuinely needs to key off it:
an unauthorized or rate-limited read means no producer on that repo can be
trusted this cycle, so the whole sweep is skipped rather than reconciling a
partial view.

The full design rationale is documented on the type itself — read the doc
comment on `forgetypes.SecurityAlerts` in `internal/forge/types/security.go`
before changing any of this.

### Truncation is a read failure, not a smaller set

A fourth field, `SecurityAlerts.Truncated`, guards a subtler version of the
same mistake. The `dependabot-alerts` producer treats a truncated answer as an
**error** (`errAlertsTruncated`), not as "the open set, just shorter": the
reconciler auto-resolves every card a producer previously raised that is
absent from this cycle's observation, so reconciling a partial page as if it
were complete would retract cards for alerts that are still open — an
auto-resolve driven by a partial observation, which
[docs/ATTENTION_PRODUCERS.md Invariant 1](ATTENTION_PRODUCERS.md#1-an-empty-result-means-the-condition-is-false)
forbids in exactly this shape.

## The `dependabot-alerts` producer

`internal/attention/sweep/dependabotalerts.go` — a repo-scoped
`sweep.Producer` (`ProducerDependabotAlerts = "dependabot-alerts"`), evaluated
once per repo, per sweep. One card per open alert.

### What a card says

The title and body lead with the advisory's own severity and the package, then
state the remediation situation as one of three distinct paragraphs — never
averaged over them, because they lead to different operator actions:

| Remediation state        | Title says                                                     | The next action is                                                                                       |
| ------------------------ | -------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `RemediationPROpen`      | "…fix is waiting in PR #N"                                     | Review and merge the PR.                                                                                 |
| `RemediationNotPossible` | "…no remediation PR, and the forge reports it cannot make one" | There is nothing to merge. A human decides: upgrade the dependents, pin an override, or accept the risk. |
| `RemediationNone`        | "…no remediation PR exists"                                    | Nobody has tried yet. Check whether the ecosystem or manifest is one the forge can update at all.        |

**This is the class that mattered enough to build the surface for.** A card
for an alert with a remediation PR already open reads, and _should_ read,
completely differently from a card for the same severity with nothing to
merge — the first says "review", the second says "go decide something", and
collapsing them into one shared sentence is exactly the failure this producer
replaces. `RemediationNotPossible` and `RemediationNone` are also kept apart
from each other for the same reason: "the forge tried and failed" and "nobody
tried" point an operator at different first questions.

Every card also states the advisory identifier (GHSA/CVE when the forge
reports one), the affected manifest and dependency scope, the vulnerable
version range, and the first patched version — or "none published" when the
advisory has none yet.

### No repair verb, deliberately

The card ships exactly one option: an honest dismiss (`VerbNoop`, "Dismiss —
I've seen it"). This follows the same precedent already established for
`default-branch-health` and `human-gate`
([docs/ATTENTION_PRODUCERS.md Invariant 3](ATTENTION_PRODUCERS.md#3-do-not-declare-an-option-you-cannot-perform)):
nothing in the closed verb registry (`internal/attention/verbs.go`) can patch a
vulnerability, and an affordance that does nothing when clicked is worse than
no affordance at all — the operator learns to distrust the next card, too. The
real next action is the URL the card points at: the remediation PR when one
exists, otherwise the alert's own deep link.

### Auto-resolve behavior

| Sweep observes                                            | Effect on open `dependabot-alerts` cards                                                                                                                                                                         |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| An empty alert list, scanning enabled                     | **Retracted.** A positive assertion that nothing is open — this is what closes a card once an alert is fixed or dismissed on the forge.                                                                          |
| Scanning disabled                                         | **Left untouched.** Toggling the setting off must never silence a live vulnerability card.                                                                                                                       |
| An unreadable / rate-limited / truncated answer           | **Left untouched**, and the whole sweep is skipped when the failure is repo-wide (see below).                                                                                                                    |
| An open alert whose severity or remediation state changed | **Updated and re-alerted** — the fingerprint (`sev:…;fix:…;patch:…`) is built from material state only, never elapsed time, so it moves exactly when something an operator would act on differently has changed. |

A newly raised alert is held back for `DependabotAlertGrace` (30 minutes,
measured from the forge's own `first_seen_at`) before it is carded, so the
producer never publishes "no remediation PR" for an alert whose PR the forge
is already seconds away from opening.

**A repo-wide read failure does not blank the surface.** A denial scoped to
_this_ producer (the token lacks the `security_events` scope, for instance) is
downgraded to a producer-local failure — its own cards are left exactly where
they were, and every other repo-scoped producer (`default-branch-health`,
`human-gate`, `stranded-ready`, …) still runs. Only `forge.ErrUnauthorized` and
`forge.ErrRateLimited` remain repo-wide, because a rejected credential or a
rate limit really does mean nothing else on that repo can be trusted to have
observed anything either.

### Producer failures are visible, not silent

A producer-local failure is not swallowed. It appears in the sweep's `Failed`
map, keyed by producer name, and surfaces through
`nightgauge attention sweep --repo <owner/name> --json` (and the equivalent
IPC path) as well as the process log. An operator who sees `dependabot-alerts`
listed there should treat it as "this repo's security surface could not be
read this cycle" and check the token's scope rather than assume the repo is
clean.

## Why polling via sweep, not webhooks

This surface is ingested by `nightgauge attention sweep`, the same
evaluation loop every repo-scoped producer uses — not by a webhook listener
reacting to a Dependabot event. This is not an oversight; it follows directly
from a decision already made for the whole Action Center
([ADR 015, Decision L](decisions/015-decision-requests.md#l--the-sweep-an-evaluation-loop-with-no-run-in-flight)):
**a sweep is not a daemon.** It is cheap, idempotent, and safe to run
redundantly, so it is invoked — on extension activation, on a repository or
Action Center refresh, on a conservative timer, and after any run terminates —
rather than scheduled as a long-lived listening process. A long-lived process
is a larger operational commitment than a local-first tool should ask for, and
it complicates the story of a product that runs entirely against the
operator's own repository and model keys with no server component required.

Polling is also self-healing in a way a bare webhook listener is not. If a
delivery is missed — a network blip, the extension not running at the moment
the forge fired the event, a downtime window — the next sweep trigger
re-discovers the condition on its own; nothing needs to replay a lost event or
notice a gap. A webhook-only design would need its own reconciliation pass to
recover that guarantee, at which point it has re-invented the sweep and kept
neither its simplicity nor its redundancy story.

**The webhook seam is not unused — it already exists for a different
forge.** `internal/forge/webhook` is a forge-agnostic webhook receiver
(`Server`, `NewMux`, `EventDispatcher`) already wired for self-hosted GitLab:
pipeline, merge-request, and push events reach it today over a tunnel or a
direct ingress (see
[docs/SELF_HOSTED_GITLAB_SETUP.md](SELF_HOSTED_GITLAB_SETUP.md)). That seam is
the accelerator this decision leaves available rather than designs out: an
operator running a tunneled or self-hosted forge, for whom the sweep's trigger
cadence is not fast enough, could layer a security-alert webhook handler on
top of the same dispatcher to shrink alert-to-card latency below what polling
alone provides. No such handler exists today — `ListOpenAlerts` is the only
ingestion path this producer has — and it should only be built once a real
operator's required latency is smaller than the sweep's cadence delivers,
not ahead of that need. The sweep would remain the source of truth and the
safety net regardless: a webhook path only ever shortens the wait for the same
observation the next sweep would have made anyway.

## Operator runbook

### Enabling alerts on a repository

Dependabot alerts are a per-repository GitHub setting, off by default on some
account tiers:

1. Repository **Settings → Code security and analysis**.
2. Enable **Dependabot alerts**. Also enable **Dependabot security updates**
   if you want the forge to attempt opening remediation PRs automatically —
   without it, every alert this producer cards will report
   `RemediationNone`, because the forge never tried.
3. The token Nightgauge runs with needs the `security_events` scope (or
   equivalent repository access) to read alerts at all; a token without it
   sees the repository as unreadable, not as clean (see the outcomes table
   above).

### Triaging by severity

The card's title always leads with the advisory's own severity — never a
fabricated one — so the Action Center's default sort (most severe first) is
already a triage queue:

| Severity            | Typical response                                                                                                                                                                                                                                                                      |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `critical` / `high` | Treat as blocking work on the affected manifest. If a remediation PR exists, review and merge it promptly; if not, start with the affected version range and check for an immediate mitigation (pin, vendor patch, disable the affected code path) while a durable fix is worked out. |
| `moderate`          | Schedule normally; merge a remediation PR in the next routine change, or plan the manual fix.                                                                                                                                                                                         |
| `low`               | Track; batch with other routine dependency work.                                                                                                                                                                                                                                      |
| `unknown`           | The forge did not report a severity for this advisory. Do not assume low — open the alert's own URL and read the advisory directly before deciding.                                                                                                                                   |

### When no remediation PR exists

Two different situations both render as "no PR to merge", and the card body
says which one applies:

- **The forge reports it cannot make one** (`RemediationNotPossible`): the
  card states the forge's own machine-readable reason and its one-line
  explanation verbatim. Common causes are that no non-vulnerable version is
  reachable from the current manifest constraints, or the update would be a
  breaking major-version bump the forge will not open unattended. The
  operator's options are all manual: upgrade whatever pins the vulnerable
  transitive dependency, add a manifest override/resolution to force a patched
  version, or accept the residual risk explicitly (and document why).
- **Nobody has tried yet** (`RemediationNone`): check whether Dependabot
  security updates are enabled for the repository at all (see above), and
  whether the ecosystem or manifest format is one the forge supports updating
  automatically. If updates are enabled and the ecosystem is supported, this
  may simply be new — the forge can take some time after raising an alert
  before it attempts a fix.

### Muting and dismissing

- **Dismiss** (`nightgauge attention resolve <id> --option dismiss`, or the
  equivalent surface action) records that a human looked. It suppresses the
  card until the fingerprint changes — the severity or remediation state
  moving is what re-opens it, not the passage of time.
- **Mute** (`nightgauge attention mute <id>`) is the non-terminal form: it
  silences the card while leaving it open, and re-alerts under the same
  condition-change rule if the situation changes while muted.
- Neither option modifies anything on the forge. The alert itself is only
  dismissed, patched, or resolved by the corresponding forge-side action (or
  by the remediation PR merging); the Nightgauge card is a notification about
  that state, not a control over it.

### What is not covered yet

This surface answers "what is open, and can it be fixed with something the
forge already prepared" for a repository whose alerts **can** be read. It does
not yet answer, at the workspace level, "which of my configured repositories
cannot report alerts at all" — a repository with scanning disabled or an
unreadable token currently shows up only as an absence of cards for that
repo, which looks identical to a clean repository unless an operator checks
`nightgauge attention sweep --repo <owner/name>` directly. A workspace-scoped
companion producer to close that gap is tracked separately; until it lands,
periodically sweeping each configured repository directly remains the way to
confirm coverage rather than assuming silence means clean.
