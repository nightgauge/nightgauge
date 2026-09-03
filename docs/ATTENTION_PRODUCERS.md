# Writing an Attention Producer

A **producer** is anything that raises a `DecisionRequest` — the record the
pipeline creates when it needs a human decision at a dead end that is otherwise
silent or one-way. This document is the authoring contract for both kinds.

The design rationale lives in
[ADR 015](decisions/015-decision-requests.md); this is the how-to. Read the
[Invariants](#invariants) section even if you skim everything else — three of
them are load-bearing in ways that are not obvious from the type signature, and
violating one degrades the inbox for every other producer.

## Which kind are you writing?

|                  | **Run-scoped**                              | **Repo-scoped**                                    | **Workspace-scoped**                     |
| ---------------- | ------------------------------------------- | -------------------------------------------------- | ---------------------------------------- |
| Fires on         | A transition inside a run                   | A condition observed by a sweep                    | A condition about the repo LIST itself   |
| Lives in         | `internal/orchestrator/attention_wiring.go` | `internal/attention/sweep/<name>.go`               | `internal/attention/sweep/<name>.go`     |
| Raised by        | The scheduler calling `raiseAttention`      | Returning an observation from `Evaluate`           | Same, from `WorkspaceProducer.Evaluate`  |
| Evaluated        | Per transition                              | Once per repo, per sweep                           | Once per sweep, with the whole repo list |
| Ends when        | A human resolves it, or it expires          | ...also when the condition stops being observed    | ...same                                  |
| `context.run_id` | Set                                         | Absent                                             | Absent                                   |
| Needs            | A stable `idempotency_key`                  | A stable `idempotency_key` **and** a `fingerprint` | Same as repo-scoped                      |

The question that decides run-scoped vs the rest: **if nobody is running
anything, can this condition still be true?** A budget ceiling cannot — it only
exists because a run spent money. A red default branch can, and does, most of
the time nobody is looking.

The question that decides repo-scoped vs workspace-scoped: **can a producer
that is handed one repo at a time observe this?** If the condition is about a
repo, it is repo-scoped. If it is about the repo _list_ — its coverage, its
consistency, what is missing from it — no repo-scoped producer can see it,
because it is never invoked for a repo nobody configured.

That blind spot is not hypothetical; it is why `WorkspaceProducer` exists
(#260). A sibling repo sat outside the configured list for six weeks with every
open PR blocked by a broken required check, and the Action Center stayed quiet
throughout — "not in scope" and "nothing wrong" render identically when the
only thing that can speak is scoped to what is already in scope.

Getting this wrong is not a style error. A standing condition raised as an event
produces one duplicate card per observation; an event raised as a standing
condition auto-resolves itself the moment the run ends.

**Where it lives and whether it is standing are two different questions.** Three
run-scoped producers — `work-exhaustion`, `owner-action-handoff`, and
`watchdog-stuck-epic` — fire from the scheduler's own loop, but the loop
re-evaluates their whole condition set on every cycle. That makes them standing:
they set `Standing: true` with a fingerprint, and their trigger sites call
`AutoResolveUnobserved` with the complete set they just saw. The test is not
which file the producer lives in, it is whether the trigger site observes a
transition once or re-answers the same question forever.

## Workspace-scoped: the interface

```go
type WorkspaceProducer interface {
    Name() string
    Evaluate(ctx context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error)
}
```

Register from an `init()` with `Default.RegisterWorkspace(&MyProducer{})`. The
`Evaluate` contract is identical to the repo-scoped one, Invariant 1 included.

`WorkspaceInput` carries `ConfiguredRepos` (every repo this sweep covers),
`WorkspaceRoot`, a `forge.ForgeClient` for board-level discovery, and
`Existing`. Use `in.Covers(repo)` rather than comparing strings — it matches a
bare name against an `owner/name` spec in either direction, so a manifest entry
written either way counts as coverage.

**Cards are still per-repo.** Only the evaluation is workspace-wide: set
`Context.Repo` to the repo the card is about, and the sweeper groups
observations by repo before reconciling.

### Reconciliation is two-step, and both steps are load-bearing

`SweepWorkspace` reconciles per repo via `ReconcileStanding`, then calls
`AutoResolveUnobserved` once for the producer. Neither alone is sufficient:

- **`ReconcileStanding` per repo** supplies ID stamping, expiry refresh, and
  the create/update/refresh/suppress decision that distinguishes a changed
  condition from a re-observation. Calling `Store.Raise` directly skips all of
  it and re-alerts on every sweep.
- **`AutoResolveUnobserved` for the producer** is what retracts a card whose
  repo stopped being uncovered. That repo produces no observation at all, so
  the per-repo pass never visits it and nothing there would ever close it.

This is safe here for the reason the [Run-scoped
producers](#run-scoped-producers) section describes: a workspace producer
re-answers its **entire** condition set on every sweep, which is exactly
`AutoResolveUnobserved`'s documented precondition. A producer that only ever
observed part of its set would need `AutoResolveKey` instead.

### Discovery must fail loudly

A workspace producer usually answers by comparing config against the world, and
"the world" can fail to enumerate. `coverage-gap` reads two independent sources
(local git checkouts, board-linked repos); either may fail alone, but if
**both** fail it returns an error rather than an empty slice. An empty slice
there would assert "no coverage gaps" on the strength of having looked nowhere
— Invariant 1's exact failure mode, and doubly wrong in a producer whose entire
purpose is that not-looking should be visible.

## Repo-scoped: the interface

```go
type Producer interface {
    Name() string
    Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error)
}
```

`Input` carries the repo in both forms (`Repo`, `Owner`, `Name`), a
`forge.ForgeClient`, and `Existing` — every non-terminal request already open
for this repo, from every producer, so you can decline to raise something
another producer is already carding.

Register from an `init()` so a producer is one self-contained file:

```go
func init() { Default.Register(&MyProducer{}) }
```

Nothing else changes. The sweep, the CLI, the IPC method, and the extension
surface all pick it up.

### A minimal producer

```go
const ProducerStaleRelease = "stale-release"

type StaleRelease struct{}

func init() { Default.Register(&StaleRelease{}) }

func (p *StaleRelease) Name() string { return ProducerStaleRelease }

func (p *StaleRelease) Evaluate(ctx context.Context, in Input) ([]attention.DecisionRequest, error) {
    tags, err := in.Forge.Repo().SomeLookup(ctx, in.Owner, in.Name)
    if err != nil {
        // "I could not look" — NEVER return an empty slice here.
        return nil, fmt.Errorf("read tags for %s: %w", in.Repo, err)
    }
    if !isStale(tags) {
        return nil, nil // a positive assertion: the condition is false
    }
    return []attention.DecisionRequest{{
        IdempotencyKey: fmt.Sprintf("%s:%s", ProducerStaleRelease, in.Repo),
        Kind:           attention.KindUnblock,
        Severity:       attention.SeverityBlockingRun,
        Title:          fmt.Sprintf("%s has unreleased commits", in.Repo),
        Body:           "...",
        Fingerprint:    "behind:" + tags.Latest,
        Context:        attention.Context{Repo: in.Repo, URL: tags.CompareURL},
        Options: []attention.Option{
            {ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop},
        },
        DefaultAction: attention.ExpireNoop,
    }}, nil
}
```

The sweep stamps `Producer` and `Standing` for you, defaults `ExpiresAt`, and
generates the `ID`. You supply the identity, the fingerprint, and the prose.

### A shipped example: `dependabot-alerts`

`internal/attention/sweep/dependabotalerts.go` (issue #343) is a repo-scoped
producer worth reading end to end once the minimal example above makes sense,
because its constraints are less obvious than a boolean condition:

- **Scope**: repo-scoped — evaluated once per repo, per sweep, against
  `forge.SecurityService.ListOpenAlerts`.
- **`fyi` at every advisory severity**, including `critical` — the cleanest
  worked example of the table above being about _blocking_, not importance. A
  vulnerability blocks no merge and stalls no run, so any blocking claim would
  be false. The advisory's own severity rides in the card's title text and
  never in the severity field, which means the inbox ordering (severity band,
  then newest-first) conveys nothing about how bad these advisories are — a
  deliberate trade the operator runbook states outright rather than leaving
  the reader to infer a severity-ranked queue that does not exist.
- **Fingerprint**: `sev:<severity>;fix:<remediation-state>[#<pr-number>];patch:<first-patched-version>`
  — material state only (how bad the advisory is, and whether something
  already fixes it), deliberately excluding the alert's `updated_at` and any
  elapsed-time value, so a re-observation of an unchanged advisory refreshes
  quietly instead of re-alerting.
- **One card per open alert**, `IdempotencyKey` keyed on the forge's own alert
  number so a dismissed-then-re-raised alert becomes a genuinely new card
  rather than a resurrection.
- **No repair verb** — the same call Invariant 3 below makes for
  `default-branch-health` and `human-gate`: nothing in the closed verb
  registry can patch a vulnerability, so the only option is an honest dismiss
  and the real next action rides on `Context.URL`.
- **Deliberately skips cross-producer dedupe** that a structurally similar
  producer (`human-gate`) performs — an open advisory and a PR that cannot
  merge are two different conditions observed from two vantage points, not one
  fact seen twice, so suppressing the security card when another producer
  already cards the same PR would make the reconciler auto-resolve a live
  vulnerability. See the file's header comment for the full reasoning.

The full producer contract, the card semantics per remediation state, and the
operator runbook live in
[docs/SECURITY_ALERTS.md](SECURITY_ALERTS.md) — this entry only orients a
producer author to the file.

A workspace-scoped companion — a repository that is configured but cannot
report alerts at all (scanning off, or the token unreadable) is a distinct
condition from "no alerts open", and belongs to a workspace producer for the
same reason `coverage-gap` does (see [above](#which-kind-are-you-writing)) —
is tracked separately (issue #344) and is not part of the shipped surface this
document describes yet.

## Invariants

### 1. An empty result means the condition is FALSE

`Evaluate` returning `(nil, nil)` is a positive assertion that nothing is wrong,
and it **auto-resolves every open card this producer previously raised**. It is
not a way to say "skip me this time."

If you cannot observe the repo — a network failure, a missing permission, an
endpoint that 500s — return an error. The sweep drops you from the
reconciliation input and leaves your existing cards exactly where they were.

This is the invariant most likely to be violated by accident, usually by a
`return nil, nil` in an error branch that seemed harmless. The consequence is
that a transient API failure silently retracts a real blocker, and the operator
never learns the branch is still red.

### 2. The fingerprint is material state, never time

```go
Fingerprint: "checks:build,lint"          // correct — what is failing
Fingerprint: fmt.Sprintf("failing_for:%s", elapsed)  // wrong — moves on its own
Fingerprint: "sha:" + headSHA             // wrong — moves on every push
```

The fingerprint decides whether a re-observation is _the same condition_ (refresh
quietly) or _a change_ (alert). A fingerprint containing anything that moves on
its own re-alerts on every sweep, which is precisely the spam-folder failure
mode that makes an inbox worthless. A fingerprint that is too coarse never
re-alerts when a second, different thing breaks.

Elapsed durations belong in the `Body`, where they are prose. Sort any list you
join into a fingerprint, so ordering from the forge cannot fake a transition.

### 3. Do not declare an option you cannot perform

Every `option.verb` must resolve in the closed verb registry
(`internal/attention/verbs.go`), and the store rejects a request that binds an
unregistered one. But the stricter rule is editorial: **if no registered verb
can actually fix the condition, ship no repair option.**

Nothing in the registry can fix a red `main`, approve a PR, or patch a
vulnerability — `default-branch-health`, `human-gate`, and `dependabot-alerts`
all land on the same conclusion for their own condition. A card that offers to
anyway is worse than a card that offers nothing — the operator clicks it,
nothing changes, and the next card they see is one they have already learned to
distrust. Use `VerbNoop` for an honest dismiss and put the real next action in
`Context.URL`, which surfaces render as a first-class affordance.

**The rule is about the registry's contents, not about the card — so it can
expire.** `coverage-gap` shipped dismiss-only and said so in a code comment:
"no registered verb can edit the workspace manifest or config." That stopped
being true when #703 gave the manifest a deterministic writer, and #706 added
`workspace.addRepo` to the registry and a repair option to the card. If you are
writing a producer whose condition is unrepairable today, record **why** in the
comment the way `coverage-gap` did, naming the missing capability rather than
just its absence — that is what makes the reasoning re-checkable when the
capability lands.

A repair verb added this way must be **bounded the same way the producer is**.
`workspace.addRepo` takes no arguments at all: its target is read from the
persisted request's `Context.Repo`, so the resolving surface cannot redirect the
write at another repository. And its coverage check is the _inverse_ of every
other verb's — it acts only on a repo that is NOT configured — which makes the
producer's matcher and the executor's matcher the same question asked twice.
They therefore share one implementation (`sweep.ConfiguredRepos`); if they
diverged, the producer would raise a card whose button the executor refuses on
click, which is precisely the dead affordance this invariant exists to prevent.

**The inverse case: when a card's condition is a hold your own pipeline
placed, a repair verb is not optional.** `out-of-scope-blocker` (#1147) reports
that a stage found the issue depends on work outside its scope, and the run
recorded that finding to disk so every later dispatch of the issue defers at
pickup for zero tokens. That durability is the feature — and it means the card
is the ONLY thing in front of an operator that can end the hold. Shipping it
dismiss-only would not be the cautious choice the paragraphs above describe; it
would leave an issue that never runs again with no affordance to say otherwise.
`blocked.clearFinding` is bounded like the verbs above, and a notch tighter:
its target has TWO coordinates, and BOTH the repository and the issue come from
the persisted request's `Context`, because a caller-supplied issue number would
let any local process lift the hold on an issue nobody clicked on.

## Choosing a severity

| Severity         | Means                            | Use when                                            |
| ---------------- | -------------------------------- | --------------------------------------------------- |
| `blocking_fleet` | Nothing can land                 | The condition blocks every unit of work in the repo |
| `blocking_run`   | One unit of work is stalled      | The repo keeps shipping everything else             |
| `fyi`            | Worth knowing, interrupts nobody | Badge only                                          |

`blocking_fleet` is a strong claim and the surface treats it as interrupt-worthy
— it is named in the view header so it survives a collapsed tree. Make the claim
only when it is literally true. A failing check that is not _required_ blocks no
merge, so it is not `blocking_fleet` no matter how red it looks.

## Say only what you observed

A card's body is an instruction. State what was **observed**, never what the
state usually implies — and when nothing was observed, say that instead of
picking the likelier story.

`watchdog-stuck-epic` shipped the counter-example (#265). It rendered
sub-issues as _"in review (PR open, awaiting merge)"_ from the board status
alone, having never queried a PR. But `In review` is overloaded: the PR-review
phase parks an issue there, and so does the architecture-approval gate
(`TerminalKindArchitectureApprovalRequired` moves the board there on purpose).
The two states need **opposite** operator actions — merge a PR, versus approve
a gate so work can _start_ — so the card did not merely round off a detail; it
named the wrong action confidently and sent the operator to an empty PR list. A
specific, confident, wrong diagnostic is worse than none.

The fix generalizes. Any lookup that can fail has **three** answers, not two:

| Verdict      | Means                              | Render as             |
| ------------ | ---------------------------------- | --------------------- |
| present      | queried, and it is there           | the concrete claim    |
| absent       | queried, and it is not there       | the explicit negative |
| _unverified_ | the query failed, or nobody ran it | name the uncertainty  |

Collapsing `unverified` into either neighbour reintroduces the same defect with
the sign flipped — an unreachable forge becomes "no PR exists". Note that a
**gating** caller may legitimately fold `unverified` into the fail-closed
branch (`inReviewPRBacked` does, so an unverifiable dep never satisfies a
`blockedBy` edge); a **reporting** caller must not, because its output is read
by a human as a statement of fact. When one map serves both, keep the
success-of-the-query signal alongside it (`prQueryOKRepos`) rather than making
reporters re-derive it.

This is the Silent No-Op class from
[docs/FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md) pointed at output instead of
control flow, and it is checked at review.

## Deduplicating against another producer

Two producers can observe the same fact from different vantage points. The
run-scoped `branch-protection` producer raises when a run punts at its merge
stage; the repo-scoped `human-gate` producer raises when a scan finds the same
PR blocked. That is one fact and deserves one card.

```go
if _, dup := in.OpenRequestForPR(producerBranchProtection, pr.Number); dup {
    continue
}
```

`Existing` is read once per sweep, before any producer runs, so the outcome does
not depend on registration order. Treat it as read-only advisory context.

## Bounding cost

A sweep runs on a timer, and on demand from several event-driven triggers. Your
producer's forge traffic is therefore recurring, not one-off:

- Cap how many entities you inspect, and say so in a constant with a comment.
- Prefer one list call carrying the fields you need over a list call plus a
  fetch per item.
- When a backlog is large, consider collapsing into one aggregate card above a
  cap. Thirty cards is not thirty decisions, and it buries every other producer.

### When a sweep actually runs

A full sweep is ~64 GraphQL points plus 18 REST calls across a six-repo
workspace, against an intended cadence of four an hour. The extension's
`AttentionSweepService` therefore gates every trigger except the timer and the
operator's own command behind the daemon's one-point board change probe
(`board.changed`, over `ProjectUpdatedAt` from the board cache), memoised for
`boardcache.ProbeTTL` so a burst of triggers costs one point.

| Trigger                                                            | Sweeps when                                                               |
| ------------------------------------------------------------------ | ------------------------------------------------------------------------- |
| Timer (`sweepIntervalMinutes`, default 15, floor 5, window active) | Always — this is the cadence.                                             |
| `nightgauge.attentionSweep` (the operator's command)               | Always — a "nothing changed" answer reads as a broken button.             |
| Activation                                                         | First sweep of the window always; thereafter as below.                    |
| Repository / Action Center refresh                                 | A bound board moved since the last sweep, OR a full interval has elapsed. |
| Run terminated (`pipeline.complete` / `pipeline.error`)            | Same as refresh.                                                          |
| Window focus regained (#484)                                       | Same as refresh.                                                          |

When the probe answers "nothing moved", the trigger re-renders the cards the
store already holds and issues no forge traffic beyond the probe. The probe
fails open: a daemon without the verb, a probe error, an adapter with no probe
(GitLab), and `unavailable` all sweep. A repo whose forge client does not
resolve is skipped at zero cost — the sweep would skip it too — and does not
vote.

Producers whose condition does not move a ProjectV2 object — default-branch
CI, dependabot alerts, branch protection — are therefore refreshed by the timer
and the manual command only, never by an alt-tab. That is the intended trade:
those conditions stand for hours, and the timer's period bounds how stale the
answer can be.

## Testing

Producers are tested against a **faked** `forge.ForgeClient` — no live API calls
in CI, and the fake proves the producer is forge-neutral. See
`internal/attention/sweep/defaultbranch_test.go` for the shape.

Test the reconciliation properties through the sweeper and store, not just the
return value of `Evaluate` — "raises exactly one card" and "auto-resolves when
the condition clears" are properties of the diff, not of your function.
`producers_integration_test.go` covers raise → refresh → auto-resolve.

Assert on bands and properties rather than pinned literals wherever the value is
derived. A test that pins a concrete model id or a formatted duration documents
today's output as the contract.

## Run-scoped producers

Add an exported `Build*` function in
`internal/orchestrator/attention_wiring.go` that returns the
`attention.DecisionRequest`, plus a thin `raise*` method that hands it to
`raiseAttention`, and call the `raise*` method at the trigger point. The same
identity rules apply — a stable `idempotency_key`, options bound to registered
verbs.

**Then answer the second question: which execution path observes this
condition?** (#305) A producer whose only call site is the Go scheduler is
silent in the extension operating mode, which is where the overwhelming majority
of runs happen. That is not a theoretical gap — it is how the run-scoped half of
ADR 015 produced literally nothing for its first 1,800 headless runs.

If the extension detects the condition too, wire it: declare the producer in the
`attention.raise` allowlist (`internal/ipc/attention_raise.go`), give it typed
scalar params, and have the handler call your `Build*` function. The builder is
shared precisely so the two paths cannot render two different cards for one
condition — pin BOTH halves, as
`TestRunScopedProducersDelegateToSharedBuilders` (Go call site → builder) and
`TestAttentionRaiseProducesTheGoPathCard` (IPC handler → same builder) do.

Six rules on that wiring:

- **The params never carry an option, a verb, or an args map.** Card options are
  executed by the daemon on resolve, so a surface that could describe them could
  mint a plausible card offering an arbitrary operation on an arbitrary issue.
  The surface reports a CONDITION; the daemon decides what the card offers.
- **Send structured inputs, classify daemon-side.** If Go decides the condition
  from a machine value (`stages.Decide`'s reason codes, a gate verdict), send
  that value, not the extension's own prose rendering of it. Two renderers put
  two different sentences on the same condition with nothing failing. Send the
  value **un-coerced**: an in-flight check's `conclusion` is `null` on the wire
  and `""` in the projection, and substituting a friendly placeholder for it
  makes the payload unable to express the very state the classifier keys on.
- **Share the whole precondition, not just the classifier.** A pure decision
  function is rarely the entire Go rule — `stages.Decide` says
  `dirty-merge-state: BLOCKED` for a PR whose only blocker is a queued required
  check, and the Go runner never acts on that punt because
  `stages.MergeBlockedByPendingCI` intercepts first and waits out CI. A surface
  that reuses `Decide` and not that predicate reproduces the classifier and
  loses the guard, which is the same dual-path drift (#257) with the sign
  flipped. Export the predicate and call it; never re-implement it — a third
  copy of a matrix is a third thing to keep in sync.
- **Never accept a number a card option will act on.** The allowlist bounds
  _which_ operation a card offers; it says nothing about that operation's
  magnitude. `budget-ceiling`'s primary option persists a per-repo ceiling
  override, so while the enforced ceiling and the run's spend were params, any
  caller on the workspace socket could choose the number one operator click
  would write. Derive such values daemon-side from state the daemon itself
  recorded (config, the run's own `RuntimeState`), and when they cannot be
  corroborated, raise the card **without** the option rather than guessing — the
  operator still learns the condition happened, and nothing executable rides on
  an uncorroborated report.
- **"Daemon-side" is not the property; UNMINTABLE is.** Deriving a number from
  daemon state buys nothing if a caller can create that state. `budget-ceiling`
  read the run's spend out of the runtime registry — which
  `pipeline.notifyStageTransition` fills, booking `costUsd` verbatim AND
  creating the entry when none exists, so one call minted a run and the next
  raise built a remedy worth $1.5M out of it. Ask what the SHORTEST call
  sequence is that manufactures the state you are about to trust, then require
  evidence only the normal flow produces: an exact repo match (not "empty or
  matching"), and progression markers the terminal call alone cannot write. See
  [ADR-015 §N](decisions/015-decision-requests.md) for the boundary this sits
  inside.
- **A raise may never take a remedy off an open card.** Two structurally
  different offers can share one `idempotency_key` — the corroborated and
  uncorroborated `budget-ceiling` shapes do — and the open-record merge is
  last-writer-wins over the whole payload. `Store.Raise` now keeps the record
  that offers a real verb and reports `refreshed`; if your producer has a
  degraded variant, that is the direction the asymmetry must point. Never the
  other way: an observation that knows LESS must not overwrite one that knew
  more.

If the extension does NOT observe it, say so in the `run-scoped-attention` row
of `internal/orchestrator/testdata/terminal_behaviors.json` with a reason. A
Go-only producer is a legitimate choice; an undeclared one is a silent gap.

**A producer raised over `attention.raise` is an EVENT, and the allowlist
enforces it** (`TestNoRaiseableProducerIsStandingWithoutRetraction`). `Standing`
is not a severity dial; it is a contract with two obligations (below), and a
one-shot report from a surface that observed a transition can satisfy neither —
there is no scan to reconcile against. Declaring it anyway inherits ADR-015 §M's
"a human already resolved this exact condition, do not hand it back" rule with
nothing that can ever lapse it, and the producer goes permanently silent for
that key on the operator's first dismissal. Event shape gives what a
re-observation actually needs anyway: `Raise` updates the open record for the
key in place (one card per condition), while a recurrence _after_ a resolution —
a new fact — gets a new card.

**Shape the card around the population that will actually reach it, not the
condition's name.** `abandoned-dispatch` reads like a stuck run, so its first
cut shipped `unblock`/`blocking_run` with a primary "Retry". Tracing the call
graph showed every card it could ever raise follows an operator pressing Stop —
`forceClearStuckSlots` is reached only from the abort deadline, and `abortAll`
only from Stop / Abort / `deactivate()`. The card was therefore telling
operators to undo their own decision, three times over for three stopped
pipelines, at a severity §I routes to alerting while nothing was blocked. Ask
who sees this card and what they just did; if the honest answer is "nothing is
blocked, but there is something worth knowing", that is an `fyi` with noop
options, not an `unblock` with a remedy.

**And if one producer covers several populations, give the body a parameter —
do not average them.** The same card then shipped ONE body for three
force-clear situations, and it was false for two: it promised a preserved
worktree that "may hold uncommitted work" to a dispatch that wedged before any
worktree existed, and "NOTHING IS BLOCKED and no action is required" to one
still holding the Go scheduler's seat. A body that is true of the union of your
populations is usually true of none of them. Pass the distinguishing facts the
call site already holds (`abandoned-dispatch`'s `situation` enum), let them
select PROSE only — never options, verbs or severity, or the parameter becomes
a way for a caller to choose what the card can do — and reject an unrecognised
value instead of defaulting, because a default prints a confident wrong body.

A genuine event needs no fingerprint: it is observed once rather than
reconciled. If instead your trigger site re-answers the same question on every
cycle, the producer is standing and needs the full treatment:

- `Standing: true` and a fingerprint, or `Raise` rejects it.
- `ExpiresAt` set to the declared standing window
  (`attention.StandingExpiry`), because expiry is now only the safety net for a
  producer that stopped being evaluated. A short TTL on a producer that runs
  every few minutes fires while the producer is perfectly healthy, and the
  condition returns under a card the operator has already dismissed once.
- A call to `autoResolveAttention(producer, observed)` at the end of the scan,
  passing the **complete** set of keys the scan just saw. Invariant 1 applies
  unchanged: only call it when the scan actually completed.

`Raise` enforces one record per `idempotency_key` across expiry, so a condition
that outlives its own TTL revives its card rather than minting a new one (#108).

Set `Context.PR` when the request is about a pull request, so repo-scoped
producers can dedupe against it.

### Retracting when the trigger site only observes ONE key per invocation

`autoResolveAttention` / `Store.AutoResolveUnobserved` assume the caller just
evaluated the producer's **entire** condition set and passes the complete
observed set as evidence of what still holds — correct for `work-exhaustion`,
`owner-action-handoff`, and `watchdog-stuck-epic`, each of which re-scans
every card the producer could have raised on every cycle.

Some run-loop-scoped standing producers do not fit that shape: they only ever
observe a single `(repo, ...)` slice per invocation, never the producer's
whole condition set across every repo it has open cards for. Calling
`AutoResolveUnobserved` from such a site would retract every OTHER open card
of the same producer, mistaking "I did not look at it this time" for "I
looked and it is fine now" — exactly what Invariant 1 exists to prevent.

`unverified-deliverable-streak` (#177, per `(repo, tier)`) is this shape: one
issue's validate run only ever resets the tiers it observed executing, never
every repo's streaks. Use `Store.AutoResolveKey(producer, idempotencyKey)`
instead — it retracts exactly the one targeted key, leaving every other open
card from the same producer untouched.

## See also

- [ADR 015 — DecisionRequests](decisions/015-decision-requests.md) — the contract
  and its rationale, including the repo-scope amendment (Decisions L and M)
- [docs/GO_BINARY.md](GO_BINARY.md#attention-operations) — the `attention` CLI
- [docs/HEALTH_MONITORING.md](HEALTH_MONITORING.md) — how attention relates to
  health analysis
