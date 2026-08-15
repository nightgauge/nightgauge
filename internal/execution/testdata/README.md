# `internal/execution` test fixtures

## `claude_stream_real_capture.jsonl`

A **real** Claude CLI transcript, not a hand-authored one. Captured with:

```bash
claude -p "Use the Bash tool to run 'echo one', then use it again to run \
'echo two', then reply done." \
  --output-format stream-json --verbose \
  --model claude-haiku-4-5-20251001 --allowedTools Bash
```

on Claude Code `2.1.223`, then passed through [`redact.jq`](redact.jq).

### Why it is captured rather than written

Issue #300 is an instance of the #166 silent-no-op class: the parser was tested
against a shape the runtime does not emit, so the tests stayed green while the
Go auto/CLI path booked zero tokens for **every** run. Hand-authoring the
fixture would have reproduced the same fiction. Two shape facts that only a
real capture exposes, both of which the old code got wrong:

1. Per-turn usage arrives on `type:"assistant"` events (`message.usage`) — the
   parser handled `"result"` and `"message"` only.
2. On the terminal `type:"result"` event, `result` is the assistant's final
   **text string** and `usage` sits at the **top level** — the parser declared
   `result` as a struct, so `json.Unmarshal` failed and dropped the event
   whole.

**Do not replace this file with a synthesized equivalent.** Recapture it if the
CLI's shape changes.

### Redaction

`redact.jq` is shape-preserving: it only rewrites values and drops whole
lines — it never adds a key, changes a key's position, or alters any token
count. What it does:

- Drops `system/hook_*` lines (local `~/.claude` hook telemetry).
- Reduces `system/init` to the fields a stream consumer reads. The raw event
  carries the whole local environment (`skills`, `plugins`, `agents`,
  `slash_commands`, `memory_paths`, absolute `cwd`).
- Replaces `session_id` / `uuid` / `request_id` / thinking `signature` with
  stable placeholders.

Everything else — every `usage` payload, every `message.id`, the event order,
and the `result` event's key layout — is byte-for-byte as the CLI emitted it.

### Ground truth encoded in the file

Two assistant turns, five assistant events (the CLI emits one per content
block, each repeating that turn's usage):

| turn                        | input | output | cache create | cache read |
| --------------------------- | ----: | -----: | -----------: | ---------: |
| `msg_011CdkixpD3Jjqq1eV8p…` |    10 |      3 |         3048 |      13287 |
| `msg_011Cdkiy8tCJsBAWYxS2…` |     8 |      1 |          260 |      16335 |
| **sum**                     |    18 |      4 |         3308 |      29622 |
| `result` event              |    18 |    236 |         3308 |      29622 |

The deduped per-turn sums reproduce the result event's input and cache totals
exactly — which is why assistant usage is summed across distinct `message.id`
values rather than maxed. Output is the one field where the streamed snapshot
is a partial (3+1 against a final 236); a killed stage therefore under-reports
output tokens, but reports real non-zero cost instead of a fabricated free run.

## `claude_stream_subagent_multi_result.jsonl`

A second real capture — same CLI version and the same `redact.jq` — of a stage
that spawns a `Task` subagent:

```bash
claude -p "Use the Task tool to launch one general-purpose subagent that runs \
'echo subagent-ran' with Bash and reports the output. Then reply done." \
  --output-format stream-json --verbose \
  --model claude-haiku-4-5-20251001 --allowedTools Bash Task
```

It exists because it carries two shapes the single-turn capture cannot, both of
which decide how the accumulator must combine its sources:

**A run emits more than one `result` envelope, and their `usage` is a delta.**

| envelope | num_turns | input | output | cache create | cache read | total_cost_usd |
| -------- | --------: | ----: | -----: | -----------: | ---------: | -------------: |
| 1        |         3 |    28 |    755 |         4903 |      47277 |      0.0280542 |
| 2        |         1 |    10 |    141 |          557 |      18190 |      0.0335836 |
| **sum**  |           |    38 |    896 |         5460 |      65467 |                |

The second envelope is smaller than the first in **every** token field, so the
payloads cannot be running totals — they are deltas, and envelopes must sum.
`total_cost_usd` moves the other way (0.028 → 0.034): it alone is
session-cumulative. That asymmetry is the one #256 was booked against, where
six summed cumulative envelopes reported $100.47 for a $23.67 stage. The TS
`TokenAccumulator.add()` already encodes exactly this split — sum the token
counts, delta the cost.

**Envelopes do not account for subagent turns.** The capture has six distinct
assistant turns, two of them the subagent's (`parent_tool_use_id` set):

| source                       | input | output | cache create | cache read |
| ---------------------------- | ----: | -----: | -----------: | ---------: |
| result envelopes (summed)    |    38 |    896 |         5460 |      65467 |
| assistant turns (6, deduped) |    56 |     16 |        13350 |      72701 |

The envelope sums equal the four **main-thread** turns exactly; the extra
18 input / 7890 cache-create tokens are the subagent's, and no envelope ever
reports them. So neither source is complete on its own — envelopes carry the
only accurate output count, turn snapshots are the only ones that see
subagents — and the accumulator takes the better-informed of the two per field.

## `grok_stream_real_capture.jsonl`

A **real** Grok Build CLI transcript (#533), not a hand-authored one. Captured
on `grok 1.0.4 (d846eb93d94d) [stable]`, logged in with grok.com, with:

```bash
grok --output-format streaming-json --always-approve --no-auto-update \
  --cwd "$PWD" \
  -p "Use the Bash tool to run 'echo one', then reply done." \
  --model grok-4.6 --max-turns 6
```

then passed through [`redact-grok.jq`](redact-grok.jq). The run exited 0 and
cost $0.00448528.

### Why it is captured rather than written

Same reason as `claude_stream_real_capture.jsonl` (#300/#166): a parser tested
only against a shape the runtime does not emit stays green while booking
nothing. Three shape facts this capture pins that no hand-authored file would
have got right:

1. `streaming-json` really is the format the adapter asks for, and the terminal
   event is `type:"end"` carrying a **session-total** `usage` — not a delta.
   Per-turn `type:"usage"` events precede it with that turn's snapshot only
   (3593 then 3540 input), so the accumulator's assign-don't-sum behavior lands
   on the `end` totals rather than double-counting.
2. `reasoning_tokens` is a **sibling** of `output_tokens`, not a component of
   it — the CLI reports 89 output and 49 reasoning against 30390 total.
3. Live stdout emits `tool_call` / `tool_call_update`. It does **not** emit
   `tool_started` / `tool_completed` / `phase_changed`: those are the
   **session-file** schema under
   `~/.grok/sessions/<url-encoded-cwd>/<id>/events.jsonl`, a different stream.
   #533's original AC3 named the session-file events for stdout; that mismatch
   is why it was split out rather than implemented here.

**Do not replace this file with a synthesized equivalent.** Recapture it if the
CLI's shape changes.

### Ground truth encoded in the file

The terminal `end` event:

| field                         | value |
| ----------------------------- | ----: |
| `input_tokens`                |  7133 |
| `output_tokens`               |    89 |
| `reasoning_tokens`            |    49 |
| `cache_read_input_tokens`     | 23168 |
| `cache_creation_input_tokens` |     0 |

`ParseGrokStreamLine` folds reasoning into output, so the accumulator lands on
in=7133, out=138, cache-read=23168, cache-created=0.

### Redaction

`redact-grok.jq` is shape-preserving in the same sense as `redact.jq`: it only
rewrites values and drops keys — it never adds a key, changes a key's position,
reorders an event, or alters any token count. What it does:

- Reduces `available_commands` (Grok's analogue of claude's `system/init`) to
  its built-in `tools` roster, dropping the `commands` list of every locally
  installed plugin's slash commands.
- Rewrites absolute local paths, which appear inside `tool_call_update`
  payloads (`current_dir`, `output_file`) rather than only at top level.
- Replaces `sessionId` / `requestId` / the model's opaque reasoning
  `signature` with stable placeholders.

Every `usage` payload, the `end` event's key layout, and the event order are
byte-for-byte as the CLI emitted them.

## `grok_unknown_model_stdout.jsonl` / `grok_unknown_model_stderr.txt`

The **real** failure the #533 fix is about, captured from the same CLI build:

```bash
grok --output-format streaming-json --always-approve --no-auto-update \
  -p "reply ok" --model grok-build-0.1 --max-turns 1
```

`grok-build-0.1` is not a model this CLI serves (`grok models` lists only
`grok-4.6` and `grok-4.5`). The process exits **1**, writes one `type:"error"`
line to stdout and the same sentence to stderr. Neither file is redacted —
neither contains a path, an id, or a token count.

This is the input that makes the defect visible. `ClassifyTerminalKind` matches
the `unknown model` clause in `internal/terminalkind/table.json` and answers
`model_unavailable`, which routes to the #42 sticky **downgrade**. But CLI mode
never delivered this text to the classifier — `execution.Manager.RunStage`
returns `(result, nil)` on a non-zero exit with the text on `result.Stderr`,
and `ExecutionManagerRunner.RunStage` dropped it — so the scheduler classified
the literal string `exit 1: <nil>` and got `subagent_crash` (the #520
signature), then **escalated** the model upward. See
`TestCLIStageErrorTextReachesClassification_GrokUnknownModel` in
`internal/orchestrator/scheduler_failure_test.go`.

Only the **stderr** copy is used as the classifier's input. The stdout capture
is kept as forensic evidence (`LastOutputLines`) and is never classified: for a
CLI adapter, stdout is the whole streaming-JSON transcript, `tool_result`
payloads included, and `ClassifyTerminalKind` is an ordered substring ladder —
a `go test` line containing `hard cap` would read as a stall-kill. The stderr
copy is sufficient, which is why the fixture pair exists: it proves the reason
is on both channels and that taking the narrow one loses nothing.
