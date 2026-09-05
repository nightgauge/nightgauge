# Relayed Resolve Routing — the agent identity is per-machine, the attention store is per-workspace

**Date:** 2026-09-04
**Author:** nightgauge
**Status:** Decided (partially deferred — see § Deferred)
**Issue:** #1421
**Amends:** [ADR-015 § E](015-decision-requests.md) (the Surface contract's relay half)

---

## Executive Summary

Two identity models disagree, and the Action Center's platform-relayed resolve
sits on the seam.

- **Registration is per machine.** `AgentRegistrationService.RegisterAgent`
  sends exactly three fields — `machine_id`, `agent_version`, `capabilities`
  (`internal/platform/agent_registration.go:50-54`) — and the machine id is
  deliberately stable across every workspace on the host
  (`internal/platform/machine_id.go:16-19`). The platform upserts on it, so
  every `nightgauge serve` on one machine collapses into **one** agent row with
  one platform-assigned UUID.
- **The attention store is per workspace.** `attention.New(rootDir)` binds one
  directory, `<rootDir>/.nightgauge/attention`, and a card id **is** its
  filename (`internal/attention/store.go:113-119`, `:154-159`). There is no
  index and no scan-by-id: a card is reachable only from the root that holds it.

A relayed resolve is therefore addressed to a machine and applied against a
workspace. Whichever daemon holds the command stream consumes it; if the card
was raised under a different root, that daemon cannot apply it.

**The decision: the receiving daemon names the misroute and contains it. It does
not resolve the card on the owner's behalf, and it does not gamble on
redelivery.** Routing the command to the owning workspace requires a
platform-side contract change and is deferred.

## Context: what this repository can and cannot decide

The platform is a separate, closed repository (`AGENTS.md` § _Public Core
Boundary_), and `api/openapi.yaml` has never existed in this tree
(`api/platform-operations.yaml:18-24`) — so no field can be added to the
generated types here, and codegen is not reproducible. Server-side delivery is
keyed on the agent id: the stream subscribes to one channel per agent
(`internal/platform/attention_command.go:144-153`). **No local change converts a
machine-addressed bus into a workspace-addressed one.**

Equally important: the precise failure mode is **not observed from this
repository**. The evidence in #1421 is a log line from one daemon rejecting a
card that lives under another root. Whether the platform fans the frame out to
every subscriber, delivers to one, or delivers to the most recent, is not
determinable here. `skills/_shared/UNOBSERVED_MECHANISM.md` is explicit that a
retry, a fallback, or added tolerance must not be landed for a failure whose
mechanism has not been directly observed. That rule decides most of what
follows.

## Decision

### 1. `attention.ErrRequestNotFound` — "not mine" becomes a typed fact

`loadLocked` wraps a sentinel instead of formatting a bare string
(`internal/attention/store.go`). The message text is byte-identical, so callers
matching on the string still match; what changes is that `errors.Is` now works
across a package boundary.

This is the prerequisite for every other option, including the deferred ones:
nothing can be decided about a command that cannot be classified.

### 2. The relayed path classifies; the local IPC path does not change

`Server.ApplyRelayedResolve` maps `attention.ErrRequestNotFound` to
`platform.ErrRelayedRequestNotHere` and logs the misroute naming **this
daemon's workspace root and store directory** — without which an operator
reading a multi-daemon host's logs cannot tell which daemon answered.

`handleAttentionResolve` is deliberately untouched. ADR-015 § J requires that
resolution failures return a generic client error with detail logged
internally; distinguishing errors _to the card_ would contradict it. The
classification here is daemon-internal, on the relay path only.

### 3. The command is still acknowledged

The ack is unchanged, and that is a decision rather than an omission.

Declining it would redeliver onto the **same** per-machine agent channel that
just misrouted the command. If the platform does not replay, the outcome is
today's loss with one fewer log line — strictly less observable. If it does
replay, it replays to the same shared channel, so convergence is a coin flip
repeated without bound. `AttentionCommandConsumer`'s three always-ack tests
exist for exactly this hazard ("it would be redelivered forever").

What changes is that the outcome now carries `NotInThisWorkspace`, and the log
line says the card belongs to another workspace instead of reading as a bad
option or a broken verb.

### 4. No cross-root write, and no serve-registry discovery

Two designs were considered and rejected.

**A daemon resolving the card in the owning workspace's store directly.** The
verb does not travel. `Store.Resolve` executes the option's verb through the
_receiving_ server (`internal/ipc/attention.go`), whose scheduler, repo
resolver and steer root belong to its own workspace — so the resolution would
persist under one root while its side effect fired against another. Write
containment (`docs/MULTI_REPO_WORKSPACE.md`) forbids it, and the store has no
cross-process lock to make it safe regardless: `dirLocks` is an in-process
`sync.Map`, and `internal/attention` has no `internal/flock` usage. Doing this
safely means giving the attention store the #1163 treatment first, which is its
own issue.

**Discovering the owner from `~/.nightgauge/serve/` and handing off.** The
registry is not trustworthy enough to route on. Measured on the maintainer's
machine while writing this ADR: 150 sidecar records, of which **143 name a root
that no longer exists** and **2** have a `.nightgauge/attention` directory at
all; a further 174 `.lock` files have no matching record and their roots are
unrecoverable, because the filename is a truncated sha256
(`internal/runstate/serve_sidecar.go:105-108`). A daemon whose root cannot be
resolved writes no sidecar at all, so enumeration can never be proven
exhaustive. Registry hygiene is a real problem and a separate one.

### 5. Two daemons, one machine, in the test suite

`internal/ipc/attention_relay_routing_test.go` is the first test in this
repository to stand up two daemons at once — two roots, two stores, the same
repo slug registered on both. Every prior attention test builds one server and
therefore delivers every command to its owner, which is structurally blind to
this defect.

It asserts the containment (the non-owner mutates neither store and fires no
verb), the classification, the preserved ack, and — as a control — that the
**owning** daemon applies the identical command. Without that control the other
assertions would pass for a card that was never resolvable at all.

## Deferred

None of the following is required for the containment above to be correct, and
none of it can be done from this repository.

- **Per-workspace agent identity** — the registration upsert key and the
  platform's agent-eviction semantics. `NIGHTGAUGE_AGENT_ID`
  (`internal/platform/machine_id.go`) already overrides the machine id and
  would yield one row per workspace with no contract change, but it also feeds
  `Client.AgentID()` and would misattribute everything else keyed on the
  machine. It is not a fix.
- **Workspace identity in the registration body.** The fields are available at
  the call site (`cmd/nightgauge/main.go`, where `workspaceRoot`, `cfg` and
  `schedIdent` are all in scope) and the extension already sends a richer
  payload. It is deliberately **not** sent here: the receiving schema is out of
  tree, a strict schema would reject the unknown key, and a failed registration
  is a live outage traded for an unobservable improvement.
- **An ack vocabulary.** `AcknowledgeAgentCommand` posts `{}`
  (`internal/platform/commands.go`), so "applied", "rejected" and "not mine"
  are byte-identical on the wire. Telemetry only — the local outcome already
  carries the distinction.
- **Cross-machine misrouting**, which no machine-local mechanism can reach.

## Consequences

- A misrouted resolve is still lost. It is now _named_ at the point of loss,
  with the workspace root that answered — which is what a future session needs
  in order to observe the mechanism rather than reason about it.
- Acceptance criteria 1 and 2 of #1421 ("applied by the owning daemon",
  "not consumed by a non-owner") are **not** met by this ADR and cannot be met
  from this repository. #1421 stays open against the platform-side change.
  Criteria 3 (this document) and 4 (the two-daemon test) are met.
- The store gains a public sentinel. `ErrRequestNotFound` is now part of the
  package's contract; the message text is unchanged, so no caller had to move.

## Follow-ups filed

- **#1421 itself** remains the tracker for the platform-side routing key. It is
  not re-filed here: its acceptance criteria 1 and 2 already state the
  requirement, and the change is blocked on a repository this one cannot reach.
- **#1425** — the store's write-temp-then-rename used a fixed temp path
  (`path + ".tmp"`), so two processes materializing one card interleaved. The
  prerequisite for any future cross-root or multi-writer design, and the reason
  § 4 rejects one now. **Closed**: the store directory's critical section is
  now serialised across processes by an advisory `internal/flock` lock and
  every writer stages at its own temp path.
- **#1426** — serve-registry hygiene. The measurements quoted in § 4 are the
  issue's evidence: dead records, orphaned `.lock` files whose root is
  unrecoverable, and test suites writing into a real `HOME`.
