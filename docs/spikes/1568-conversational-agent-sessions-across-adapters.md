# Spike #1568: Conversational agent sessions across execution adapters — multi-turn, streaming and effort mapping

**Issue**: #1568
**Status**: Complete
**Date**: 2026-10-02
**Observed against**: claude 2.1.287, codex-cli 0.154.0, opencode 1.18.32, grok 1.0.25
(installed, not authenticated); gemini and copilot are not installed
**Feeds**: #1569 (the daemon hosts conversations), #1570, #1587, and the
`executionprofile.ConversationViable` gate #1567 put in place

## Spike Contract (Path A)

Path A: the recommendations below materialize as follow-up issues after this spike's PR
merges. The one adopted adapter gets the adapter-level half of its conversational turn,
which #1569 consumes; each deferred adapter gets a re-probe that names what this spike
could not observe. The overall recommendation is recorded with `action: skip` only
because #1569 already is that issue, so the materializer must not file a second one.

**Artifact**: `docs/spikes/1568-conversational-agent-sessions-across-adapters.md`

## Executive Summary

**Verdicts: adopt `claude-headless`; defer `claude-sdk`, `codex`, `opencode`, `grok`,
`gemini`, `gemini-sdk` and `copilot`; skip `ollama` and `lm-studio`, which no longer
exist.** One adapter clears the viability bar (§ 13). `claude-sdk` spawns the same
binary but its own credential was never exercised, so it waits for one probe.

**How a turn gets its context is decided by #1568's acceptance criterion 4 as reconciled
on 2026-10-02 (with ADR-017; not this repository's
`docs/decisions/017-runtime-identity-keying.md`), and #1569's multi-turn criterion says
the same:** a turn's prior context comes only from the hosted service's context read,
because consecutive turns can run on different machines. The preferred mode is
**stateless replay with the adapter's session persistence turned off**; an adapter's own
held session may at most be a cache, and only where it is shown to equal that read.
This record therefore states, per adapter, how to run that way, whether a held session
can be shown to equal the read, and what the adapter keeps on disk (§ 12).

Observed for Claude: with `--no-session-persistence`, a new process given turn 1's
exchange replayed in its stdin message answered turn 2 correctly (§ 3.2), and the
turn's text was in **no file** the CLI wrote; without the flag it was in the session
transcript under `~/.claude/projects/` (§ 12.2). The same replay worked on `opencode`
with a per-turn data root that is deleted after the turn, after which the text was
nowhere on disk. `codex exec --ephemeral` wrote nothing holding the turn's text, while
a persisted run wrote it to a rollout file and to two shared SQLite stores.

A held Claude process measured **218–226 MiB resident per open conversation** on a
developer laptop and answered a second turn about 1.8 s sooner than a process per
turn (§ 12.3). It is not the design: it cannot follow a conversation to another
machine, and its context was observed to diverge from the conversation's log after a
cancel (§ 3.2).

What defers the other adapters is an observation, not a guess:

- **claude-sdk**: no `ANTHROPIC_API_KEY` was set, so its own credential path has no
  transcript, and #1568's Verification accepts no `adopt` without one.
- **codex**: the authenticated account's usage limit refused every turn, so no reply was
  observed. Independently, `codex exec --json` delivers an agent message whole, in one
  `item.completed` event, so it cannot stream within a reply; only the experimental
  `codex app-server` streams deltas.
- **opencode**: replay across two turns works (observed against a local model), but
  `opencode run --format json` delivered a 58-word reply as **one** event, a millisecond
  before the turn ended. It does not stream within a reply.
- **grok** is installed but not authenticated; **gemini** and **copilot** are not
  installed. Each is recorded as unverified, with the candidate invocation its help text
  or vendor documentation implies.

**No adapter needs a bypass flag to converse, but the pipeline builders must not be
reused for a conversation.** The codex builder falls back to
`--dangerously-bypass-approvals-and-sandbox` for an empty tool list, the grok builder
always passes `--always-approve`, the copilot builder `--allow-all-tools`, and the gemini
builders put the prompt on argv; the Claude builders carry no `--restricted`, no
permission mode and no `--tools` list. #1569 needs a conversational builder per adapter, never
`BuildCommand`. The Claude path is safe by construction: `--restricted` refuses
`bypassPermissions` and `--dangerously-skip-permissions` at startup, before any model
call, and `--permission-mode dontAsk` denied an unapproved `Bash` call and reported it.

**Effort cannot be left to the CLIs.** Claude silently ignores `--effort max` on a model
with no effort axis (exit 0, no warning); opencode silently ignores an undeclared
`--variant`; codex does not carry the effort onto a resumed turn. The envelope is
therefore mapped on the Nightgauge side from the registry's `supported_efforts`, passed
on every turn, and governed by one rule (§ 11).

One finding reaches beyond conversation: `codex exec resume` honours
`-c sandbox_mode="<mode>"` and, on codex-cli 0.154.0, takes the sandbox from the resume
invocation rather than the session, so the SDK's opt-in resume path no longer needs the
bypass flag for a scoped stage. That is #2342, fixed alongside this record.

The `conversation` capability stays unadvertised. `conversationViableAdapters` remains
empty: only #1569's change, the one that adds the turn handler, fills it.

## 1. Method

### 1.1 The registry, not the issue's list

The issue names nine adapters. The registry (`internal/execution/adapters/registry.go`,
`NewRegistry`) has eight: `claude-headless`, `claude-sdk`, `codex`, `gemini`,
`gemini-sdk`, `copilot`, `grok` and `opencode`. `ollama` and `lm-studio` were removed by
#2128; naming either is now an error (`internal/config/retired_adapters.go`), and local
models run through `opencode` against any OpenAI-compatible server. `opencode`, which the
issue does not name, is assessed here. All eight report `Agentic() == true`, so no
chat-only adapter remains in the Go registry; `gemini-sdk` is chat-only only in the
TypeScript SDK, while the Go adapter spawns the `gemini` CLI.

### 1.2 What was installed

| Adapter                         | Binary     | Version on this machine        | State                                       |
| ------------------------------- | ---------- | ------------------------------ | ------------------------------------------- |
| `claude-headless`, `claude-sdk` | `claude`   | 2.1.287                        | authenticated; no `ANTHROPIC_API_KEY`       |
| `codex`                         | `codex`    | codex-cli 0.154.0              | authenticated; usage limit reached          |
| `opencode`                      | `opencode` | 1.18.32                        | no stored credentials; local model used     |
| `grok`                          | `grok`     | 1.0.25 (f7e67d6988e2) [stable] | `grok models`: "You are not authenticated." |
| `gemini`, `gemini-sdk`          | `gemini`   | not installed                  | unverified                                  |
| `copilot`                       | `copilot`  | not installed                  | unverified                                  |

The registry's own view of the same machine, with the binary built from `origin/main`:

```text
$ nightgauge adapter list
NAME               DISPLAY            BINARY   STATUS
----               -------            ------   ------
claude-headless    Claude Headless    claude   available
claude-sdk         Claude SDK         claude   available
codex              Codex              codex    available
copilot            GitHub Copilot     copilot  not found
gemini             Gemini Headless    gemini   not found
gemini-sdk         Gemini SDK         gemini   not found
grok               Grok               grok     available
opencode           OpenCode           opencode available
```

No `ANTHROPIC_API_KEY` was set, so `claude-sdk`'s credential was not exercised (§ 3.7).

### 1.3 Probes

- Every probe ran in a scratch directory under `/tmp`, outside any repository, with no
  tools or read-only tools, on the smallest model offered: Claude Haiku 4.5 (one effort
  probe on Sonnet 5.5), `gpt-5.6-luna` at effort `low` for codex, and `qwen3:1.7b` served
  by a local Ollama 0.32.11 for opencode. The opencode config's permission map denied
  `edit`, `bash`, `webfetch` and `external_directory`.
- The two-turn test: turn 1 says "My favourite fruit is the plum. Reply with just the
  word: noted."; turn 2, **in a new process**, asks "Which fruit did I say was my
  favourite? Answer in one word." A reply of "plum" is the evidence that context
  carried. The first round of probes carried it by the CLI's own session; the replay
  round (§ 3.2, § 5.2) carried it only as text in turn 2's stdin message, rendered from
  turn 1's exchange as the context read would return it:

  ```text
  Earlier messages in this conversation, oldest first, as a JSON array:
  [{"role": "user", "text": "My favourite fruit is the plum. Reply with just the word: noted."}, {"role": "assistant", "text": "noted"}]

  New message:
  Which fruit did I say was my favourite? Answer in one word.
  ```

  JSON keeps a message that itself contains a role marker from forging a turn.

- The on-disk probes put a random reference, generated inside the probe and printed
  nowhere, in the turn's message, then searched every file the CLI could have written
  since the turn started (its home directory, `~/Library/Caches`, `~/Library/Logs`,
  `$TMPDIR` and `/tmp`) for it.
- Every child was spawned as a session leader. "Dead" means `kill(-pgid, 0)` failed
  afterwards.
- Resident memory is the summed RSS of the process tree, 3 s after a turn completed.
- The machine: an Apple-silicon developer laptop with 64 GiB. The replay round ran with
  a load average near 100, so its latencies are not comparable with the first round's.

## 2. Where observation contradicts the issue

| The issue assumes                                                                       | Observed at `origin/main`                                                                                                                                                                                                                                                                                                                                                                                                               |
| --------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Nine adapters, including `ollama` and `lm-studio`                                       | Eight. #2128 retired both; `opencode` replaces them and is assessed (§ 1.1)                                                                                                                                                                                                                                                                                                                                                             |
| The extension's interactive path hardcodes `spawn("claude", …)` and ignores the adapter | It follows the adapter: it refuses `opencode`, `gemini`, `grok`, `gemini-sdk` and `copilot` with an error, opens Codex's own interface in a terminal, and spawns `claude` only for the two Claude adapters (`validateAdapterPrerequisites` and `launchCodexInteractiveTerminal` in `packages/nightgauge-vscode/src/utils/skillRunner.ts`). It is still stage-scoped and invisible to the daemon, so #1569's rule not to reuse it stands |
| "The Claude adapter passes `--no-session-persistence`"                                  | `claude-headless` does; `claude-sdk` does not. With the flag, a later `--resume` fails: `No conversation found with session ID: <uuid>`, exit 1 (§ 3.2). Under the reconciled criterion that is the wanted posture: a turn never resumes, and the flag keeps the turn's text off disk (§ 12.2)                                                                                                                                          |
| `Agentic()` exists because some adapters are chat-only                                  | None of the eight Go adapters is chat-only (§ 1.1)                                                                                                                                                                                                                                                                                                                                                                                      |
| Stateless means "each turn replays the transcript"                                      | Claude, codex and opencode can each replay their own stored transcript when resumed by a handle, but that store is local to one machine and was observed to diverge from the conversation after a cancel (§ 3.2). The replay #1569 needs is of the hosted context read, with persistence off, and it was observed to work on Claude and opencode (§ 3.2, § 5.2)                                                                         |

None of these changes the question. Each changes an implementation detail #1569 inherits,
and they are recorded here so the recommendation rests on what is true today.

## 3. `claude-headless` — adopt; `claude-sdk` — defer

### 3.1 The conversational invocation

```bash
# Every turn: a new process, nothing saved, the context read replayed in the message.
claude -p --no-session-persistence --model <model> [--effort <rung>] \
  --output-format stream-json --verbose --include-partial-messages \
  --restricted --permission-mode dontAsk --tools "<allow-list or empty>" \
  --strict-mcp-config [--mcp-config <conversation tools>]   # message on stdin
```

The stdin message is the context read rendered as in § 1.3, followed by the new message.
The replayed context goes in the user message, never in `--system-prompt` or
`--append-system-prompt`, so teammate text carries no more authority than the message
itself. The first round of probes also passed `--setting-sources project`, which
`--restricted` makes redundant: restricted mode ignores user, project and local settings
files.

### 3.2 Multi-turn — observed

**Replay, persistence off** (the invocation above, no tools, one process per turn):

| Turn | Process | Given                                       | Reply   | Input tokens |
| ---- | ------- | ------------------------------------------- | ------- | ------------ |
| 1    | new     | the first message                           | `noted` | 3,606        |
| 2    | new     | turn 1's exchange as JSON, then the message | `plum`  | 3,665        |

Each turn reported its own fresh session id, which nothing reuses, and each turn's
process group was gone when the turn ended. Turn 2's only knowledge of turn 1 was the
replayed text.

**Resumed by handle** (the first round, `--session-id <uuid>` then `--resume <uuid>`,
without `--no-session-persistence`): turn 2 answered `plum` with 3,748 input tokens, the
CLI having loaded the first exchange from its own stored session. Without `--restricted`
(a longer system prompt, above the model's minimum cacheable length) turn 2 read 6,567
prompt tokens from the cache. A held process (`--input-format stream-json`, each message
written to stdin as a JSON `user` line) also answered `plum`. Both keep the context on
one machine, and are measured in § 12 only as the alternatives the criterion rejects.

Four results matter to #1569:

- **`--no-session-persistence` makes resume impossible.** After a first turn with the
  flag, `--resume <uuid>` printed `No conversation found with session ID: <uuid>` and
  exited 1 with `result.subtype: error_during_execution`. A replayed turn never resumes,
  so nothing is lost.
- **A handle cannot be minted twice.** `--session-id` with an existing handle printed
  `Error: Session ID <uuid> is already in use.` and exited 1. Replay never names one.
- **A cancelled resumed turn leaves the CLI's session divergent.** A turn asked to count
  to 400 was ended by `SIGTERM` to its process group after five text deltas: exit 143,
  the group gone. The next `--resume` turn answered `Plum. No.` (the fruit, and "no, the
  count was not completed"). The CLI kept the cancelled prompt but none of the partial
  reply, and wrote a placeholder assistant message ("No response requested.") so the
  transcript still alternates. The CLI's session then no longer equals the
  conversation's log, which holds the partial text the daemon streamed.
- **A cancelled replayed turn leaves nothing behind.** The same cancel under
  `--no-session-persistence`: `SIGTERM` after five non-empty text deltas, exit 143, the
  process group dead 657 ms after the signal. There is no CLI session to diverge.

### 3.3 Streaming — observed

With `--include-partial-messages` the stream carries `stream_event` lines of type
`content_block_delta`. On the replay invocation, asked for five sentences about rivers,
a 118-word reply arrived as **66 non-empty `text_delta` events in one text content
block**, the first 1,631 ms before that block's `content_block_stop` (at +6,279 ms and
+7,910 ms from spawn). The first round counted 58 text deltas for a 106-word reply,
after two thinking deltas and a signature. Without the flag, assistant text arrives
only as whole `assistant` messages. The `system/init` line carries the session id, the
resolved model, the permission mode, the tool list and `per_turn_effort_active`. The
terminal `result` line carries the turn's own `usage`, `total_cost_usd`, `num_turns` and
`permission_denials`.

### 3.4 Effort — observed

`--effort` takes `low`, `medium`, `high`, `xhigh` and `max`.

| Probe                       | Exit | `per_turn_effort_active` | Notice                                                                               |
| --------------------------- | ---- | ------------------------ | ------------------------------------------------------------------------------------ |
| Sonnet 5.5, `--effort low`  | 0    | `true`                   | none                                                                                 |
| Haiku 4.5, `--effort max`   | 0    | `false`                  | **none**: the rung is dropped silently                                               |
| Haiku 4.5, `--effort bogus` | 0    | `false`                  | stderr: `Unknown --effort value 'bogus' — ignoring it and using the default effort.` |

The registry already declares Haiku 4.5 with `supported_efforts: []`. The pipeline's
Claude branch consults it (`modelSupportsEffort`, then `assertEffortSupported`, in
`skillRunner.ts`) and emits no `--effort` for such a model; a conversation follows the
rule in § 11, and `per_turn_effort_active` lets the daemon confirm what the CLI applied.

### 3.5 Usage, cost and ceilings — observed

- `result.usage` is the turn's own usage. On a resumed session `result.total_cost_usd`
  is **cumulative**: turn 2's figure equalled turn 1's plus turn 2's own usage priced at
  list rates, to the sixth decimal. A replayed turn is a fresh session, so its figure
  covers that turn alone (0.003881 and 0.003975 for the two replay turns). Metering
  still uses `usage`, which means the same thing in both modes.
- `--max-budget-usd` is enforced per invocation, after the call that crosses it. A
  budget below the turn's own cost ended the turn with `error_max_budget_usd`, exit 1,
  after the model call had completed, and with no reply. It is a per-turn soft ceiling;
  the token ceiling #1569 requires stays the daemon's to enforce from the stream.
- `--max-turns` is accepted, although `--help` does not list it, and bounds the turn's
  agent loop.

### 3.6 Security scope

| Required on a conversation turn                                                                                                                                                                 | Optional                                                                                                                                                                                                         | Forbidden                                                                                                                                                                                                                                                                                                                             |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--no-session-persistence`; `--restricted`; `--permission-mode dontAsk`; `--tools` with an explicit list (empty for none); `--strict-mcp-config`; the message and the replayed context on stdin | `--allowedTools` rules for the narrow mutations #1570 allows; `--mcp-config` for the conversation tools of #1587; `--add-dir`; `--append-system-prompt`; `--max-budget-usd` and `--max-turns` as per-turn bounds | `--dangerously-skip-permissions`; `--allow-dangerously-skip-permissions`; `--permission-mode bypassPermissions`, `acceptEdits` or `auto`; `--resume`, `--continue` and `--session-id`, which would take context from this machine rather than the read (§ 12.1); the message on argv, where text beginning `--` would parse as a flag |

**Bypass is unreachable by construction.** `claude -p --restricted --permission-mode
bypassPermissions …` printed `Error: bypassPermissions not supported in restricted mode`
and exited 1 before any model call; `--restricted --dangerously-skip-permissions` printed
the same. Under `--permission-mode dontAsk` with `--tools Bash,Read`, a request to run
`echo hi > made-by-bash.txt` was denied twice (`tool_result` with `is_error: true`:
"Permission to use Bash has been denied because Claude Code is running in don't ask
mode"), the file was not created, and both attempts were listed in
`result.permission_denials` with their input. A mutation the turn is not explicitly
allowed is denied without a prompt, and the denial is reportable.

### 3.7 `claude-sdk`

The Go `claude-sdk` adapter spawns the same `claude -p` and differs from
`claude-headless` in two ways: it passes `ANTHROPIC_API_KEY` through, and it does not pass
`--no-session-persistence`. Its conversational invocation would be § 3.1 with that
credential, persistence off as for `claude-headless`. **No turn ran on that credential**:
none was set on this machine, so every transcript in § 3 is the subscription-login path.
The adapters differ only in the credential, so the result is likely to carry over, but
#1568's Verification accepts no `adopt` without a reproducible transcript.

**Verdicts:** `claude-headless` **adopt** — every axis observed on the replay
invocation, a reproducible two-turn transcript with persistence off, a bypass-proof
permission floor, and an effort control whose silent failure mode is detectable.
`claude-sdk` **defer** until the two-turn replay runs once with `ANTHROPIC_API_KEY`.

## 4. `codex` — defer

### 4.1 Candidate invocation (no reply observed)

```bash
# Every turn: a new process, nothing saved, the context read replayed on stdin.
codex --ask-for-approval never exec --ephemeral --sandbox read-only --json \
  --ignore-user-config -m <model> -c model_reasoning_effort=<rung> -   # message on stdin
```

### 4.2 What was observed

- **No reply.** Every turn, first or resumed, ended before the model answered (the
  message is cut after its first sentence, which is the part that is not about the
  account):

  ```text
  $ codex --ask-for-approval never exec --sandbox read-only --skip-git-repo-check \
      --ignore-user-config --ignore-rules --json -m gpt-5.6-luna \
      -c model_reasoning_effort=low "My favourite fruit is the plum. …"
  {"type":"thread.started","thread_id":"<thread-id>"}
  {"type":"turn.started"}
  {"type":"error","message":"You've hit your usage limit. …"}
  {"type":"turn.failed","error":{"message":"You've hit your usage limit. …"}}
  ```

  The replay round's two on-disk probes (§ 12.2) ended the same way, `turn.failed`
  before any agent message. Multi-turn context is therefore unverified.

- **Resume re-attaches, and re-derives the sandbox on every resume** (relevant to the
  pipeline's resume, #2342, not to a replayed turn). `exec resume <thread-id>` emitted
  `thread.started` with the same thread id. Each resumed turn's policy was read back
  from the `turn_context` record Codex writes to the session's rollout file:

  | Session started with        | Resumed with                                         | Resumed turn ran with                              |
  | --------------------------- | ---------------------------------------------------- | -------------------------------------------------- |
  | `--sandbox read-only`       | `exec resume <id> --sandbox read-only`               | refused: `unexpected argument '--sandbox'`, exit 2 |
  | `--sandbox read-only`       | `exec resume <id> -c sandbox_mode="workspace-write"` | `workspace-write`                                  |
  | `--sandbox workspace-write` | `exec resume <id>`, user config ignored              | `read-only`                                        |
  | `--sandbox workspace-write` | `exec resume <id> -c sandbox_mode="read-only"`       | `read-only`                                        |

  Without `--ignore-user-config`, an operator's `config.toml` decides the sandbox
  instead, which is why it is passed on a conversation turn too.

- **Effort is not inherited either.** A first turn with `-c model_reasoning_effort=low`
  recorded `effort: low`; a resume without the option recorded `effort: null`.
- **Stdin must be closed.** With a prompt argument and stdin left open, `codex exec`
  printed `Reading additional input from stdin...` and waited indefinitely. The message
  belongs on stdin, after `-`, as the pipeline adapter already sends it.

### 4.3 Streaming — documented and captured, not observed live

`codex exec --json` emits `thread.started`, `turn.started`, `item.started`,
`item.completed`, `turn.completed` and `turn.failed`, and an agent message arrives whole
in `item.completed` (vendor documentation, § 15). The repository's real capture agrees:
`internal/execution/testdata/codex_stream_real_capture.jsonl` holds two `item.completed`
agent messages and no delta, so every message is one fragment and fails § 13's
condition 4. `turn.completed` carries `usage` (`input_tokens`, `cached_input_tokens`,
`output_tokens`, `reasoning_output_tokens`).

Deltas exist only on `codex app-server`, which the installed CLI labels experimental. Its
protocol schema, generated from the installed binary with
`codex app-server generate-json-schema`, has `thread/start` (whose params include
`ephemeral`), `thread/resume`, `turn/start` (with per-turn `effort`, `sandboxPolicy` and
`approvalPolicy`) and `turn/interrupt`, and notifies `item/agentMessage/delta` and
`thread/tokenUsage/updated`. A per-turn app-server (spawn, start an ephemeral thread,
run one turn on the replayed context, exit) would be a stateless codex turn that
streams. It is the follow-up.

### 4.4 Security scope

| Required on a conversation turn                                                                                                                                                                  | Optional                                                  | Forbidden                                                                                                                                                                                                                                                               |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--ephemeral` (or an `ephemeral` thread); `--sandbox read-only`; `--ask-for-approval never` before `exec`; `--ignore-user-config`, so `config.toml` cannot set the sandbox; the message on stdin | `--skip-git-repo-check` outside a clone; `--ignore-rules` | `--dangerously-bypass-approvals-and-sandbox`; `--sandbox danger-full-access` or `-c sandbox_mode="danger-full-access"`; `--approve-for-me`; `--dangerously-bypass-hook-trust`; `exec resume`, which takes context from this machine's rollout file rather than the read |

The pipeline builder must not be reused: `resolveCodexSandboxMode` returns full access, and
`codexSandboxFlags` the bypass flag, for an empty tool list or any tool implying a shell.

**Verdict: defer.** Two independent reasons: no reply was observed, and the stateless
`exec` path fails the streaming bar. No bypass is needed, so this is not a `skip`.

## 5. `opencode` — defer

### 5.1 The invocation (observed)

```bash
# A fresh data root per turn: XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_CACHE_HOME and
# XDG_STATE_HOME point into it, OPENCODE_CONFIG_CONTENT carries the provider and the
# permission map (edit, bash, webfetch, external_directory: deny). The root is deleted
# when the turn ends.
opencode run --format json --print-logs --log-level ERROR -m <provider/model> \
  --dir <workdir>                                  # replayed context + message on stdin
```

### 5.2 What was observed

- **Multi-turn by replay works.** Turn 1, in a fresh root, answered `noted`; the root
  was deleted; turn 2, in another fresh root, given turn 1's exchange as in § 1.3,
  answered `plum`. Both processes exited 0.
- **Multi-turn by resume works within one root.** In the first round, turn 1 answered
  `noted`, turn 2 (`-s <sessionID>`, message on argv) answered `plum`, and turn 3
  (message on stdin) answered `plum`, all in the session the first turn's events named.
  Those runs shared one private `HOME` and one set of XDG roots that outlived each turn.
  That is not the lifecycle the pipeline gives a root: ADR-022 § 22 deletes the whole
  per-run root, session database included, when the run ends, "which is also why session
  resume (#1643) works only within a run". Resume across turns would need a root kept
  per conversation, which is the host-local state the reconciled criterion rules out.
- **No streaming within a reply.** `--format json` emitted `step_start`, one `text` event,
  then `step_finish`. Asked for five sentences, the model's 58-word reply arrived as a
  single 414-character `text` event, written 37 ms after the part's own timestamps say
  its 814 ms of generation ended, and 1 ms before `step_finish`. A reader sees nothing
  until the reply is complete. Abridged, with times relative to `step_start`:

  ```text
  $ opencode run --format json -m ollama/qwen3:1.7b --dir <workdir> \
      "Write five sentences about rivers. Plain prose, no tools."
  {"type":"step_start",  … +0 ms}
  {"type":"text",        … +2310 ms, "part":{"type":"text","text":"<414 characters>", …}}
  {"type":"step_finish", … +2311 ms, "part":{"reason":"stop","tokens":{…},"cost":0, …}}
  ```

  Each replayed turn also produced exactly one `text` event.

- **Effort.** `--variant high` against a model that declares no variants exited 0 with
  no warning: silently ignored. The adapter already emits `--variant` only for a declared
  variant (#1643), which is the right gate.
- **Usage.** `step_finish` carries `tokens` (input, output, reasoning, cache read and
  write) and `cost`, which is 0 for a local model.

### 5.3 Security scope

- **Required:** a data root per turn, created fresh and deleted when the turn ends, with
  the isolation ADR-022 § 8 gives a run's root (the four XDG roots moved into it and the
  turn's own `OPENCODE_CONFIG_CONTENT`; § 8 leaves `HOME` where it is); a permission map
  that denies `edit`, `bash`, `webfetch` and `external_directory`; the message on stdin,
  which ADR-022 § 19 already requires because a positional message beginning `--auto`
  would switch on auto-approval.
- **Optional:** `--variant` for a variant the model declares; `--print-logs --log-level
ERROR`, as the pipeline adapter passes them.
- **Forbidden:** `--auto`; an `allow` wildcard in the permission map; `--share`; `-s`
  and `--continue`, which would take context from a root rather than the read; and a
  `serve` process shared across conversations, since spike #1650 showed an attached run
  takes the server's config, permission map and session database rather than its own.

opencode has no bypass flag in the sense the issue names; `--auto` is its equivalent, and
nothing in the working invocation needs it. The adapter is also gated behind
`NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1`.

**Verdict: defer.** It fails the streaming bar on the invocation that works. A `serve`
process started per turn, inside that turn's own root and with that turn's config, would
keep the isolation #1650 found missing from a shared server and stream deltas over
`/event`, at the 1.2 s boot cost #1650 measured. That is the follow-up.

## 6. `grok` — defer (unverified)

The installed CLI is 1.0.25 and is not authenticated, so no turn could run:

```text
$ grok models
You are not authenticated.

Default model: grok-4.6
…
```

From its `--help`:

- `-r, --resume <SESSION_ID_OR_TITLE>` resumes a session; `-s, --session-id <SESSION_ID>`
  names a **new** conversation and "does not resume existing sessions". The vendor's
  headless page says `--session-id` "creates or resumes"; the two disagree, and the
  installed help is what would run. Neither is needed on a replayed turn.
- No option turns session persistence off; `grok du` reports what the grok home
  (`~/.grok`) uses on disk, and `~/.grok/sessions/` exists on this machine.
- `--output-format streaming-json` emits one ACP session update per line;
  `streaming-messages-json` with `--include-partial-messages` emits text deltas.
- `--reasoning-effort` (alias `--effort`); `--permission-mode` with `default`,
  `acceptEdits`, `auto`, `dontAsk`, `bypassPermissions` and `plan`; `--sandbox`;
  `--tools`; `--disable-web-search`; `--always-approve`.

The repository's real `streaming-json` capture from an earlier 1.0.x CLI
(`internal/execution/testdata/grok_stream_real_capture.jsonl`) shows text arriving in
word-sized `text` events and a terminal `end` event carrying `sessionId`, `usage` and
`total_cost_usd`. Streaming is therefore likely; neither replay nor what a turn leaves
on disk is observed on any version.

Candidate: `grok --output-format streaming-json --permission-mode dontAsk --tools <list>
--disable-web-search --no-auto-update --prompt-file <file> -m <model> --reasoning-effort
<rung>`, with the replayed context and the message in the prompt file, a fresh session
each turn, and that session deleted after the turn by whatever means the re-probe
establishes.

- **Required:** `--permission-mode dontAsk` (or `plan`); an explicit `--tools` list; the
  message in `--prompt-file`, never on argv; the turn's session removed after the turn.
- **Optional:** `--sandbox <profile>`, whose profiles this spike could not inspect;
  `--disable-web-search` unless #1570 allows the web; `--max-turns`.
- **Forbidden:** `--always-approve`, which the pipeline builder passes;
  `--permission-mode bypassPermissions`, `acceptEdits` or `auto`; `--resume` and
  `--continue`.

**Verdict: defer** until an authenticated probe.

## 7. `gemini` and `gemini-sdk` — defer (unverified)

Not installed:

```text
$ which gemini
gemini not found
```

The vendor's documentation (§ 15) describes a candidate:
`gemini --output-format stream-json --approval-mode plan -m <model>` with the replayed
context and the message on stdin, where `stream-json` emits an `init` event carrying the
session id and `message` events carrying "user and assistant message chunks", and `plan`
is a read-only approval mode. There is no per-invocation effort control; a thinking
budget lives only in settings (`thinkingConfig.thinkingBudget`), so effort is reported as
not applied. The documentation describes no switch that stops a session being saved:
sessions are written to `~/.gemini/tmp/<project_hash>/chats/`, kept 30 days by default
(`general.sessionRetention`, with a 1-day minimum), and removable with
`--delete-session <index>`.

- **Required:** `--approval-mode plan`; the message on stdin; the turn's session removed
  after the turn.
- **Optional:** `--sandbox`.
- **Forbidden:** `--yolo` (`-y`); `--approval-mode yolo` or `auto_edit`; `-r`/`--resume`;
  the message on argv, which is where both Go builders put the prompt today.

`gemini-sdk` in the Go registry spawns the same CLI, so it follows `gemini`.

**Verdict: defer** for both, until probed with the CLI installed.

## 8. `copilot` — defer (unverified)

Not installed:

```text
$ which copilot
copilot not found
```

The vendor's documentation (§ 15) shows `copilot --resume=SESSION-ID`, `-p PROMPT` (the
prompt as an argv value), `--output-format json` emitting JSONL, `--available-tools`,
`--deny-tool` and `--no-ask-user`, and documents no reasoning-effort control, so effort
would be reported as not applied. It stores "the complete record of each session under
`~/.copilot/session-state/`" and a subset in a local SQLite session store; `COPILOT_HOME`
moves the `~/.copilot` directory; no switch that stops a session being saved is
documented. `docs/ADAPTER_MATRIX.md` says the CLI emits no JSON; the current
documentation says otherwise, which the re-probe should settle.

Candidate, to be established: `copilot --output-format json --available-tools <read-only
list> --no-ask-user`, with the replayed context and the message on stdin as the pipeline
adapter already sends a prompt, and `COPILOT_HOME` pointed at a directory created for the
turn and deleted after it. If a turn runs non-interactively only as `-p <message>`, the
message is on argv, which a conversation path forbids, and copilot would be recorded
`skip` unless the CLI offers another channel for it.

- **Required:** `--available-tools` with a read-only list; `--no-ask-user`; the message
  off argv, which the documented `-p PROMPT` form does not offer and the re-probe must
  establish; a per-turn `COPILOT_HOME`, if the re-probe shows it holds the session.
- **Optional:** `--deny-tool` for defence in depth.
- **Forbidden:** `--allow-all` and its alias `--yolo`; `--allow-all-tools`, which the
  pipeline builder passes; `--resume`; `--share`, which writes the transcript to a file.

**Verdict: defer** until probed with the CLI installed.

## 9. `ollama` and `lm-studio` — skip

Neither adapter exists. #2128 removed both:

```text
$ nightgauge adapter test ollama
Error: adapter ollama: unknown adapter "ollama"
$ nightgauge adapter test lm-studio
Error: adapter lm-studio: unknown adapter "lm-studio"
```

A configuration that names either is rejected with the retirement error in
`internal/config/retired_adapters.go`. A conversation on a local model would come through
`opencode` (§ 5), which is how the opencode probes in this record ran.

## 10. Summary

| Adapter           | Multi-turn by replay, persistence off | Streaming within a reply               | Effort control                              | Bypass needed | Verdict |
| ----------------- | ------------------------------------- | -------------------------------------- | ------------------------------------------- | ------------- | ------- |
| `claude-headless` | observed: `--no-session-persistence`  | observed: `text_delta` events          | `--effort`, dropped silently if unsupported | no            | adopt   |
| `claude-sdk`      | same binary; its credential not run   | same binary                            | same                                        | no            | defer   |
| `codex`           | unverified: every turn refused        | none on `exec --json`; app-server only | `-c model_reasoning_effort`, per turn       | no            | defer   |
| `opencode`        | observed: a fresh root per turn       | none: one event per reply              | `--variant`, declared variants only         | no            | defer   |
| `grok`            | unverified (not authenticated)        | captured on an earlier version         | `--reasoning-effort`                        | no (help)     | defer   |
| `gemini`          | unverified (not installed)            | documented                             | none per invocation                         | no (docs)     | defer   |
| `gemini-sdk`      | follows `gemini`                      | follows `gemini`                       | follows `gemini`                            | no (docs)     | defer   |
| `copilot`         | unverified (not installed)            | documented JSONL, granularity unknown  | none                                        | no (docs)     | defer   |
| `ollama`          | retired (#2128)                       | —                                      | —                                           | —             | skip    |
| `lm-studio`       | retired (#2128)                       | —                                      | —                                           | —             | skip    |

## 11. Mapping the performance-mode envelope

A mode's envelope (`routing.ModeEnvelope`) has five fields. A conversation turn is not a
pipeline stage, so no stage pin applies to it, including `maximum`'s six; the envelope
bounds the turn the way it bounds a router-chosen tier.

| Envelope field                 | Claude adapters                                                                                                                  | `codex`                                            | `opencode`                                                       | `grok`                                                | `gemini`, `gemini-sdk`                 | `copilot`                               |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------- | ---------------------------------------------------------------- | ----------------------------------------------------- | -------------------------------------- | --------------------------------------- |
| `Floor`, `Ceiling` (model)     | `--model`, the band clamped by `routing.ClampToEnvelope`                                                                         | `-m`, the clamped band through `resolveCodexModel` | not applicable: a model must name its provider, and bands do not | `--model` through `resolveGrokModel`                  | `--model` through `resolveGeminiModel` | `--model` through `resolveCopilotModel` |
| `EffortFloor`, `EffortCeiling` | `--effort`, only for a rung the launched model's `supported_efforts` holds                                                       | `-c model_reasoning_effort=`, on every turn        | `--variant`, only for a declared variant                         | `--reasoning-effort`, checked by `validateGrokEffort` | none per invocation                    | none                                    |
| `ThinkingPolicy`               | `off` sets `CLAUDE_CODE_DISABLE_THINKING=1` in the turn's environment; no flag turns thinking on, so `on` is the model's default | none apart from effort                             | none (`--thinking` only displays thinking)                       | none known                                            | settings only                          | none                                    |

The resolved effort is the one #1567 already advertises: the default effort clamped by
`routing.ClampEffortToEnvelope`. No mode declares a `ThinkingPolicy` today.

**One rule for effort, the pipeline's:**

1. **No effort axis:** when the adapter has no effort control, or the launched model's
   `supported_efforts` is empty, the turn runs with no effort flag and its terminal frame
   reports the effort as not applied and why. It never reports the requested rung as
   the one used.
2. **An undeclared rung on a model that has an axis:** the turn fails before it spawns,
   as a stage does (`assertEffortSupported`, which "deliberately throws rather than
   downgrading"; #569 applies the same gate to adapter dispatch). For example,
   `claude-opus-4-8` declares `low` to `xhigh`, so a resolved `max` fails the turn rather
   than running it at the model's default.
3. **A declared rung** is passed through the adapter's native control on every turn.

Because codex drops the effort on resume and claude drops an unsupported one silently,
the rule is enforced on the Nightgauge side, on every turn, and never delegated to the
CLI. The same holds for the model envelope on `opencode`: the turn runs on the
operator's pinned `provider/model` and reports the envelope as not applied.

## 12. Stateless replay, held sessions and what stays on disk

### 12.1 The mode is decided

#1568's acceptance criterion 4, as reconciled on 2026-10-02, decides it, and #1569's
multi-turn criterion repeats it: a turn's prior context comes only from the hosted
context read, which the daemon fetches with the turn's lease, because two consecutive
turns may be answered on different machines. The preferred mode replays statelessly,
with the adapter's session persistence turned off. A held session, in a process or in
the CLI's own store, may at most act as a cache, and only where it is shown to equal the
read exactly.

The observations agree with that. A CLI's stored session exists only on the machine
that wrote it. Claude's was observed to diverge from the conversation after a cancel
(§ 3.2), and every CLI that can resume keeps the turn's text in a store of its own,
which a thread's deletion would then have to reach on every host it ever ran on.

**So each turn is a new process group**, spawned with the existing `Setpgid`
discipline, with persistence off, given the context read rendered into its stdin
message (§ 1.3) and never a session handle. It follows that:

1. Idle conversations cost nothing; cost scales with turns, not with open threads.
2. Cancelling a turn is the kill-and-verify #1569 already requires (§ 3.2: the group was
   dead 657 ms after `SIGTERM`), and there is no CLI session to fall out of step.
3. A daemon restart, or a turn on another machine, loses nothing: the context is the
   read.
4. Model, effort and permission posture are resolved and passed on every turn.

### 12.2 Per adapter: persistence off, cache equality, and what is kept on disk

| Adapter           | Stateless with persistence off                                                                                                         | Can a held session be shown to equal the read?                                                                                                                                                   | What the adapter keeps on disk, and how it is bounded and erased                                                                                                                                                                                                                                                                                         |
| ----------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `claude-headless` | **Observed:** `--no-session-persistence`, context in the stdin message (§ 3.1, § 3.2)                                                  | Testable in principle, since the session file is JSONL of user and assistant messages, but **not shown**: it diverged after a cancel (§ 3.2) and exists only on the host that wrote it. Not used | **Observed:** with the flag, the turn's text was in no file under `~/.claude`, `~/.claude.json`, the caches, the logs, `$TMPDIR` or `/tmp`. Without it, the text was in `~/.claude/projects/<working-directory key>/<session-id>.jsonl`, kept until something deletes it                                                                                 |
| `claude-sdk`      | As `claude-headless`; the API-key leg is unverified                                                                                    | As `claude-headless`                                                                                                                                                                             | As `claude-headless` (same binary); unverified on the API-key credential                                                                                                                                                                                                                                                                                 |
| `codex`           | **Observed on disk, no reply:** `exec --ephemeral` ("Run without persisting session files to disk"); app-server: an `ephemeral` thread | Not shown. A resumed rollout re-derives sandbox and effort (§ 4.2), so a resume is not even a faithful copy of the session it names                                                              | **Observed:** with `--ephemeral`, the turn's text was in no file under `~/.codex` or `$TMPDIR`. Without it, the text was in the rollout file `~/.codex/sessions/<yyyy>/<mm>/<dd>/rollout-…jsonl` and in two shared databases, `~/.codex/state_5.sqlite` (its WAL) and `~/.codex/thread_history_1.sqlite`, which deleting one file cannot erase           |
| `opencode`        | **Observed:** a fresh data root per turn, deleted when the turn ends; context in the stdin message (§ 5.1, § 5.2)                      | No: the session database lives in the turn's root, which is deleted with the turn, and ADR-022 § 22 deletes a run's root when the run ends                                                       | **Observed:** the turn's text was only in the root's `data/opencode/opencode.db-wal`; after the root was deleted it was in no file under the scratch area, `~/.local`, `~/.cache`, `~/.config`, `~/.opencode`, `~/Library`, `$TMPDIR` or `/tmp`. ADR-022 § 22: `opencode.db` holds the full prompt and transcript, with `snapshot/` and `log/` beside it |
| `grok`            | Unverified: 1.0.25's `--help` offers no switch; a fresh session per turn, removed after it                                             | Unverified                                                                                                                                                                                       | Unverified: sessions under `~/.grok/sessions/`; `grok du` reports the home's use; no erase command was found in the help                                                                                                                                                                                                                                 |
| `gemini`          | Unverified: no switch is documented                                                                                                    | Unverified                                                                                                                                                                                       | Documented: `~/.gemini/tmp/<project_hash>/chats/`, 30 days by default (`general.sessionRetention`, 1-day minimum); `--delete-session <index>`                                                                                                                                                                                                            |
| `gemini-sdk`      | Follows `gemini`                                                                                                                       | Follows `gemini`                                                                                                                                                                                 | Follows `gemini`                                                                                                                                                                                                                                                                                                                                         |
| `copilot`         | Unverified: no switch is documented; a per-turn `COPILOT_HOME` is the candidate                                                        | Unverified                                                                                                                                                                                       | Documented: `~/.copilot/session-state/` and a local SQLite session store; `COPILOT_HOME` moves `~/.copilot`                                                                                                                                                                                                                                              |

A cache would need, before every turn, a comparison of the held session's user and
assistant text, in order, with the read, discarding the session and replaying on any
difference, including after every cancelled turn. No adapter has one, and none is
recommended: the replay was observed to cost one process start and the replayed text's
input tokens.

### 12.3 What a held session would cost

| Measurement                                                    | Held process per conversation                        | Process per turn, persistence off |
| -------------------------------------------------------------- | ---------------------------------------------------- | --------------------------------- |
| Resident memory while idle, Claude (three runs)                | **218–226 MiB**, one process                         | 0; nothing is kept                |
| Resident memory while idle, `codex app-server` with one thread | 163 MiB for `codex` itself                           | 0                                 |
| Resident memory while idle, `opencode serve` with one session  | 633 MiB                                              | 0                                 |
| Claude turn 2, first text delta                                | 0.86 s, 0.95 s, 0.87 s                               | 7.63 s, 2.63 s, 2.73 s (resumed)  |
| Survives a daemon restart or a move to another host            | no: the context is in the held process               | yes: the context is the read      |
| Cancel one turn                                                | must interrupt in-protocol, or kill the conversation | kill the turn's process group     |

The per-turn latencies were measured on the resumed first round, at normal load; the
replayed turns of § 3.2 reached first text in 4.9 s and 5.5 s at a load average near
100, which is not a comparable figure. Ten open conversations held on Claude would cost
about 2.2 GiB on a developer laptop, and ten on `opencode serve` about 6.2 GiB, whether
or not anyone is typing. If a surface later measures the per-turn start as the problem,
a warm process can be revisited only as a cache under § 12.2's equality rule.

## 13. The viability bar

An adapter enters `conversationViableAdapters`, and so advertises `conversation`, only
when every condition below holds, each pinned by a test or a recorded fixture in this
repository. This spike states the bar; it does not fill the set. Only #1569's change,
the one that adds the turn handler, does.

1. **Verdict.** This record, or a later amendment to it, gives the adapter
   `action: adopt`.
2. **Builder.** #1569's runner dispatches the adapter through a conversational turn
   builder, and an argv contract test enumerates every argv that builder can produce and
   asserts that the adapter's persistence-off control (§ 12.2) is present, that no flag
   on the adapter's forbidden list appears (§ 3.6, § 4.4, § 5.3, § 6, § 7, § 8, which
   include every resume and session-handle flag), that the permission or sandbox posture
   is explicit, and that the message is never on argv.
3. **Multi-turn by replay.** A recorded two-turn capture from a pinned CLI version shows
   turn 2, a new process with persistence off whose only knowledge of turn 1 is the
   replayed context in its stdin message, stating a fact only turn 1 contained.
4. **Streaming within a message.** A recorded capture of a single-message, tool-free
   reply of at least 50 words carries at least two non-empty text fragments **of the
   same message part** (for Claude, `text_delta` events with the same content-block
   `index` inside one message; for another adapter, the same message or part id), the
   first arriving at least 500 ms before that part completes (Claude:
   `content_block_stop`; otherwise the event that closes the message). Whole messages
   sent one after another do not count, so a batch emitter fails however many it sends.
5. **Accounting.** The terminal event yields the turn's own token usage; a
   cumulative-only figure is differenced against the previous turn.
6. **Cancel.** `SIGTERM` to the turn's process group leaves `kill -0` on the group failing
   within 10 s.
7. **Effort.** The builder follows § 11's rule: no flag, reported as not applied, for an
   adapter or model with no effort axis; the turn fails before spawn for a rung outside
   the model's declared ladder; a declared rung is passed through the native control.
8. **Retention.** After a completed turn and after a cancelled one, a probe reference
   carried in the turn's message is in no file the adapter wrote outside a per-turn
   directory, and that directory is gone once the turn has ended (§ 1.3's search).

An adapter whose only working conversational invocation needs a forbidden flag is
recorded `skip`, not `adopt`. Until #1569's runner exists, condition 2 cannot hold for
any adapter, so `ConversationViable` keeps answering `false` for all eight. #1569 makes
the advertised set and the dispatchable set the same set by construction:
`ConversationViable(a)` is true exactly when `a` has a registered conversational builder
that passes conditions 2 to 8. #1569's own verification asks for "at least two distinct
chunk events before the terminal frame"; condition 4 is the form of that check that a
batch emitter cannot pass.

## 14. Recommendation for #1569

- **Replay every turn statelessly, with persistence off** (§ 12), on `claude-headless`
  only until `claude-sdk`'s credential has its transcript.
- **Build a conversational argv per adopted adapter** (§ 3.1); never reuse
  `BuildCommand`, whose Claude forms carry no `--restricted`, no permission mode and no
  `--tools` list,
  and whose codex, grok, copilot and gemini forms carry a bypass-class flag or the prompt
  on argv.
- **Render the context read into the turn's stdin message** as data (§ 1.3), never into
  the system prompt, and never pass a session handle.
- **Read the stream with the existing Claude `stream-json` parser and token accumulator.**
  Emit assistant text from the `text_delta` events, take the turn's tokens from
  `result.usage`, and never meter `total_cost_usd` directly.
- **Enforce the floor in the builder:** `--no-session-persistence`, `--restricted`,
  `--permission-mode dontAsk`, an explicit `--tools` list, `--strict-mcp-config`, and a
  forbidden-flag test over every argv the builder can produce, whatever the workspace's
  auto-accept configuration.
- **Map the envelope as § 11 says**, on every turn, and report what was not applied.
- **Bound the turn** with the daemon's own timeout and token ceiling. `--max-budget-usd`
  and `--max-turns` are useful per-turn backstops but not hard ceilings (§ 3.5).
- **Keep the capability gate closed until the runner serves a turn** (§ 13).

## 15. Evidence

Installed CLIs, each `--help` read at the version in § 1.2: `claude --help`;
`codex --help`, `codex exec --help`, `codex exec resume --help`,
`codex app-server --help` and `codex app-server generate-json-schema`; `opencode --help`
and `opencode run --help`; `grok --help` and `grok agent --help`.

Vendor documentation, read on 2026-10-02:

- Codex non-interactive mode: `developers.openai.com/codex/noninteractive` (redirects to
  `learn.chatgpt.com/docs/non-interactive-mode`).
- Gemini CLI, `github.com/google-gemini/gemini-cli` at `docs/cli/`: `headless.md`,
  `cli-reference.md`, `session-management.md`, `plan-mode.md` and
  `generation-settings.md`.
- GitHub Copilot CLI: `docs.github.com/en/copilot/reference/copilot-cli-reference/cli-programmatic-reference`,
  `docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/overview`,
  `docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/work-with-multiple-sessions`
  and `docs.github.com/en/copilot/concepts/security-governance-and-network-settings/session-data`.
- Grok CLI headless scripting: `docs.x.ai/build/cli/headless-scripting`.

This repository: `internal/execution/adapters/` (the eight builders and the registry),
`internal/config/retired_adapters.go`, `internal/executionprofile/profile.go`,
`internal/intelligence/routing/performance_mode.go`, the real stream captures under
`internal/execution/testdata/`, `docs/decisions/022-opencode-multi-provider-adapter.md`
(§ 8, § 19, § 22), `packages/nightgauge-vscode/src/utils/resolvers/stageResolver.ts`
(`assertEffortSupported`) and `packages/nightgauge-vscode/src/utils/skillRunner.ts`.

## Recommendations

```yaml recommendations
spike: 1568
recommendations:
  - id: claude-headless-conversation-turn
    action: adopt
    title: "conversation: Claude turn builder — stateless replay with persistence off, restricted, streamed, per-turn usage"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:conversation"]
    body: |
      The adapter half of #1569 for `claude-headless`, as spike #1568 recorded it
      (docs/spikes/1568-conversational-agent-sessions-across-adapters.md, § 3, § 12
      and § 13).

      Build the conversational argv beside the pipeline's `BuildCommand`, never by
      reusing it: the pipeline builder has no `--restricted`, no permission mode and no
      `--tools` list.

      - Every turn: `claude -p --no-session-persistence --model <m> [--effort <e>]
        --output-format stream-json --verbose --include-partial-messages --restricted
        --permission-mode dontAsk --tools <list> --strict-mcp-config`, with the context
        read rendered as a JSON array of earlier messages, then the new message, on
        stdin. Never a session handle, never the system prompt.
      - Effort by the record's one rule (§ 11): no flag, reported as not applied, for a
        model whose `supported_efforts` is empty; fail before spawn for a rung outside a
        non-empty ladder; otherwise `--effort <rung>`.
      - Tokens from `result.usage`; never meter `total_cost_usd` directly.
      - A table-driven argv test over every argv the builder can produce: it always
        carries `--no-session-persistence`, and never `--dangerously-skip-permissions`,
        `--allow-dangerously-skip-permissions`, `bypassPermissions`, `acceptEdits`,
        `auto`, `--resume`, `--continue`, `--session-id` or the message itself.
      - Recorded fixtures for the viability bar: a two-turn replay capture, a capture
        of a single-message reply of 50 words or more with at least two text deltas in
        one content block, the first at least 500 ms before its `content_block_stop`,
        a cancel run with the group dead within 10 s, and a retention probe showing the
        turn's text in no file the CLI wrote.
    depends_on: []
  - id: claude-sdk-conversation-reprobe
    action: defer
    title: "conversation: re-probe claude-sdk on its API-key credential before adopting it"
    type: spike
    priority: medium
    size: XS
    labels: ["component:go-binary", "program:conversation"]
    body: |
      `claude-sdk` spawns the same `claude -p` as `claude-headless`, but spike #1568
      (§ 3.7) never ran a turn on its credential: no `ANTHROPIC_API_KEY` was set, and
      #1568's Verification accepts no `adopt` without a reproducible transcript.

      Run the two-turn replay of § 3.2 once with `ANTHROPIC_API_KEY` set and
      persistence off, plus the retention probe of § 13 condition 8. If both hold,
      amend the record to `adopt` and register `claude-sdk` with the Claude turn
      builder, passing the credential through as its pipeline adapter does.
    depends_on: ["claude-headless-conversation-turn"]
  - id: codex-conversation-reprobe
    action: defer
    title: "conversation: re-probe codex with quota — a per-turn app-server on an ephemeral thread, read-only sandbox"
    type: spike
    priority: medium
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      Spike #1568 (§ 4) could not observe a codex reply: the account's usage limit
      refused every turn. Independently, `codex exec --json` delivers an agent message
      whole, so the `exec` path cannot stream within a reply.

      Re-probe against the viability bar (§ 13) with an account that has quota: a
      per-turn `codex app-server` that starts an `ephemeral` thread, runs one
      `turn/start` on the replayed context read with `effort` and a read-only
      `sandboxPolicy`, relays `item/agentMessage/delta`, and exits. Record the two-turn
      replay capture and the retention probe: `exec --ephemeral` was observed to leave
      the turn's text in no file, while a persisted run wrote it to a rollout file and
      to `state_5.sqlite` and `thread_history_1.sqlite` (§ 12.2), so confirm the
      app-server's ephemeral thread does the same. Never `exec resume`, and never the
      pipeline's `BuildCommand`, which falls back to
      `--dangerously-bypass-approvals-and-sandbox`.
    depends_on: []
  - id: opencode-conversation-streaming
    action: defer
    title: "conversation: opencode streams a reply only whole — prove a per-turn serve streaming /event deltas"
    type: spike
    priority: medium
    size: S
    labels: ["component:go-binary", "program:conversation", "program:opencode"]
    body: |
      Spike #1568 (§ 5) observed opencode answer a replayed second turn from a fresh
      data root, with the turn's text erased by deleting that root, but
      `run --format json` delivered a 58-word reply as one event, so it fails the
      streaming bar.

      Probe a `serve` process started per turn inside that turn's own root (the four
      XDG roots moved into it, its own config and permission map), deleted when the
      turn ends: spike #1650's isolation finding concerns a server shared across runs,
      which this is not. Measure delta granularity on `/event` against § 13 condition 4,
      the per-turn boot cost, that the server's process group is dead after the turn,
      and that the turn's text is nowhere once the root is gone. Keep the message on
      stdin, or in the request body; never pass `--auto`, `-s` or `--continue`.
    depends_on: []
  - id: grok-conversation-reprobe
    action: defer
    title: "conversation: probe grok with an authenticated CLI — replay, streaming chunks, dontAsk, session erasure"
    type: spike
    priority: low
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      grok 1.0.25 was installed but not authenticated, so spike #1568 (§ 6) observed no
      turn. Probe the candidate `grok --output-format streaming-json --permission-mode
      dontAsk --tools <list> --disable-web-search --no-auto-update --prompt-file <file>`
      with the replayed context in the prompt file, against the viability bar (§ 13).
      Its `--help` offers no switch that stops a session being saved, and sessions sit
      under `~/.grok/sessions/`: establish what one turn writes there and how it is
      erased, or record that it cannot be. The pipeline builder's `--always-approve`
      is forbidden on this path.
    depends_on: []
  - id: gemini-conversation-reprobe
    action: defer
    title: "conversation: probe gemini with the CLI installed — replay, stream-json chunks, plan mode, session erasure"
    type: spike
    priority: low
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      The gemini CLI was not installed, so spike #1568 (§ 7) recorded only what its
      documentation describes: `stream-json` message chunks, the read-only
      `--approval-mode plan`, and sessions saved to `~/.gemini/tmp/<project_hash>/chats/`
      for 30 days by default with no documented switch to stop it. Probe a replayed
      turn against the viability bar (§ 13) with the message on stdin (both Go builders
      put the prompt on argv, which a conversation must not), and establish how a
      turn's session is kept off disk or erased. Effort has no per-invocation control
      and is reported as not applied.
    depends_on: []
  - id: gemini-sdk-conversation
    action: defer
    title: "conversation: gemini-sdk follows gemini — the Go adapter spawns the same CLI"
    type: spike
    priority: low
    size: XS
    labels: ["component:go-binary", "program:conversation"]
    body: |
      In the Go registry `gemini-sdk` spawns the `gemini` CLI, so its verdict follows the
      gemini re-probe from spike #1568 (§ 7). Confirm only what differs: the credential
      cascade (`GEMINI_API_KEY`, then `GOOGLE_API_KEY`).
    depends_on: ["gemini-conversation-reprobe"]
  - id: copilot-conversation-reprobe
    action: defer
    title: "conversation: probe copilot with the CLI installed — message off argv, JSONL granularity, session erasure"
    type: spike
    priority: low
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      The copilot CLI was not installed, so spike #1568 (§ 8) recorded only what its
      documentation describes: `--output-format json` (JSONL), `--available-tools`,
      `--deny-tool`, sessions stored under `~/.copilot/session-state/` plus a SQLite
      session store, and `COPILOT_HOME` to move that directory. Establish whether a turn
      runs non-interactively with the message and the replayed context off argv, how
      finely the JSONL streams a reply, whether a per-turn `COPILOT_HOME` keeps the
      session off the operator's disk, and whether `docs/ADAPTER_MATRIX.md`'s "no JSON
      output" still holds. The pipeline builder's `--allow-all-tools` is forbidden on
      this path, as are `--allow-all`, `--yolo` and `--share`; there is no effort
      control, so effort is reported as not applied.
    depends_on: []
  - id: ollama-conversation
    action: skip
    title: "ollama: no conversation path — the adapter was retired by #2128"
    type: chore
    priority: low
    size: XS
    body: |
      The registry rejects `ollama`; a local model converses through `opencode`
      (spike #1568, § 9).
    depends_on: []
  - id: lm-studio-conversation
    action: skip
    title: "lm-studio: no conversation path — the adapter was retired by #2128"
    type: chore
    priority: low
    size: XS
    body: |
      The registry rejects `lm-studio`; a local model converses through `opencode`
      (spike #1568, § 9).
    depends_on: []
  - id: overall-recommendation-for-1569
    action: skip
    title: "Overall, for #1569: stateless replay of the context read, persistence off, on claude-headless (recorded here; #1569 is the issue)"
    type: feature
    priority: high
    size: L
    labels: ["component:go-binary", "program:conversation"]
    body: |
      Recorded with `action: skip` only so the materializer files no duplicate: #1569
      already tracks the runner. The recommendation (spike #1568, § 12 to § 14): run each
      turn as a new process group with the adapter's session persistence off, its prior
      context replayed from the hosted context read into its stdin message and never
      from a session handle; ship `claude-headless` only; build a conversational argv
      per adapter instead of reusing `BuildCommand`; map the performance-mode envelope
      on every turn by § 11's one effort rule; and keep `ConversationViable` false until
      the runner serves a turn.
    depends_on: ["claude-headless-conversation-turn"]
```
