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
