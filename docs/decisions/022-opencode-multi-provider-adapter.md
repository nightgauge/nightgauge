# OpenCode Multi-Provider Adapter — one adapter, many providers, dispatched only behind an enable gate

**Date:** 2026-09-12
**Author:** nightgauge
**Status:** Decided
**Issue:** #1612 (epic #1609)
**Amends:** [ADR-016](016-model-aware-skill-overlays.md) (§ 14, host overlay segment) and
[ADR-018](018-adapter-usage-quota-model.md) (§ 4, `local` plan kind)
**Extends:** [ADR-020](020-value-adding-features-default-on.md) (§ 15, security and privacy as
reasons for a pipeline default to be off)
**Observed against:** opencode 1.18.30

---

## Executive Summary

OpenCode is registered as the `opencode` adapter. One adapter id reaches model
servers the operator runs (LM Studio, Ollama, or another server with an
OpenAI-compatible API) and hosted providers (Anthropic, OpenAI, xAI, Google).
The provider is chosen per dispatch by the provider-qualified model id passed
on `-m`, so the provider is a property of the dispatch, not of the adapter.
That breaks an assumption the model layer makes everywhere, and this ADR
decides how identity, cost, isolation and credentials work once it no longer
holds.

The adapter ships **Experimental**. `Manager.RunStage` refuses every `opencode`
dispatch before spawning anything unless `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1`
is set in the environment, and every dispatch it allows prints the controls
that are not enforced yet. An `anthropic/*` model is refused even with the
switch set, until #1616 enforces § 17's credential policy. The gate stays until
those controls exist and the beta decision in § 23 lifts it.

Three things are fixed by the change that carries this ADR:

1. The argv is exactly
   `opencode run --format json --print-logs --log-level ERROR -m <provider/model> --dir <worktree>`.
   No auto-approve, share or discovery flag is ever emitted.
2. The prompt goes on stdin and nowhere else.
3. Every spawn carries a fresh random `OPENCODE_SERVER_PASSWORD`.

Everything the rest of epic #1609 builds is decided below, question by
question. Hosted providers are supported by design, not tolerated: before beta
they are covered by stub-provider contract tests, and a live hosted leg is a GA
requirement.

Two things the plan assumed about OpenCode turned out to be false on 1.18.30.
Disabling project config also drops the repository's `AGENTS.md` and
`CLAUDE.md`, and disabling the Claude Code prompt also drops the repository's
`CLAUDE.md` fallback. § 8 and § 11 follow the observed behaviour: Nightgauge
injects the repository's steering itself.

## Context

### One adapter used to mean one provider

`models.ProviderForAdapter` maps an adapter name to a registry provider with a
switch: `claude*` to `anthropic`, `codex` to `openai`, `gemini*` to `google`,
`grok*` to `xai`, the local bridges to themselves, and anything else to
`other`. Cost stamping, skill overlays (ADR-016), the usage model (ADR-018),
cap recovery and stream parsing all start from that answer. For `opencode` the
switch has no correct answer: the same adapter serves
`lmstudio/qwen/qwen3.8-27b` and `anthropic/claude-sonnet-5` in consecutive
stages.

The local path today is the `lm-studio` and `ollama` adapters. They are chat
bridges with no tool loop, `Agentic()` reports `false` for both, and
`Manager.RunStage` refuses to dispatch a pipeline stage to them. OpenCode is the
first agentic path to a model the operator hosts.

### What opencode 1.18.30 does

Every behavioural claim in this ADR was observed on opencode 1.18.30 on
2026-09-12 or 2026-09-13, not taken from documentation. The method: throwaway
directories for all four XDG base directories, a scratch git repository as
`--dir`, and a stub OpenAI-compatible model server on `127.0.0.1` that recorded
every request and returned canned replies, so no hosted provider and no
operator configuration took part. The captured `opencode run --help`, the
capture script and the full observation table are in
[`internal/execution/adapters/testdata/opencode-cli/`](../../internal/execution/adapters/testdata/opencode-cli/README.md).

| Observation                                                                                                                                                                    | Where it decides something |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------- |
| Piped stdin, with no positional message, becomes the user message verbatim                                                                                                     | § 19                       |
| `opencode run` opens no TCP listener; its only sockets are outbound to the model endpoint                                                                                      | § 18                       |
| A permission that resolves to `ask` is rejected automatically, the run ends after that step, and the process exits 0                                                           | § 9                        |
| `allow` runs a tool with no auto-approve flag; `deny` removes the tool from the model's tool list                                                                              | § 9                        |
| `OPENCODE_DISABLE_PROJECT_CONFIG=1` ignores the repository's `opencode.json` **and** its `AGENTS.md` and `CLAUDE.md`                                                           | § 8, § 11                  |
| `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1` drops the repository's `CLAUDE.md` fallback **and** `~/.claude/CLAUDE.md`                                                              | § 11                       |
| `AGENTS.md` wins over `CLAUDE.md`, and `@path` imports in `CLAUDE.md` are never followed                                                                                       | § 11                       |
| An `instructions` entry with an absolute path loads a repository file even with project config disabled                                                                        | § 8, § 11                  |
| The session database is `$XDG_DATA_HOME/opencode/opencode.db` (SQLite, WAL) and holds the full prompt and transcript                                                           | § 22                       |
| `opencode export <session> --sanitize` redacts prompts, replies and tool input, and keeps per-message `tokens` and `cost`                                                      | § 22                       |
| `OPENCODE_SERVER_PASSWORD` is written to no output and no file, even at `--log-level DEBUG`                                                                                    | § 18                       |
| `step_finish` events carry per-step tokens (`input`, `output`, `reasoning`, `cache.read`, `cache.write`) and a `cost` that read `0` for a provider OpenCode holds no price for | § 3                        |
| `OPENCODE_AUTH_CONTENT`, when set, is read as the stored logins instead of `auth.json`, so a run whose data directory is empty still has logins                                | § 17                       |
| Read from the bundled source: OpenCode exports `OPENCODE_AUTH_CONTENT`, holding every login it has stored, to the processes it starts for a workspace                          | § 17                       |
| A config provider block whose key is in OpenCode's bundled catalog inherits that provider's API-key variables, and sends the key to the block's `baseURL`                      | § Endpoints                |
| With `env: []` or an explicit `apiKey`, the same block sends no key; a key outside the catalog binds no variable                                                               | § Endpoints                |
| The catalog a run loads is the one bundled in the binary; no run fetched one                                                                                                   | § Endpoints                |
| A `--format json` `error` event for a failed model request carries the request's full URL                                                                                      | § Endpoints                |
| `--print-logs` adds stderr but still writes `log/opencode.log` in the data directory, and an error goes to both                                                                | The command, § 22          |
| The four XDG base variables move config, data, cache and state; `home` does not move, and `tmp` stays at `$TMPDIR/opencode`                                                    | § 8                        |
| `$HOME/.opencode` is read as a config directory whatever the XDG variables, `OPENCODE_DISABLE_PROJECT_CONFIG` or `OPENCODE_PURE` say                                           | § 8                        |
| `$HOME/.agents/skills` loads whatever the XDG variables say; `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` stops it                                                                     | § 8, § 11                  |
| Config precedence, lowest first: the XDG config directory's files, `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR`, `OPENCODE_CONFIG_CONTENT`                                         | § 8                        |

The first contradiction changes § 8: once project config is disabled, which it
must be (a repository must not grant itself permissions, plugins or providers),
OpenCode no longer finds the repository's steering by itself. The second
changes § 11: there is no switch that drops the operator's personal
`~/.claude/CLAUDE.md` and keeps the repository's `CLAUDE.md`. Both lead to the
same answer. Nightgauge hands OpenCode the repository's steering explicitly and
lets OpenCode discover nothing.

A third assumption failed when run isolation was built (#1616): moving the XDG
base directories does not move everything OpenCode reads from the operator.
It still reads `~/.opencode` as a config directory and `~/.agents/skills` for
skills, from the home directory, which does not move. § 8 records what closes
each.

## Decision

### The enable gate

`opencode` is registered in `NewRegistry()` (display name "OpenCode", binary
`opencode`), and `Agentic()` is `true`. It can be listed, named with
`--adapter` or `NIGHTGAUGE_ADAPTER`, and tested with
`nightgauge adapter test opencode`. It is not dispatched unless the operator
has opted in.

- **The switch** is `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1`. Only the exact value
  `1` opens it; `true`, `yes` and ` 1` do not. It is read from the process
  environment and nowhere else, so a committed repository config can never turn
  it on for the operator. There is no `adapters:` config namespace and none is
  added.
- **The hook** is a new optional adapter method, `PreDispatch(RunOptions) error`,
  found by interface assertion the same way `ValidateModel` and `ValidateEffort`
  are. `Manager.RunStage` calls it after worktree setup and ahead of the model
  check, the effort check and `BuildCommand`. A refusal therefore states the
  real reason and spawns nothing. It runs after worktree setup so that a check
  which has to read the tree the stage will run in (the project-config tamper
  gate, § 8) can join it.
- **A refusal** names the controls that are missing, the switch, and the way
  out (`--adapter` or `NIGHTGAUGE_ADAPTER`).
- **`anthropic/*` is refused with the switch set.** § 17 lets Anthropic through
  OpenCode authenticate only with `ANTHROPIC_API_KEY`, never with a
  subscription or OAuth login OpenCode has stored, and until #1616 enforces
  that, nothing stops OpenCode using a stored login. `PreDispatch` therefore
  refuses every model whose provider key is `anthropic` before spawn, whatever
  the switch says. It checks this first, so the refusal states the one reason
  the switch cannot lift and no warning precedes it. The remediation names the
  `claude-headless` adapter, which also serves a Claude subscription, and says
  that `anthropic/*` through OpenCode unlocks when #1616 enforces § 17. The
  credential-policy row below stays until then, because the refusal covers only
  the model a stage names: any OpenCode config the run reads, the target
  repository's included, can name another `anthropic/` model that receives the
  stage prompt (§ 17).
- **An allowed dispatch** prints a warning to stderr, one line per control
  that is not enforced yet. It is the operator's only disclosure of what the
  dispatch runs without, so it lists every control this ADR assigns to a later
  change. The list is data (`openCodeUnenforcedControls` in
  `internal/execution/adapters/opencode.go`) and holds exactly the rows below,
  in order; `TestOpenCodeUnenforcedControlsMatchADR` fails when the two differ.
  The change that implements a control deletes its entry and its row.

| Control not yet enforced   | Owning change |
| -------------------------- | ------------- |
| stream parsing             | #1624, #1630  |
| failure classification     | #1624, #1631  |
| run isolation              | #1616         |
| output redaction           | #1616, #1624  |
| project-config tamper gate | #1638         |
| permission map             | #1638         |
| safety plugin              | #1635, #1640  |
| egress defaults            | #1616, #1625  |
| credential policy          | #1616         |
| endpoint policy            | #1678, #1679  |
| stage limits               | #1625, #1630  |
| version policy             | #1613, #1627  |

- **Removal.** #1643 deletes the enable check once the list is empty and
  § 23's beta criteria hold. Nothing else removes it.
- **Cap recovery.** `AdapterUsableForCapHop("opencode")` stays `false` while
  the gate is closed, so a capped run never hops onto an adapter that would
  refuse the dispatch. Today that holds because the doctor has no `opencode`
  spec; #1627 adds one and must keep the verdict `false` while the gate is
  closed. `TestOpenCodeIsNeverACapHopTargetWhileGated` pins it.
- **Stream parsing.** Until #1624, `StreamFormatForAdapter("opencode")` falls
  back to the Claude parser, which reads nothing from OpenCode's events. That
  is harmless only because every dispatch is gated, and the warning says so.
- **ADR-020.** The switch is a default-off setting. ADR-020 requires its reason
  beside it, and the reason is security: a dispatch runs without controls every
  other adapter has.

### The command

```text
opencode run --format json --print-logs --log-level ERROR -m <provider/model> --dir <worktree>
```

The prompt goes on stdin (§ 19). `--format json` is the NDJSON event stream the
parser reads. `--print-logs --log-level ERROR` also prints OpenCode's own log to
stderr, limited to errors. It does not replace the log file: observed, 1.18.30
still writes `log/opencode.log` in the run's data directory, and an error goes
to both (§ 22). `--dir` is the worktree; the manager also sets the process
working directory to it.

The adapter never emits `--auto`, `--yolo`, `--dangerously-skip-permissions`,
`--share` or `--mdns`. In 1.18.30 `--auto` and `--share` are real `run`
options, and a test fails if either stops being one, because the forbidden list
would then be guarding a name that no longer exists. `--yolo`,
`--dangerously-skip-permissions` and `--mdns` are not `run` options today, and
they stay forbidden in case a later version adds them.

The adapter exports the all-adapters environment contract: `NIGHTGAUGE_RUN_ID`
and `NIGHTGAUGE_TARGET_REPO` when set and never as empty values,
`NIGHTGAUGE_OUTPUT_FORMAT=json` (mirroring `--format json`, the value
`scripts/run-stage.sh` already exports for every non-Claude adapter on the
extension path), `GITHUB_TOKEN` from the host, the usual
`NIGHTGAUGE_ISSUE_NUMBER`/`REPO`/`STAGE`/`ADAPTER`/`DISPATCH_MODEL`/`CONTEXT_FILE`/`OUTPUT_FILE`,
and `OPENCODE_SERVER_PASSWORD` (§ 18).

A dispatch must name a model OpenCode can take on `-m`. Without `-m`, OpenCode
falls back to whatever model its own config names, which is the operator's
choice and not the pipeline's. `ValidateModel` accepts only an explicit
`<provider>/<model>`. It refuses an empty model, any bare id, and any value
whose provider key or model id could read as a flag. A bare registry id
(`claude-sonnet-5`) is refused exactly like a tier band (`sonnet`) or an id
the registry does not know, with remediation to name the provider. The adapter
never infers one: the provider decides where the repository's code goes and
what the stage costs, and qualifying a bare id to a hosted provider would make
that choice for the operator, the implicit crossing § Endpoints forbids for
failover. #1614 resolves a band only against a provider the operator
configured (§ 7).

`RunOptions` fields OpenCode has no flag for are not mapped yet, and each has
an owner: `AllowedTools` becomes the permission map (#1638), `Effort` becomes
`--variant` (#1643), `MaxTurns` becomes a steps cap and `MaxTokens` becomes
provider limits in the per-run config (#1625), and `CostBudget` becomes the
USD watchdog (#1630).

### 1. Provider derivation and normalization

The provider comes from the provider block Nightgauge injects into the per-run
OpenCode config (§ 8), never from how an operator's own config or a model
string spells it. The `-m` value is split on the **first** slash:
`lmstudio/qwen/qwen3.8-27b` is key `lmstudio`, model `qwen/qwen3.8-27b`. The
key is then looked up in what Nightgauge injected:

| OpenCode provider key                    | Nightgauge provider (`model_provider`)             |
| ---------------------------------------- | -------------------------------------------------- |
| a declared endpoint id (see § Endpoints) | that endpoint's declared `provider`                |
| `lmstudio`                               | `lm-studio`                                        |
| `ollama`                                 | `ollama`                                           |
| `anthropic`                              | `anthropic`                                        |
| `openai`                                 | `openai`                                           |
| `xai`                                    | `xai`                                              |
| `google`                                 | `google`                                           |
| anything else                            | `other`, with the raw key kept in `upstream_model` |

A key Nightgauge did not inject normalizes to `other` however it is spelled.
These are the fixture strings the parser and cost tests (#1624, #1630) use:

| `-m` value (`upstream_model`)      | Key               | Model id             | `model_provider` | `endpoint`        | `model` on the wire             |
| ---------------------------------- | ----------------- | -------------------- | ---------------- | ----------------- | ------------------------------- |
| `lmstudio/qwen/qwen3.8-27b`        | `lmstudio`        | `qwen/qwen3.8-27b`   | `lm-studio`      | `lmstudio`        | `lm-studio/qwen/qwen3.8-27b`    |
| `lmstudio-remote/qwen/qwen3.8-27b` | `lmstudio-remote` | `qwen/qwen3.8-27b`   | `lm-studio`      | `lmstudio-remote` | `lm-studio/qwen/qwen3.8-27b`    |
| `ollama/qwen3-coder:30b`           | `ollama`          | `qwen3-coder:30b`    | `ollama`         | `ollama`          | `ollama/qwen3-coder:30b`        |
| `anthropic/claude-sonnet-5`        | `anthropic`       | `claude-sonnet-5`    | `anthropic`      | null              | `claude-sonnet-5`               |
| `openai/gpt-5.5`                   | `openai`          | `gpt-5.5`            | `openai`         | null              | `gpt-5.5`                       |
| `xai/grok-4.6`                     | `xai`             | `grok-4.6`           | `xai`            | null              | `grok-4.6`                      |
| `google/gemini-2.5-pro`            | `google`          | `gemini-2.5-pro`     | `google`         | null              | `gemini-2.5-pro`                |
| `openai/gpt-9-preview`             | `openai`          | `gpt-9-preview`      | `openai`         | null              | `openai/gpt-9-preview`          |
| `openrouter/meta-llama/llama-4`    | `openrouter`      | `meta-llama/llama-4` | `other`          | null              | `openrouter/meta-llama/llama-4` |

The first three rows assume endpoints declared with those ids. `gpt-9-preview`
stands for a hosted model the registry does not know. Provider keys, and
therefore endpoint ids, are lowercase letters, digits and `-`, and never start
with `-`. A key can then never read as a flag, and because a dot is not
allowed, a host name or an address can never become one.

### 2. Wire format

- `model`: a registry-known model is recorded as its bare registry id
  (`claude-sonnet-5`), so cost and by-model breakdowns key identically to the
  same model run through its own vendor's adapter. Any other model is recorded
  as `<model_provider>/<id>` (`lm-studio/qwen/qwen3.8-27b`), so a local model
  can never collide with a registry id. An `other` model is recorded as its raw
  `-m` value.
- `upstream_model`: the raw `-m` value exactly as dispatched. It is a field of
  the local record.
- `provider` on the V5 stage metric keeps its current meaning, the executing
  adapter, so it reads `opencode`.
- **Yes, the V5 stage metric gains nullable fields**: `model_provider` (§ 1)
  and `endpoint` (§ Endpoints). Both are written to the local V2 record from
  the first parser change (#1624). The platform mapper emits them only after
  the platform's strict stage-metric schema accepts them, because that schema
  rejects unknown keys and an early emission would fail the whole upload. This
  is the same local-first pattern `cost_unstamped` follows in
  `internal/platform/execution_history_mapper.go`.

### 3. Cost

`cost_usd` is what a provider bills for the tokens a stage used. Nightgauge
meters no other cost of a stage, so a stamped zero says only that no provider
bills it.

- **Stamped zero only where the model runs on the endpoint.** A stage records
  `cost_usd: 0` with `cost_unstamped: false` only when its model is known to
  run on the endpoint that served it. The stamp follows where the model runs,
  not what kind of endpoint answered, because an endpoint can forward a request
  and Nightgauge does not see past it (§ Endpoints). It is known in three
  cases:
  - an `lm-studio` endpoint, the `lmstudio` key of § 1 included;
  - an `ollama` endpoint, the `ollama` key included, for a model Ollama runs
    itself. Ollama serves its cloud models through the same local API from its
    hosted service, so a model whose tag is `cloud` or ends in `-cloud`, or
    for which Ollama reports a `remote_host`, is refused before spawn (#1679);
  - an `openai-compatible` endpoint whose entry declares `self_hosted: true`
    (#1678). The declaration is the operator's statement that the model runs
    on that server, and Nightgauge cannot check it. Without it the server may
    be a gateway to a hosted API, such as a LiteLLM proxy, and its stages are
    unstamped.
- **Re-priced from the registry, per step.** For a hosted model the registry
  knows, every `step_finish` is priced from the registry's rate card for the
  model that served that step: input, output and reasoning tokens, plus the
  cache read and cache write pools at the registry's cache rates. A stage's cost
  is the sum. Subagent steps (the `task` tool) are rolled up the same way
  (#1624). The USD watchdog (#1630) runs on this figure.
- **OpenCode's own `cost` is never trusted.** It comes from OpenCode's catalog
  and not from the bill, and it read `0` for a provider it had no price for.
- **Every other zero is unstamped.** A hosted or `other` model the registry
  cannot price, and a stage on an `openai-compatible` endpoint not declared
  `self_hosted`, records `cost_usd: 0` with `cost_unstamped: true`. An unknown
  cost is never recorded as a stamped zero.

### 4. The `local` usage plan (amends ADR-018)

ADR-018 gains a plan kind, `plan.kind: "local"`, with no windows. It is
produced for an adapter whose attributed stages in the snapshot all ran on
models known to run on the operator's endpoints: `opencode` stages § 3 stamps
at zero, and `lm-studio` and `ollama` bridge stages on a model § 3 would stamp.
No provider bills those stages, so there is no allowance to meter, and today
they fall to `unknown`, which tells the user "cannot say" about stages no
provider bills. When any attributed stage was hosted or unstamped, the
snapshot is the ordinary `pay-per-token` one over the priced stages. #1665
records the amendment in ADR-018 and implements it.

### 5. Open model policy

`opencode` is an **open** adapter. Its model set is whatever the operator's
endpoints serve plus whatever OpenCode's hosted providers offer, and no
finite registry set describes that. `adapter_transports` must equal the
closed-transport set exactly (`validateAdapterTransports`), so `opencode` is
not listed there. Registry membership decides pricing (§ 3) and how a model is
recorded (§ 2), never admission: a provider-qualified model is not refused for
being unknown, and a bare id is not admitted for being known. Admission is the
`-m` shape check now, and endpoint readiness (#1646, #1678), the refusal of an
Ollama cloud model on an endpoint (§ 3, #1679) and the doctor (#1627) later.

### 6. The agentic gate and #521

`opencode` reports `Agentic()` true: `opencode run` drives a real tool loop
(the observed default tools are `bash`, `edit`, `write`, `read`, `grep`,
`glob`, `task`, `todowrite`, `skill` and `webfetch`). The `lm-studio` and
`ollama` chat bridges stay `Agentic()` false and stay barred from stage
dispatch; nothing in this epic changes them.

For pipeline stages, OpenCode supersedes #521's option of an agentic local
proxy: the agentic path to a local model is OpenCode, and no proxy is built.
#521 keeps its non-agentic scope (routing surfaces with no tool loop to a
local model). The ADR fixes **one local-endpoint config shape**, the
`opencode.endpoints[]` entry (§ 7, § Endpoints). Any later consumer of a local
model server, #521's routing and the doctor's readiness probe included, reads
those entries and defines no second shape.

### 7. One config namespace

All OpenCode configuration lives in a single `opencode:` block, and that block
is read only from the **machine tier** (`~/.nightgauge/config.yaml` and the
gitignored `.nightgauge/config.local.yaml`). An `opencode:` key in the
committed project config fails config validation with a message naming the
machine-tier files. Every value in the block is a fact about one machine: a
binary path, endpoint URLs, local model limits. Endpoint URLs must never be
committed.

```yaml
opencode:
  binary: opencode # binary pin: a command on PATH or an absolute path; § 20 checks it
  inherit_user_config: false # the default; § 8
  model: lmstudio/qwen/qwen3.8-27b # used when a stage's model names no provider (#1614)
  endpoints:
    - id: lmstudio # becomes the OpenCode provider key
      provider: lm-studio # lm-studio | ollama | openai-compatible
      base_url: http://127.0.0.1:1234/v1
      limits:
        context: 131072 # overrides the value discovered from the server (#1633)
        output: 16384
```

`limits` apply to every model an endpoint serves and override discovered
values. Until #1614 lands, a stage must name `<provider>/<model>` itself; the
adapter refuses anything else before spawning.

### 8. Isolation and project config

- **XDG layout.** Each run gets one root, shared by its stages and outside
  every worktree and repository: `~/.nightgauge/opencode/runs/<run_id>/`,
  created with mode 0700.
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and `XDG_STATE_HOME`
  point at its `config/`, `data/`, `cache/` and `state/`. OpenCode 1.18.30
  writes `config/opencode/` (a config file and its own `.gitignore`),
  `data/opencode/` (`opencode.db` with its WAL files, `log/`, `snapshot/`,
  `repos/`), `cache/opencode/bin/` and `state/opencode/locks/`. A dispatch with
  no run identity mints a root id of its own, which is never exported as
  `NIGHTGAUGE_RUN_ID`.
- **The per-run config.** Nightgauge writes
  `config/opencode/opencode.json` (mode 0600): the injected provider blocks
  (§ 1, § 17, § Endpoints), the permission map (§ 9), the plugin list, the `instructions`
  entries (below), MCP servers (#1626) and every locked key in § 15. Provider
  URLs never go into the environment, where the process table would show them.
- **The environment.** Every inherited `OPENCODE_*` variable is removed, and
  only the variables this ADR names are set. The operator's shell can then
  never change a pipeline run's posture through an OpenCode variable.
- **`inherit_user_config`** defaults to `false`: the operator's global
  OpenCode config is not read. When `true`, that config is layered under the
  per-run config, and every locked key in § 15 still wins. Credentials are
  never inherited either way (§ 17).
- **The target repository's `opencode.json`, `opencode.jsonc` and
  `.opencode/**`.** `OPENCODE_DISABLE_PROJECT_CONFIG=1` is set on every spawn,
  so OpenCode never loads them. Nightgauge reads them instead. Keys outside the
  locked set are merged into the per-run config, and a locked key the
  repository sets is dropped with a warning. The tamper gate records the files'
  hashes when the worktree is created, and `PreDispatch` refuses a dispatch
  when they have changed since, because a stage must not rewrite the config the
  next stage runs under (#1638).
- **Steering.** Observed: that switch also hides the repository's `AGENTS.md`
  and `CLAUDE.md`. The repository's steering therefore reaches OpenCode only as
  an `instructions` entry with an absolute path into the worktree, which was
  observed to load. #1626 applies the rule Codex and Gemini steering already
  use: `AGENTS.md`, or `CLAUDE.md` without its `@AGENTS.md` import only when
  `AGENTS.md` has no content of its own.
- **`--pure` is never passed.** It would also drop the Nightgauge plugin.
  Plugins are controlled by the per-run `plugin` list (the Nightgauge plugin
  and nothing else) and `OPENCODE_DISABLE_DEFAULT_PLUGINS=1`.

#1632's adversarial suite proves the merge: a repository config that sets
permissions, plugins, providers, MCP servers or remote instructions changes
nothing about a run.

### 9. Headless posture

Observed: in `opencode run`, a permission that resolves to `ask` is rejected
automatically. OpenCode prints `! permission requested: <permission>
(<pattern>); auto-rejecting`, the tool call fails with "The user rejected
permission to use this specific tool call.", the run ends after that step, and
the process **exits 0**. An `ask` is a silent stop that looks like success.

- Permission maps Nightgauge generates contain only `allow` and `deny`, never
  `ask`. That covers the permissions OpenCode defaults to `ask`, such as
  `external_directory`. `allow` was observed to run a tool with no
  auto-approve flag, and `deny` removes the tool from the model's tool list.
- Auto-approve flags (`--auto`, and `--yolo` or
  `--dangerously-skip-permissions` should a version add them) are never
  emitted. Approval is the map's job, derived from the stage's allowed tools
  (#1638).
- The parser classifies a rejected-permission tool event as a failure, exit
  code notwithstanding (#1624, #1631).
- The project directory OpenCode uses is the resolved path, so an absolute
  path through a symlinked prefix (such as macOS `/tmp`) reads as an external
  directory. The permission map is built against resolved paths.

### 10. Egress defaults

| Egress                                  | Pipeline default                                                                      |
| --------------------------------------- | ------------------------------------------------------------------------------------- |
| Session share                           | `share: "disabled"`, `OPENCODE_DISABLE_SHARE=1`, `--share` never emitted              |
| Autoupdate                              | `autoupdate: false`, `OPENCODE_DISABLE_AUTOUPDATE=1`                                  |
| Model-catalog fetch                     | `OPENCODE_DISABLE_MODELS_FETCH=1`; the registry prices, not the catalog               |
| LSP server download                     | `OPENCODE_DISABLE_LSP_DOWNLOAD=1`                                                     |
| Default plugins                         | `OPENCODE_DISABLE_DEFAULT_PLUGINS=1`                                                  |
| Remote `instructions` and `skills.urls` | refused: only absolute paths inside the worktree or the per-run root                  |
| `webfetch`                              | `deny` unless the stage's allowed tools include web fetch                             |
| Web search                              | off; its enabling variable is stripped with every inherited `OPENCODE_*`              |
| Session-title generation                | `agent.title.disable: true`, so no title request is sent; `small_model` locked (§ 15) |

Every variable and config key named here appears in the 1.18.30 binary.
Whether they stop the traffic they name is #1644's to prove.

Session-title generation is a second model request in every run, as read from
the 1.18.30 bundled source rather than observed. `opencode run` gives its
session a default title, and OpenCode replaces a default title by sending the
stage prompt to the `title` agent's model, or else to `small_model` on whatever
provider it names, or else to a small model of the dispatched provider, or
else to the dispatched model. The title has no use in a run whose session
database is deleted at the end (§ 22), so the per-run config removes the
`title` agent, and § 15 locks `small_model` and every agent's model to the
dispatched model as well.

#1644 records the connections OpenCode's process tree opens during a run on a
local provider. A pass verifies that OpenCode reached only loopback and the
endpoints the operator configured, and it verifies nothing about the rest of
the run or about where a model ran. Every stage also reaches the Git forge,
which is what the `GITHUB_TOKEN` in its environment is for. An endpoint on the
local network (§ Endpoints) is another machine, reached over plain HTTP. And an
endpoint can forward a request to a hosted service that Nightgauge does not see
(§ Endpoints). Nightgauge documents a run's network traffic only as far as
#1644 has measured it, and only after it has (§ 23).

### 11. Claude compatibility

The plan was to set `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT` and
`OPENCODE_DISABLE_CLAUDE_CODE_SKILLS` instead of the blanket
`OPENCODE_DISABLE_CLAUDE_CODE`, so that a repository's `CLAUDE.md` fallback
would survive. On 1.18.30 it does not: `_PROMPT` drops the repository's
`CLAUDE.md` as well as `~/.claude/CLAUDE.md`, and project config being
disabled (§ 8) drops it anyway.

- `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1` and
  `OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1` are set, so the operator's personal
  `~/.claude/CLAUDE.md` and `~/.claude/skills` never enter a pipeline run. The
  blanket switch is still not used: Nightgauge disables what it means to and no
  more.
- The repository's `CLAUDE.md` fallback survives because Nightgauge injects it
  (§ 8), not because OpenCode finds it.
- `@imports` are not followed; OpenCode never followed them in any
  configuration observed. Steering that depends on an import is inlined by
  #1626's injection, which already strips the `@AGENTS.md` import line.

### 12. Pipeline defaults for `snapshot`, `lsp` and `formatter`

| Setting     | Pipeline default                  | Operator may override (machine tier) | Why                                                                                                                           |
| ----------- | --------------------------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `snapshot`  | `false`                           | yes                                  | The worktree and its git history already undo anything; a snapshot is a second copy of the tree in the per-run data directory |
| `lsp`       | on, for servers already installed | yes, off                             | Diagnostics after an edit add value; the download of new servers is locked off (§ 10)                                         |
| `formatter` | on                                | yes, off                             | Formatting on edit adds value; turn it off where the repository's own hook formats                                            |

### 13. The registry's "local providers have no entries" note

The registry's schema note in `internal/models/model-registry.json` says that
local providers (ollama and lm-studio) have no entries by design, that the
configured local model serves every band, and that an unknown id is priced at
zero. #1633 amends it to say that local providers have no **committed**
entries. A local model's descriptor (context length and tool support, and a
zero rate card with `rate_provenance: local` only for a model § 3 stamps) is
discovered from the endpoint at run time, lives in machine-local state, and is
keyed by normalized provider and model id, never by endpoint address. A model
on an endpoint is priced by § 3's rule: `cost_usd: 0` stamped where the model
is known to run on the endpoint, unstamped otherwise. The committed file
carries no local model and no endpoint.

### 14. Host overlay segment (amends ADR-016)

ADR-016's overlay cascade gains a **host** segment, keyed by adapter name and
resolved from the adapter alone. It applies even when the model resolves to no
`ModelDescriptor`, which is every local model today:

```text
_shared/_overlays/host/<adapter>.md → _shared/_overlays/<provider>.md → _shared/_overlays/<id>.md
  → <skill>/_overlays/host/<adapter>.md → <skill>/_overlays/<provider>.md → <skill>/_overlays/<id>.md
```

Host fragments live in a `host/` subdirectory because an adapter name and a
provider name can be the same string (`copilot` is both). The host segment is
the most general and is composed first. #1636 records the amendment in ADR-016,
implements it in `nightgauge skill render`, and ships the `opencode` host
overlay.

### 15. Capability disposition and the ADR-020 carve-outs

Every OpenCode capability has one disposition:

| Capability                       | Disposition                  | Owner or reason                                                                               |
| -------------------------------- | ---------------------------- | --------------------------------------------------------------------------------------------- |
| Skills                           | supported                    | #1666 (install target), rendered per stage                                                    |
| Commands                         | supported                    | #1666 (`configs/opencode` templates)                                                          |
| Subagents (`task`)               | supported                    | #1624 rolls subagent usage into the stage                                                     |
| Plugins                          | supported, Nightgauge's only | #1635, #1640, #1641, #1642                                                                    |
| MCP                              | supported                    | #1626                                                                                         |
| Permissions                      | supported                    | #1638                                                                                         |
| Sandboxing                       | non-goal                     | OpenCode has none upstream; containment is § 8 isolation, the permission map and the worktree |
| Resume and fork                  | deferred                     | #1643 (session resume within a run)                                                           |
| Export                           | supported, sanitized only    | § 22                                                                                          |
| Import                           | non-goal                     | a session file or URL is untrusted input with nothing to gain                                 |
| Usage (`opencode stats`)         | non-goal                     | usage comes from the stream and the registry (§ 3), not OpenCode's catalog prices             |
| `json_schema` output             | deferred                     | #1650                                                                                         |
| Variants (`--variant`)           | supported                    | #1643 maps effort to a variant                                                                |
| Compaction                       | supported                    | #1625 (settings), #1641 (events)                                                              |
| Worktrees and workspaces         | non-goal                     | Nightgauge owns worktrees; OpenCode's experimental workspaces stay off                        |
| Snapshots                        | off by default               | § 12                                                                                          |
| LSP                              | supported, installed servers | § 12                                                                                          |
| Share                            | non-goal                     | disabled and locked (§ 10)                                                                    |
| GitHub agent (`opencode github`) | deferred                     | #1650                                                                                         |
| ACP                              | deferred                     | #1650                                                                                         |
| `serve` and `run --attach`       | deferred                     | #1650, under § 18's guardrails                                                                |

ADR-020 allows an opt-out of a value-adding feature for footprint and cost, and
already keeps destructive, money-spending and data-exporting features opt-in.
This ADR adds **security** and **privacy** as reasons for a pipeline default to
be off. Each is allowed only with its reason recorded in this table and beside
the setting. A **locked** row cannot be turned back on from any config tier.

| Feature disabled or overridden           | Pipeline setting                                                       | Reason    | Locked or overridable                        |
| ---------------------------------------- | ---------------------------------------------------------------------- | --------- | -------------------------------------------- |
| Dispatch itself                          | `NIGHTGAUGE_EXPERIMENTAL_OPENCODE` gate                                | security  | overridable by environment only, until #1643 |
| Session share                            | `share: "disabled"`, `OPENCODE_DISABLE_SHARE=1`                        | privacy   | locked                                       |
| Autoupdate                               | `autoupdate: false`, `OPENCODE_DISABLE_AUTOUPDATE=1`                   | security  | locked (§ 20 owns upgrades)                  |
| Model-catalog fetch                      | `OPENCODE_DISABLE_MODELS_FETCH=1`                                      | privacy   | locked                                       |
| LSP server download                      | `OPENCODE_DISABLE_LSP_DOWNLOAD=1`                                      | security  | locked                                       |
| Default and third-party plugins          | `OPENCODE_DISABLE_DEFAULT_PLUGINS=1`; `plugin` lists Nightgauge's only | security  | locked                                       |
| Repository project config                | `OPENCODE_DISABLE_PROJECT_CONFIG=1`; reviewed merge (§ 8)              | security  | locked                                       |
| Operator's global OpenCode config        | `inherit_user_config: false`                                           | security  | overridable; locked keys still win           |
| Session titles                           | `agent.title.disable: true` (§ 10)                                     | privacy   | locked                                       |
| A model other than the dispatched one    | `small_model` and every agent's `model` pinned to the dispatched model | security  | locked                                       |
| OAuth and subscription credentials       | never read (§ 17)                                                      | security  | locked                                       |
| Operator's `~/.claude` prompt and skills | `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1`, `_SKILLS=1`                   | privacy   | locked                                       |
| Remote `instructions` and `skills.urls`  | refused                                                                | security  | locked                                       |
| Inherited `OPENCODE_*` variables         | stripped                                                               | security  | locked                                       |
| `webfetch`                               | `deny` unless the stage's allowed tools include it                     | privacy   | overridable per stage, through allowed tools |
| `ask` permissions                        | never generated (§ 9)                                                  | security  | locked                                       |
| Auto-approve flags                       | never emitted                                                          | security  | locked                                       |
| Listener, mDNS and CORS                  | `--port`, `--mdns` and `--cors` never passed to `run`                  | security  | locked                                       |
| Session import                           | not used                                                               | security  | locked                                       |
| Snapshots                                | `snapshot: false`                                                      | footprint | overridable                                  |

The pinned models are `small_model` and the `model` of every agent: the hidden
`title`, `compaction` and `summary` agents and every subagent the `task` tool
starts. Each of them decides where a model request carrying the stage's prompt
or transcript goes, so none may name a model other than the one the stage
names (§ 17). #1625 writes them into the per-run config, and the
project-config merge drops a value the repository sets, with a warning
(#1638); a value in an inherited operator config loses to them like every
other locked key (§ 8).

### 16. Orchestration capability

`opencode` declares `sdk-fanout`, not `native-workflow`. OpenCode's `task`
subagents are not Nightgauge's workflow runtime. Fan-out runs through the
portable `sdk-fanout` executors like every non-Claude adapter, and costs on
those runs are labelled estimates until #1630's re-pricing lands. The
declaration ships with the SDK adapter (#1637).

### 17. Anthropic credentials

An `anthropic/*` stage through OpenCode has exactly one credential: the
`ANTHROPIC_API_KEY` variable. Nightgauge injects the `anthropic` provider block
with its key read from that variable, and a dispatch to `anthropic/*` with the
variable unset is refused before spawn, with remediation.

A subscription or OAuth login is never used. OpenCode 1.18.30 has two sources
of stored logins, and a run reads neither:

- **`auth.json` in the data directory.** The per-run data directory starts
  empty, and `inherit_user_config` never carries credentials.
- **`OPENCODE_AUTH_CONTENT`.** When it is set, OpenCode reads its JSON as the
  stored logins instead of `auth.json`, and OpenCode exports it, holding every
  login it has stored, to the processes it starts for a workspace (§ Context).
  An inherited value would hand a run the parent's logins however empty its
  data directory is. The adapter therefore withholds it from every spawn
  already: its `WithheldEnv` hook names it, and the manager removes it from the
  inherited environment before adding the adapter's exports, without reading
  or logging its value.

This is not a pending default. A pipeline run is automated work billed per
token against a key the operator can audit and revoke per machine, and
reversing it takes a superseding ADR. Every other hosted provider likewise
authenticates with its own API-key variable from the environment.

Until #1616 enforces this section, `PreDispatch` refuses every `anthropic/*`
dispatch before spawn, with the enable switch set or not (§ The enable gate).
Nothing yet stops OpenCode using a login stored in the operator's own
`auth.json`, because a run has no data directory of its own yet, and
`ANTHROPIC_API_KEY` being set does not change that. The refusal sees only the
model a stage names. Any OpenCode config the run reads can name another: the
operator's own, and the target repository's `opencode.json`, `opencode.jsonc`
and `.opencode/`, which load because the adapter does not set
`OPENCODE_DISABLE_PROJECT_CONFIG` yet (§ 8). Read from the 1.18.30 bundled
source, two keys send the stage prompt to the model they name, whatever its
provider:

- `small_model` titles every session. `opencode run` gives its session a
  default title, and OpenCode replaces it by sending the stage prompt to the
  `title` agent's model or else to `small_model`, without checking that it is
  on the dispatched provider (§ 10).
- An agent's `model` is what that agent runs on: the `title` agent, the
  `compaction` agent, which summarizes the whole transcript, and a subagent the
  stage starts through the `task` tool, which otherwise runs on the stage's
  model.

Named as `anthropic/...`, either one reaches Anthropic on whatever credential
OpenCode holds for it, a stored login included, and the repository, not the
operator, may be what names it. Run isolation (#1616) closes the stored-login
route, and #1616 lifts the refusal only when both sources are closed: the
per-run data directory starts empty, and no inherited login-bearing
`OPENCODE_*` variable reaches the run. The adapter withholds
`OPENCODE_AUTH_CONTENT` now, and #1616 owns the full inherited-environment
policy for `OPENCODE_*` (§ 8), so a login-bearing variable a later version
adds is removed with the rest. § 15 pins both keys to the dispatched model
(#1625), and the project-config merge drops a repository's value (#1638), so
no config chooses where the prompt goes. Until #1616 lands, the
enabled-dispatch warning's credential-policy line names both routes, and #1616
replaces the refusal with the key requirement above.

`nightgauge doctor` reports a subscription or OAuth login for `anthropic` in
either source of OpenCode's stored logins as a finding (#1627): `auth.json` in
the operator's OpenCode data directory, and `OPENCODE_AUTH_CONTENT` in the
environment the doctor runs in. For both it reads only each entry's `type`,
never a credential value, and it prints neither source's content. The finding
says that a pipeline run never uses the login and that an `anthropic/*` stage
through OpenCode needs `ANTHROPIC_API_KEY`. Its remediation names
`claude-headless`, the adapter that runs Claude Code under the login Claude
Code itself holds.

### 18. Listener

Observed: `opencode run` 1.18.30, without `--attach` or `--port`, opens no TCP
listening socket. `lsof -a -iTCP -sTCP:LISTEN -p <pid>` was empty while a model
request was held open for ten seconds, and the process's only sockets were
outbound to the model endpoint.

Every spawn still carries a fresh random `OPENCODE_SERVER_PASSWORD` (at least
128 bits, from `crypto/rand`), so that any listener a later version or an added
flag opens is authenticated. The value is set only in the child's environment:
it is never on argv, never logged by Nightgauge, and was observed in no
OpenCode output or file even at `--log-level DEBUG`. Tools the stage runs can
read their own environment; the value is per spawn and guards nothing once the
process exits.

The guardrails for `opencode serve` (#1650):

- bind `--hostname 127.0.0.1` only, with a random port;
- set a random per-server `OPENCODE_SERVER_PASSWORD`;
- never pass `--mdns`, which rebinds the server to every interface, or `--cors`;
- capture the server's PID at spawn, and at stage end kill its process group
  and verify it is gone.

### 19. Prompt delivery

The prompt is written to the child's **stdin**, and the pipe is closed.
Observed: with no positional message, piped stdin becomes the user message
verbatim. No temp file is written, so the 0600-file alternative the plan
allowed is not needed.

- **Positional** is rejected. Linux caps a single argv string at 128 KiB, and a
  rendered pr-merge prompt is already about 87 KB. Argv is visible to every
  local user in the process table. And a positional prompt that starts with
  `--` is parsed as flags, so an issue body opening with `--auto` would switch
  on auto-approval.
- **`-f`** is rejected: it attaches a file to a message rather than being the
  message.

`TestOpenCodePromptNeverOnArgv` checks that a 200 KiB prompt starting with
`--auto` puts no prompt bytes on argv and writes no file.

### 20. Version policy

The compat manifest (#1613) is the single source for the floor and the
max-tested version; the doctor (#1627) and `PreDispatch` enforce it.

- **Floor: 1.18.30**, the version every observation here was made on. Below
  it, dispatch fails closed with remediation.
- **Max-tested: 1.18.30.** Above it, dispatch warns and runs a self-test once
  per machine and version before the first dispatch. The self-test is the
  observation method above: a loopback stub provider checks stdin delivery,
  the `--format json` event types, `ask` auto-rejection, the absence of a TCP
  listener, and the project-config switch. A failed self-test refuses dispatch.
- **Endpoints stop at max-tested.** The self-test cannot re-check the reserved
  endpoint ids or the `lmstudio` exception (§ Endpoints). Which provider keys a
  binary bundles and which keys its custom loaders claim are read from its
  bundled catalog and source, and a stub provider observes neither.
  Above max-tested, a dispatch to a model server the operator runs (a declared
  endpoint, or the `lmstudio` or `ollama` key of § 1) is therefore refused
  before spawn, and no endpoint block is written into any run's config. The
  refusal names the installed version and max-tested, and its remediation is a
  `binary` pin (§ 7) to a max-tested build. Hosted dispatch continues under the
  warning and self-test above.
- Raising max-tested re-captures `testdata/opencode-cli/`, the reserved
  endpoint ids and the `lmstudio` exception (§ Endpoints) included, in the same
  change.

### 21. Capability spine

No alpha entry. The public capabilities map (`capabilities.yaml`, and
`docs/CAPABILITIES_MAP.md` generated from it) is a claim about what the product
does, and while every `opencode` dispatch is refused by default there is no
capability to claim. The experimental adapter is described only by this ADR
and its own refusal and warning text. #1672 adds `opencode-adapter` directly at
`beta`, with the `ADAPTER_MATRIX` row, in the change that follows the gate's
removal.

### 22. Transcript retention

- **Where.** The session database is
  `$XDG_DATA_HOME/opencode/opencode.db` (SQLite, with `-wal` and `-shm` files),
  inside the per-run root. It holds the full prompt and transcript (observed
  in its `part` table). `snapshot/` and `log/` sit beside it.
- **When deleted.** The whole per-run root is deleted when the run ends,
  whether it succeeded, failed or was cancelled. Nothing from it outlives the
  run, which is also why session resume (#1643) works only within a run.
- **What is kept.** Usage only. The stream (#1624) is the source.
  `opencode export <session> --sanitize` is read for its `tokens` and `cost`
  fields only, as a cross-check; it was observed to redact prompts, replies and
  tool input while keeping those fields. Nothing else from an export is kept.
- **Stderr.** `--print-logs --log-level ERROR` limits OpenCode's log to
  errors. Captured stderr is redacted of every secret value Nightgauge placed
  in the child's environment (the server password, API keys, `GITHUB_TOKEN`)
  before it is persisted (#1616).

### 23. Promotion criteria

**Experimental to beta.** #1643 removes the gate when all of these hold:

1. `openCodeUnenforcedControls` is empty, each entry removed by the change that
   enforces it.
2. #1644 has verified zero non-loopback egress for OpenCode on a local
   provider.
3. #1659 shows live six-stage pipeline runs on a local model through LM Studio
   completing with usage recorded and `cost_usd: 0` stamped.
4. Hosted providers pass stub-provider contract tests: a loopback stub speaking
   each hosted provider's protocol checks argv, credential injection (§ 17),
   the per-step re-pricing (§ 3) and failure classification (#1631).
5. The version policy (§ 20) and the doctor checks (#1627) are live, the
   stored-login finding of § 17 among them.
6. Extension and SDK parity (#1615, #1623, #1637) ships, so both execution
   paths treat `opencode` the same way.

**Beta to GA.** All of the beta criteria still hold on the current max-tested
version, and:

1. A live hosted-model leg through OpenCode (#1680) completes a six-stage run
   with registry-priced cost stamped.
2. Named endpoints and endpoint-aware dispatch (#1678, #1679) are live, with
   the posture in § Endpoints.
3. The daemon and relay paths (#1647, #1656) dispatch OpenCode.
4. The documentation (#1649, #1670, #1673) describes only verified behaviour.

## Endpoints: multiple local model endpoints (amendment 2026-09-12)

An operator may run several model servers: two LM Studio instances, or LM
Studio beside Ollama. Each is a named **endpoint** in `opencode.endpoints[]`
(§ 7). #1678 implements the declarations and per-endpoint readiness, and
#1679 implements endpoint-aware dispatch.

- **Instance identity.** The operator-chosen endpoint `id` becomes the
  OpenCode provider key. Nightgauge injects one provider block per endpoint
  under that key, and a stage dispatches with `-m <id>/<model>`. `provider`
  stays the normalized kind (`lm-studio`, `ollama` or `openai-compatible`). It
  drives overlays (§ 14) and records, and with § 3's test of where the model
  runs, cost. Two LM Studio instances are two endpoints of one kind. An id is
  lowercase letters, digits and `-`, at most 32 characters, and unique.
- **Reserved ids.** An id is never a provider key in the catalog bundled with
  the max-tested OpenCode version (§ 20). 1.18.30 bundles 213: the four hosted keys
  of § 1 and every other service it knows, `deepseek`, `mistral`, `openrouter`,
  `groq` and `opencode` among them. OpenCode merges a config provider block into
  the catalog provider with the same key, and whatever the block does not
  override is kept, including the environment variables the provider reads its
  API key from. Observed: a `deepseek` block that set only `baseURL` sent the
  value of `DEEPSEEK_API_KEY` from the environment to that URL as a bearer
  token, even with `enabled_providers` narrowed to that one key. An endpoint
  named after a hosted service would send that service's key to the machine the
  endpoint names, in the LAN case over plain HTTP, and § 1 and § 3 could record
  the stage as local, stamped `cost_usd: 0`. The catalog binds `GITHUB_TOKEN`,
  which every spawn carries, to `github-copilot`. OpenCode also keys its custom
  provider loaders, some of which read credentials of their own, by provider
  key, and every key with a loader in 1.18.30 is a catalog key.

  The one exception is `lmstudio`, and only on an `lm-studio` endpoint. That
  catalog entry is LM Studio itself (`@ai-sdk/openai-compatible` on a loopback
  address, no custom loader), and the only thing it hands on, the
  `LMSTUDIO_API_KEY` binding, is cut by the complete block below. `ollama` and
  `lm-studio` are not catalog keys.

  #1678 refuses a reserved id at config load, naming the catalog key it
  collides with. The reserved set is captured from the max-tested binary into
  `testdata/opencode-cli/`, and § 20 re-captures it whenever max-tested rises.
  A run on the max-tested version sees exactly that catalog: 1.18.30 loads the
  catalog from its cache file and otherwise from the snapshot bundled in the
  binary (read from its bundled source), the per-run cache starts empty, and
  `OPENCODE_MODELS_PATH` goes with every inherited `OPENCODE_*` variable (§ 8).
  No observed run wrote a catalog to its cache. A newer binary can bundle a key
  equal to an endpoint id, or give `lmstudio` a custom loader, and only a
  re-capture shows it, so no endpoint is dispatched above max-tested (§ 20).

- **Complete endpoint blocks.** Every provider block Nightgauge injects for a
  model server the operator runs, the `lmstudio` and `ollama` keys of § 1
  included, sets `npm` for its kind, `env: []`, `options.baseURL` and
  `options.apiKey`. `apiKey` is empty unless the endpoint entry supplies a key
  of its own (#1678). No environment variable can then bind to an endpoint.
  Observed: the `deepseek` and `lmstudio` blocks sent no key once complete. On
  1.18.30 `env: []` alone and an empty `apiKey` alone each stop the binding;
  both are set, so a version that changes how one of them takes precedence
  still binds nothing.
- **The `endpoint` wire label** is the endpoint id and nothing else. It is the
  nullable `endpoint` field of § 2, and it is how every log line, trace event,
  doctor finding and error names an endpoint ("endpoint `lmstudio-remote` is
  not answering"). The `base_url`, host, address and port never appear in any
  record, log, trace, telemetry field, fixture, test or committed file; they
  live only in the machine-tier config. An id cannot carry an address, because
  a dot is not a legal id character. OpenCode itself does not keep to this.
  Observed: when a model request fails, the `--format json` `error` event
  carries the request's full URL in `metadata.url`, while stderr and the log
  file did not name it. The parser (#1624) never copies a URL from an OpenCode
  event, and captured output is redacted of every endpoint's `base_url` before
  it is persisted (#1616).
- **Endpoints on the local network.** An endpoint is loopback by default. A
  `base_url` whose host resolves to anything other than loopback is refused
  unless the entry sets `allow_lan: true`. Even then the address must be a
  private-network one (RFC 1918, or an IPv6 unique-local address), because the
  endpoint mechanism exists for servers the operator runs, and a public host
  would make the stage's local record, stamped `cost_usd: 0`, untrue about
  where the code went. A LAN endpoint reached over `http://` sends prompts and
  repository content across the network unencrypted, so the doctor and the
  first dispatch of each run print a warning naming the endpoint id. Loopback
  over `http://` does not warn.

  ```yaml
  opencode:
    endpoints:
      - id: lmstudio
        provider: lm-studio
        base_url: http://127.0.0.1:1234/v1
      - id: lmstudio-remote
        provider: lm-studio
        base_url: http://<private-network-address>:1234/v1 # machine tier only
        allow_lan: true
  ```

  A documentation address such as `192.0.2.10` (RFC 5737) is neither loopback
  nor private-network, so an entry naming it is refused even with
  `allow_lan: true`, and the refusal names the endpoint id, not the address.

- **An endpoint can forward.** The server at a `base_url` may pass a request
  on: a gateway such as a LiteLLM proxy hands it to a hosted API, and Ollama
  serves its cloud models through its local API from its hosted service.
  Nightgauge sees the endpoint and nothing past it. The loopback and
  private-network checks above say which machine OpenCode sends a prompt to,
  not where the model runs, and #1644's egress check watches only OpenCode's
  own connections (§ 10), so a pass does not say it either. § 3 therefore
  stamps `cost_usd: 0` only for a model known to run on the endpoint. A stage
  on an `openai-compatible` endpoint is stamped only when its entry declares
  `self_hosted: true` (#1678). An Ollama cloud model is refused before spawn
  (#1679), and the refusal's remediation is `ollama-cloud/<model>`, the
  hosted provider key OpenCode's 1.18.30 catalog has for Ollama's service,
  which § 1 records as `other` and § 3 leaves unstamped unless the registry
  prices it.

- **Failover.** A dispatch may move from one endpoint to another only when the
  second serves the same model id (#1679). That covers parallel stages spread
  across endpoints, slot waits, and an endpoint that stops answering. Failover
  never crosses to a hosted provider implicitly. Moving a stage from a model
  the operator hosts to a hosted one changes where the repository's code goes
  and what the stage costs, so it happens only through an explicit fallback
  chain entry the operator configured (#1643), and it is recorded as a
  provider change, not an endpoint change.

## Consequences

- The model layer's one-adapter-one-provider assumption becomes a special
  case. `model_provider` (§ 1) is the answer for a multi-provider adapter, and
  `ProviderFor(adapter, model)` (#1614, #1622) replaces `ProviderForAdapter` at
  every caller that needs the serving provider rather than the executing
  adapter.
- An operator gets an agentic route to a model they host with no hosted
  account, and the local bridges keep their narrower role.
- Until the gate lifts, `opencode` is visible in `nightgauge adapter list` and
  in `--adapter` errors, but dispatching it takes a deliberate environment
  switch, and every such dispatch says what it lacks.
- Two OpenCode behaviours contradicted the plan, and the decisions follow what
  was observed. On a version above max-tested, § 20's self-test re-checks the
  behaviours it lists before the first dispatch. It cannot re-check the
  catalog behind the reserved endpoint ids, so endpoint dispatch stops at
  max-tested until a re-capture raises it.
- ADR-020 gains two reasons for a default to be off, security and privacy, each
  valid only when written down with its row.

## Implementation

This change (#1612) registers the adapter, adds the `PreDispatch` hook, the
enable gate and the interim refusal of `anthropic/*` (§ 17), pins the argv,
the stdin prompt channel and the per-spawn password with tests, and captures
`opencode run --help` for 1.18.30. The rest of epic #1609 implements the
decisions above; the owning issue is named at each decision.
