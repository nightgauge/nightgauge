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
merges. Each adopted adapter gets the adapter-level half of its conversational turn,
which #1569 consumes; each deferred adapter gets a re-probe that names what this spike
could not observe. The overall recommendation is recorded with `action: skip` only
because #1569 already is that issue, so the materializer must not file a second one.

**Artifact**: `docs/spikes/1568-conversational-agent-sessions-across-adapters.md`

## Executive Summary

**Verdicts: adopt `claude-headless` and `claude-sdk`; defer `codex`, `opencode`, `grok`,
`gemini`, `gemini-sdk` and `copilot`; skip `ollama` and `lm-studio`, which no longer
exist.** Two adapters clear the viability bar (§ 13), and they are one binary.

**#1569 should run conversational turns stateless**: one process group per turn, with
the context carried by the CLI's own session and resumed by a handle the daemon records.
For the Claude adapters the daemon mints that handle itself (`--session-id <uuid>` on the
first turn, `--resume <uuid>` after). Measured on a 64 GiB laptop: a held Claude session
costs **218–226 MiB resident per open conversation** and answers a second turn in
0.86–0.95 s to first text; a stateless turn costs nothing while idle and answers in
2.6–2.7 s typically (7.6 s at worst in three runs). Holding a process buys about 1.8 s
per turn at about 220 MiB per conversation, loses the context when the daemon restarts,
and makes cancelling a turn kill the conversation with it (§ 12).

What defers the other adapters is an observation, not a guess:

- **codex**: the authenticated account's usage limit refused every turn, so no reply was
  observed. Independently, `codex exec --json` delivers an agent message whole, in one
  `item.completed` event, so the stateless path cannot stream within a reply; only the
  experimental `codex app-server` streams deltas.
- **opencode**: two- and three-turn resume works (observed live against a local model),
  but `opencode run --format json` delivered a 58-word reply as **one** event, a
  millisecond before the turn ended. It does not stream within a reply.
- **grok** is installed but not authenticated; **gemini** and **copilot** are not
  installed. Each is recorded as unverified, with the candidate invocation its help text
  or vendor documentation implies.

**No adapter needs a bypass flag to converse, but five pipeline builders would be unsafe
to reuse for a conversation.** The codex builder falls back to
`--dangerously-bypass-approvals-and-sandbox` for an empty tool list, the grok builder
always passes `--always-approve`, the copilot builder `--allow-all-tools`, and the gemini
builders put the prompt on argv. The claude-headless builder passes
`--no-session-persistence`, with which `--resume` fails outright. #1569 needs a
conversational builder per adapter, never `BuildCommand`. The Claude path is safe by
construction: `--restricted` refuses `bypassPermissions` and
`--dangerously-skip-permissions` at startup, before any model call, and
`--permission-mode dontAsk` denied an unapproved `Bash` call and reported it.

**Effort cannot be left to the CLIs.** Claude silently ignores `--effort max` on a model
with no effort axis (exit 0, no warning); opencode silently ignores an undeclared
`--variant`; codex does not carry the effort onto a resumed turn. The envelope is
therefore mapped on the Nightgauge side, from the registry's `supported_efforts`, passed
on every turn, and reported as not applied wherever it cannot be applied (§ 11).

One finding reaches beyond conversation: `codex exec resume` honours
`-c sandbox_mode="<mode>"` and never inherits the session's sandbox, so the SDK's opt-in
resume path, which bypasses the sandbox on every resume, drops a read-only stage's
sandbox for no reason. That is #2342, fixed alongside this record.

The `conversation` capability stays unadvertised. `conversationViableAdapters` remains
empty until #1569's runner can serve a turn on an adopted adapter: a workspace must not
claim a capability no code path serves.

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
| `claude-headless`, `claude-sdk` | `claude`   | 2.1.287                        | authenticated (subscription login)          |
| `codex`                         | `codex`    | codex-cli 0.154.0              | authenticated; plan usage limit reached     |
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
  by a local Ollama 0.32.11 for opencode. The opencode runs had a private `HOME` and
  private XDG roots, and a config whose permission map denied `edit`, `bash`, `webfetch`
  and `external_directory`.
- The two-turn test: turn 1 says "My favourite fruit is the plum. Reply with just the
  word: noted."; turn 2, **in a new process**, asks "Which fruit did I say was my
  favourite? Answer in one word." A reply of "plum" is the evidence that context
  carried.
- Every child was spawned as a session leader. "Dead" means `kill(-pgid, 0)` failed
  afterwards.
- Resident memory is the summed RSS of the process tree, 3 s after a turn completed.
- The machine: a 12-core Apple-silicon laptop with 64 GiB. The probe sessions were
  deleted afterwards.

## 2. Where observation contradicts the issue

| The issue assumes                                                                       | Observed at `origin/main`                                                                                                                                                                                                                                                                                                                                                                                                               |
| --------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Nine adapters, including `ollama` and `lm-studio`                                       | Eight. #2128 retired both; `opencode` replaces them and is assessed (§ 1.1)                                                                                                                                                                                                                                                                                                                                                             |
| The extension's interactive path hardcodes `spawn("claude", …)` and ignores the adapter | It follows the adapter: it refuses `opencode`, `gemini`, `grok`, `gemini-sdk` and `copilot` with an error, opens Codex's own interface in a terminal, and spawns `claude` only for the two Claude adapters (`validateAdapterPrerequisites` and `launchCodexInteractiveTerminal` in `packages/nightgauge-vscode/src/utils/skillRunner.ts`). It is still stage-scoped and invisible to the daemon, so #1569's rule not to reuse it stands |
| "The Claude adapter passes `--no-session-persistence`"                                  | `claude-headless` does; `claude-sdk` does not. With the flag, a later `--resume` fails: `No conversation found with session ID: <uuid>`, exit 1 (§ 3.2). The extension's `resumeSessionWithResponse` passes it on the resumed run too, so an answered question is not saved either                                                                                                                                                      |
| `Agentic()` exists because some adapters are chat-only                                  | None of the eight Go adapters is chat-only (§ 1.1)                                                                                                                                                                                                                                                                                                                                                                                      |
| Stateless means "each turn replays the transcript"                                      | For claude, codex and opencode the CLI replays its own stored transcript. The daemon has to replay text only when the handle is lost or the adapter changes between turns (§ 12)                                                                                                                                                                                                                                                        |

None of these changes the question. Each changes an implementation detail #1569 inherits,
and they are recorded here so the recommendation rests on what is true today.

## 3. `claude-headless` and `claude-sdk` — adopt

### 3.1 The conversational invocation

```bash
# First turn: the daemon mints the session handle.
claude -p --session-id <uuid> --model <model> [--effort <rung>] \
  --output-format stream-json --verbose --include-partial-messages \
  --restricted --permission-mode dontAsk --tools "<allow-list or empty>" \
  --strict-mcp-config [--mcp-config <conversation tools>]   # message on stdin

# Every later turn: the same, with --resume in place of --session-id.
claude -p --resume <uuid> … # identical flags, message on stdin
```

The probes also passed `--setting-sources project`, which `--restricted` makes redundant:
restricted mode ignores user, project and local settings files.

### 3.2 Multi-turn — observed

Stateless, one process per turn, with the invocation above and no tools:

| Turn | Process | Reply   | Session handle reported | Input tokens |
| ---- | ------- | ------- | ----------------------- | ------------ |
| 1    | new     | `noted` | the minted `<uuid>`     | 3,627        |
| 2    | new     | `plum`  | the same `<uuid>`       | 3,748        |

Turn 2's process was given only the new message and the handle; its prompt grew by the
first exchange, which the CLI loaded from its own stored session. The same probe
without `--restricted` (a longer system prompt, above the model's minimum cacheable
length) showed turn 2 reading 6,567 prompt tokens from the cache: the CLI replays the
stored transcript, and prompt caching keeps that replay cheap.

Held, one process for both turns (`--input-format stream-json`, each message written to
stdin as a JSON `user` line): turn 2 answered `plum` in the same session. That is the
stateful option measured in § 12.

Three negative results matter to #1569:

- **`--no-session-persistence` makes resume impossible.** After a first turn with the
  flag, `--resume <uuid>` printed `No conversation found with session ID: <uuid>` and
  exited 1 with `result.subtype: error_during_execution`.
- **A handle cannot be minted twice.** `--session-id` with an existing handle printed
  `Error: Session ID <uuid> is already in use.` and exited 1, so a retried first turn
  must resume rather than re-create.
- **A cancelled turn leaves the session usable, and divergent.** A turn asked to count to
  400 was ended by `SIGTERM` to its process group after five text deltas: exit 143, the
  group gone. The next `--resume` turn answered `Plum. No.` (the fruit, and "no, the
  count was not completed"). The CLI kept the cancelled prompt but none of the partial
  reply, and wrote a placeholder assistant message ("No response requested.") so the
  transcript still alternates. The partial text the daemon streamed before the cancel is
  therefore in the conversation's log but not in the model's context.

### 3.3 Streaming — observed

With `--include-partial-messages` the stream carries `stream_event` lines of type
`content_block_delta`: **58 `text_delta` events for a 106-word reply**, after two thinking
deltas and a signature. Without the flag, assistant text arrives only as whole `assistant`
messages. The `system/init` line carries the session handle, the resolved model, the
permission mode, the tool list and `per_turn_effort_active`. The terminal `result` line
carries the session handle, the turn's own `usage`, `total_cost_usd`, `num_turns` and
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
`skillRunner.ts`) and emits no `--effort` for such a model; a conversation must do the
same, and `per_turn_effort_active` lets the daemon confirm what the CLI applied.

### 3.5 Usage, cost and ceilings — observed

- `result.usage` is the turn's own usage. `result.total_cost_usd` is **cumulative across
  a resumed session**: turn 2's figure equalled turn 1's plus turn 2's own usage priced at
  list rates, to the sixth decimal. The CLI persists a `cost-state` record in the session
  transcript. Metering must use `usage`, or difference successive totals.
- `--max-budget-usd` is enforced per invocation, after the call that crosses it. On a
  resumed turn, a budget above the turn's own cost but below the session's total passed;
  a budget below the turn's own cost ended the turn with `error_max_budget_usd`, exit 1,
  after the model call had completed, and with no reply. It is a per-turn soft ceiling; the
  token ceiling #1569 requires stays the daemon's to enforce from the stream.
- `--max-turns` is accepted, although `--help` does not list it, and bounds the turn's
  agent loop.

### 3.6 Security scope

| Required on a conversation turn                                                                                                                                          | Optional                                                                                                                                                                                                         | Forbidden                                                                                                                                                                                                                                  |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `--restricted`; `--permission-mode dontAsk`; `--tools` with an explicit list (empty for none); `--strict-mcp-config`; the message on stdin; `--session-id` or `--resume` | `--allowedTools` rules for the narrow mutations #1570 allows; `--mcp-config` for the conversation tools of #1587; `--add-dir`; `--append-system-prompt`; `--max-budget-usd` and `--max-turns` as per-turn bounds | `--dangerously-skip-permissions`; `--allow-dangerously-skip-permissions`; `--permission-mode bypassPermissions`, `acceptEdits` or `auto`; `--no-session-persistence`; the message on argv, where text beginning `--` would parse as a flag |

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
`--no-session-persistence`. The conversational invocation is § 3.1 plus that credential.
Session storage is the CLI's local transcript, which the credential does not touch, so
the transcript in § 3.2 is this adapter's transcript too. The API-key credential itself
was not exercised, since none was set on this machine; #1569's live multi-turn test must
run once with it.

**Verdict for both: adopt.** Every axis observed, a reproducible two-turn transcript, a
bypass-proof permission floor, and an effort control whose silent failure mode is
detectable.

## 4. `codex` — defer

### 4.1 Candidate invocation (no reply observed)

```bash
# First turn: the handle is thread.started.thread_id in the --json stream.
codex --ask-for-approval never exec --sandbox read-only --json \
  -m <model> -c model_reasoning_effort=<rung> -      # message on stdin

# Every later turn: --sandbox is refused here, so the sandbox travels as config.
codex --ask-for-approval never exec resume <thread-id> - --json \
  -c 'sandbox_mode="read-only"' -m <model> -c model_reasoning_effort=<rung>
```

### 4.2 What was observed

- **No second-turn reply.** Every turn, first or resumed, ended before the model answered
  (the message is cut after its first sentence, which is the part that is not about the
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

  Multi-turn context is therefore unverified.

- **The handle re-attaches.** `exec resume <thread-id>` emitted `thread.started` with the
  same thread id.
- **The sandbox is re-derived on every resume, never inherited.** Each resumed turn's
  policy was read back from the `turn_context` record Codex writes to the session's
  rollout file:

  | Session started with        | Resumed with                                         | Resumed turn ran with                              |
  | --------------------------- | ---------------------------------------------------- | -------------------------------------------------- |
  | `--sandbox read-only`       | `exec resume <id> --sandbox read-only`               | refused: `unexpected argument '--sandbox'`, exit 2 |
  | `--sandbox read-only`       | `exec resume <id> -c sandbox_mode="workspace-write"` | `workspace-write`                                  |
  | `--sandbox workspace-write` | `exec resume <id>`, user config ignored              | `read-only`                                        |
  | `--sandbox workspace-write` | `exec resume <id> -c sandbox_mode="read-only"`       | `read-only`                                        |

  A conversation must pass the sandbox on every resumed turn. Without
  `--ignore-user-config`, an operator's `config.toml` decides it instead.

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
agent messages and no delta. `turn.completed` carries `usage` (`input_tokens`,
`cached_input_tokens`, `output_tokens`, `reasoning_output_tokens`).

Deltas exist only on `codex app-server`, which the installed CLI labels experimental. Its
protocol schema, generated from the installed binary with
`codex app-server generate-json-schema`, has `thread/start`, `thread/resume`,
`turn/start` (with per-turn `effort`, `sandboxPolicy` and `approvalPolicy`) and
`turn/interrupt`, and notifies `item/agentMessage/delta` and `thread/tokenUsage/updated`.
A per-turn app-server (spawn, resume the thread, run one turn, exit) would be a stateless
codex turn that streams. It is the follow-up.

### 4.4 Security scope

| Required on a conversation turn                                                                                                                                  | Optional                                                                                                 | Forbidden                                                                                                                                                                                                                             |
| ---------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| The sandbox on every turn (`--sandbox read-only` first, `-c sandbox_mode="read-only"` on resume); `--ask-for-approval never` before `exec`; the message on stdin | `--ignore-user-config`, so `config.toml` cannot set the sandbox; `--skip-git-repo-check` outside a clone | `--dangerously-bypass-approvals-and-sandbox`; `--sandbox danger-full-access` or `-c sandbox_mode="danger-full-access"`; `--approve-for-me`; `--dangerously-bypass-hook-trust`; `--ephemeral`, which stops the session being resumable |

The pipeline builder must not be reused: `resolveCodexSandboxMode` returns full access, and
`codexSandboxFlags` the bypass flag, for an empty tool list or any tool implying a shell.

**Verdict: defer.** Two independent reasons: no reply was observed, and the stateless
`exec` path fails the streaming bar. No bypass is needed, so this is not a `skip`.

## 5. `opencode` — defer

### 5.1 The invocation (observed)

```bash
# Private HOME and XDG roots; OPENCODE_CONFIG_CONTENT carries the provider and the
# permission map (edit, bash, webfetch, external_directory: deny).
opencode run --format json --print-logs --log-level ERROR -m <provider/model> \
  --dir <workdir>                                        # message on stdin
opencode run --format json --print-logs --log-level ERROR -s <sessionID> \
  -m <provider/model> --dir <workdir>                    # later turns, message on stdin
```

### 5.2 What was observed

- **Multi-turn works.** Turn 1 answered `noted`, turn 2 (`-s <sessionID>`, message on
  argv) answered `plum`, turn 3 (message on stdin) answered `plum`, all in the one
  session the first turn's events named.
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

- **Effort.** `--variant high` against a model that declares no variants exited 0 with
  no warning: silently ignored. The adapter already emits `--variant` only for a declared
  variant (#1643), which is the right gate.
- **Usage.** `step_finish` carries `tokens` (input, output, reasoning, cache read and
  write) and `cost`, which is 0 for a local model.

### 5.3 Security scope

- **Required:** the per-run isolation of ADR-022 § 8 (a private `HOME`, private XDG roots
  and the run's own `OPENCODE_CONFIG_CONTENT`); a permission map that denies `edit`,
  `bash`, `webfetch` and `external_directory`; the message on stdin, which ADR-022 § 19
  already requires because a positional message beginning `--auto` would switch on
  auto-approval; `-s <sessionID>` on every later turn.
- **Optional:** `--variant` for a variant the model declares; `--print-logs --log-level
ERROR`, as the pipeline adapter passes them.
- **Forbidden:** `--auto`; an `allow` wildcard in the permission map; `--share`; and a
  `serve` process shared across conversations, since spike #1650 showed an attached run
  takes the server's config, permission map and session database rather than its own.

opencode has no bypass flag in the sense the issue names; `--auto` is its equivalent, and
nothing in the working invocation needs it. The adapter is also gated behind
`NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1`.

**Verdict: defer.** It fails the streaming bar on the invocation that works. A `serve`
process started per turn, with that turn's config, would keep the isolation #1650 found
missing from a shared server and stream deltas over `/event`, at the 1.2 s boot cost
#1650 measured. That is the follow-up.

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
  installed help is what would run.
- `--output-format streaming-json` emits one ACP session update per line;
  `streaming-messages-json` with `--include-partial-messages` emits text deltas.
- `--reasoning-effort` (alias `--effort`); `--permission-mode` with `default`,
  `acceptEdits`, `auto`, `dontAsk`, `bypassPermissions` and `plan`; `--sandbox`;
  `--tools`; `--disable-web-search`; `--always-approve`.

The repository's real `streaming-json` capture from an earlier 1.0.x CLI
(`internal/execution/testdata/grok_stream_real_capture.jsonl`) shows text arriving in
word-sized `text` events and a terminal `end` event carrying `sessionId`, `usage` and
`total_cost_usd`. Streaming and the handle are therefore likely; resume is not observed
on any version.

Candidate: `grok --output-format streaming-json --session-id <uuid> --permission-mode
dontAsk --tools <list> --disable-web-search --no-auto-update --prompt-file <file> -m
<model> --reasoning-effort <rung>`, then `--resume <uuid>` in place of `--session-id`.

- **Required:** `--permission-mode dontAsk` (or `plan`); an explicit `--tools` list; the
  message in `--prompt-file`, never on argv; `--resume` on every later turn.
- **Optional:** `--sandbox <profile>`, whose profiles this spike could not inspect;
  `--disable-web-search` unless #1570 allows the web; `--max-turns`.
- **Forbidden:** `--always-approve`, which the pipeline builder passes;
  `--permission-mode bypassPermissions`, `acceptEdits` or `auto`.

**Verdict: defer** until an authenticated probe.

## 7. `gemini` and `gemini-sdk` — defer (unverified)

Not installed:

```text
$ which gemini
gemini not found
```

The vendor's documentation (§ 15) describes a candidate:
`gemini -r <session-uuid> --output-format stream-json --approval-mode plan -m <model>`
with the message on stdin, where `stream-json` emits an `init` event carrying the session
id and `message` events carrying "user and assistant message chunks", and `plan` is a
read-only approval mode. There is no per-invocation effort control; a thinking budget
lives only in settings (`thinkingConfig.thinkingBudget`), so effort would be reported as
not applied.

- **Required:** `--approval-mode plan`; the message on stdin; `-r <session-uuid>` on every
  later turn.
- **Optional:** `--sandbox`.
- **Forbidden:** `--yolo` (`-y`); `--approval-mode yolo` or `auto_edit`; the message on
  argv, which is where both Go builders put the prompt today.

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
would be reported as not applied. `docs/ADAPTER_MATRIX.md` says the CLI emits no JSON; the
current documentation says otherwise, which the re-probe should settle.

Candidate, to be established: `copilot --resume=<id> --output-format json
--available-tools <read-only list> --no-ask-user`, with the message on stdin as the
pipeline adapter already sends it. If a turn runs non-interactively only as
`-p <message>`, the message is on argv, which a conversation path forbids, and copilot
would be recorded `skip` unless the CLI offers another channel for it.

- **Required:** `--available-tools` with a read-only list; `--no-ask-user`; the message
  off argv, which the documented `-p PROMPT` form does not offer and the re-probe must
  establish; `--resume=<id>` on every later turn.
- **Optional:** `--deny-tool` for defence in depth.
- **Forbidden:** `--allow-all` and its alias `--yolo`; `--allow-all-tools`, which the
  pipeline builder passes.

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

| Adapter           | Multi-turn                          | Streaming within a reply               | Effort control                              | Bypass needed | Verdict |
| ----------------- | ----------------------------------- | -------------------------------------- | ------------------------------------------- | ------------- | ------- |
| `claude-headless` | observed: `--session-id`/`--resume` | observed: `text_delta` events          | `--effort`, dropped silently if unsupported | no            | adopt   |
| `claude-sdk`      | same binary as above                | same                                   | same                                        | no            | adopt   |
| `codex`           | handle observed; reply refused      | none on `exec --json`; app-server only | `-c model_reasoning_effort`, per turn       | no            | defer   |
| `opencode`        | observed: `-s <sessionID>`          | none: one event per reply              | `--variant`, declared variants only         | no            | defer   |
| `grok`            | unverified (not authenticated)      | captured on an earlier version         | `--reasoning-effort`                        | no (help)     | defer   |
| `gemini`          | unverified (not installed)          | documented                             | none per invocation                         | no (docs)     | defer   |
| `gemini-sdk`      | follows `gemini`                    | follows `gemini`                       | follows `gemini`                            | no (docs)     | defer   |
| `copilot`         | unverified (not installed)          | documented JSONL, granularity unknown  | none                                        | no (docs)     | defer   |
| `ollama`          | retired (#2128)                     | —                                      | —                                           | —             | skip    |
| `lm-studio`       | retired (#2128)                     | —                                      | —                                           | —             | skip    |

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

**When an adapter has no effort concept, the mapping degrades explicitly.** The turn runs
with no effort flag, and its terminal frame says the effort was not applied and why: the
adapter has no effort control, or the model declares no effort axis. It never reports
the requested rung as the one used. For a rung the model does not declare, a turn fails
as a stage does (`assertEffortSupported`): the pipeline never silently downgrades an
effort, and a conversation must not either. Because codex drops the effort on resume
and claude drops an unsupported one silently, both rules are enforced on the Nightgauge
side, on every turn, and not delegated to the CLI. The same holds for the model envelope
on `opencode`: the turn runs on the operator's pinned `provider/model` and reports the
envelope as not applied.

## 12. Stateful or stateless

| Measurement                                                    | Held process per conversation                                                                               | Process per turn, resumed by handle |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- | ----------------------------------- |
| Resident memory while idle, Claude (three runs)                | **218–226 MiB**, one process                                                                                | 0; the transcript is on disk        |
| Resident memory while idle, `codex app-server` with one thread | 163 MiB for `codex` itself; 261–264 MiB with the helper processes this machine's Codex configuration starts | 0                                   |
| Resident memory while idle, `opencode serve` with one session  | 633 MiB                                                                                                     | 0                                   |
| Claude turn 2, first text delta                                | 0.86 s, 0.95 s, 0.87 s                                                                                      | 7.63 s, 2.63 s, 2.73 s              |
| Survives a daemon restart                                      | no: the context is in the held process                                                                      | yes                                 |
| Cancel one turn                                                | must interrupt in-protocol, or kill the conversation                                                        | kill the turn's process group       |

Ten open conversations held on Claude cost about 2.2 GiB on a developer laptop, and ten
on `opencode serve` about 6.2 GiB, whether or not anyone is typing.

**Decision: stateless.** Each turn is a new process group, spawned with the existing
`Setpgid` discipline, carrying the context by the CLI's own session handle. The handle is
recorded per conversation together with the adapter and the working directory, and a
turn resumes it only when both match; otherwise the turn starts a new session seeded from
the conversation's transcript. That is the rule the opencode retry path already applies
to a recorded session (`resolveResumeSessionID`, #1643). The reasons:

1. Idle conversations cost nothing; cost scales with turns, not with open threads.
2. A turn is a process group, so cancelling it is the kill-and-verify #1569 already
   requires; the probe in § 3.2 showed the session resumable afterwards.
3. The context survives a daemon restart, because it is the CLI's own transcript.
4. Model, effort and permission posture are resolved and passed on every turn. Codex
   inherits neither effort nor sandbox on resume (§ 4.2), so each turn has to carry them
   anyway; a held process would fix them at spawn.

The cost is about 1.8 s more per turn on Claude, in the typical case. If a surface later
measures that as the problem, a short-lived warm process can be revisited behind the same
turn contract.

## 13. The viability bar

An adapter enters `conversationViableAdapters`, and so advertises `conversation`, only
when every condition below holds, each pinned by a test or a recorded fixture in this
repository:

1. **Verdict.** This record, or a later amendment to it, gives the adapter
   `action: adopt`.
2. **Builder.** #1569's runner dispatches the adapter through a conversational turn
   builder, and an argv contract test enumerates that builder's output for a first turn
   and a resumed turn and asserts that no flag on the adapter's forbidden list appears,
   that the permission or sandbox posture is explicit on both, and that the message is
   never on argv.
3. **Multi-turn.** A recorded two-turn capture from a pinned CLI version shows turn 2, a
   new process given only the handle from turn 1, stating a fact only turn 1 contained.
4. **Streaming.** A recorded capture of a reply of at least 50 words carries at least two
   assistant-text events before the terminal event.
5. **Accounting.** The terminal event yields the session handle and the turn's own token
   usage; a cumulative-only figure is differenced against the previous turn.
6. **Cancel.** `SIGTERM` to the turn's process group leaves `kill -0` on the group failing
   within 10 s, and the next turn resumes the same handle.
7. **Effort.** The builder applies the resolved effort through the adapter's native
   control only for a rung the registry declares for the model, and otherwise reports the
   effort as not applied.

An adapter whose only working conversational invocation needs a forbidden flag is
recorded `skip`, not `adopt`. Until #1569's runner exists, condition 2 cannot hold for
any adapter, so `ConversationViable` keeps answering `false` for all eight. #1569 makes
the advertised set and the dispatchable set the same set by construction:
`ConversationViable(a)` is true exactly when `a` has a registered conversational builder
that passes conditions 2 to 7.

## 14. Recommendation for #1569

- **Run turns stateless** (§ 12), for `claude-headless` and `claude-sdk` only.
- **Build a conversational argv per adopted adapter** (§ 3.1); never reuse
  `BuildCommand`, whose Claude form passes `--no-session-persistence` and whose codex,
  grok, copilot and gemini forms carry a bypass-class flag or the prompt on argv.
- **Read the stream with the existing Claude `stream-json` parser and token accumulator.**
  Emit assistant text from the `text_delta` events, take the turn's tokens from
  `result.usage`, and never meter `total_cost_usd` directly.
- **Enforce the floor in the builder:** `--restricted`, `--permission-mode dontAsk`, an
  explicit `--tools` list, `--strict-mcp-config`, and a forbidden-flag test over every
  argv the builder can produce, whatever the workspace's auto-accept configuration.
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
  `docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/overview` and
  `docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/work-with-multiple-sessions`.
- Grok CLI headless scripting: `docs.x.ai/build/cli/headless-scripting`.

This repository: `internal/execution/adapters/` (the eight builders and the registry),
`internal/config/retired_adapters.go`, `internal/executionprofile/profile.go`,
`internal/intelligence/routing/performance_mode.go`, the real stream captures under
`internal/execution/testdata/`, and `packages/nightgauge-vscode/src/utils/skillRunner.ts`.

## Recommendations

```yaml recommendations
spike: 1568
recommendations:
  - id: claude-headless-conversation-turn
    action: adopt
    title: "conversation: Claude turn builder — resume by session id, restricted, streamed, per-turn usage"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:conversation"]
    body: |
      The adapter half of #1569 for `claude-headless`, as spike #1568 recorded it
      (docs/spikes/1568-conversational-agent-sessions-across-adapters.md, § 3 and § 13).

      Build the conversational argv beside the pipeline's `BuildCommand`, never by
      reusing it: the pipeline builder passes `--no-session-persistence`, after which
      `--resume` fails with "No conversation found with session ID".

      - First turn: `claude -p --session-id <uuid> --model <m> [--effort <e>]
        --output-format stream-json --verbose --include-partial-messages --restricted
        --permission-mode dontAsk --tools <list> --strict-mcp-config`, the message on
        stdin. Later turns replace `--session-id` with `--resume <uuid>`; a retried first
        turn resumes, because a handle cannot be minted twice.
      - `--effort` only for a rung the launched model's `supported_efforts` holds;
        otherwise no flag, and the turn reports effort as not applied.
      - Tokens from `result.usage`. `total_cost_usd` is cumulative across a resumed
        session, so cost is computed from usage or differenced.
      - A table-driven argv test proves no output can carry
        `--dangerously-skip-permissions`, `--allow-dangerously-skip-permissions`,
        `bypassPermissions`, `acceptEdits`, `auto`, `--no-session-persistence` or the
        message itself.
      - Recorded fixtures for the viability bar: a two-turn capture, a capture of a reply
        of 50 words or more with at least two text deltas, and a cancel-then-resume run.
    depends_on: []
  - id: claude-sdk-conversation-turn
    action: adopt
    title: "conversation: claude-sdk reuses the Claude turn builder with its API-key credential"
    type: feature
    priority: medium
    size: XS
    labels: ["component:go-binary", "program:conversation"]
    body: |
      `claude-sdk` spawns the same `claude -p` as `claude-headless`; spike #1568 (§ 3.7)
      adopts it on the same transcript. Register it with the Claude turn builder, passing
      `ANTHROPIC_API_KEY` through as its pipeline adapter does, and run the two-turn
      fixture once with an API key: the spike could not, because none was set.
    depends_on: ["claude-headless-conversation-turn"]
  - id: codex-conversation-reprobe
    action: defer
    title: "conversation: re-probe codex with quota — a per-turn app-server for deltas, read-only sandbox on every turn"
    type: spike
    priority: medium
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      Spike #1568 (§ 4) could not observe a codex reply: the account's usage limit
      refused every turn. Independently, `codex exec --json` delivers an agent message
      whole, so the stateless `exec` path cannot stream within a reply.

      Re-probe against the viability bar (§ 13) with an account that has quota:
      a per-turn `codex app-server` that resumes the thread (`thread/resume`), runs one
      `turn/start` with `effort` and a read-only `sandboxPolicy`, relays
      `item/agentMessage/delta`, and exits. Record the two-turn capture.

      Already established: `exec resume` refuses `--sandbox` but honours
      `-c sandbox_mode=...`; a resumed turn inherits neither the session's sandbox nor its
      effort, so both are passed on every turn. Never reuse the pipeline's `BuildCommand`:
      it falls back to `--dangerously-bypass-approvals-and-sandbox`.
    depends_on: []
  - id: opencode-conversation-streaming
    action: defer
    title: "conversation: opencode streams a reply only whole — prove a per-turn serve streaming /event deltas"
    type: spike
    priority: medium
    size: S
    labels: ["component:go-binary", "program:conversation", "program:opencode"]
    body: |
      Spike #1568 (§ 5) observed opencode resume a session across three turns
      (`run -s <sessionID>`), but `run --format json` delivered a 58-word reply as one
      event, so it fails the streaming bar.

      Probe a `serve` process started per turn, with that turn's own private roots, config
      and permission map: spike #1650's isolation finding concerns a server shared across
      runs, which this is not. Measure delta granularity on `/event`, the per-turn boot
      cost, and that the server's process group is dead after the turn. Keep the message
      on stdin, or in the request body, and never pass `--auto`.
    depends_on: []
  - id: grok-conversation-reprobe
    action: defer
    title: "conversation: probe grok with an authenticated CLI — resume, streaming chunks, dontAsk"
    type: spike
    priority: low
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      grok 1.0.25 was installed but not authenticated, so spike #1568 (§ 6) observed no
      turn. Probe the candidate `grok --output-format streaming-json --session-id <uuid>
      --permission-mode dontAsk --tools <list> --disable-web-search --no-auto-update
      --prompt-file <file>` and its `--resume <uuid>` follow-up against the viability bar
      (§ 13). Settle whether `--session-id` resumes: the installed help says it does not,
      the vendor's headless page says it does. The pipeline builder's
      `--always-approve` is forbidden on this path.
    depends_on: []
  - id: gemini-conversation-reprobe
    action: defer
    title: "conversation: probe gemini with the CLI installed — resume by session id, stream-json chunks, plan mode"
    type: spike
    priority: low
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      The gemini CLI was not installed, so spike #1568 (§ 7) recorded only what its
      documentation describes: `-r <session-uuid>` in headless mode, `stream-json`
      message chunks, and the read-only `--approval-mode plan`. Probe it against the
      viability bar (§ 13) with the message on stdin: both Go builders put the prompt on
      argv, which a conversation must not. Effort has no per-invocation control and is
      reported as not applied.
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
    title: "conversation: probe copilot with the CLI installed — resume by id with -p, JSONL granularity"
    type: spike
    priority: low
    size: S
    labels: ["component:go-binary", "program:conversation"]
    body: |
      The copilot CLI was not installed, so spike #1568 (§ 8) recorded only what its
      documentation describes: `--resume=SESSION-ID`, `--output-format json` (JSONL),
      `--available-tools` and `--deny-tool`. Establish whether a resumed turn runs
      non-interactively with the message off argv, how finely the JSONL streams a reply,
      and whether `docs/ADAPTER_MATRIX.md`'s "no JSON output" still holds. The pipeline
      builder's `--allow-all-tools` is forbidden on this path, as are `--allow-all` and
      `--yolo`; there is no effort control, so effort is reported as not applied.
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
    title: "Overall, for #1569: stateless resume-by-id turns on claude-headless and claude-sdk (recorded here; #1569 is the issue)"
    type: feature
    priority: high
    size: L
    labels: ["component:go-binary", "program:conversation"]
    body: |
      Recorded with `action: skip` only so the materializer files no duplicate: #1569
      already tracks the runner. The recommendation (spike #1568, § 12 to § 14): run each
      turn as a new process group that resumes the CLI's own session by a handle the
      daemon records with the adapter and working directory; ship the adopted Claude
      adapters only; build a conversational argv per adapter instead of reusing
      `BuildCommand`; map the performance-mode envelope on every turn and report what
      was not applied; and keep `ConversationViable` false until the runner serves a turn.
    depends_on: ["claude-headless-conversation-turn", "claude-sdk-conversation-turn"]
```
