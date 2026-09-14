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

## OpenCode: `opencode_stream_research_sample.jsonl`, `opencode_auto_reject_*`

**Real** `opencode run --format json` transcripts (#1624), not hand-authored
ones, captured by
[`scripts/capture-opencode-fixture.sh`](../../../scripts/capture-opencode-fixture.sh)
and redacted by [`redact-opencode.jq`](redact-opencode.jq):
`opencode_stream_research_sample.jsonl`, `opencode_auto_reject_stream.jsonl`
with `opencode_auto_reject_stderr.txt`, and
`opencode_auto_reject_heredoc_stream.jsonl` with
`opencode_auto_reject_heredoc_stderr.txt`. Each stage runs the argv the
adapter emits, with the prompt on stdin:

```bash
opencode run --format json --print-logs --log-level ERROR \
  -m lmstudio/qwen/qwen3.8-27b --dir <scratch git repository>
```

The model is this repository's stub provider (`cmd/stub-provider`), bound to
`127.0.0.1`. The run's throwaway config points a complete `lmstudio` provider
block at it (`env: []`, an empty `apiKey`), so no hosted provider, no model
server on another machine and no API key takes part. OpenCode runs under
`env -i` with a throwaway `HOME` and four throwaway XDG directories. The
heredoc capture's script, `bash-heredoc-then-stop`, is `bash-then-stop`
calling `python3 - <<'PYEOF'`, `print(1)`, `PYEOF` on three lines; the stub
embeds its scripts, so the capture script builds it from a staged copy whose
`scripts.json` adds that one script.

| Field               | Value                                                             |
| ------------------- | ----------------------------------------------------------------- |
| Captured at         | 2026-09-13                                                        |
| CLI version         | `1.18.30` (`opencode --version`)                                  |
| Host OS             | macOS 27.0 (Darwin 27.0.0, arm64)                                 |
| Research            | stub script `tool-edit-stop`, `edit` allowed; exit 0              |
| Auto-reject         | stub script `bash-then-stop`, `bash` set to `ask`; exit 0         |
| Auto-reject heredoc | stub script `bash-heredoc-then-stop`, `bash` set to `ask`; exit 0 |
| Redaction           | sandbox paths and the session id; no credential was found         |

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
5. The tool's input is printed unescaped, so the heredoc's notice spans three
   stderr lines: the first names the permission and ends inside the command,
   the last is the command's last line followed by `); auto-rejecting`. The
   parser takes the permission from the first line and keeps none of the
   command.

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
is the line OpenCode printed for it. `opencode_auto_reject_heredoc_stream.jsonl`
is one step (`tool-calls`, 1534 input, 4 output) whose heredoc `bash` call was
rejected, and `opencode_auto_reject_heredoc_stderr.txt` the three lines
OpenCode printed for it.

The stub counts words rather than tokens and reports no reasoning or cache
tokens, so `TestParseOpenCodeStream` also replays the two captured
`step_finish` events carrying the research numbers (7550 and 7750 input, 101
output and 54 reasoning) and cache pools, on the real event shape. Captures
from a real model, with reasoning tokens and subagent sessions, follow in
[§ OpenCode: real-model captures](#opencode-real-model-captures).

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
  `opencode db` and walks the tree itself. The subagent capture below pins
  this against a real run with two subagent sessions.
- A subagent may not start another subagent unless the config raises
  `subagent_depth`, which defaults to 1.
- `opencode --version` creates the XDG directories it finds missing, and
  `export` of an unknown session creates a database and a config file, both
  in the XDG directories, which are inside the run's root.
- `export` bootstraps a project from its working directory. Run without
  `--pure` from a scratch repository holding `.opencode/`, it wrote
  `.opencode/.gitignore` there and began installing that config's
  dependencies (with the npm registry pointed at a closed loopback port, it
  did not finish within 45 s). `--pure` ("run without external plugins") is
  among the options `export` and `db` both list.
  `export <session> --sanitize --pure` run from the run's root, with only
  `PATH`, `HOME`, `TMPDIR`, the four XDG variables and the
  `OPENCODE_DISABLE_*` switches set, returned `info.tokens` and each
  assistant message's `providerID` and `modelID`, and a plugin planted in the
  repository's `.opencode/plugin/` did not run; `db --pure` answered the same
  way. The parser runs every one of its processes like that.

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

## OpenCode: real-model captures

**Real** `opencode run --format json` transcripts (#1629), captured on the
maintainer's machine and redacted by [`redact-opencode.jq`](redact-opencode.jq), each
read by a `TestParseOpenCodeRealCapture*` test in `stream_test.go`:

| File                                     | Endpoint id                       | Model                     | Steps |
| ---------------------------------------- | --------------------------------- | ------------------------- | ----: |
| `opencode_stream_local_capture.jsonl`    | `lmstudio`                        | `qwen/qwen3.8-27b`        |     3 |
| `opencode_stream_subagent_capture.jsonl` | `lmstudio`                        | `qwen/qwen3.8-27b`        |     5 |
| `opencode_stream_subagent_stderr.txt`    | `lmstudio`                        | the subagent run's stderr |     - |
| `opencode_stream_cloud_capture.jsonl`    | none: the stub provider, as `xai` | `grok-4.6` (stub)         |     2 |
| second endpoint (`lmstudio-remote`)      | **BLOCKED**, not captured         | -                         |     - |

**`opencode_stream_cloud_capture.jsonl` is not a hosted provider's output.**
It is the repository's stub provider (`cmd/stub-provider`, script
`tool-edit-stop`) on `127.0.0.1`, dispatched as `xai/grok-4.6`, and it stands
in for the hosted shape until #1680 captures a real hosted model. The run's
config gives the `xai` key a complete provider block pointing at the stub
(`npm: @ai-sdk/openai-compatible`, `env: []`, an empty `apiKey`), and OpenCode
runs under `env -i`, so no hosted provider, no request off the machine and no
API key took part; the stub's log counted both model requests. What it shows
is the priced shape: OpenCode prices each step from its bundled catalog
(`part.cost` is 2 USD per million input tokens and 6 per million output), and
the served model is one the registry prices. The stub reports no cache
tokens, so hosted cache pools are #1680's to capture.

**The second-endpoint leg is BLOCKED.** The `lmstudio-remote` endpoint did
not answer on 2026-09-14 (the connection failed at every probe, before and
after the other legs), so there is no capture, fixture, test or manifest
entry for it. No hosted model was substituted. Its capture, which is to show
that the provider key and endpoint id are the only labels a stage records,
waits for that endpoint.

| Field       | Value                                                                 |
| ----------- | --------------------------------------------------------------------- |
| Captured at | 2026-09-14                                                            |
| CLI version | `1.18.30` (`opencode --version`)                                      |
| Host OS     | macOS 27.0 (Darwin 27.0.0, arm64)                                     |
| Model       | `qwen/qwen3.8-27b` on LM Studio, MLX 8-bit, loaded context 131072     |
| Endpoint    | `lmstudio`, on `127.0.0.1:1234`; `lmstudio-remote` BLOCKED            |
| Wall clock  | local 148 s, subagent 528 s, cloud stand-in 3 s; each capped at 900 s |
| Redaction   | sandbox paths and session ids; no credential was found                |

Each leg ran the argv the adapter emits, with the prompt on stdin, from a
throwaway git repository holding `calc.py` (`add` returning `a - b`; the
stub's script expects `a + b`):

```bash
opencode run --format json --print-logs --log-level ERROR \
  -m <provider key>/<model> --dir <scratch git repository>
```

- **Isolation (#1616).** `env -i`, a throwaway `HOME`, `TMPDIR` and four XDG
  directories, the `OPENCODE_DISABLE_*` switches of
  `scripts/capture-opencode-fixture.sh`, and a random
  `OPENCODE_SERVER_PASSWORD`. The run's config names one provider, as a
  complete block (`env: []`, an empty `apiKey`).
- **Bounds.** Each run was its own process group under a 900 s alarm
  (`perl -e 'setpgrp(0,0); alarm 900; exec @ARGV'`); after exit the group was
  killed and checked empty, and so was the stub's pid.
- **Sessions.** From the sandbox root, with the same environment,
  `opencode db "SELECT id, parent_id, time_created FROM session ..." --format json --pure`
  listed the session tree, and `opencode export <session> --sanitize --pure`
  was read for `info.tokens`, `info.cost` and the assistant messages'
  `providerID` and `modelID`, which are the numbers below. The exports were
  never kept. The sandbox, its session database with it, was deleted when
  each leg ended; the maintainer's own OpenCode database gained no session
  (its newest predates the captures).
- **Permissions.** Local: `edit` allowed, `bash`, `webfetch` and
  `external_directory` denied. Subagent: the same plus `task` allowed, and the
  `general` agent's `bash` set to `ask`, so a subagent's `bash` call is
  auto-rejected while the run's own session has no `bash` tool.
- **Prompts.** Local: "calc.py has a bug: add(a, b) should return the sum of a
  and b. Fix it." Subagent: "Use the task tool twice, one call after the other.
  First, have a general subagent read calc.py and report the bug in add.
  Second, have another general subagent run `python3 calc.py` with the bash
  tool and report what it printed. Then fix the bug in calc.py yourself." Cloud
  stand-in: "Change add so that it subtracts in calc.py."
- **Redaction.** `redact-opencode.jq` over stdout and stderr, then
  `scripts/capture-opencode-fixture.sh --check` on every file. The subagent
  run's stderr, one auto-reject notice, is committed redacted, as the #1624
  auto-reject stderr is; no raw stream, raw stderr or export was kept.

**Do not replace these files with synthesized equivalents.** A recapture on
another model or version will not reproduce these numbers; update the tables
and the tests' ground truth in the same change.

### Ground truth encoded in the files

`opencode_stream_local_capture.jsonl` (read, edit, stop):

| step    | reason       | input | output | reasoning | cache read | cache write | `part.cost` |
| ------- | ------------ | ----: | -----: | --------: | ---------: | ----------: | ----------: |
| 1       | `tool-calls` |  5640 |     44 |        10 |          0 |           0 |           0 |
| 2       | `tool-calls` |  5809 |     78 |         7 |          0 |           0 |           0 |
| 3       | `stop`       |  5916 |     22 |        30 |          0 |           0 |           0 |
| **sum** |              | 17365 |    144 |        47 |          0 |           0 |           0 |

The session's export: `info.tokens` 17365 input, 144 output, 47 reasoning,
no cache; `info.cost` 0; served by `lmstudio` / `qwen/qwen3.8-27b`. The stage
records 17365 input and 191 output (reasoning folded in), provider
`lm-studio`, a stamped zero.

`opencode_stream_cloud_capture.jsonl` (the stub's edit, then stop):

| step    | reason       | input | output | reasoning | cache read | cache write | `part.cost` |
| ------- | ------------ | ----: | -----: | --------: | ---------: | ----------: | ----------: |
| 1       | `tool-calls` |  1539 |      8 |         0 |          0 |           0 |    0.003126 |
| 2       | `stop`       |  1550 |      6 |         0 |          0 |           0 |    0.003136 |
| **sum** |              |  3089 |     14 |         0 |          0 |           0 |    0.006262 |

The session's export: `info.tokens` 3089 input, 14 output; `info.cost`
0.006262; served by `xai` / `grok-4.6`. The stage records provider `xai`,
model `grok-4.6`, priced from the registry's rate card, which is not
OpenCode's 0.006262 (ADR-022 § 3).

`opencode_stream_subagent_capture.jsonl`, the run's own session (task,
task, read, edit, stop):

| step    | reason       | input | output | reasoning | cache read | cache write | `part.cost` |
| ------- | ------------ | ----: | -----: | --------: | ---------: | ----------: | ----------: |
| 1       | `tool-calls` |  5681 |    110 |        12 |          0 |           0 |           0 |
| 2       | `tool-calls` |  5915 |    108 |        60 |          0 |           0 |           0 |
| 3       | `tool-calls` |  6140 |     45 |        85 |          0 |           0 |           0 |
| 4       | `tool-calls` |  6386 |     79 |        23 |          0 |           0 |           0 |
| 5       | `stop`       |  6510 |     97 |        70 |          0 |           0 |           0 |
| **sum** |              | 30632 |    439 |       250 |          0 |           0 |           0 |

The session tree and each session's export (`info.tokens`, `info.cost`), all
served by `lmstudio` / `qwen/qwen3.8-27b`:

| session                          | parent | input | output | reasoning | cache | cost |
| -------------------------------- | ------ | ----: | -----: | --------: | ----: | ---: |
| `ses_fixture0000000000000000001` | none   | 30632 |    439 |       250 |     0 |    0 |
| `ses_fixture0000000000000000002` | `…001` | 12832 |     95 |        18 |     0 |    0 |
| `ses_fixture0000000000000000003` | `…001` |  6330 |     49 |        15 |     0 |    0 |
| **stage**                        |        | 49794 |    583 |       283 |     0 |    0 |

The parent's export equals the stream's sum; the stage's usage is the three
exports' sum, 49794 input and 866 output. A parser that summed only the
stream would book 30632 and 689. The peak step prompt stays the parent's
6510: a subagent's session total is not a step.

### Observed on opencode 1.18.30

- The three assumptions held: no final usage event; no subagent step in the
  run's stream, every event of which carries the run's own `sessionID`; and
  `part.cost` and `info.cost` 0 for the local model.
- Qwen reports reasoning tokens (`part.tokens.reasoning`) on every step, yet
  without `--thinking` the stream has no `reasoning` event; the export has the
  reasoning parts.
- The stream is not sanitized, and a `task` tool event names its subagent
  session: `part.state.metadata.sessionId` (with `parentSessionId` and the
  subagent's `model`), the output's `<task id="ses_…" state="completed">`,
  and a failed call's `Subagent failed (task_id: ses_…): …`. The parser reads
  none of these; the session table stays the record it walks.
- A subagent's rejected permission does not end the run. OpenCode prints the
  notice on the run's stderr, the subagent's session ends, the run's `task`
  call fails with `Subagent failed (task_id: <session>): ` and the rejection
  message, which is not the rejection of the run's own call, and the run goes
  on: here it read `calc.py`, fixed it and stopped, and exited 0. The notice
  is the first on stderr, so it decides the stage's marker (ADR-022 § 9): the
  stage reports exit 1 with `tool=bash` although its own session finished the
  task.
