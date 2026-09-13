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

## `codex_stream_real_capture.jsonl`

A **real** Codex CLI `exec --json` transcript (#1620), not a hand-authored
one. Captured 2026-09-13 on `codex-cli 0.153.4`, logged in with the account
already present on the capturing machine, with:

```bash
codex --ask-for-approval never --sandbox workspace-write exec --json - \
  <<< 'Use a shell command to run "echo one", then again to run "echo two", then reply done.'
```

### Why the flags differ from `BuildCommand`'s literal argv

`CodexAdapter.BuildCommand` ([codex.go](../adapters/codex.go)) emits
`exec --dangerously-bypass-approvals-and-sandbox --json -` when a stage has no
`AllowedTools` (the default), which is the path this capture set out to
reproduce. That exact invocation is unavailable from this capture
environment (a policy blocks spawning an unsandboxed sub-agent from an
already-sandboxed session), so the capture instead exercises
`BuildCommand`'s other real branch: `codexSandboxFlags` for a
`workspace-write`-eligible stage, which is `--sandbox workspace-write
--ask-for-approval never` appended **after** `exec`
([codex_sandbox.go](../adapters/codex_sandbox.go)).

Running that literal argv against the installed `codex-cli 0.153.4` fails:

```
error: unexpected argument '--ask-for-approval' found
```

`codex exec --help` on this version lists no `--ask-for-approval` flag at
all; `-a`/`--ask-for-approval` only appears on the base `codex --help` (before
the `exec` subcommand). This capture therefore places the two flags before
`exec` instead, which is the same sandbox policy `BuildCommand` intends
(workspace-write, never ask) reached through an argv order the real 0.153.4
binary accepts. **This is a `BuildCommand`/CLI-version mismatch, not a stream
parser mismatch** — `ParseCodexStreamLine`'s field-name and per-event
assumptions all held against the real output below — so it is out of this
issue's scope (`internal/execution/adapters/codex_sandbox.go` is not among
this issue's owned files). It is recorded here as the reason this fixture's
command differs from `BuildCommand`'s output, and is reported back rather
than fixed in this PR.

### CLI version note

The `codex` manifest's `max_tested` ([codex.json](../../adaptercompat/manifests/codex.json))
is `0.145.0`. The capturing machine has `0.153.4` installed — newer than
`max_tested`, not older, so the manifest's floor policy is not implicated.
Only the version number differs; nothing in the NDJSON shape or the
`turn.completed` usage payload contradicts `ParseCodexStreamLine`, so the
manifest's version fields are left unchanged, per this issue's scope.

### Why it is captured rather than written

Same reason as the claude and grok captures (#166/#300): a parser tested only
against hand-written lines stays green while the runtime emits a different
shape. This capture confirms `ParseCodexStreamLine`'s two shape assumptions
against real output:

1. Per-turn usage arrives on a single `type:"turn.completed"` event's `usage`
   object, with `input_tokens` / `cached_input_tokens` / `output_tokens`
   exactly where the parser reads them. The real payload also carries
   `cache_write_input_tokens` and `reasoning_output_tokens`, which
   `codexUsage` does not declare — `json.Unmarshal` ignores them silently,
   which is correct today (codex reports 0 for both here) but is the first
   place to look if a future capture shows nonzero reasoning tokens going
   unbooked.
2. A tool call is `type:"item.completed"` with `item.type:"command_execution"`
   and the command/output on `item.command` / `item.aggregated_output` — not
   `"agent_message"`, which is reserved for the assistant's own text turns.
   `ParseCodexStreamLine` only special-cases `"agent_message"`
   (event becomes `message`/`text`); `command_execution` items pass through
   with `event.Type` left as `"item.completed"`, which is the parser's
   intended default for anything it does not need to remap for token
   purposes.

**Do not replace this file with a synthesized equivalent.** Recapture it if
the CLI's shape changes.

### Ground truth encoded in the file

One turn (`turn.completed`), its `usage` object:

| turn      | input_tokens | cached_input_tokens | output_tokens |
| --------- | -----------: | ------------------: | ------------: |
| turn 1    |        67553 |               56704 |           124 |
| **total** |        67553 |               56704 |           124 |

`ParseCodexStreamLine` stores `input_tokens - cached_input_tokens` as input
(the cached subset is disjoint from input, matching the Claude/SDK
convention) and `cached_input_tokens` as cache-read:

| accumulator field |                 value |
| ----------------- | --------------------: |
| `InputTokens`     | 67553 − 56704 = 10849 |
| `OutputTokens`    |                   124 |
| `CacheRead`       |                 56704 |
| `CacheCreated`    |                     0 |

### Redaction

`redact-cli.jq` (new, shared across the codex/gemini/copilot family of real
captures this issue introduces) is shape-preserving in the same sense as
`redact.jq` / `redact-grok.jq`: it only rewrites values, never adds, drops, or
reorders a key or event, and never touches a token count. Run on this capture
it changed exactly one value — `thread.started`'s `thread_id` — to a stable
placeholder. The raw capture had no absolute path, email address, or other
identifier to redact (the capture's scratch working directory never appears
in any event; commands ran via `/bin/zsh -lc '<cmd>'` with no path argument).

### gemini and copilot

`gemini_stream_real_capture.jsonl` and `copilot_stream_real_capture.jsonl`
are **skipped**: neither CLI is installed on the maintainer's machine that
performed this capture (`gemini`, `copilot` both resolve to "not found"). The
acceptance criteria for this issue allow recording a skip with that reason
rather than installing a new CLI or authenticating a new account to produce
one. No fixture, README ground-truth table, manifest entry, or
`TestParse{Gemini,Copilot}RealCapture` test exists for either adapter as a
result — `ParseGeminiStreamLine` and `ParseCopilotStreamLine` remain untested
against real output until a maintainer with those CLIs installed captures
them.

## OpenCode: `opencode_stream_research_sample.jsonl`, `opencode_auto_reject_stream.jsonl`, `opencode_auto_reject_stderr.txt`

**Real** `opencode run --format json` transcripts (#1624), not hand-authored
ones, captured by
[`scripts/capture-opencode-fixture.sh`](../../../scripts/capture-opencode-fixture.sh)
and redacted by [`redact-opencode.jq`](redact-opencode.jq). Each stage runs the
argv the adapter emits, with the prompt on stdin:

```bash
opencode run --format json --print-logs --log-level ERROR \
  -m lmstudio/qwen/qwen3.8-27b --dir <scratch git repository>
```

The model is this repository's stub provider (`cmd/stub-provider`), bound to
`127.0.0.1`. The run's throwaway config points a complete `lmstudio` provider
block at it (`env: []`, an empty `apiKey`), so no hosted provider, no model
server on another machine and no API key takes part. OpenCode runs under
`env -i` with a throwaway `HOME` and four throwaway XDG directories.

| Field       | Value                                                     |
| ----------- | --------------------------------------------------------- |
| Captured at | 2026-09-13                                                |
| CLI version | `1.18.30` (`opencode --version`)                          |
| Host OS     | macOS 27.0 (Darwin 27.0.0, arm64)                         |
| Research    | stub script `tool-edit-stop`, `edit` allowed; exit 0      |
| Auto-reject | stub script `bash-then-stop`, `bash` set to `ask`; exit 0 |
| Redaction   | sandbox paths and the session id; no credential was found |

### Why it is captured rather than written

Same reason as the claude, grok and codex captures (#166, #300): a parser
tested only against hand-written lines stays green while the CLI emits
something else. The capture pins these shapes, each of which the parser
depends on:

1. Every line is one event: `{type, timestamp, sessionID, part}`. The error
   event carries `error` in place of `part` (observed against the stub's
   `error` script; not committed, because OpenCode retries it for over a
   minute).
2. `step_finish` carries the step's usage in `part.tokens`
   (`total`, `input`, `output`, `reasoning`, `cache.read`, `cache.write`),
   the finish reason in `part.reason` (`tool-calls`, then `stop`) and
   `part.cost`, which is `0` for a provider OpenCode holds no price for.
   OpenCode subtracts both cache pools from `input` and the reasoning tokens
   from `output`, so the five fields are disjoint.
3. There is no final usage event, and no event names the model or the CLI
   version.
4. A permission that resolves to `ask` is rejected on its own: stderr gets
   `! permission requested: bash (python3 calc.py); auto-rejecting`, with
   terminal escape codes around the `!` although stderr is a file; the
   `tool_use` event has `status: "error"` and OpenCode's rejection message;
   the run ends after that step and exits 0. The line names the permission,
   which for the write tools is `edit`, and the tool's input, which the
   parser never reads.

**Do not replace these files with synthesized equivalents.** Recapture them
with the script, which refuses an OpenCode version other than the one it pins.

### Ground truth encoded in the files

`opencode_stream_research_sample.jsonl`, two steps:

| step    | reason       | input | output | reasoning | cache read | cache write |
| ------- | ------------ | ----: | -----: | --------: | ---------: | ----------: |
| 1       | `tool-calls` |  1539 |      8 |         0 |          0 |           0 |
| 2       | `stop`       |  1550 |      6 |         0 |          0 |           0 |
| **sum** |              |  3089 |     14 |         0 |          0 |           0 |

The stage's usage is the sum; a parser that kept only the last step would book
1550 and 6. The peak step prompt (input plus both cache pools) is 1550.

`opencode_auto_reject_stream.jsonl` is one step (`tool-calls`, 1533 input, 3
output) whose `bash` call was rejected, and `opencode_auto_reject_stderr.txt`
is the line OpenCode printed for it.

The stub counts words rather than tokens and reports no reasoning or cache
tokens, so `TestParseOpenCodeStream` also replays the two captured
`step_finish` events carrying the research numbers (7550 and 7750 input, 101
output and 54 reasoning) and cache pools, on the real event shape. Captures
from a real model, with reasoning, cache and subagent sessions, are #1629's.

### Observed on the same version, not committed

- `opencode export <session> --sanitize` writes `Exporting session: <id>` to
  stderr and `{info, messages}` to stdout. `info.tokens` is the session's
  total (here equal to the stream's sum) and `info.cost` its cost; each
  assistant message's `info` has `providerID` and `modelID`. Prompts, replies,
  tool input, output and metadata are redacted.
- `opencode session list` lists root sessions only, and its JSON has no parent
  id; a sanitized export redacts the `task` tool's metadata, which names the
  child session. The session table's `parent_id` column is the record of which
  session started which, so the parser lists subagent sessions with
  `opencode db` and walks the tree itself. #1629's subagent capture pins this.
- A subagent may not start another subagent unless the config raises
  `subagent_depth`, which defaults to 1.
- `opencode --version` creates the XDG directories it finds missing, and
  `export` of an unknown session creates a database and a config file. The
  parser runs both in the stage's own environment, inside the run's root.

### Redaction

`redact-opencode.jq` is shape-preserving in the same sense as `redact.jq`: it
only rewrites string values, and never adds, drops or reorders a key or an
event, or touches a token count or cost. It rewrites the capture's sandbox
paths to `/tmp/nightgauge-fixture`, gives each session id a stable
`ses_fixture…` placeholder, replaces an error event's `url`, and removes the
credential shapes `RedactCredentials` removes from a live stage's output. The
stderr keeps its escape codes. The script then refuses any file that still
holds a credential shape, an IPv4 address other than `127.0.0.1`, a sandbox
path or the run's server password; `--self-test` proves the refusal by
planting a credential, and `--check` runs it on existing files.
