# Conversation turns: the daemon's side of the contract

The Go daemon (`nightgauge serve`) answers the conversation turns the hosted
service addresses to its platform agent. It runs each turn on the workspace's
own execution adapter, performance mode and effort, streams the reply back,
and reports how the turn ended (#1569). This document states the daemon's
side of the conversation contract (ADR-017); the hosted service's OpenAPI
document is the authority on the wire. It covers the turn host (#1569), the
tool and permission boundary (#1570), retention classification (#1893),
the pipeline status, running-jobs and Needs Attention tools (#1587), the
VS Code extension's chat panel (#1571), and dispatching a run from a turn
(#1894). A later issue adds the other acts a turn may take on runs (#1895).

Code: `internal/conversation` (the turn host), `internal/conversation/tools`
(the tool boundary), `internal/platform/conversation_turn.go` and
`internal/platform/conversation_attention.go` (the wire),
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
   cancelled), then the runs the conversation dispatched, then the new
   message (see [Dispatching a run](#dispatching-a-run)). The context goes into the user message,
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

A conversation reads broadly, mutates narrowly, and never escalates (#1570,
#1587). Its tools are defined for conversations, not inherited from a
pipeline stage, and the set is closed: `internal/conversation/tools` lists it,
and a test pins it to exactly this list, so a change that adds a tool,
removes one, or makes one mutating, fails CI.

| Tool                    | Mutates | What it does                                                                              |
| ----------------------- | ------- | ----------------------------------------------------------------------------------------- |
| `read_file`             | no      | Reads a text file git tracks in a readable repository                                     |
| `list_directory`        | no      | Lists what git tracks in a directory of a readable repository                             |
| `search_files`          | no      | Searches the tracked text files of the readable repositories for an RE2 pattern           |
| `get_issue`             | no      | Reads an issue of a repository the poster may read                                        |
| `search_issues`         | no      | Searches a readable repository's open issues; qualifiers are removed                      |
| `get_pull_request`      | no      | Reads a pull request of a repository the poster may read                                  |
| `pipeline_status`       | no      | Reports the runs in flight, paused and recently finished, from this machine's state       |
| `list_running_jobs`     | no      | Lists the runs executing now, with the evidence that each is live                         |
| `list_attention_cards`  | no      | Lists the Needs Attention cards of the turn's workspace, from the hosted service          |
| `create_issue`          | yes     | Files an issue the poster asked for in their own message                                  |
| `act_on_attention_card` | yes     | Resolves, comments on, acknowledges or mutes a card the poster asked about, as the poster |
| `dispatch_run`          | yes     | Starts a run on an issue the operator asked for; proposes a teammate's request to them    |

No tool writes the working tree, creates a branch, commits, pushes or opens a
pull request, and none pauses, approves or cancels a run: those stay on the
existing command bus. `dispatch_run` starts a run only through the hosted
service, to run in its own context (see
[Dispatching a run](#dispatching-a-run)). When a conversation concludes that code should
change, its output is an issue, not an edit. `tools.IsMutating` reports a
tool's class, and treats a name it does not know as mutating.

**Mutating tools are gated by role, not by creator.** Who started the
conversation gives nobody anything: every mutation acts for the account that
posted the turn, under that account's role on the team and its own GitHub
permission, and only for that account's own request in that turn. A `viewer`
cannot post a turn, and an act the poster's role does not permit is refused
by the hosted service and reported in the transcript.

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

- The file tools serve only what every participant could read. The reply
  goes to the whole conversation and the turn command does not name its
  participants, so, as with the run tools, every turn, the host operator's
  own included, is served only the files git tracks (the files its index
  lists) in the checkout of a repository in the turn command's
  `readable_repos`, judged by the innermost checkout that holds the path. A
  tracked file the checkout's own ignore rules exclude is not served either:
  someone force-added it, and under `.nightgauge/`, whose rules ignore
  everything but the team's committed files, it is runtime state. An
  untracked or ignored file, such as a local `.env` or credential,
  `.nightgauge/` runtime state or a scratch file, is never read, named in a
  listing or matched by a search, and neither is a file in an unlisted
  repository's checkout (a nested clone's included), in no checkout, or in a
  checkout git cannot list. A directory on the way to a readable checkout
  lists only the entries that lead there. A tracked file is read as the
  working tree holds it.
- The tracked files of every readable checkout under the root are listed
  once per turn, before the first path is judged, with `git ls-files`. No
  path is ever passed to git, so a name is matched exactly, never as an
  option, a glob or pathspec magic, and git never runs in a repository the
  turn may not read.
- Paths are relative to the workspace root. A path is judged by its name
  before anything on disk is consulted: an absolute path or a `..` that
  leaves the root (`path_outside_workspace`), a secret-bearing name
  (`path_secret`), and a path that is not a served file of a readable
  repository. Then its symlinks are resolved, and the target must stay
  inside the root, name no secret, and be a served file of a readable
  repository. The file opened is that exact resolved path, one component at
  a time with `O_NOFOLLOW` from a descriptor on the root, so a symlink
  swapped in after the check fails the open instead of leading somewhere
  unchecked. A FIFO never blocks a read.
- A refusal never says what is on the machine. A missing path, an
  untracked or ignored one, a path in an unlisted repository or in none, and
  a symlink whose target is not served all get the same `path_not_found`
  refusal: a reason of their own would show that a file exists, or where an
  unlisted repository's clone is. No refusal names a local path.
- Secret-bearing paths are refused anywhere in the root, in every component,
  tracked or not (`path_secret`). Names are compared as a case-insensitive
  filesystem may compare them: Unicode case folding (so `ſ` is `s` and the
  Kelvin sign is `k`), invisible format characters ignored, and trailing
  dots and spaces trimmed. The list: `.env*`, `*.pem`, `*.key`, `*.p12`,
  `*.pfx`, `*.jks`, `*.keystore`, anything inside `.git`, `.netrc`, `.npmrc`,
  `.pypirc`, `.git-credentials` and SSH private keys (`id_rsa` and the like).
  A listing omits them, and a search skips them.
- A tracked `.nightgauge/config*.yaml` (or the legacy `config.json`) is read
  with every credential field's value (tokens, private keys and their paths,
  license and API keys, secrets, passwords) replaced by `[REDACTED]`. A
  config file that does not parse is not shown at all.
- The forge tools read only the repositories in the turn command's
  `readable_repos`, whoever posted the turn.
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

**Filing an issue.** For `create_issue`, every check
runs before the forge is reached:

1. **The poster's own request.** It runs only when the poster's message in
   this turn asks for the issue, or confirms one the reply just before
   proposed in answer to the poster's own previous turn (a reply to a
   teammate's turn proposes nothing to anyone else, #1587). The message the gate reads is the turn's own message as the
   hosted service's record attributes it: when the record does not name the
   turn's poster as its author, or the turn holds more than one message,
   nothing can be filed. The model must quote the poster's words
   (`request_quote`), but the quote only locates them; the decision is made
   on the message. Either a request that names an issue ("file an issue for
   ...", "create a ticket") lies inside the quoted words, on word
   boundaries, with no negation in its sentence ("do not", "no need to") and
   no later sentence taking it back ("No, wait."); or, only when the reply
   just before this turn proposed an issue, a request that names none ("file
   it") or a message that is wholly a confirmation ("yes", "go ahead", "yes,
   file it"). A courtesy alone ("thanks", "please") is not a confirmation.
   The gate is also held to the first version's: it never accepts a quote
   and message that version refused, and a test checks that over a corpus.
   Words from an earlier message, a file or an issue body are never in the
   poster's message, so they never satisfy it, whatever they say. The check
   understands English and refuses what it does not recognize, so a missed
   request costs the poster a confirmation, never an unrequested write.
   Refused: `not_requested_by_poster`.
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
server and appends one line to the reply for each call of a mutating tool,
from the server's own record: `[nightgauge] create_issue: filed owner/repo#N ...`,
`[nightgauge] create_issue refused (<code>): ...`, or `failed (forge_error)`
when the forge request itself failed. A refusal is in the
transcript whatever the model said about it.

**Pipeline status and running jobs.** `pipeline_status` and
`list_running_jobs` answer from this machine's own state and nothing else:
the runtime snapshots and the execution-history index each checkout of the
workspace keeps (`internal/state`). They reach no forge, no hosted service
and no model provider, need no inference key, and write nothing. A run is
`running` only when the snapshot scan has evidence that it executes now (its
stage process, its orchestrator's sidecar, or a snapshot written within the
liveness window), and the result names that evidence; otherwise it is
`paused`, `abandoned`, `stalled` or its finished outcome. The reply goes to
every participant of the conversation, and the turn command does not name
them, so the daemon treats every conversation as shared: every turn, the
host operator's own included, sees only the runs of the repositories the
turn command lists as readable, including when one checkout's state holds
another repository's run, and never a private run. A private run is its
creator's alone, and every private run on a machine is its operator's; the
operator still sees theirs in their own tools, never in a conversation. A
live issue the state names without a snapshot (an orchestrator's sidecar
that has not written one yet) has no repository or visibility to judge, and
is not shown. Nothing the tools return names a local path or a process id:
an unreadable state file is counted, not described. Titles are returned as
untrusted data.

**Needs Attention cards.** A turn reaches the cards only through the hosted
service's turn-scoped routes, on the turn's execution lease:
`GET /v1/agents/{agentId}/turns/{turnId}/attention` to list and
`POST /v1/agents/{agentId}/turns/{turnId}/attention/{requestId}` to act. It
never calls the plain attention routes, which would record the host's
operator as the actor, and never touches the local attention store. The
hosted service lists only the cards of the turn's target workspace that the
poster and the host's operator may both see, and takes the actor from the
turn's posting account: the request has no actor field, and nothing the
daemon sends can name one. It allows an act only when both the poster and
the operator hold the permission, and only on a card in the turn's target
workspace. Card text is returned as untrusted data.

Every act of `act_on_attention_card` reaches the hosted service by one path: the MCP `tools/call`, the
tool's dispatch, and one authorization (`authorizeCardAct`) at the only
place the daemon sends an act. There is no fallback, retry or local-store
path. The arguments are parsed once, strictly: an unknown key, a key given
twice or in another case, and trailing data are refused (for every
conversation tool), and the values the checks read are the values sent.
Who acts is never an argument: it is the turn's poster, and the hosted
service records that account and checks the roles on the lease. The checks,
in order:

1. **The arguments' shape.** A card id is one path segment of letters,
   digits, `_` and `-`; the verb is `resolve`, `comment`, `acknowledge` or
   `mute`, with exactly its own arguments: `option_id` (and optional
   `steer_text`) for `resolve`, `body` for `comment`, nothing for the others.
   Text is at most 2,000 characters, with no control or invisible format
   character. Refused: `invalid_arguments`.
2. **A posting account** to act for (`poster_unknown`).
3. **The poster's own request, for this act on this card.** As for an
   issue, the poster's message in this turn must ask for an act of that kind
   ("approve", "comment on", "acknowledge", "mute") inside the words the
   model quotes (`request_quote`), with no negation and no condition in its
   sentence and nothing after it taking it back. A condition ("if the build
   passes", "once CI is green", "after the deploy") makes the act wait, so it
   refuses rather than acts now; one inside the poster's own steer or
   comment text later in the sentence does not count. A retraction counts
   wherever it opens a later clause, after at most an interjection ("Approve
   dr_1 with wait. Hmm, no."), and a phrase that only retracts ("scratch
   that", "never mind") counts anywhere after the request. The request is an
   imperative in the verb's base form, opening its clause after at most a
   courtesy ("please", "OK"): a report or description ("Alice muted dr_1",
   "Bob is approving it"), another person's act ("Alice will approve"), a
   question ("should I approve dr_1?") and someone else's words ("the card
   says: approve dr_1", a quotation, or a speaker's label such as "Bob:
   approve dr_1") ask for nothing. The request binds the words right after
   it: the first words other than fillers ("the", "card", "with", "option")
   must be the card's exact id and, for `resolve`, the option's exact id,
   with nothing else between them, another card's id included: "approve
   dr_... with wait". Case and the whole id count: `DR_1`, `dr_12` and
   `dr_1-b` do not name `dr_1`. A clause, and each comma-separated part of
   one, binds only its own words, so in "approve dr_1 with wait and mute
   dr_2" or "approve dr_1 with wait, leave dr_2 alone" nothing is lent from
   one card to another. A bare list entry joined to a request by commas
   takes the request's verb and its own ids: "mute dr_1, dr_2" mutes both,
   and "approve dr_1 with wait, dr_2 with abort" allows exactly those two
   acts, never `dr_1` with `abort`. An entry that is not bare ("leave dr_2
   alone", "ignore dr_2", "keep dr_2") ends the list, and is never acted
   on. A card's own text, such as an option's label, never names anything,
   and a function word ("with", "it") is never taken as an option. Steer
   and comment text must occur in the poster's message exactly as it is
   written to the card. Otherwise the whole message must be a confirmation
   ("yes", "go ahead") of the completed reply just before this turn, which
   must answer the poster's own previous turn, so one teammate cannot have
   the model propose an act that another's unrelated "yes" makes; and that
   reply must offer exactly one act, this one, plainly, named the same way,
   with any text in its own words. Every request word in the reply is an
   act, and so is every list entry after one, so "I will approve dr_1 with
   wait, acknowledge dr_1" offers two. The offer is a question that offers
   ("Shall I approve card dr_1 with option wait?", "Do you want me to ...?")
   or a statement of intent ("I will ...", "I can ..."), with no hedge
   ("I would ...?", "Not sure."), condition (other than "if you confirm"),
   negation or retraction. A reply that only mentions a card, offers two
   acts or offers one tentatively confirms nothing. So a model that follows
   a card's text, or prefers another card or option, cannot act under the
   poster's words on anything the poster did not name. The gate is the
   conjunction of this decision and a plain floor (a quote in the message;
   the exact card and option ids, as written, and a request word in the
   message, or in the proposal it wholly confirms). A test pins the floor
   with a case only it refuses (an id with an invisible character inside
   it), and a property test over 6,000 generated cases holds the two to
   each other. Refused: `not_requested_by_poster`.
4. **Bounds.** Each act on a card at most once a turn, whatever became of
   it, so a retry after a timeout cannot repeat one that landed
   (`duplicate_act`); at most five acts a turn (`limit_reached`).

The list keeps only cards of the workspace the hosted service names for
the turn, and withholds any other.

A refusal by the hosted service comes back with its own code, lower-cased:
`act_not_permitted` (the poster's or the operator's role does not permit
it, a `viewer`'s for one), `card_outside_target`, `attention_refused` (the
card is already resolved, or the option is not its own), `turn_ended`. The
receipt names what was done and the actor the hosted service recorded:
`[nightgauge] act_on_attention_card: resolve card dr_... with option wait:
resolved at the request of account ...; the hosted service recorded actor
...`. A turn that calls `act_on_attention_card`, even one that is refused, is
`durable` (below); a turn that only read is `ephemeral`.

## Dispatching a run

A turn that starts work dispatches a pipeline run and keeps only its handle
(#1894). The run executes in its own context, on this host's executor; its
transcript, stage output and diffs never enter the conversation, its stored
record or a later turn's input. "What happened to what you started?" is
answered from the handle.

**Who may start one: the host's operator only (phase 1).** This is the
decision recorded for #1894 under the conversation contract (ADR-017,
private), made in place of an App-only credential mode for a whole run. Such
a mode would change the credential chain every pipeline stage uses, so it is
not built now.

- A turn the turn command marks as posted by the host's operator
  (`poster_is_operator`) dispatches the run directly. The run uses the
  operator's ordinary credential chain, unchanged.
- A teammate's turn never dispatches. Its request becomes a Needs Attention
  card on the host, `Start a run on owner/repo#N?`, naming the poster and
  nothing they wrote. Its options are `start`, which queues the issue on the
  operator's machine through the trusted `queue.add` verb, and `dismiss`.
  It dismisses itself on expiry, and one card stays open per issue. The run
  starts only when the operator resolves the card on their own machine
  (VS Code or the CLI). A resolution the hosted service relays, from the
  dashboard or a conversation, never starts it, because the relay cannot
  tell the operator from a teammate who may act on cards: the daemon refuses
  it and the card stays open. Dismissing the card is allowed from anywhere.
- Three layers enforce this: the tool, the hosted service (which refuses a
  teammate's dispatch with `OPERATOR_REQUIRED` and creates no run) and the
  extension, which acknowledges `rejected` (`operator-required:`) any trigger
  that names a conversation turn or a poster without `posterIsOperator`
  exactly `true`.

**The checks, in order, before anything is sent:**

1. **The arguments.** `owner`, `repo`, `issue_number` and `request_quote`,
   parsed strictly as every conversation tool's are (`invalid_arguments`).
2. **A posting account** (`poster_unknown`), a repository the poster may read
   (`repo_not_readable`) and one this workspace has a checkout of
   (`repo_not_in_workspace`).
3. **The poster's own request**, for this issue, in this turn's message. A
   start word (`start`, `run`, `dispatch`, `kick off`, `launch`, `build`,
   `implement`, `work on`, `pick up`, `begin`) opens its clause after at most
   a courtesy. Its sentence carries no negation, condition or question, nothing
   later takes it back, and the words are not someone else's ("Bob: start
   api#12"). The first words after the start word, fillers aside, must name the
   issue exactly: `owner/repo#N`, `repo#N`, `issue N`, or `#N` only when the
   poster may read exactly one repository. `#120` and `#12-b` do not name
   `#12`. Otherwise the whole message is a confirmation of the reply just
   before, which answered the poster's own previous turn and offered exactly
   this run, plainly ("Shall I start a run on api#12?"). Text in an earlier
   message, an issue, a card or any tool result never satisfies it.
   Refused: `not_requested_by_poster`.
4. **Bounds.** Each issue at most once a turn (`duplicate_act`), and at most
   three runs or proposals a turn (`limit_reached`), each counted before the
   request so a retry after a timeout cannot repeat one that landed.
5. **For the operator, an attached executor.** The daemon hands a dispatched
   run's trigger to the extension on its stdio transport, which runs the
   workspace's pipelines. The extension says it does with the
   `agent.attachExecutor` IPC method each time it connects, and a socket
   client cannot. With none attached the dispatch is refused with
   `no_executor` and nothing reaches the hosted service.

Then the daemon calls `POST /v1/agents/{agentId}/turns/{turnId}/runs`
(`agents.dispatchTurnRun`) on the turn's lease, with exactly `{owner, repo,
issue_number}`. The hosted service checks the thread's run bound, the
workspace throttle and the poster's GitHub write permission, records the
poster as the run's requester, and addresses a trigger to this same host,
which relays it to the extension as any trigger. The reply's handle is kept
only when it names the run asked for. A refusal comes back with the hosted
service's code, lower-cased (`operator_required`, `thread_run_limit`,
`workspace_throttled`, `github_permission_denied`, ...), and says that no run
was started. A failure the hosted service did not answer says that a run may
exist. The receipt is `[nightgauge] dispatch_run: dispatched owner/repo#N as
run <id> ...`, or `... proposed owner/repo#N to the host's operator as Needs
Attention card <id> ...; no run was started`. A turn that dispatched or
proposed a run is `durable`.

**What a later turn sees.** The context read carries the thread's runs,
newest first: each run's handle, the last status the hosted service relayed
as `run.status` (null until one is) and, for a failed run, the path of its
evidence. The daemon renders them into the turn's input as a JSON array of
`{run, run_id, status, evidence}` between the earlier messages and the new
one. Each field is held to its shape (a repository and issue, a run id, a
lower-case status word, `/v1/pipeline-runs/<id>`), so no text reaches the
model through them; at most 50 are carried. Nothing of a run is read from
this machine's state for a turn's input.

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
from later turns' context. The tool boundary (#1570) says which tools mutate
(`tools.IsMutating`); a tool it does not know counts as mutating. The calls
classified are the tool server's own record of every call it served, which
no model output can change, joined with every call the stream shows that the
server has no record of, matched by name, so a mutating call either saw
forces the floor. A call counts once it is made, whether or not it then
succeeded (a refused `create_issue` keeps the turn durable), and a
subagent's calls count as the turn's own.

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

## The extension's chat panel

**Nightgauge: Open Conversation** opens the VS Code extension's chat panel
(#1571). It is a client of the hosted conversation, like the dashboard and the
app, not a second local chat: a conversation started in the editor is the one
the others resume. Code: `packages/nightgauge/src/services/conversation`
(the client, the stream and the transcript) and
`packages/nightgauge/src/views/conversation` (the panel and its page).

- **Which threads.** The panel lists one team's conversations, never filtered
  by creator or by workspace: the team that owns the window's workspace, else
  the account's default team. It names the team it lists, and an account on
  two teams can switch. Any developer, admin or owner of the team can open a
  thread someone else started and post to it; a viewer can read but not post.
- **The record.** The panel reads the thread (`GET /v1/conversations/{id}`)
  and follows its stream (`GET /v1/conversations/{id}/stream`). Messages keep
  the service's ids and are ordered by its positions; a frame the panel
  already applied is a no-op, and a `stream.reset` refetches the thread. A
  human message shows the teammate who posted it. A 404, or a redaction,
  removes the text from the panel.
- **Streaming.** A reply renders chunk by chunk as `turn.delta` frames
  arrive, with the turn's state (queued, sent to the host, answering) and a
  cancel that reaches the running turn
  (`POST .../turns/{turnId}/cancel`). Once a turn is terminal no later chunk
  is appended. The panel never runs a model or spawns a process; the
  stage-scoped interactive mode is not its transport, and message text only
  ever travels as the `content` field of a JSON body.
- **The next turn's profile.** The panel shows the provider, mode and effort
  the hosted picker (`GET /v1/workspaces?conversation_id=`) reports for the
  host that would answer you, which may be another teammate's machine, and
  never this machine's own resolvers. It re-reads the picker every 15
  seconds, so an adapter or mode change on the host shows within one
  heartbeat. The provider is the adapter's display name and the mode is the
  performance mode: `frontier` is a mode and never appears as a provider. A
  mode or effort the panel does not recognize is shown as received.
- **The target.** The composer sends a target workspace only when you pick
  one. Otherwise the turn goes where the service routes it (the thread's
  previous target) and the panel shows that target. When an untargeted post
  is refused as ambiguous, or because the previous target can no longer
  answer, the panel offers the workspaces the refusal lists.
- **Failures.** Workspace offline, not included in your plan, allowance used
  up, a failed turn and a disconnected stream each render as their own
  state, as do a role that cannot post, rate limiting, a full transcript and
  a full turn queue.
- **Needs Attention cards.** A card a read tool referenced in a turn
  (`turn.reference`) shows in the transcript. A developer, admin or owner can
  act on it, which calls the attention routes as the clicking account and
  creates no turn; a viewer sees it read only. The control is built from the
  reference frame, never from model text.
- **Private.** When the panel creates a conversation it offers a private
  choice, off by default and never carried over, beside what private does
  and does not hide (see
  [TELEMETRY_PRIVACY.md § Private runs](TELEMETRY_PRIVACY.md#private-runs)).
  The create request sends `visibility`. The panel shows a _Private_ badge
  only when the service returns the conversation as private, and says so
  when it does not.
- **The page.** The webview renders every message, reply and name as text,
  never as markup or links, under a content security policy that allows only
  its own script and style and no network access; the extension host makes
  every request and checks every message the page sends against its own
  state.
