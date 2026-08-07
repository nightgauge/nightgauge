# `terminalkind` fixtures — provenance

Evidence and expectations for the canonical terminal-kind rule table
(`../table.json`, #306) and the captured telemetry they are built from (#166).

| File                   | What it is                                                                   |
| ---------------------- | ---------------------------------------------------------------------------- |
| `corpus.json`          | Behaviour corpus. Hand-authored expectations, each with a written rationale. |
| `captured-shapes.json` | Generated evidence. Real failure text from live telemetry, redacted.         |
| `stress-golden.json`   | Generated behaviour snapshot. Derived from the table, not hand-authored.     |

## What this pins, and why it needs pinning

Terminal-kind classification decides how the fleet reacts to a failure — failure
weighting ([docs/FAILURE_TAXONOMY.md](../../../docs/FAILURE_TAXONOMY.md)),
cascade feeding, lifetime caps, board reverts, backoff length. It used to exist
three times, hand-written and held aligned by comments; the ladders disagreed on
19 of 98 inputs. It is now **one ordered rule table** with three interpreters:

| Site       | How it gets the rules                                               | Authority                                                                       |
| ---------- | ------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| **Table**  | `internal/terminalkind/table.json`                                  | The definition. Nothing else holds a matching literal.                          |
| **Go**     | `internal/terminalkind` embeds it; `ClassifyTerminalKind` delegates | **Authoritative.** Writes `terminal_kind` into the run record; drives recovery. |
| **SDK**    | `terminalKindTable.generated.ts`, generated from the table          | Published classifier; reproduces Go by reading the same rules.                  |
| **Signal** | the extension projects the same table's `signal: true` rules        | Sent to Go with `autonomousComplete`; a **non-empty** answer is used verbatim.  |

`expected` in `corpus.json` is **Go's** answer, because Go writes the record.
`expected_signal` is what the extension may forward — the winning rule's kind
when that rule is in the signal subset, and empty otherwise, so it can never
contradict `expected`.

Three suites read this one file, so a bad expectation fails whichever CI reaches
first:

- `internal/terminalkind/corpus_test.go`
- `packages/nightgauge-sdk/tests/analysis/health/failureClassifier.corpusParity.test.ts`
- `packages/nightgauge-vscode/tests/services/terminalKindSignal.corpusParity.test.ts`

### What is actually pinned

| Guard                                                                | What a violation looks like                                                           |
| -------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Every row classifies to `expected` (all three suites)                | The table is wrong about a real failure shape.                                        |
| Every row projects to `expected_signal` (all three suites)           | A rule joined or left the signal subset without anyone deciding to.                   |
| Every rule WINS at least one corpus row (Go)                         | A rung of the ladder with no argued example behind it.                                |
| Every multi-term clause has a negative row for each half             | An AND quietly widened into an OR — round 2's blind spot, now red rather than diffed. |
| Every `TerminalKind*` constant has a rule and a row (Go)             | A **kind** added to the taxonomy and never routed to.                                 |
| The derived stress set reproduces `stress-golden.json` (all three)   | A clause deleted, a literal widened, or two rules swapped.                            |
| The generated SDK module is byte-identical to the table (Go + hooks) | A consumer edited on its own.                                                         |
| Predicate probes hold in both languages (Go + SDK)                   | The one non-literal term answering differently in the two runtimes.                   |
| `captured` rows exist in `captured-shapes.json`, and vice versa (Go) | A hand-authored string claims to be telemetry, or a real shape goes unclassified.     |

Round 2 tried to close the pattern-level hole from the outside, with a guard
requiring every matcher literal to APPEAR in some corpus input and a diff of the
two ladders' literal sequences. Both were evaded by execution: literal coverage
was satisfied by negative rows and by rows an earlier rule already claimed, and a
literal diff cannot see boolean structure, so flipping an `&&` to `||` in the
authoritative ladder left all three suites green. Neither guard exists any more.
They are unnecessary: `stress.go` derives an input for every clause, every term
and every ordered rule pair straight from the table, and the answers are
committed, so structure and precedence are checked directly rather than
inferred from string lists.

Two Go recovery paths ask a narrower question — "did the per-stage cost cap kill
this?", which stays true even for text a higher rule claims for the RECORD. They
used to carry their own copy of the three cost-cap spellings, one with a comment
explaining it was duplicated to avoid a reverse import on `orchestrator`. They
now call `terminalkind.RuleFires`, and the leaf package makes that import legal,
so those markers exist in one place too.

One term is deliberately unreachable — `exitSignalSource`, which carries
capitals and is therefore never satisfied against lowercased text. It is
preserved verbatim from Go rather than lowercased (that would be a live routing
change) and declared in the table's `dead_terms`, where the schema lint requires
it and a test proves the clause really is unsatisfiable.

## Captured

- **Capture date (UTC)**: 2026-08-07
- **Telemetry window**: 2026-04-02 → 2026-08-03 (recorded in the fixture as
  `telemetry_first_seen` / `telemetry_last_seen`)
- **Workspace roots scanned**: **21** (every root on this machine with a
  `.nightgauge/logs/` directory, across public and private repositories)
- **Log files scanned**: **1112**
- **Structured pipeline-logger lines scanned**: **956,903**
- **Distinct redacted failure shapes observed**: **35**
- **Shapes emitted** (minimal covering set): **18** — 13 pipeline-logger lines,
  5 adapter result envelopes
- **Mineable table literals**: 99 (of 114 total; the rest are too generic to
  mine on — see `GENERIC` in the script), of which **22** were observed in live
  telemetry

`distinct_shapes_observed: 35` versus 18 emitted is not a discrepancy to
explain away: the script emits a **minimal covering set**, so a newly observed
shape whose markers are already covered by a higher-occurrence shape is dropped
before it reaches this directory. The both-directions assertion therefore cannot
see those 17, and they are not reviewable anywhere. Widening the emission would
trade a reviewable fixture for an unreviewable one; the honest statement is that
this is a covering set, not a census.

One emitted shape carries `"log_truncated": true` — the logger had already cut
the line, so that row's input is the truncated text rather than the full string
the classifier received. It is kept because the markers survive truncation and
the shape is real, but it is not a byte-exact record of the producer's output.

Re-running the script against the same roots and the same logs reproduces the
file byte-for-byte. Two things make that true and neither is incidental:
selection is deterministic (highest occurrence count, ties broken
lexicographically, iterated in the table's own term order),
and **no emitted field comes from the wall clock** — the dated fields describe
the telemetry, so they move only when the telemetry does. The capture date above
is hand-written here precisely because it does not belong in the generated file:
stamping it there made every re-run on a later day a spurious diff, which is
what a byte-for-byte claim cannot survive.

```bash
scripts/capture-terminal-kind-fixture.sh $(ls -d ~/Repositories/*/*/.nightgauge/logs | sed 's#/.nightgauge/logs##')
```

### Why the miner is fussy

`.nightgauge/logs/*_session.log` interleaves two very different things:
structured pipeline-logger lines, and raw agent session output. The agent output
includes agents **reading and editing the classifier source** — so a naive
`grep -r '\[cost-cap-exceeded\]' .nightgauge/logs/` hits the classifier's own
source code as often as it hits a real failure, and a fixture built that way
would be evidence of nothing. That is not hypothetical: it is what the first
pass of this capture produced.

Two miners survive that:

1. **Pipeline-logger lines** — `[<ISO8601>] [LEVEL] [stage] [#issue] <message>`,
   with a source-code denylist on top (JSON envelopes, `tool_use_id`,
   backticks, `strings.Contains`, …).
2. **Adapter result envelopes** — `{"type":"result", …, "is_error":true}`, whose
   `result` field is the terminal error text **exactly as the CLI handed it to
   the pipeline**. This is the highest-grade evidence available: it is literally
   the classifier's input, and it is the only reason the raw session JSON is
   read at all.

The search vocabulary is not hand-written either — it is the literal terms of
`../table.json`, in table order, so the miner follows the classifier
automatically when the table grows a pattern.

### Redaction — deny by default, fail closed

This is a public repository and the mined logs are a multi-repo workspace's
telemetry, mixing private repositories' runs. #304's rule ("drop every string
that is not a bare machine token") cannot apply here: terminal error text is
prose by construction, and the prose is the shape under test. So the rule is
inverted at the **token** level — every construct that can carry an identity is
rewritten to a fixed placeholder, and then a verification pass re-scans every
emitted string and **aborts the whole run** if any survived:

| Construct             | Placeholder                    |
| --------------------- | ------------------------------ |
| `owner/repo` slug     | `REDACTED-OWNER/REDACTED-REPO` |
| absolute path         | `/REDACTED/PATH`               |
| branch name           | `REDACTED-BRANCH`              |
| URL                   | `https://redacted.invalid/`    |
| e-mail                | `redacted@redacted.invalid`    |
| `#<number>` forge ref | `#000`                         |
| git SHA               | `0000000`                      |
| title trailing a ref  | `(REDACTED)`                   |

Placeholders are deliberately shouty. The redactor is blunt — it cannot tell the
English phrase `repository/repositories` from a private slug, so it rewrites
both, and one captured shape reads
`…wrote outside its worktree into 1 REDACTED-OWNER/REDACTED-REPO it does not own`
for exactly that reason. A quiet, plausible-looking stand-in would leave a reader
unable to tell a redacted slug from a real one.

Nothing downstream inspects fixture string contents — the publication-boundary
guard classifies files, not the private slug inside a JSON string — so this pass
is the only mechanical gate there is. Two constructs it caught on real data: a
private repository's issue **title** trailing a `blocked-dependency` reference,
and a private `owner/repo` inside the same message.

Numeric ratios are exempt: `8/5000 remaining` in the GitHub quota preflight is a
machine count, and redacting it would destroy the shape while protecting
nothing.

## `captured` vs `synthetic`

`corpus.json` marks every row, and the Go suite enforces the distinction in both
directions:

- A row marked **`captured`** must appear verbatim in `captured-shapes.json`.
  A hand-authored string cannot be passed off as evidence.
- Every shape in `captured-shapes.json` must have a corpus row. A newly observed
  real failure cannot be quietly ignored — it has to be classified deliberately.
- A row marked **`synthetic`** must name the `producer` it was modelled on.
  Synthetic rows are not invented: they are the emitting call site's own
  template with placeholders filled (`SkillRunner.ts`'s
  `` `[stall-killed] ${stage} terminated` ``, `HeadlessOrchestrator`'s quota and
  approval messages, and so on). Round 2 added seventeen rows that broke this —
  bare kind tokens no producer emits, sharing one copy-pasted `producer`
  sentence that was false for sixteen of them — authored purely to satisfy the
  literal-coverage guard. They are gone along with the guard: mechanical
  coverage is derived from the table now, and a fixture row has to carry
  evidence or an argument.

Both directions are checks over this **tracked, generated file**. The script
cannot run in CI — it needs the operator's 21 local workspace roots — so what
they buy is that the evidence and its use move together in one reviewable diff:
promoting a hand-authored string to `captured` requires appending it here, in
the diff, next to the row that claims it. That is a review gate, not a
signature.

Synthetic rows **extend** coverage to markers, ordering overlaps and negatives
the live window happens not to contain. They never stand in for the real
population. Current split: **18 captured / 85 synthetic**.

## Why the captured set is smaller than the failure population

The exit-record store on this machine holds terminal kinds for far more failures
than there are captured shapes:

```text
budget_ceiling_hit 26   premature_turn_end 14   rate_limit_quota_exhausted 9
stall_kill 8            budget_exceeded 4       api_overloaded 4
pr_merge_unmerged 2     validation_failed 1     validation_error 1
runaway_progress 1      containment_breach 1
```

Neither the exit-record schema nor the run-record schema stores the raw error
**text** — only the resolved kind — so the strings themselves have to come from
the logs, where retention is shorter and coverage is uneven. The captured set is
therefore a floor on the real population, not a census: 18 shapes covering 22 of
99 classifier literals, concentrated (correctly) on the kinds that actually fire.

Four of the captured shapes were, before #306, classified **differently** by the
authoritative Go classifier and the SDK mirror — `exceeded stall idle threshold`,
`[cost-cap-exceeded]`, a bare `adapter-auth-failed`, and the USD budget ceiling.
Those are among the most frequent real failures the machine produces, so the
drift was not on exotic inputs.

## Changing a row

`rationale` is mandatory and is the reason the row exists — not a restatement of
`expected`. Changing an expectation means editing the argument for it in the
same diff. That is the point: a classifier edit that silently changes an outcome
has to argue with a written claim rather than update a string.

There is no `known_divergence` mechanism any more, and no divergence for it to
record. Round 2 carried two — a usage limit that names a model, and an overload
carrying an explicit quota marker — where the extension signalled one kind while
the record said another, each "tracked" at an issue that had nothing to do with
terminal-kind taxonomy. Both dissolved when the two sides started reading one
table with one precedence: the corpus keeps their inputs as ordering rows,
because the overlaps they exercise are real, but there is nothing left to
diverge. A site that disagrees with the table now is a bug in an interpreter,
not a gap to record.

Note what closing them cost, so it is not rediscovered as a regression: a bare
Anthropic `session limit` / `usage limit` with **no model named** is a shape the
table does not classify at all, so the extension no longer signals
`rate_limit_quota_exhausted` for it — Go decides, and Go returns nothing.
`ConcurrentPipelineManager` still skips the queue halt for that wording as a
documented local policy. Teaching the table about it would change the
authoritative classifier's live routing, which is a taxonomy decision rather
than a parity fix.
