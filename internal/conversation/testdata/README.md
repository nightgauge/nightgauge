# `internal/conversation` test fixtures

Real Claude Code captures of conversation turns, recorded with the argv the
`claude-headless` conversational builder produces (spike #1568 § 3.1, #1569),
on Claude Code `2.1.296`, then passed through [`redact.jq`](redact.jq). They
are the recorded evidence the spike's viability bar (§ 13) asks for, pinned by
`TestViability_*` and `TestRenderMessage_MatchesTheRecordedReplay`. Do not
replace them with synthesized equivalents; recapture them if the CLI's shape
changes.

Each run was a new process in a scratch directory, the message on stdin:

```bash
claude -p --no-session-persistence --model <model> [--effort <rung>] \
  --output-format stream-json --verbose --include-partial-messages \
  --restricted --permission-mode dontAsk --tools "" --strict-mcp-config
```

| File                         | Model, effort               | Stdin                                                                  |
| ---------------------------- | --------------------------- | ---------------------------------------------------------------------- |
| `claude_turn_stream.jsonl`   | `claude-sonnet-5-5`, `low`  | "Write five sentences about rivers, at least sixty words in all. …"    |
| `claude_turn_replay_1.jsonl` | `claude-haiku-4-5-20251001` | "My favourite fruit is the plum. Reply with just the word: noted."     |
| `claude_turn_replay_2.jsonl` | `claude-haiku-4-5-20251001` | `claude_turn_replay_2.stdin.txt`: turn 1's exchange, then the question |

- `claude_turn_stream.timing.json` holds each kept line's arrival, in
  milliseconds from spawn: the streaming condition reads the first text
  delta's lead over its block's `content_block_stop` from it.
- The replay's second turn ran in a new process with persistence off; its
  only knowledge of the first turn was the JSON in its stdin, and it answered
  `plum`.
- `redact.jq` drops the account's rate-limit line and local hook lines,
  reduces `system/init` to what a stream consumer reads (keeping
  `per_turn_effort_active`), and replaces session ids, uuids, thinking
  signatures and the per-line `api_message_id` with placeholders. Every usage
  payload, every `message.id`, the event order and every text delta are as
  the CLI emitted them.
