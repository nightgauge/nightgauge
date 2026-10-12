# Conversation turns: the daemon's side of the contract

The Go daemon (`nightgauge serve`) answers the conversation turns the hosted
service addresses to its platform agent. It runs each turn on the workspace's
own execution adapter, performance mode and effort, streams the reply back,
and reports how the turn ended (#1569). This document states the daemon's
side of the conversation contract (ADR-017); the hosted service's OpenAPI
document is the authority on the wire. Later issues add their parts: the tool
boundary (#1570), the conversation tools (#1587), retention classification
(#1893), and the acts a turn may take (#1894, #1895).

Code: `internal/conversation` (the turn host), `internal/conversation/tools`
(the tool boundary), `internal/platform/conversation_turn.go` (the wire),
`internal/execution/adapters/conversation.go` (the builders),
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

| Field                                                            | Use                                                                         |
| ---------------------------------------------------------------- | --------------------------------------------------------------------------- |
| `conversation_id`, `turn_id`                                     | The turn                                                                    |
| `target_workspace_id`                                            | Which daemon serves it (above)                                              |
| `poster_account_id`, `poster_github_login`, `poster_is_operator` | The poster: the actor of every mutation, and the credential a write may use |
| `readable_repos`                                                 | The repositories the poster may read; the tools refuse every other          |
| `visibility`                                                     | Kept for the conversation tools                                             |
| `deadline_at`                                                    | The turn's timeout                                                          |
| `token_ceiling`                                                  | The turn's token ceiling (default 200,000)                                  |
| `limits.max_delta_bytes`, `max_deltas`, `max_reply_bytes`        | Reply bounds                                                                |
| `limits.max_frames_per_request`, `limits.cancel_confirm_seconds` | Batch size and cancel window                                                |

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
   `{role, author, text}` (`author` is the posting account, or `assistant`
   for a reply; `by_poster` marks the new message's poster, `redacted` a
   redacted message and `turn_state` one from a turn that failed or was
   cancelled), then the new message. The context goes into the user message,
   never into a system prompt. A context read that attributes the turn's own
   message to an account other than its poster fails the turn.
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
     --tools "" --strict-mcp-config --model <model> [--effort <rung>] \
     --mcp-config <turn dir>/mcp.json \
     --allowedTools mcp__nightgauge__create_issue,...,mcp__nightgauge__search_issues \
     --append-system-prompt <the boundary>
   ```

   Session persistence is off, so no turn text is written to disk and no
   session handle is ever named. `--restricted` refuses
   `--permission-mode bypassPermissions` and `--dangerously-skip-permissions`
   at startup, and no configuration, auto-accept settings included, can put a
   bypass flag on a conversation argv. The turn has no built-in tool; its
   tools are the conversation's own (below).

4. **Streaming.** The reply is read with the pipeline's own NDJSON parser and
   token accumulator. Text deltas are redacted, coalesced and posted every
   250 ms as `turn.delta` frames of at most `max_delta_bytes`; past half the
   turn's delta budget the posts slow so the budget lasts until the deadline.

## Tools and permissions

A conversation reads broadly, mutates narrowly, and never escalates (#1570).
Its tools are defined for conversations, not inherited from a pipeline stage,
and the set is closed: `internal/conversation/tools` lists it, and a test pins
it, so a change that adds a tool, or makes one mutating, fails CI.

| Tool               | Mutates | What it does                                                         |
| ------------------ | ------- | -------------------------------------------------------------------- |
| `read_file`        | no      | Reads a text file under the workspace root                           |
| `list_directory`   | no      | Lists a directory under the workspace root                           |
| `search_files`     | no      | Searches the workspace's text files for an RE2 pattern               |
| `get_issue`        | no      | Reads an issue of a repository the poster may read                   |
| `search_issues`    | no      | Searches a readable repository's open issues; qualifiers are removed |
| `get_pull_request` | no      | Reads a pull request of a repository the poster may read             |
| `create_issue`     | yes     | Files an issue the poster asked for in their own message             |

No tool writes the working tree, creates a branch, commits, pushes or opens a
pull request. When a conversation concludes that code should change, its
output is an issue, not an edit. `tools.IsMutating` reports a tool's class,
and treats a name it does not know as mutating.

**How a turn reaches them.** For each turn the daemon starts a tool server
that speaks the Model Context Protocol over HTTP on a loopback port, and
writes its address and a per-turn bearer token to `mcp.json` in the turn's
own directory, readable by its owner only and removed with the turn. The
server refuses a request without that token, one with an `Origin` header,
and one addressed to another host name. It holds no credential: the forge
calls resolve theirs inside the daemon. Under `--permission-mode dontAsk`,
only the tools `--allowedTools` names run, and every other call is denied
without a prompt.

**Reads.**

- Paths are relative to the workspace root. An absolute path, a `..` that
  leaves the root, and a symlink that resolves outside it are refused
  (`path_outside_workspace`). The path is checked lexically, then again with
  every symlink resolved, and every open goes through an `os.Root` on the
  workspace, which the kernel holds to the root as well.
- Secret-bearing paths are refused anywhere in the root, case-insensitively,
  whether named or reached through a symlink (`path_secret`): `.env*`,
  `*.pem`, `*.key`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, anything inside
  `.git`, `.netrc`, `.npmrc`, `.pypirc`, `.git-credentials` and SSH private
  keys (`id_rsa` and the like). A listing omits them, and a search skips
  them.
- `.nightgauge/config*.yaml` (and the legacy `config.json`) is read with
  every credential field's value (tokens, private keys and their paths,
  license and API keys, secrets, passwords) replaced by `[REDACTED]`. A
  config file that does not parse is not shown at all.
- The forge tools read only the repositories in the turn command's
  `readable_repos`. The file tools do too, judged by the checkout that holds
  the path, except for a turn posted by the host's operator, which reads its
  own checkout without the check.
- Every credential-shaped string in a tool result is redacted before the
  model sees it.

**The injection boundary.** Repository and forge content is text other
people wrote. Every read tool returns it inside a JSON envelope whose
`notice` says it is untrusted data, never instructions; JSON escaping keeps
the content from closing the envelope. The thread is data too: earlier
messages reach the model attributed to their authors, and the system prompt
the daemon appends (`--append-system-prompt`, fixed text, never message text)
states both rules and the tool ceiling. No message text reaches argv, a
shell string or a path the daemon builds.

**Filing an issue.** `create_issue` is the only mutation, and every check
runs before the forge is reached:

1. **The poster's own request.** It runs only when the poster's message in
   this turn asks for the issue, or confirms one proposed earlier. The model
   must quote the poster's words (`request_quote`); the quote must appear in
   the poster's message and either ask for an issue ("file an issue for
   ...", "create a ticket") or open the message by confirming ("yes", "go
   ahead"). Words from an earlier message, a file or an issue body are never
   in the poster's message, so they never satisfy it, whatever they say. The
   check understands English and refuses what it does not recognize, so a
   missed request costs the poster a confirmation, never an unrequested
   write. Refused: `not_requested_by_poster`.
2. **A repository the poster may read**, from `readable_repos`
   (`repo_not_readable`), and a posting account to file for
   (`poster_unknown`): an issue is never filed anonymously.
3. **The credential.** A teammate's issue is written through the workspace's
   GitHub App installation and nothing else
   (`github.NewAppOnlyClient`): no CI environment token, no `GITHUB_TOKEN`,
   no configured token and no `gh` account is tried. Without a usable App it
   is refused with `app_unavailable`. A turn the command marks as posted by
   the host's operator uses the workspace's ordinary credential chain, which
   prefers the App.
4. **Bounds.** At most three issues a turn; a one-line title of at most 256
   characters; credential-shaped strings are redacted from the title and
   body.

The issue goes through the same creation path as the extension's
`issue.create`. GitHub shows the App (or the operator) as its author; the
body ends with a provenance footer naming the poster's GitHub login and
account and an HTML comment the daemon writes and `tools.ParseProvenance`
reads:

```html
<!-- nightgauge:provenance {"origin":"conversation","conversation_id":"...","turn_id":"...","actor_account_id":"...","actor_github_login":"..."} -->
```

A marker the model writes into the body is defused, so the only record is
the daemon's. After the turn's process ends, the daemon closes the tool
server and appends one line to the reply for each `create_issue` call, from
the server's own record: `[nightgauge] create_issue: filed owner/repo#N ...`,
or `[nightgauge] create_issue refused (<code>): ...`. A refusal is in the
transcript whatever the model said about it.

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
