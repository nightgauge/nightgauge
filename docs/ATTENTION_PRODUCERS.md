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

|                  | **Run-scoped**                              | **Repo-scoped**                                    |
| ---------------- | ------------------------------------------- | -------------------------------------------------- |
| Fires on         | A transition inside a run                   | A condition observed by a sweep                    |
| Lives in         | `internal/orchestrator/attention_wiring.go` | `internal/attention/sweep/<name>.go`               |
| Raised by        | The scheduler calling `raiseAttention`      | Returning an observation from `Evaluate`           |
| Ends when        | A human resolves it, or it expires          | ...also when the condition stops being observed    |
| `context.run_id` | Set                                         | Absent                                             |
| Needs            | A stable `idempotency_key`                  | A stable `idempotency_key` **and** a `fingerprint` |

The question that decides it: **if nobody is running anything, can this
condition still be true?** A budget ceiling cannot — it only exists because a
run spent money. A red default branch can, and does, most of the time nobody is
looking.

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

Nothing in the registry can fix a red `main` or approve a PR. A card that offers
to anyway is worse than a card that offers nothing — the operator clicks it,
nothing changes, and the next card they see is one they have already learned to
distrust. Use `VerbNoop` for an honest dismiss and put the real next action in
`Context.URL`, which surfaces render as a first-class affordance.

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

A sweep runs on activation, on refresh, on a timer, and after every run. Your
producer's forge traffic is therefore recurring, not one-off:

- Cap how many entities you inspect, and say so in a constant with a comment.
- Prefer one list call carrying the fields you need over a list call plus a
  fetch per item.
- When a backlog is large, consider collapsing into one aggregate card above a
  cap. Thirty cards is not thirty decisions, and it buries every other producer.

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

Add a `raise*` method in `internal/orchestrator/attention_wiring.go` and call it
at the trigger point. The same identity rules apply — a stable
`idempotency_key`, options bound to registered verbs.

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

## See also

- [ADR 015 — DecisionRequests](decisions/015-decision-requests.md) — the contract
  and its rationale, including the repo-scope amendment (Decisions L and M)
- [docs/GO_BINARY.md](GO_BINARY.md#attention-operations) — the `attention` CLI
- [docs/HEALTH_MONITORING.md](HEALTH_MONITORING.md) — how attention relates to
  health analysis
