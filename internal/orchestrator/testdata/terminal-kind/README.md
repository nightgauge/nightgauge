# `terminal-kind` fixtures — provenance

The shared corpus that pins all three terminal-kind classifiers (#306), and the
captured telemetry it is built from (#166).

| File                   | What it is                                                           |
| ---------------------- | -------------------------------------------------------------------- |
| `corpus.json`          | The corpus. Hand-authored expectations, each with a rationale.       |
| `captured-shapes.json` | Generated evidence. Real failure text from live telemetry, redacted. |

## What this pins, and why it needs pinning

Terminal-kind classification exists three times, and every copy decides how the
fleet reacts to a failure — failure weighting
([docs/FAILURE_TAXONOMY.md](../../../../docs/FAILURE_TAXONOMY.md)), cascade
feeding, lifetime caps, board reverts, backoff length:

| Site       | Implementation                                                                         | Authority                                                                       |
| ---------- | -------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------- |
| **Go**     | `ClassifyTerminalKind` — `internal/orchestrator/failure_handler.go`                    | **Authoritative.** Writes `terminal_kind` into the run record; drives recovery. |
| **SDK**    | `classifyTerminalKind` — `packages/nightgauge-sdk/…/failureClassifier.ts`              | Published mirror. No in-repo runtime caller today; consumed by SDK users.       |
| **Signal** | `classifyTerminalKindForSignal` — `packages/nightgauge-vscode/…/terminalKindSignal.ts` | Sent to Go with `autonomousComplete`; a **non-empty** answer is used verbatim.  |

`expected` in `corpus.json` is **Go's** answer, because Go writes the record: a
disagreement is by definition another site being wrong about what the pipeline
recorded.

Three suites read this one file, so drift fails whichever CI reaches first:

- `internal/orchestrator/terminal_kind_corpus_parity_test.go`
- `packages/nightgauge-sdk/tests/analysis/health/failureClassifier.corpusParity.test.ts`
- `packages/nightgauge-vscode/tests/services/terminalKindSignal.corpusParity.test.ts`

## Captured

- **Date (UTC)**: 2026-08-07
- **Workspace roots scanned**: **21** (every root on this machine with a
  `.nightgauge/logs/` directory, across public and private repositories)
- **Log files scanned**: **1112**
- **Structured pipeline-logger lines scanned**: **956,903**
- **Distinct redacted failure shapes observed**: **35**
- **Shapes emitted** (minimal covering set): **18** — 13 pipeline-logger lines,
  5 adapter result envelopes
- **Go classifier literals**: 99, of which **22** were observed in live
  telemetry

Re-running the script against the same roots and the same logs reproduces the
file byte-for-byte: selection is deterministic (highest occurrence count, ties
broken lexicographically, iterated in the Go classifier's literal-declaration
order).

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

The search vocabulary is not hand-written either — it is extracted from the
`strings.Contains(t, "…")` literals inside `ClassifyTerminalKind` and
`isModelUnavailableText`, so the miner follows Go automatically when Go grows a
pattern.

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
  approval messages, and so on).

Synthetic rows **extend** coverage to markers, ordering overlaps and negatives
the live window happens not to contain. They never stand in for the real
population. Current split: **18 captured / 50 synthetic**.

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

`known_divergence` records a site that deliberately disagrees with `expected`
today. It **pins** the disagreement rather than hiding it — the divergent side
must produce exactly the stated kind — so the gap is visible in test output and
any new divergence is red. Resolving one is a taxonomy decision about how the
fleet should react, which is #305/#370 territory, not a parity fix.
