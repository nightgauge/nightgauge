# Conversation turns: the daemon's side of the contract

The Go daemon (`nightgauge serve`) answers the conversation turns the hosted
service addresses to its platform agent. It runs each turn on the workspace's
own execution adapter, performance mode and effort, streams the reply back,
and reports how the turn ended (#1569). This document states the daemon's
side of the conversation contract (ADR-017); the hosted service's OpenAPI
document is the authority on the wire. Later issues add their parts: the tool
boundary (#1570), the conversation tools (#1587), retention classification
(#1893), and the acts a turn may take (#1894, #1895).

Code: `internal/conversation` (the turn host), `internal/platform/conversation_turn.go`
(the wire), `internal/execution/adapters/conversation.go` (the builders),
`internal/credshape/egress.go` (redaction).

## Who hosts turns

A turn is one posted message, answered by one model turn on one workspace.
The daemon advertises the `conversation` capability on its registration and
on every heartbeat, and only while the workspace's resolved adapter has a
conversational turn builder. A heartbeat replaces the stored capability list,
so switching to an adapter that cannot host a turn withdraws the capability
within one beat (30 seconds).

Spike #1568 set the bar an adapter clears, and `claude-headless` is the only
adapter that clears it today. Every other adapter neither advertises the
capability nor runs a turn; the two answers come from one registry, so they
cannot disagree. The VS Code extension never advertises `conversation`, and
leaves both turn command types unacknowledged if one reaches it.

Every daemon on a machine shares the machine's agent, so each receives every
turn. A daemon claims a turn only when the turn's target workspace is one the
agent covers and has the slug of the workspace the daemon declares (the
team's `default` workspace when it declares none). It leaves every other turn
alone, unacknowledged.

## The turn command

Turns arrive on the daemon's own agent command stream,
`GET /v1/agents/{agentId}/commands`, as the `conversation_turn` command type.
The daemon handles it before it relays anything to the extension, and never
relays it. The payload carries ids and bounds, never message text:

| Field                                                            | Use                                                       |
| ---------------------------------------------------------------- | --------------------------------------------------------- |
| `conversation_id`, `turn_id`                                     | The turn                                                  |
| `target_workspace_id`                                            | Which daemon serves it (above)                            |
| `poster_account_id`, `poster_github_login`, `poster_is_operator` | The poster; kept for the tool boundary                    |
| `readable_repos`, `visibility`                                   | What the poster may read; kept for the conversation tools |
| `deadline_at`                                                    | The turn's timeout                                        |
| `token_ceiling`                                                  | The turn's token ceiling (default 200,000)                |
| `limits.max_delta_bytes`, `max_deltas`, `max_reply_bytes`        | Reply bounds                                              |
| `limits.max_frames_per_request`, `limits.cancel_confirm_seconds` | Batch size and cancel window                              |

A payload that names a model, an adapter, a performance mode or an effort has
those fields ignored. The client selects none of them.

## Claiming a turn

The daemon acknowledges a turn on `POST /v1/agents/{agentId}/commands/{commandId}/ack`
before it spawns anything, and runs it only when its own acknowledgement
returned `200` with an execution lease for that turn.

1. **Refuse what it cannot serve.** Before claiming, the daemon resolves the
   profile and builds the argv. When it cannot, it acknowledges the command
   `rejected` with a detail, and the turn ends `failed` with `refused`. The
   detail's category is one of `profile-unresolved`,
   `adapter-not-conversational`, `effort-unsupported`, `adapter-unavailable`
   (the adapter's binary is not installed) or `host-busy` (four turns are
   already running). A command whose payload cannot be read, or whose ids are
   not 1 to 100 of `[A-Za-z0-9_-]`, is left unacknowledged: without a target
   the daemon cannot tell the turn is its own.
2. **Claim.** The claim is an acknowledgement with an empty body. A failed
   request runs nothing, and a redelivery of the command may claim it later.
   `409 ALREADY_ACKED` (another daemon, or this one, already claimed it),
   `409 TURN_ENDED` or `404` runs nothing, ever.
3. **At most once.** The daemon remembers every command it claimed or
   refused, so a redelivered copy is never acknowledged twice.

The lease is returned once. The daemon presents it in `X-Nightgauge-Turn-Lease`
on the turn's own routes only, bound to the agent that claimed it and the
turn it names, and never logs it.

## Running a turn

1. **Context.** `GET /v1/agents/{agentId}/turns/{turnId}/context` is the only
   source of the turn's prior context. The daemon renders it into the turn's
   stdin message: the earlier messages, oldest first, as a JSON array of
   `{role, text}` (with `redacted` for a redacted message and `turn_state`
   for one from a turn that failed or was cancelled), then the new message.
   The context goes into the user message, never into a system prompt.
2. **Profile.** The adapter, performance mode and effort are the ones the
   workspace advertises (`executionprofile.Resolve`), resolved when the turn
   starts. The model is `ui.core.default_model` (or `sonnet`) clamped into the
   mode's envelope and the workspace's `model_routing.max_model`. A model with
   no effort axis runs with no effort flag and reports none applied; a rung
   the model does not declare is refused before the claim.
3. **Process.** Each turn is a new process group, started in a fresh
   temporary directory that is removed when the turn ends. Forge tokens and
   every `NIGHTGAUGE_*` variable are withheld from its environment. For
   `claude-headless` the argv is:

   ```bash
   claude -p --no-session-persistence --output-format stream-json --verbose \
     --include-partial-messages --restricted --permission-mode dontAsk \
     --tools "" --strict-mcp-config --model <model> [--effort <rung>]
   ```

   Session persistence is off, so no turn text is written to disk and no
   session handle is ever named. `--restricted` refuses
   `--permission-mode bypassPermissions` and `--dangerously-skip-permissions`
   at startup, and no configuration, auto-accept settings included, can put a
   bypass flag on a conversation argv. Until the tool boundary lands, a turn
   has no tools.

4. **Streaming.** The reply is read with the pipeline's own NDJSON parser and
   token accumulator. Text deltas are redacted, coalesced and posted every
   250 ms as `turn.delta` frames of at most `max_delta_bytes`; past half the
   turn's delta budget the posts slow so the budget lasts until the deadline.

## Frames

The daemon writes three frame types to
`POST /v1/agents/{agentId}/turns/{turnId}/frames`, in order, at most
`max_frames_per_request` per request, and no others. Actor, turn and host
identifiers are never the daemon's to set.

| Frame            | Carries                                                                                                    |
| ---------------- | ---------------------------------------------------------------------------------------------------------- |
| `turn.delta`     | `seq` (from 1, the idempotency key; a retried post repeats it) and redacted `text`                         |
| `turn.reference` | A card, issue or run a read tool returned (the conversation tools add these)                               |
| `turn.end`       | `state`, `failure_reason`, `execution_profile` (adapter, mode, effort applied), `usage`, `retention_class` |

`usage.input_tokens` counts every prompt token the model processed (fresh,
cache-written and cache-read); `usage.output_tokens` its output.
`retention_class` is the turn's retention class (below).

When an ingestion answers that the turn ended (`409 TURN_ENDED`, `422
SECRET_DETECTED`, `403`, `404`, or a reply whose turn state is terminal), the
daemon stops the turn's process and sends nothing more.

## Retention class

Every terminal report carries the turn's retention class (#1893). A `durable`
turn stays in every later turn's context. An `ephemeral` turn is an answer
read from live state that changed nothing: the hosted service's context read
keeps only a window of the most recent ones. That window is a setting of the
conversation, held and applied by the hosted service. The daemon renders
exactly the messages the read returns and applies no window of its own, so a
turn's context is the same whichever workspace answers it. The class governs
context only: an ephemeral turn is still stored, its reply streams to the
poster in full, and surfaces show it marked with its class.

The daemon classifies a turn when it ends, from its terminal state and the
tool calls its stream recorded, never from the model's reply. The rules are a
table in `internal/conversation/retention.go`, held equal to this one by a
test. The first rule that matches decides, and the daemon logs each turn's
class with the id of the rule that gave it.

| Rule           | Terminal state        | Tool calls   | Class       | Why                                                                                    |
| -------------- | --------------------- | ------------ | ----------- | -------------------------------------------------------------------------------------- |
| `unfinished`   | `failed`, `cancelled` | any          | `durable`   | The request was not completed, and later turns must see that it was left unfinished    |
| `no-live-read` | `completed`           | none         | `durable`   | Nothing shows the answer came from live state, so it may hold a decision or commitment |
| `live-read`    | `completed`           | at least one | `ephemeral` | The answer was read from live state                                                    |
| `no-match`     | any other             | any          | `durable`   | No rule matched                                                                        |

**The mutation floor** runs after the rules and overrides them. A turn that
called any mutating tool is `durable` (rule `mutation-floor`), whatever the
rules or any other classifier answered, and every answer other than
`ephemeral` is `durable`. The floor is code that runs after classification,
not an instruction to a model, so no model output can drop a state change
from later turns' context. The tool boundary (#1570) says which tools mutate;
a tool the daemon cannot place counts as mutating. A call counts once the
stream records it, whether or not it then succeeded, and a subagent's calls
count as the turn's own. Until the tool boundary lands a turn has no tools,
so every turn is `durable`, by `no-live-read` or `unfinished`.

The hosted service applies the floor again from its own records: a turn it
recorded an act or a dispatch for is durable whatever the daemon reported,
and a turn that ended with no class from the daemon (`agent_lost`,
`cancel_unconfirmed`) is durable. A teammate may pin an ephemeral turn
durable, or demote a durable one that changed nothing; demoting a turn that
mutated is refused with a typed error. The hosted service records each change
in its audit log with the acting account.

## States and reasons

| Terminal report           | When                                                                                                                                   |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `completed`               | The adapter exited cleanly with a successful result                                                                                    |
| `failed`, `timeout`       | The daemon's timeout fired: five seconds before `deadline_at`                                                                          |
| `failed`, `token_ceiling` | The turn's tokens, as above, passed `token_ceiling`                                                                                    |
| `failed`, `refused`       | The turn was acknowledged `rejected`: the daemon declined it before the claim and sends no terminal report; the hosted service ends it |
| `failed`, `host_error`    | Once claimed, the context read failed, the adapter did not start or failed, its stream broke, or the daemon shut down (#2449)          |
| `cancelled`               | A teammate cancelled the turn and its process group was verified dead                                                                  |

Every other reason (`chunk_limit`, `secret_detected`, `agent_lost`,
`target_unavailable`, `poster_unauthorized`, `cancel_unconfirmed`) is the
hosted service's to set.

## Cancellation

A running turn is stopped by a `conversation_turn_cancel` command, never the
run verb `cancel`. Its payload names `conversation_id`, `turn_id`,
`turn_command_id` and `reason`. The daemon acts only on a cancel for a turn it
claimed under that command; any other is left alone.

1. The daemon sends `SIGTERM` to the turn's process group, then `SIGKILL` two
   seconds later, and polls `kill(-pgid, 0)` until it fails.
2. For a teammate's cancel (`reason: cancelled`), once the group is verified
   dead it sends `turn.end` with `cancelled`, the profile and the tokens used,
   then acknowledges the cancel `applied`.
3. For a stop the hosted service already made terminal (`timeout`,
   `chunk_limit`, `secret_detected`, `agent_lost`, `token_ceiling`), it
   acknowledges the cancel `applied` once the group is dead, and sends no
   report.
4. When the group cannot be verified dead within `cancel_confirm_seconds`
   (less a margin for the acknowledgement), the daemon acknowledges the
   cancel `rejected` with `cancel-unconfirmed` and reports nothing, so the
   turn ends `failed` with `cancel_unconfirmed`. It never reports a turn
   cancelled while its process may still run.

## Redaction

Before any frame leaves the machine, the daemon replaces every
credential-shaped string with `[REDACTED]`: GitHub tokens, Nightgauge license
keys, Anthropic, OpenAI, xAI and Google API keys, AWS access key ids, Slack,
Stripe and npm tokens, signed JWTs, and whole PEM private-key blocks
(`credshape.EgressPatterns`). The hosted service checks each frame joined to
the reply before it, so a credential split across two CLI deltas must not
leave in parts either: the redacting stream releases text only up to a point
no credential can straddle, holding the trailing run of token characters, a
line that has begun a PEM header, and everything from a private-key header
to its footer.

- A run of token characters is never released in part. One that grows past
  8 KiB with no delimiter is replaced by one `[REDACTED]` and the rest of it
  is dropped, so padding cannot push a credential across a frame boundary.
- Invisible characters (zero-width spaces and joiners, bidirectional
  controls, the soft hyphen, variation selectors) are removed from ASCII text
  before matching, so none can break a credential's shape while it still
  displays whole. One inside an emoji sequence is kept.

A frame that still matches a credential shape is refused by the hosted
service, and the turn ends `secret_detected`.
