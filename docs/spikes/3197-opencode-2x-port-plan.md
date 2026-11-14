# Spike #3197: OpenCode 2.x run contract and the adapter port plan

**Issue**: #3197
**Status**: Complete
**Date**: 2026-11-14
**Prerequisite ADR**: [ADR-022](../decisions/022-opencode-multi-provider-adapter.md)
**Observed against**: opencode **2.2.7** (`opencode --version` prints `opencode v2.2.7`).
The adapter, its compat manifest and ADR-022 were observed against 1.18.30.

## Spike Contract (Path A)

Path A: the recommendations below become follow-up issues after this spike's PR merges.
No port issue was filed up front.

**Artifact**: `docs/spikes/3197-opencode-2x-port-plan.md`

## Executive Summary

**The adapter cannot dispatch a single stage on 2.x today, and three of its safety
controls fail open there.** Each finding below comes from running 2.2.7 in a throwaway
directory, with a loopback stub model and no credential (§ 7).

- **Dispatch breaks first.** `run --dir` is gone. `opencode run --dir … hi` exits 1
  with `Unrecognized flag: --dir`. A positional directory does not replace it: `run`
  treats every positional argument as message text, so the path is prepended to the
  prompt. The working directory is the process's cwd.
- **A run now opens a listener.** Every `run`, with `--standalone` or without, spawns
  a private `opencode serve --stdio --port 0`. That child holds a loopback TCP
  listener on an ephemeral port for the whole run. On 1.x a plain `run` opened no
  listener (#1650 § 6). The child exits with a clean run. When the client is killed,
  the child was still alive at the next process listing.
- **The config still loads, but some of it is dropped.** 2.x accepts the 1.x keys
  the adapter writes and translates them into a new schema (`providers`, `agents`,
  `permissions`, `policies`, `plugins`). It drops three things without a word:
  `subagent_depth`, an empty `mode`, and a value of the wrong type. A subagent is
  then given the `subagent` tool again. `debug config` no longer prints the merged
  config, and it never exits non-zero on bad input. So no probe can catch the loss.
- **The plugin no longer loads, and the run continues anyway.** 2.x requires a
  plugin to be a directory. Its default export must be `{ id, setup }`, and hooks
  register through `ctx.tool.hook(…)`. A 1.x plugin fails to load with only a `WARN`
  in the log, and the run proceeds with every Nightgauge gate absent. Once ported,
  the gate is stronger than on 1.x: `execute.before` is **awaited**, a throw
  **blocks** the tool and reaches the model, and the hook fires inside subagent
  sessions too (§ 4).
- **The event stream is mostly unchanged, but a failing provider no longer ends the
  run.** A provider 500 or a refused connection produces `retry` events with growing
  back-off, and the run had not ended after 25 s. An unknown model ends with an
  `error` event and exit 1. Tool names changed: `bash` is now `shell`, `task` is now
  `subagent`, and `edit` and `write` take `path` instead of `filePath`.

Of the ADR-022 § 20 behaviours, the six Context-table rows and the version-policy
checks give **9 re-observed, 5 changed and 2 not observable** (§ 5). The port splits
into nine recommendations, each `M` or smaller. The version gate and the stub
scripts come first and depend on nothing. The ADR-022 amendment comes last.

## 1. Dispatch

### `run` has no directory flag

```text
$ opencode run --dir <scratch>/work --format json hi
ERROR
  Unrecognized flag: --dir in command opencode run
exit=1
```

`opencode run --help` (2.2.7, printed on **stdout**, exit 0) lists:
`--continue`, `--session`, `--fork`, `--worktree [=name]`, `--model provider/model#variant`,
`--agent`, `--format default|json`, `--file`, `--title`, `--archived`, `--thinking`,
`--auto`, plus the global `--log-level`, `--print-logs`, `--standalone` and `--server`.
`--print-logs` and `--log-level` survive, but `--print-logs` now notes "server logs
require --standalone".

### A positional directory becomes part of the prompt

With `opencode run --standalone --format json -m stub/stub-model <scratch>/w2` and the
prompt on stdin, the stub received this first user message:

```text
<scratch>/w2
probe prompt
```

The help says why: `opencode run [flags] [<message...>]`. Only the top-level
interactive command takes `[<directory>]`. So the adapter must set the child's working
directory, and it must never pass a path as an argument.

### Confinement follows the working directory and the permission map

From `cwd=<scratch>/w2`, a stub-driven `write` of `probe.txt` landed in
`<scratch>/w2/probe.txt`. A `write` of the absolute path `<scratch>/outside.txt` behaved
as follows:

| Permission map                                    | Result                                                                      |
| ------------------------------------------------- | --------------------------------------------------------------------------- |
| `{"*":"allow"}`                                   | `Created file successfully: <scratch>/outside.txt` (file exists)            |
| `{"*":"allow","external_directory":{"*":"deny"}}` | `status: error`, `Permission denied: external_directory` (no file)          |
| `{"*":"allow","external_directory":{"*":"ask"}}`  | `status: error`, "This non-interactive run cannot ask the user …" (no file) |

So 2.x does not confine a run to its directory. The `external_directory` rule still
does, as on 1.x.

### `--standalone`, the background service and the private server

`opencode --help`: "`--standalone` Run with a private server instead of the background
service; `--server` Connect to a server URL instead of the background service". The
`service` subcommand starts, stops and configures a persistent background server.

- **`run` spawns a private server whether or not `--standalone` is passed.** That
  holds in an isolated `HOME` with XDG roots. During a run with a 6 s first-token
  delay:

  ```text
  --- during run: processes
  <pid> opencode run --standalone --format json -m stub/stub-model
  <pid> <opencode> serve --stdio --port 0
  --- during run: listeners
  opencode  <pid>  …  TCP 127.0.0.1:<ephemeral> (LISTEN)
  --- after run (0.2s)
  (none)
  ```

  Without `--standalone`, the isolated run also spawned `serve --stdio --port 0`. It
  did not attach to a background service already running on the machine under
  another `HOME`. With the operator's real `HOME`, `run` attaching to their background
  service was **not observed**, because the spike must not touch operator state. The
  adapter should pass `--standalone` explicitly so the choice never depends on state
  it does not own.

- **The private server can outlive a killed client.** When the 20–25 s probe timeout
  killed `run` with SIGALRM, the process listing taken right afterwards still showed
  `<opencode> serve --stdio --port 0`. It was gone a second later. The adapter must
  kill the whole process group, not only the `run` PID.
- **A killed run disturbs the terminal.** Killing `run` under a tty discarded the
  calling shell's captured output. Probes after that were run detached (`setsid`, no
  tty). The adapter already spawns without a tty, so this is only a probing hazard.

### `debug config` without `--standalone` reaches for the managed service

```text
$ opencode debug config            # isolated HOME / XDG, OPENCODE_DISABLE_PROJECT_CONFIG=1
Error: Server process exited with code 1
Error: Managed service port 49374 on 127.0.0.1 is already in use by PID <pid>
  (<home>/.opencode/bin/opencode serve --service). Configure another port with
  `opencode service set port <port>` and start the service again.
exit=1
```

This is why `debug config` "exits 1 or hangs" in an isolated environment. The command
tries to start a background service on the fixed default port, and that port is held
by any other service on the machine. `opencode debug config --standalone` exits 0.

## 2. Configuration

### 2.x translates the 1.x keys

`OPENCODE_CONFIG_CONTENT` with the keys `opencode_config.go` emits, read back with
`opencode debug config --standalone`, shows the document source's `info`:

```json
{
  "model": { "providerID": "stub", "model": "x" },
  "default_agent": "build",
  "update": "disable",
  "share": "disabled",
  "permissions": [{ "action": "shell", "resource": "*", "effect": "ask" }],
  "policies": [
    { "action": "provider.use", "resource": "*", "effect": "deny" },
    { "action": "provider.use", "resource": "stub", "effect": "allow" }
  ],
  "agents": {
    "build": { "model": { "providerID": "stub", "model": "x" }, "steps": 5 },
    "title": { "model": { "providerID": "stub", "model": "y" } }
  },
  "snapshots": false,
  "formatter": false,
  "lsp": false,
  "tool_output": { "max_lines": 10 },
  "mcp": {},
  "compaction": { "auto": false },
  "instructions": ["AGENTS.md"],
  "plugins": ["file:///nonexistent/p.js"],
  "providers": {
    "stub": {
      "env": [],
      "package": "aisdk:@ai-sdk/openai-compatible",
      "settings": { "apiKey": "***", "baseURL": "http://127.0.0.1:9/v1" },
      "models": { "x": { "modelID": "x" } }
    }
  }
}
```

| 1.x key (emitted today)                                                         | 2.x form                                              | Note                                                |
| ------------------------------------------------------------------------------- | ----------------------------------------------------- | --------------------------------------------------- |
| `model`, `agent.*.model`                                                        | `{providerID, model}` objects                         | translated                                          |
| `small_model`                                                                   | `agents.title.model`                                  | translated                                          |
| `agent`                                                                         | `agents`                                              | translated, `steps` kept                            |
| `mode` (empty)                                                                  | —                                                     | dropped                                             |
| `subagent_depth: 1`                                                             | —                                                     | **dropped**, see below                              |
| `enabled_providers`                                                             | `policies` `provider.use` deny `*`, allow each listed | translated                                          |
| `provider.*.npm`                                                                | `providers.*.package: "aisdk:<npm>"`                  | translated                                          |
| `provider.*.options`                                                            | `providers.*.settings`                                | translated, `apiKey` masked in output               |
| `permission` map                                                                | `permissions` list of `{action, resource, effect}`    | translated in key order, tools renamed (§ 3)        |
| `plugin`                                                                        | `plugins`                                             | translated, but the entry must be a directory (§ 4) |
| `autoupdate: false`                                                             | `update: "disable"`                                   | translated                                          |
| `snapshot`                                                                      | `snapshots`                                           | translated                                          |
| `share`, `lsp`, `formatter`, `compaction`, `tool_output`, `instructions`, `mcp` | same names                                            | kept                                                |

The permission-map translation renames `task` to `subagent` and `bash` to `shell`, and
keeps glob patterns in order:

```json
{
  "permissions": [
    { "action": "*", "resource": "*", "effect": "allow" },
    { "action": "external_directory", "resource": "*", "effect": "deny" },
    { "action": "edit", "resource": "*", "effect": "deny" },
    { "action": "edit", "resource": "src/**", "effect": "allow" },
    { "action": "subagent", "resource": "*", "effect": "deny" },
    { "action": "todowrite", "resource": "*", "effect": "deny" },
    { "action": "list", "resource": "*", "effect": "allow" },
    { "action": "doom_loop", "resource": "*", "effect": "deny" },
    { "action": "webfetch", "resource": "*", "effect": "deny" }
  ]
}
```

`todowrite` and `list` pass through as actions. 2.x has no tool by either name (§ 3).

### What is lost without a word

- **`subagent_depth`.** The 1.x key that stops a subagent from starting another
  (`openCodeSubagentDepth = 1`) is dropped. In the subagent probe, the subagent's own
  request offered the full tool list, `subagent` included:
  `["edit","execute","glob","grep","question","read","shell","skill","subagent","webfetch","websearch","write"]`.
  No 2.x equivalent was found in the translated output. The bound has to come from a
  per-agent `subagent` deny, or from the plugin gate (§ 4).
- **A value of the wrong type.** `{"model":5}` gives `exit=0` and an empty document.
  1.x exited 1 (ADR-022 § 20). An unknown key (`nightgauge_unknown`) is also dropped
  with exit 0, as on 1.x.
- **`debug config` no longer shows the merged config.** It lists _configuration
  sources_ (`directory` and `document` entries, each with its own `info`). So it
  cannot confirm the effective value of a key when several layers set it.

### Selecting an isolated config

`opencode debug paths` follows the XDG variables in full: `home`, `data`, `cache`,
`config` and `state` sit under the throwaway roots, `tmp` is `$TMPDIR/opencode`, and the
database is `<data>/opencode/opencode.db`. `OPENCODE_CONFIG_CONTENT` and
`OPENCODE_CONFIG_DIR` both still load (§ 5, rows 1 and 6).

## 3. Output: the `--format json` event stream

### Event types and tool shape

A completed two-step stub run printed only JSON lines on stdout:

```text
step_start  tool_use  step_finish  step_start  text  step_finish
```

`step_finish.part` carries `reason` (`tool-calls` or `stop`), `cost: 0` and
`tokens {input, output, reasoning, cache {read, write}}`, the same shape as 1.x
(ADR-022 § 3). `tool_use.part.state` carries `status`, `input` and `output` or
`error`. A new `metadata.providerCall` / `providerResult` pair records whether the tool
ran.

The tool names offered to the model on 2.x are `edit`, `execute`, `glob`, `grep`,
`question`, `read`, `shell`, `skill`, `subagent`, `webfetch`, `websearch` and `write`.
`bash` is now `shell` and `task` is now `subagent`. `list` and `todowrite` are gone,
and `execute` and `question` are new. The repository's stub script `tool-edit-stop`,
which sends `filePath`, fails on 2.x:

```text
Invalid arguments for tool "edit":
- path: Missing key
```

`subagent` takes `agent`, `description` and `prompt`. Its result is
`<subagent sessionID="ses_…" state="completed">…</subagent>`. Subagent steps do not
appear on the parent's stdout stream; only the parent's `subagent` `tool_use` does.

### Rejected permission request

With `"bash":"ask"`, which 2.x translates to `shell`, the `shell` call ended
`status: error`, output "This non-interactive run cannot ask the user for permission,
so the request was rejected. Continue without this action." The run continued and
exited 0. Even without `--print-logs`, stderr received one ANSI-coloured line:
`! permission requested: shell (echo hi > shell.txt); auto-rejecting`. With
`"bash":"deny"` the tool is removed from the model's list: "No tool named "shell" is
currently available."

### Run end, exit code and provider failures

| Case                                    | Stream                                                                                                                             | Exit                                   |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------- |
| Normal end                              | last event `step_finish` with `reason: stop`                                                                                       | 0                                      |
| Unknown model (`-m stub/nope`)          | one `{"type":"error","error":{"type":"provider.no-route","message":"Model unavailable: stub/nope"}}`                               | 1                                      |
| Provider HTTP 500 (stub `error` script) | `step_start`, then `retry` events: `attempt` 2…6, `nextRetryAt` back-off ~2 s, 3 s, 9 s, 8 s, 9 s; `error.type: provider.internal` | none: killed by the 25 s timeout (142) |
| Endpoint refusing connections           | `retry` events, `error.type: provider.transport`, message `ECONNREFUSED: Unable to connect…`                                       | none: killed by the 25 s timeout (142) |

Two changes matter here. A provider error no longer ends the run: the run retries
until something outside it stops it. And the `provider.transport` error carried no URL
(`grep -c 127.0.0.1:9` on the stream: 0), where ADR-022 § Endpoints observed the full
URL on 1.x.

## 4. Plugin API

### Loading

| Plugin form                                                      | Observed (log line in `<data>/opencode/log/opencode.log`)                                                              |
| ---------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `file:///…/probe.js` (a file, the 1.x form)                      | `WARN … message="configured plugin path must be a directory"`                                                          |
| directory, `export default async (ctx) => ({hooks})` (1.x shape) | `WARN … "failed to load plugin" … Plugin must export a default definition with an id and an effect or setup function.` |
| directory, `export default { id, setup: async (ctx) => {…} }`    | `INFO msg="loading plugin"`, and `setup` ran                                                                           |
| missing path                                                     | `WARN … "failed to load plugin" … ENOENT`                                                                              |

In every failure case the run carried on and exited 0. **A plugin that does not load
fails open.** Nightgauge's gates live in the plugin (ADR-022 "Nightgauge OpenCode
plugin"), so a dispatch must prove the plugin loaded before it trusts the run.

`setup` receives a context object of services: `app, location, options, agent, aisdk,
command, credential, event, experimental, generate, model, provider, integration, mcp,
permission, plugin, reference, rpc, skill, storage, tool, vcs, websearch, worktree,
session, shell`. Returning a 1.x hook map from `setup` registers nothing: none of
`tool.execute.before`, `tool.execute.after`, `permission.ask` or `event` fired. Hooks
register by calling `ctx.tool.hook(name, fn)`, `ctx.permission.hook(name, fn)`,
`ctx.session.hook(…)`, `ctx.shell.hook(name, fn)` and `ctx.event.subscribe(fn)`.

A plugin of this shape, with no package dependency, loaded and ran. The vendored
`@opencode-ai/plugin` 1.18.30 tarball is not needed to load one. On npm, the
`latest` dist-tag of both `opencode-ai` and `@opencode-ai/plugin` is **1.19.5**, and
`npm view opencode-ai@2.2.7` returns 404. 2.x is not distributed under those package
names.

### `tool.execute.before` and `after`

`ctx.tool.hook("execute.before", fn)` and `ctx.tool.hook("execute.after", fn)` fired.
Of the other names tried, `before`, `after`, `execute`, `definition`, `call` and
`result` registered without error but never fired. So **registering an unknown hook
name is not an error**. None of the `ctx.permission.hook` names tried fired during an
`external_directory` ask: `ask`, `before`, `request`, `asked`, `reply` and `check`.
The permission hook's event names remain **not observed**.

`execute.before` receives `{tool, sessionID, agent, messageID, id, input}`.
`execute.after` adds `status` and `result` (or `error`). One probe exercised the
main session and a subagent session (`agent: general`). The before-hook slept
1500 ms, and it threw for any path containing `blocked`:

```text
t=59721 before subagent  session=…SzA agent=build
t=61222 beforeDone subagent
t=61246 before write     session=…dkdl agent=general path=sub.txt
t=62748 beforeDone write path=sub.txt
t=62751 after write      session=…dkdl status=completed
t=62772 after subagent   session=…SzA status=completed
t=62782 before write     session=…SzA agent=build path=blocked.txt
t=64284 beforeDone write path=blocked.txt
stream: write → status=error, output "nightgauge-probe: blocked by plugin"
```

- **The hook is awaited.** Each tool ran only after its before-hook's 1.5 s delay
  ended. On 1.18.30 OpenCode did not await hook promises (ADR-022, #1810).
- **A throw blocks the tool**, and the model sees the error message.
- **The hook fires in subagent sessions**, with that session's `sessionID` and
  `agent`.

### What the version gate should compare

Two independent things decide whether a dispatch works on 2.x. The binary decides the
argv, the config schema and the stream. The plugin definition shape decides whether
the gates exist at all. 2.x needs no plugin package, so there is no package version to
compare. Keep gating on the **binary version**, and make plugin load a runtime check
(the plugin proves it loaded) rather than a version comparison.

## 5. Safety controls: ADR-022 § 20 on 2.x

Status: **re-observed** means it holds on 2.2.7, **changed** means it differs, and **not
observable** means it could not be exercised here.

| ADR-022 § 20 behaviour (1.18.30)                                                                                                          | 2.2.7 status                                                          | Command and output                                                                                                                                                                                                                     |
| ----------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `opencode debug config` exits 1 on a wrong-type value in `OPENCODE_CONFIG_CONTENT`, 0 on an unknown key, and prints every other key set   | **changed**                                                           | `debug config --standalone`: `{"model":5}` gives exit 0 and an empty document. An unknown key gives exit 0, dropped. Output lists sources, not the merged config. Without `--standalone`: exit 1, managed-service port collision (§ 1) |
| `opencode models` lists a configured endpoint's model with the endpoint's server stopped                                                  | **re-observed**                                                       | `models --standalone` with a `stub` provider at `127.0.0.1:9`: `stub/stub-model`, exit 0                                                                                                                                               |
| `opencode run --help` prints its help on stderr and nothing on stdout                                                                     | **changed**                                                           | `run --help`: stdout 1999 bytes, stderr 0, exit 0                                                                                                                                                                                      |
| Outside a git repository, OpenCode reads `opencode.json` from the directories above the cwd; `OPENCODE_DISABLE_PROJECT_CONFIG=1` stops it | **re-observed**                                                       | `debug config --standalone` from `<scratch>/nest/a/b` (no git): without the switch, a `document` source `<scratch>/nest/opencode.json` (`model: stub/parent-marker`). With it, the config directory only                               |
| `opencode models` lists a hosted provider the config has no block for only when one of its variables is set, whatever the value           | **re-observed**                                                       | No variables: 12 models, all `opencode/*`. `GROQ_API_KEY=placeholder`: 19 (7 `groq/*`)                                                                                                                                                 |
| `OPENCODE_CONFIG_DIR` holding a hosted provider's `options.apiKey` loads it with none of its variables set                                | **re-observed**                                                       | `OPENCODE_CONFIG_DIR` with `{"provider":{"groq":{"options":{"apiKey":"placeholder"}}}}`: 19 models (7 `groq/*`). The model-entry half was not probed                                                                                   |
| Version string is readable for the floor check                                                                                            | **changed**                                                           | `opencode --version` prints `opencode v2.2.7` (1.x: bare `X.Y.Z`). Parsing this form is already accepted (#3172, `opencode_preflight.go`)                                                                                              |
| Floor 1.18.30, fail closed below it; max-tested information only                                                                          | **re-observed**                                                       | Policy, not binary behaviour: 2.2.7 is above the floor and passes it. `required_flags` in `internal/adaptercompat/manifests/opencode.json` still lists `--dir`, which 2.x rejects                                                      |
| Managed-install remediation `npm i … opencode-ai@<max-tested>`                                                                            | **changed**                                                           | `npm view opencode-ai dist-tags.latest` gives `1.19.5`; `npm view opencode-ai@2.2.7` gives 404. The remediation cannot install 2.x                                                                                                     |
| Every probe runs in a throwaway `HOME`/`TMPDIR`/XDG root with `OPENCODE_DISABLE_PROJECT_CONFIG=1` under a 20 s timeout                    | **re-observed**                                                       | `debug paths` resolves all roots inside the scratch directory. But a probe must add `--standalone` (§ 1), and the timeout must kill the process group                                                                                  |
| Canary: prompt delivered on stdin                                                                                                         | **re-observed**                                                       | `echo "probe prompt" \| opencode run …`: the stub's user message was `probe prompt`                                                                                                                                                    |
| Canary: `--format json` event types                                                                                                       | **re-observed**                                                       | `step_start`, `tool_use`, `text`, `step_finish`, `error`, with `step_finish.tokens` unchanged. New: `retry` (§ 3)                                                                                                                      |
| Canary: an `ask` is auto-rejected in headless `run`                                                                                       | **re-observed**                                                       | `shell` with `ask` gives `status: error`, "This non-interactive run cannot ask the user for permission …", and stderr `auto-rejecting`                                                                                                 |
| Canary: `run` opens no TCP listener                                                                                                       | **not observable** as stated, since the private server always listens | During `run --standalone`: `serve --stdio --port 0` holds `127.0.0.1:<ephemeral> (LISTEN)`. The 1.x behaviour cannot recur, so this row becomes "listens on loopback only, and the listener dies with the run"                         |
| Canary: `OPENCODE_DISABLE_PROJECT_CONFIG` switch                                                                                          | **re-observed**                                                       | As row 4                                                                                                                                                                                                                               |
| Drift record (`last-dispatch.json`) and the doctor's catalog probe through a real dispatch                                                | **not observable**                                                    | Needs a real dispatch, and dispatch is blocked by `--dir` (§ 1). Re-verify after `opencode2-dispatch`                                                                                                                                  |

## 6. Divergences from the issue's stated assumptions

| Assumption in #3197                                                          | Observed                                                                                                                                    |
| ---------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| 2.x config schema is `plugins`, `providers`, `agents`, `settings`, `package` | Confirmed as the translated shape. `settings` and `package` sit inside each `providers.*` entry, and `permissions` and `policies` are added |
| `debug config` exits 1 or hangs in an isolated environment                   | Exits 1 because it tries to start the managed service on a fixed port held by another service. `--standalone` fixes it                      |
| `--standalone` / `--server` choose how a run is served                       | Confirmed. In isolation, a run without `--standalone` also used a private server                                                            |
| The adapter's vendored plugin package matters for 2.x                        | No: a dependency-free plugin loads. The default-export shape and hook registration are what changed                                         |

## 7. Method

- Binary: `opencode v2.2.7`, installed outside npm. Stub: `go build ./cmd/stub-provider`
  for the `error` and `slow` scripts. Its embedded scripts use 1.x tool names and
  arguments (§ 3), so tool-call probes used a throwaway Python OpenAI-compatible
  streaming stub in the scratch directory. That stub answers title-generation
  requests (which carry no tools) with a fixed title and serves the scripted turns in
  order. It also logged the tool list each request offered.
- Every probe ran from a `mktemp -d` directory outside any repository, under
  `env -i` with only `PATH`, a throwaway `HOME`, `TMPDIR` and the four XDG variables,
  plus `OPENCODE_DISABLE_PROJECT_CONFIG=1` and `OPENCODE_DISABLE_AUTOUPDATE=1`. No
  provider credential was set; the stub's `apiKey` was a placeholder. Each probe ran in
  its own process group under a 20–60 s `alarm`, detached from the tty after the
  terminal finding in § 1.
- After every probe, `pgrep -fl opencode` and `lsof -iTCP -sTCP:LISTEN` were checked for
  leftovers, excluding one pre-existing background service the spike did not start or
  touch. The only leftover, a `serve --stdio` child of a killed client, had exited by
  the next check. Each stub was killed by its captured PID and confirmed dead.
- Paths, PIDs and ports in captured output are replaced with `<scratch>`, `<home>`,
  `<opencode>`, `<pid>` and `<ephemeral>`.

## Recommendations

```yaml recommendations
spike: 3197
recommendations:
  - id: opencode2-version-gate
    action: adopt
    title: "opencode: move the compat manifest, fixtures and install remediation to OpenCode 2.x"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 1, § 4 and § 5. 2.x rejects `--dir`, prints `run --help` on stdout,
      and is not published to npm as `opencode-ai` (`latest` is 1.19.5, and
      `opencode-ai@2.2.7` returns 404).

      - `internal/adaptercompat/manifests/opencode.json`: set `min_version` and
        `max_tested` to 2.2.7. In `required_flags`, drop `--dir` and add
        `--standalone`. Replace the `install.npm` remediation with the 2.x install
        channel. The project is pre-customer, so 1.x support is removed, not kept
        beside 2.x.
      - `internal/execution/adapters/opencode_preflight.go`: keep the floor check on
        the binary version. The `opencode vX.Y.Z` parse is already in place (#3172).
        Replace the npm remediation text.
      - Re-capture `internal/execution/adapters/testdata/opencode-cli/` (`run-help.txt`
        now from stdout, and `version.txt`) and
        `internal/execution/adapters/testdata/cli-help/`.
      - `internal/doctor/opencode.go`: the floor and help-capture checks for 2.x.

      The adapter stays experimental and gated until the rest of this chain lands.
    depends_on: []
  - id: opencode2-stub-scripts
    action: adopt
    title: "stub-provider: speak OpenCode 2.x tool names and argument shapes"
    type: chore
    priority: medium
    size: S
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 3. On 2.x, `edit` and `write` take `path`, not `filePath`; `bash` is
      `shell`; `task` is `subagent`, with `agent`, `description` and `prompt`. OpenCode
      sends a tool-less title-generation request before the first turn.

      - `internal/stubprovider/scripts.json`: update `tool-edit-stop`,
        `bash-then-stop`, `long-session`, `tools-forever`, `compaction-probe` and
        `task-then-subagent-bash` to the 2.x names and arguments.
      - `internal/stubprovider/server.go`: it advances the turn index on every
        request. Make sure the tool-less title request cannot take a scripted turn,
        for example by answering a request with no `tools` outside the script.
      - `internal/stubprovider/server_test.go`: cover both changes.
    depends_on: []
  - id: opencode2-config-schema
    action: adopt
    title: "opencode: emit the native 2.x config schema and restore the dropped controls"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 2. 2.x translates the 1.x keys but drops `subagent_depth`, an empty
      `mode` and any wrong-type value without a word. `debug config` lists sources
      instead of the merged config, and it exits 0 on bad input.

      - `internal/execution/adapters/opencode_config.go`: write `providers`
        (`package: "aisdk:<npm>"`, `settings`), `agents` (with `title` in place of
        `small_model`), `policies` in place of `enabled_providers`, `plugins`,
        `update: "disable"` and `snapshots`. Bound subagent depth with a deny of
        `subagent` on every subagent-mode agent, because `subagent_depth` no longer
        exists.
      - Validate the emitted document's types in Go before dispatch, because OpenCode
        no longer reports them.
      - `internal/execution/adapters/opencode_isolation.go` and
        `internal/doctor/opencode.go`: every probe passes `--standalone`, because
        without it `debug config` reaches for the managed service port.
      - Tests: `opencode_config_test.go`, `opencode_merge_contract_test.go`.
    depends_on: [opencode2-version-gate]
  - id: opencode2-permission-map
    action: adopt
    title: "opencode: rebuild the permission map on the 2.x tool and action names"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 2 and § 3. 2.x offers `edit, execute, glob, grep, question, read,
      shell, skill, subagent, webfetch, websearch, write`. `list` and `todowrite` no
      longer exist, and `execute` and `question` are new and ungoverned by today's map.
      `external_directory` deny still blocks writes outside the worktree.

      - `internal/execution/adapters/opencode_guard.go`: emit `permissions` entries
        `{action, resource, effect}` for the 2.x actions. Decide `execute` and
        `question` explicitly, and remove `list` and `todowrite`. Keep "never `ask`"
        (ADR-022 § 9): a 2.x `deny` removes the tool from the model's list.
      - Map Claude tool names to 2.x tools (`Bash` to `shell`, `Task` to `subagent`).
      - Tests: `opencode_guard_test.go`, `opencode_guard_integration_test.go`.
    depends_on: [opencode2-config-schema]
  - id: opencode2-dispatch
    action: adopt
    title: "opencode: dispatch with cwd and --standalone, and reap the private server"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 1. `run --dir` is gone, and a positional path becomes prompt text.
      Every run spawns `serve --stdio --port 0`, which listens on loopback for the
      whole run and was still alive just after its client was killed.

      - `internal/execution/adapters/opencode.go`: argv
        `run --standalone --format json --print-logs --log-level ERROR -m <model>`, with
        `cmd.Dir` set to the worktree and no positional argument. Consider `--archived`.
      - Signal the whole process group on cancel and timeout, and confirm the
        `serve --stdio` child is gone.
      - `internal/execution/adapters/opencode_isolation.go`: the same `--standalone`
        and process-group rules for every helper process (`db`, `export`, `--version`).
      - Update the listener assertion that the #1644 egress check and the #1639 canary
        rely on: loopback only, gone at run end.
      - Tests: `opencode_test.go`, `opencode_isolation_test.go`.
    depends_on: [opencode2-config-schema]
  - id: opencode2-event-stream
    action: adopt
    title: "opencode: parse the 2.x stream and bound provider retry storms"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 3. The event types are unchanged, plus `retry` (`attempt`,
      `nextRetryAt`, `error.type` such as `provider.internal` or
      `provider.transport`). A provider 500 or a refused connection retries until
      something kills the run. An unknown model ends with `error`
      (`provider.no-route`) and exit 1. A rejected `ask` prints an ANSI line on stderr.

      - `internal/execution/adapters/opencode.go`: parse `retry`. Classify a run that
        only retries as a provider failure after a bound, rather than letting the idle
        timer (#2176) wait it out, and map `provider.no-route` and
        `provider.transport`.
      - `internal/execution/adapters/opencode_evidence.go`: the tool-use `state` shape
        and `metadata.providerCall`.
      - Re-verify descendant-session usage through `opencode db` and `opencode export`
        on 2.x (not observed in #3197).
      - Fixtures under `internal/execution/adapters/testdata/`; tests in
        `opencode_test.go` and `opencode_stream_idle_2176_test.go`.
    depends_on: [opencode2-dispatch, opencode2-stub-scripts]
  - id: opencode2-plugin-entry
    action: adopt
    title: "opencode plugin: port the Nightgauge plugin to the 2.x definition and hook API"
    type: feature
    priority: high
    size: M
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 4. 2.x needs `export default { id, setup }`. Hooks register through
      `ctx.tool.hook("execute.before" | "execute.after", fn)`, and a returned hook map
      is ignored. `execute.before` is awaited, a throw blocks the tool and reaches the
      model, and the hook fires in subagent sessions. Arguments are
      `{tool, sessionID, agent, messageID, id, input}`, with `input.path`, not
      `filePath`.

      - `internal/execution/opencodeplugin/plugin/nightgauge.js`: the new default
        export and registration.
      - `internal/execution/opencodeplugin/plugin/nightgauge/gates.js`, `edit.js` and
        `session.js`: the 2.x argument shapes and tool names. Session-lifecycle events
        move to `ctx.event.subscribe`, whose event names must be observed while porting.
      - Revisit the #1810 workaround for un-awaited hooks, which is no longer needed.
      - Tests: `plugin_gates_test.go`, `plugin_edit_test.go`,
        `plugin_integration_test.go`.
    depends_on: [opencode2-version-gate, opencode2-stub-scripts]
  - id: opencode2-plugin-load-proof
    action: adopt
    title: "opencode plugin: install as a directory and fail closed when it does not load"
    type: feature
    priority: critical
    size: S
    labels: ["component:go-binary", "program:opencode"]
    body: |
      Spike #3197 § 4. A plugin entry must be a directory, and every load failure (a
      file path, the 1.x shape, a missing path) is only a `WARN` while the run
      proceeds with no gate. A dependency-free plugin loads, so the vendored
      `@opencode-ai/plugin` 1.18.30 tarball is not needed.

      - `internal/execution/adapters/opencode_plugin_deps.go` and
        `internal/execution/opencodeplugin/deps.go`: install the plugin as a directory
        with `package.json`. Remove `depsdata/opencode-ai-plugin-1.18.30.tar.gz` and
        `depsdata/regenerate` unless the port needs a 2.x package.
      - The plugin writes a load marker from `setup`. The adapter fails the stage
        before the first model request when the marker is absent.
      - Tests: `deps_test.go`, `opencode_plugin_test.go`.
    depends_on: [opencode2-plugin-entry, opencode2-dispatch]
  - id: opencode2-adr022-s20-amendment
    action: adopt
    title: "docs: amend ADR-022 for the OpenCode 2.x contract"
    type: docs
    priority: medium
    size: S
    labels: ["component:docs", "program:opencode"]
    body: |
      Spike #3197 § 5. Record the 2.x status of each ADR-022 § 20 behaviour. The
      changed ones are: `debug config` source listing and exit 0 on wrong type,
      `run --help` on stdout, the `opencode vX.Y.Z` version string, the npm
      remediation, and the private-server loopback listener. Record the
      plugin-awaited finding, which reverses #1810. Re-observe the two
      not-observable rows (drift record, doctor catalog probe) through a real
      dispatch.

      - `docs/decisions/022-opencode-multi-provider-adapter.md`: a dated amendment,
        and the Context-table rows updated.
      - `docs/ADAPTER_MATRIX.md`: the floor and tested-up-to versions.
    depends_on:
      - opencode2-version-gate
      - opencode2-config-schema
      - opencode2-permission-map
      - opencode2-dispatch
      - opencode2-event-stream
      - opencode2-plugin-entry
      - opencode2-plugin-load-proof
```
