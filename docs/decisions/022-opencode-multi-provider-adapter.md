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
that are not enforced yet. An `anthropic/*` model is refused while
`ANTHROPIC_API_KEY` is unset, because § 17 gives it no other credential. The
gate stays until those controls exist and the beta decision in § 23 lifts it.

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
| Config precedence, lowest first: the XDG config directory's files, `OPENCODE_CONFIG`, `OPENCODE_CONFIG_DIR`, `OPENCODE_CONFIG_CONTENT`, then the machine's managed config      | § 8                        |
| The catalog binds variables to 213 providers, and any one of a provider's variables makes OpenCode load it: `GROQ_API_KEY` adds groq's models, `AWS_REGION` amazon-bedrock's   | § 8                        |
| `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` send an `anthropic/` or `openai/` run, and its API key, to the server they name                                                     | § 8, § 17                  |
| With no credentials at all, OpenCode's own hosted provider lists its free models                                                                                               | § 8, § 10                  |
| Processes started together on a fresh data directory race its database migration, and all but one fail; once the database exists, concurrent runs share it                     | § 8                        |
| `OPENCODE_CONFIG_CONTENT` wins over the XDG config file, the repository's `opencode.json` and `OPENCODE_CONFIG_DIR` for each key it sets; a lower layer adds other keys        | § 8                        |
| Every merged `mode.<name>` is merged over `agent.<name>` after every layer and made primary, so a lower layer's wins unless the content sets that `mode.<name>` too            | § 8                        |
| A `{file:path}` reference in `OPENCODE_CONFIG_CONTENT` resolves to the file's trimmed content, as it does in a config file                                                     | § 8                        |
| `enabled_providers` narrowed to one key keeps out the providers the forge, AWS and Google Cloud variables would load, and OpenCode's own free models                           | § 8                        |
| Read from the bundled source: a model whose `limit.context` is 0 is never compacted                                                                                            | § 7, § 8                   |
| Read from the bundled source: a model with a `limit.input` compacts at `limit.input` less `compaction.reserved`, so a lower layer's `limit.input` moves the threshold          | § 7, § 8                   |
| A repository `opencode.json` that gives `anthropic` a `baseURL` re-points `ANTHROPIC_API_KEY` to it unless `OPENCODE_CONFIG_CONTENT` sets one                                  | § 17                       |
| An unknown key in `OPENCODE_CONFIG_CONTENT` is dropped without a word                                                                                                          | § 8                        |
| An `instructions` entry naming the resolved path of the `AGENTS.md` OpenCode finds itself loads it once; read from the bundled source, the entry's name is a glob              | § 8                        |
| `mcp` entries of the `local` and `remote` shapes parse in `OPENCODE_CONFIG_CONTENT`, and OpenCode resolves an `{env:VAR}` in them in its own process                           | § 8                        |
| An `{env:VAR}` value is pasted unescaped: a quote, backslash or control character in it fails the parse, whose error prints the config; a `{file:...}` in it is read           | § 8                        |
| While project config loads, OpenCode's own search loads an `AGENTS.md` that is a symbolic link to a file outside the worktree                                                  | § 8                        |
| `opencode debug config` exits 1 on a value of the wrong type in `OPENCODE_CONFIG_CONTENT`, 0 on an unknown key, and prints every other key the content sets                    | § 20                       |
| `opencode models` lists a configured endpoint's model with the endpoint's server stopped                                                                                       | § 20                       |
| `opencode run --help` prints its help on stderr and nothing on stdout                                                                                                          | § 20                       |
| Outside a git repository, OpenCode reads `opencode.json` from the directories above the working directory; `OPENCODE_DISABLE_PROJECT_CONFIG=1` stops it                        | § 20                       |
| `opencode models` lists a hosted provider the config declares no block for only when one of its variables is set, whatever the value                                           | § 20                       |
| `OPENCODE_CONFIG_DIR` holding a hosted provider's `options.apiKey` loads it with none of its variables set; a model entry there adds the model to `opencode models`            | § 20                       |

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
skills, from the home directory, which does not move, and the machine's
managed config, which merges above every layer Nightgauge sets. Nor does it
stop at OpenCode: every tool a stage starts sees the moved directories. § 8
records what closes each.

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
- **The hook** is a new optional adapter method,
  `PreDispatch(context.Context, RunOptions) error`, found by interface
  assertion the same way `ValidateModel` and `ValidateEffort` are.
  `Manager.RunStage` calls it after worktree setup and ahead of the model
  check, the effort check and `BuildCommand`. A refusal therefore states the
  real reason and spawns nothing. It runs after worktree setup so that a check
  which has to read the tree the stage will run in (the project-config tamper
  gate, § 8) can join it. It gets the stage's context, and a stage whose
  context is already done is not dispatched and never reaches it, so the
  version policy's probes (§ 20) start nothing for a cancelled stage and are
  killed with one cancelled while they run.
- **A refusal** names the controls that are missing, the switch, and the way
  out (`--adapter` or `NIGHTGAUGE_ADAPTER`).
- **`anthropic/*` needs `ANTHROPIC_API_KEY`.** § 17 lets Anthropic through
  OpenCode authenticate only with that variable, never with a subscription or
  OAuth login, and run isolation (§ 8) leaves a run no stored login to use.
  `PreDispatch` refuses a model whose provider key is `anthropic` before spawn
  while the variable is unset or empty, whatever the switch says. It checks
  this first, so the refusal states the one reason the switch cannot lift and
  no warning precedes it. The remediation names the `claude-headless` adapter,
  which serves a Claude subscription.
- **A platform provider is refused.** A model whose provider key is one the
  forge's or a cloud platform's credentials serve (`github-copilot`, `gitlab`,
  `amazon-bedrock`, `google-vertex`, `google-vertex-anthropic` and the rest of
  § 8's platform providers) is refused before spawn the same way, first and
  whatever the switch says (§ 17).
- **`~/.opencode` holding config, or the machine's managed OpenCode config,
  refuses an enabled dispatch** unless the operator has opted into their own
  OpenCode config (§ 8), because run isolation cannot keep either out of a
  run. The refusal is made where the run's config and environment are built
  from the machine-tier block (`PrepareOpenCodeRun`), after the gate and
  before anything is created.
- **`nightgauge opencode config` makes every refusal the adapter makes before
  spawning**: it runs `PreDispatch`, the model check and the same preparation,
  so a caller that spawns OpenCode from its output (#1648) meets the same
  gate and refusals.
- **An allowed dispatch** prints a warning to stderr, one line per control
  that is not enforced yet. It is the operator's only disclosure of what the
  dispatch runs without, so it lists every control this ADR assigns to a later
  change. The list is data (`openCodeUnenforcedControls` in
  `internal/execution/adapters/opencode.go`) and holds exactly the rows below,
  in order; `TestOpenCodeUnenforcedControlsMatchADR` fails when the two differ.
  The change that implements a control deletes its entry and its row.

| Control not yet enforced   | Owning change |
| -------------------------- | ------------- |
| failure classification     | #1624, #1631  |
| output redaction           | #1624, #1678  |
| project-config tamper gate | #1638         |
| permission map             | #1638         |
| safety plugin              | #1635, #1640  |
| endpoint policy            | #1678, #1679  |
| stage limits               | #1652         |
| subagent cost              | #1748         |

- **Removal.** #1643 deletes the enable check once the list is empty and
  § 23's beta criteria hold. Nothing else removes it.
- **Cap recovery.** `AdapterUsableForCapHop("opencode")` stays `false` while
  the gate is closed, so a capped run never hops onto an adapter that would
  refuse the dispatch. The doctor's `opencode` row (#1627) reports the closed
  gate as its one blocking finding and runs no other check.
  `TestOpenCodeIsNeverACapHopTargetWhileGated` pins it.
- **Stream parsing.** #1624 gives `opencode` its own parser: it sums every
  `step_finish`'s tokens, folds in the usage of subagent sessions (§ 22), and
  puts the served model (§ 1, § 2), the CLI's version and drift markers on the
  stage's run result. #1630 prices a stage from the registry, runs the USD
  watchdog and writes `model_provider`, `upstream_model` and `endpoint` to the
  stage record (§ 2, § 3), so the row is gone.
- **Stage limits and subagent cost.** The stage's token cap on a hosted model
  is not passed to OpenCode; that row stays with #1652's per-stage token
  ceiling. The USD watchdog cannot stop a stage while its subagents spend,
  because their steps never reach the stream (§ 3); that row stays with #1748
  until a change bounds them while the stage runs.
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
`--share`, `--port`, `--mdns` or `--cors` (§ 15). In 1.18.30 `--auto` and
`--share` are listed `run` options, and a test fails if either stops being
one, because the forbidden list would then be guarding a name that no longer
exists.

`--yolo` and `--dangerously-skip-permissions` are hidden `run` options.
`run --help` does not list them, but 1.18.30 defines both and treats either one
as `--auto`: its bundled source switches on auto-approval when any of the three
is set, and each was accepted exactly as `--auto` was (#1617, observed
2026-09-13). A hidden option never appears in the help, so only the explicit
list catches one. `--mdns` and `--cors` are not `run` options: `run` exits 1 on
either and prints its help to stderr, as it does for any flag it does not
define. All seven stay forbidden, and `TestOpenCodeNeverEmitsBypassFlags` checks
them against every option combination. It catches each flag in the other
spellings yargs takes for an option as well: 1.18.30 accepts
`--dangerouslySkipPermissions`, `--yolo=true` and `--auto.x` as it accepts
`--auto`. The probes are recorded in
`internal/execution/adapters/testdata/cli-help/README.md`. An earlier version
of this paragraph said `--yolo` and `--dangerously-skip-permissions` were not
`run` options.

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
  the local record. When the session export shows that another model served
  the stage, such as an agent's model from a config the run read (§ 10),
  `model` and `model_provider` are the served model's and `upstream_model`
  stays the `-m` value, the only record of what was dispatched.
- `provider` on the V5 stage metric keeps its current meaning, the executing
  adapter, so it reads `opencode`.
- **Yes, the V5 stage metric gains nullable fields**: `model_provider` (§ 1)
  and `endpoint` (§ Endpoints). The parser (#1624) puts `model_provider` and
  `upstream_model` on the stage's run result, beside the recorded `model`;
  #1630 writes them, and `endpoint`, to the local V2 record, on the stage's
  `model_selection`. The platform mapper sends the recorded `model_provider`
  as `modelProvider`, never one derived from the model string, and emits
  `endpoint` only after the platform's strict stage-metric schema accepts it,
  because that schema rejects unknown keys and an early emission would fail
  the whole upload. This is the same local-first pattern `cost_unstamped`
  follows in `internal/platform/execution_history_mapper.go`.

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
- **Re-priced from the registry, at one model's rates.** For a hosted model
  the registry knows, the stage's usage is priced from the registry's rate
  card: input, output and reasoning tokens, plus the cache read and cache
  write pools at the registry's cache rates. No stream event names the model
  that served a step, so every step is priced at one model's rates, never its
  own: the recorded cost at the rates of the model § 2 records as having
  served the stage, which is the dispatched model unless the session export
  names another, and the watchdog below at the dispatched model's. Subagent
  sessions (the `task` tool) are rolled up at the same rates (#1624), once the
  stage has ended, because their steps never reach the stream; a subagent on
  a pricier model of the same provider is therefore under-counted, and the
  `subagent cost` row says so.
- **The USD watchdog bounds the stage's own steps.** The watchdog (#1630)
  prices the stream's steps as they arrive, at the registry rates of the
  dispatched model because no stream event names the model that served a
  step, and stops the stage at the `step_finish` that takes it past its cost
  budget. Once the stage has ended it prices the stage again with its
  subagent sessions folded in, and a stage they took past its budget fails as
  `budget_exceeded` then, so it is never recorded as a success. A partial
  read of that usage (§ 22: more than 64 subagent sessions, an export that
  fails, times out or prints more than 64 MB, the fold's 2-minute budget
  spent, the session list failing, or a stage session id of an unrecognized
  shape) is priced only on what was read, so the budget cannot be verified:
  a stage with a cost budget on a model the registry prices above zero, not
  stopped by the operator, that would otherwise succeed then fails as
  `budget_exceeded`, its `[cost-cap-exceeded]` line saying the budget could
  not be verified because its subagent usage was only partly read. A model priced at zero has no unread usage that could
  cost it anything, and a stage that already failed keeps its own failure.
  The watchdog cannot stop a stage while its subagents spend, and the
  enabled-dispatch warning says so (the `subagent cost` row).
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
being unknown to the registry, and a bare id is not admitted for being known.
Admission is the `-m` shape check and the provider key (#1625): a key must be
a declared endpoint id or a provider in OpenCode's bundled catalog, because
any other key could only be defined by a config Nightgauge does not build
(§ 1, § 7), and a platform provider's key is refused (§ 17). Endpoint
readiness at dispatch (#1646) and per declared endpoint (#1678), and the
refusal of an Ollama cloud model on an endpoint (§ 3, #1679), come later; the
doctor (#1627) probes one endpoint per call so both can reuse it.

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
is read only from the **machine tier**, the file `~/.nightgauge/config.yaml`.
An `opencode:` key in the committed project config refuses every `opencode`
dispatch, and `nightgauge opencode config`, with a message naming the
machine-tier file. The checkout's gitignored `.nightgauge/config.local.yaml`
is not read for it either: a stage can write its own worktree, and the block
decides where the next stage's code is sent. Every value in the block is a
fact about one machine: a binary path, endpoint URLs, local model limits.
Endpoint URLs must never be committed.

```yaml
opencode:
  binary: /opt/opencode/bin/opencode # binary pin: an absolute path, never looked up on PATH; § 20
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

Until #1678 adds `endpoints[]`, the block describes one model server with flat
keys, and its endpoint id follows from its kind: `lmstudio` for `lm-studio`
and `ollama` for `ollama` (#1625). The per-run config is built per endpoint id
all the same, so a second endpoint is a second block, not a new shape.

```yaml
opencode:
  model: lmstudio/qwen/qwen3.8-27b
  provider: lm-studio # lm-studio | ollama; the endpoint id is lmstudio
  base_url: http://127.0.0.1:1234/v1
  limit:
    context: 131072 # at or below what the server has loaded
    output: 8192
  timeouts:
    header: 3m # the default; a cold prefill took 76 s before the first byte
    chunk: 3m
  inherit_user_config: false
  snapshot: false # § 12's defaults; lsp and formatter default to true
```

A stage's turn cap becomes the steps cap of the build agent and each
subagent, and a stage with none gets 200 steps, room for a long stage on a
local model that still ends a session caught in a loop. A dispatch to an
endpoint model whose `limit.context` or `limit.output` neither the machine-tier
`limit` nor discovery from the server (§ 13) gives is refused before spawn, as
is a local provider key no endpoint declares, and any other
key that is neither a declared endpoint id nor a key in OpenCode's bundled
catalog (a second LM Studio's id until #1678 lets it be declared): LM Studio
reports a context limit of 0, OpenCode never compacts a session whose limit is
0, and only a config Nightgauge does not build could give such a key a block.
The endpoint's model block sets `limit.input` to `limit.context` and
`compaction.reserved` to the output limit: on 1.18.30 a model with a
`limit.input` compacts at `limit.input` less `reserved`, so a lower layer
could otherwise add one and lift the threshold past the loaded window, and
with both set the threshold is `limit.context` less `limit.output`. A
`base_url` must be `http` or `https` and carry no user name or password; one
whose host is not this machine is accepted, and `nightgauge opencode config`
reports it `non_loopback: true`, as it does every hosted provider's model.
#1678 decides which non-loopback hosts are refused.

### 8. Isolation and project config

- **XDG layout.** Each run gets one root, shared by its stages and outside
  every worktree and repository: `~/.nightgauge/opencode/runs/<run_id>/`,
  created with mode 0700, as are its four directories. A root or directory
  that is a symbolic link is refused, so nothing is written through one.
  `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME` and `XDG_STATE_HOME`
  point at its `config/`, `data/`, `cache/` and `state/`. OpenCode 1.18.30
  writes `config/opencode/` (a config file and its own `.gitignore`),
  `data/opencode/` (`opencode.db` with its WAL files, `log/`, `snapshot/`,
  `repos/`), `cache/opencode/bin/` and `state/opencode/locks/`. `home` does not
  move, and OpenCode's `tmp` stays at `$TMPDIR/opencode`. A dispatch with no
  run identity mints a root id of its own, which is never exported as
  `NIGHTGAUGE_RUN_ID`, and deletes that root when it returns. § 22 decides when
  every other root is deleted. Stages of one run share the session database,
  which is safe once it exists. On a fresh data directory, processes started
  together race its migration and all but one fail, so the first stage of a run
  must start alone. The Go path runs a run's stages one at a time; a path that
  starts them in parallel starts one first (#1648).
- **Tools that move with XDG.** The move would also take from a stage's tools
  what they read from the operator, so each is given back what it resolves to
  outside the run, computed from the environment Nightgauge inherited. The
  root's `config/` holds a symbolic link to every entry of the operator's XDG
  config directory (`$XDG_CONFIG_HOME`, else `~/.config`) except `opencode/`,
  the only entry of it OpenCode 1.18.30 loads config from, which stays the
  run's. A tool that keeps its config there therefore reads the operator's:
  git its XDG config, `ignore`, `attributes` and `credentials` files, beside
  `~/.gitconfig` and in the same order, and uv, pip or podman the private
  package index or registry that would otherwise fall back to a public
  default. An inherited
  `GIT_CONFIG_GLOBAL` passes through. `GIT_CONFIG_GLOBAL` holds one file, and
  `git config --global` does not follow an `[include]`, so a link is the one
  form that keeps all three layouts of an operator's git config identical.
  Each stage links what the operator has added since and re-points a link
  whose target moved; an entry the run created itself, while the operator had
  none, is left as it is. `GH_CONFIG_DIR` is set as well, to the operator's gh
  directory (`GH_CONFIG_DIR`, else `$XDG_CONFIG_HOME/gh`, else
  `~/.config/gh`), where gh keeps its hosts and auth. `NIGHTGAUGE_CONFIG_HOME`
  is the directory of the machine-tier config the nightgauge process reads,
  the Linux legacy `~/.nightgauge` included, so a `nightgauge` command in a
  stage keeps the machine tier. `GOCACHE`, when the operator has not set it, is
  their Go build cache, which on Linux would otherwise move with
  `XDG_CACHE_HOME`. Deleting the root unlinks every link and never touches a
  target. The run's `data/`, `cache/` and `state/` are its own, so any other
  tool that keeps state or a cache there starts empty.
- **The per-run config.** One builder (`BuildOpenCodeConfig`, #1625) makes
  the config a stage runs under, and everything it sets goes in
  `OPENCODE_CONFIG_CONTENT`: the provider block of the endpoint or hosted
  provider the stage dispatches to (§ 1, § 17, § Endpoints), keyed by endpoint
  id; the locked keys of § 15; `enabled_providers` narrowed to the dispatched
  key; the limits, compaction policy, tool-output caps and steps cap; and
  § 12's settings; and the repository's steering as `instructions` entries and
  the pipeline's MCP servers (below, #1626). The permission map (§ 9, #1638)
  and the plugin list (#1635) extend the same builder, so there is no second
  writer. Observed on 1.18.30, OpenCode merges its
  config in this order, lowest first: the files in the XDG config directory,
  `OPENCODE_CONFIG`, the repository's files, the config directories
  (`OPENCODE_CONFIG_DIR` last among them), `OPENCODE_CONFIG_CONTENT`, and then,
  read from the bundled source, an active console organization's remote config
  and the machine's managed config (below). `OPENCODE_CONFIG_CONTENT` wins
  over every layer below it for each key it sets, an inherited operator config
  included, so no lower layer can change the `id` or `provider.npm` a
  declared endpoint's or `anthropic`'s dispatched-model entry sets (the
  fourth rule below), a declared endpoint's limits or base URL, the
  `anthropic` block's API root, or turn sharing back on. Pinning those keys
  does not pin the model actually served (see the fourth rule and § 15, § 17,
  #1638). Nor does winning a key pin a permission pattern map: observed by
  #1632, the content wins each pattern's action but not its position, the
  merged map keeps the key order of the lowest layer that has the key, and the
  last matching rule wins, so a lower layer that lists the content's patterns
  in another order changes what they resolve to (the results table below).
  A lower layer can add keys the content does not set, and
  four merge rules needed more than setting a key. First, every merged
  `mode.<agent>` is merged over `agent.<agent>` after every layer and forced
  to `mode: "primary"`, so a lower layer's mode entry would win over the
  content's agent entry. The content sets the mode entry of every built-in
  primary agent, `build`, `plan`, `title`, `summary` and `compaction`, the
  same as its agent entry. It cannot do so for the `general` and `explore`
  subagents without making them primary agents, so a lower layer's
  `mode.general` or `mode.explore` still replaces that subagent's model (on
  the dispatched provider only) and steps cap; the tamper gate drops it
  (#1638), and until then its warning line says so. Second, a model's
  `limit.input` decides its compaction threshold, so the endpoint's model
  block sets it (§ 7). Third, `instructions` are concatenated across layers,
  not replaced, so the content's list removes none; the repository's are the
  tamper gate's (#1638). Fourth, read from the 1.18.30 bundled source
  and observed, a model entry's `id` is the model name OpenCode sends and its
  `provider.npm` the SDK package it loads, both in place of the provider
  block's: with a repository entry giving the dispatched model an `id` of its
  own, a run sent that id to the endpoint. So the content's entry for the
  dispatched model sets both, to the model the stage names and the block's
  package, for a declared endpoint and for `anthropic` (§ 17). Pinning `id`
  and `provider.npm` does not pin the request `@ai-sdk/openai-compatible`
  and the Anthropic SDK actually send: reproduced on 1.18.30, a repository
  can add `options.model` to the same model entry
  (`provider.<key>.models.<id>.options.model`) or to an agent's own options
  (`agent.<name>.options.model`), or a variant, which merges last, and the
  SDK spreads an unknown `providerOptions` key into the request body after
  the pinned `id`, so the served model still changes. On `anthropic`, the
  same options object can set `speed` or `fallbacks`, which the SDK turns
  into its own beta headers. An agent's own `options.mcpServers` can also
  give an MCP server an `authorizationToken` of `{env:ANTHROPIC_API_KEY}` (or
  any other variable the run holds), which OpenCode resolves and sends to a
  URL the repository names. None of these three routes touches a key this
  config sets, so the fourth rule does not close them; #1638 is the control
  that does, and until then the tamper-gate warning line discloses them. A
  hosted provider other than `anthropic` gets no block, so a lower layer's
  block for it can still set its `baseURL` and the model its stage is sent
  as, which the endpoint-policy warning line discloses. A hosted model's
  limits, `anthropic`'s included, come from OpenCode's catalog, and a lower
  layer's model entry can replace them, which the stage-limits warning line
  discloses. The organization's config needs a console account in the session
  database, which starts empty in every run, and managed config refuses a
  dispatch, so without the opt-in a run reads no layer above the per-run
  config.
- **Provider URLs never go into the environment.** Every tool a stage runs
  inherits the environment and many print it, so an endpoint's base URL is
  written to a 0600 file in the run root's own `nightgauge/` directory (0700,
  outside the four XDG directories), and the block's `baseURL` is a
  `{file:...}` reference to it. Observed on 1.18.30, OpenCode resolves such a
  reference in `OPENCODE_CONFIG_CONTENT` as it does in a config file. A
  credential is never in the content either: the `anthropic` block's key is
  the reference `{env:ANTHROPIC_API_KEY}`, beside its SDK package and
  Anthropic's API root, `https://api.anthropic.com/v1`, the values the
  1.18.30 catalog uses (§ 17). OpenCode substitutes both
  kinds of reference in the config text before parsing it, so a model id with
  a brace is refused.
- **The environment.** Every inherited `OPENCODE_*` variable is removed, and
  only the variables this ADR names are set: the four XDG variables and the
  pins above; the switches of § 10 and § 11, `OPENCODE_DISABLE_MODELS_FETCH`,
  `_AUTOUPDATE`, `_LSP_DOWNLOAD`, `_DEFAULT_PLUGINS`, `_SHARE`,
  `_CLAUDE_CODE_PROMPT`, `_CLAUDE_CODE_SKILLS` and `_EXTERNAL_SKILLS`, each
  `1`; `OPENCODE_SERVER_PASSWORD` (§ 18); `OPENCODE_CONFIG_CONTENT` (#1625);
  and `OPENCODE_CONFIG_DIR` under `inherit_user_config` only. An inherited
  value of a name Nightgauge sets is replaced, never left beside it. The
  operator's shell can then never change a pipeline run's posture through an
  OpenCode variable. Also removed is every variable OpenCode's bundled catalog
  binds to a model service other than the one the stage dispatches to,
  because any one of a provider's variables makes OpenCode load it. The
  snapshot is read from the 1.18.30 binary, 213 provider keys
  (`internal/execution/adapters/opencode_catalog_env.go`): `ANTHROPIC_API_KEY`,
  `OPENAI_API_KEY`, `XAI_API_KEY`, `OPENROUTER_API_KEY`, and `GOOGLE_API_KEY`,
  `GOOGLE_GENERATIVE_AI_API_KEY` and `GEMINI_API_KEY` for `google`, and the
  rest of the catalog's, such as `GROQ_API_KEY` and `DEEPSEEK_API_KEY`.
  `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` are removed whatever the
  provider: the bundled Anthropic and OpenAI SDKs read them as the provider's
  endpoint, and each was observed to send a run and its key to the server it
  named, so a provider's endpoint comes from config only (§ Endpoints). A
  stage's own tools share the environment, so they do not get the removed
  variables either. A tool that needs one fails without it, or uses a login of
  its own, so every enabled dispatch names on stderr each removed variable the
  environment holds, never a value.
- **Platform credentials stay.** Some catalog providers bind the variables of
  a general-purpose platform account, which a stage's tools read for work that
  is not a model request: the forge (`GITHUB_TOKEN` and `GITLAB_TOKEN`, bound
  to `github-copilot` and `gitlab`), AWS (`amazon-bedrock`), Google Cloud
  (`google-vertex` and `google-vertex-anthropic`), Cloudflare, Databricks,
  DigitalOcean, Snowflake, Hugging Face, Weights & Biases and Vultr. Every
  variable the catalog binds to one of them stays, whatever the provider, and
  a dispatch to one of those providers is refused (§ 17).
  Removing part of such a family does not leave a tool without credentials: it
  moves the tool to the next source in the platform's credential chain, which
  can be another account in another region, and nothing says so. With
  `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` removed and
  `AWS_SESSION_TOKEN` left, the AWS CLI reads `~/.aws/credentials` and
  `~/.aws/config` instead. With `GOOGLE_APPLICATION_CREDENTIALS` removed and
  `GOOGLE_CLOUD_PROJECT` left, Google's clients use the operator's own
  application default credentials, and so does OpenCode's `google-vertex`,
  which, read from the 1.18.30 bundled source, loads on `GOOGLE_CLOUD_PROJECT`
  as well.
- **What the environment does not decide.** A run on a local model holds no
  hosted model service's catalog variable. That narrows which providers a run
  can load; it does not decide it. OpenCode can load a platform provider on
  the credentials the stage keeps, a provider's own loader can find
  credentials elsewhere (`amazon-bedrock` also loads on `AWS_PROFILE` and the
  AWS credentials file), and OpenCode's own hosted provider serves its free
  models with no key. The per-run config closes those routes to another
  model: `enabled_providers` holds the dispatched provider key alone, and every
  model a run uses is pinned (§ 15, #1625). Observed on 1.18.30, a run holding
  `AWS_REGION`, `GITHUB_TOKEN`, `GITLAB_TOKEN` and `GOOGLE_CLOUD_PROJECT`
  loaded `amazon-bedrock`, `github-copilot`, `gitlab`, `google-vertex`,
  `google-vertex-anthropic` and OpenCode's own free models beside the
  dispatched provider without it, and the dispatched provider alone with it.
- **The home directory.** `home` does not move, and OpenCode 1.18.30 reads two
  operator locations from it whatever the XDG variables say.
  `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` closes `~/.agents/skills` and
  `~/.claude/skills`. `~/.opencode` is a config directory to OpenCode: it loads
  its `opencode.json`, `opencode.jsonc`, and `agent`, `command`, `mode`,
  `plugin`, `tool` and `skill` directories under either spelling. Neither
  `OPENCODE_DISABLE_PROJECT_CONFIG` nor `OPENCODE_PURE` stops that read, and
  moving `HOME` would move every other tool with it. So while `~/.opencode`
  holds any of those entries, an enabled dispatch is refused before anything
  is created, naming the entries without reading them, unless
  `inherit_user_config` is on. What
  an install leaves there (`bin/`, a `package.json` and its `node_modules`) is
  not config. The remediation is to move the entries into the XDG config
  directory, which the operator's own OpenCode reads and a pipeline run does
  not.
- **The machine's managed config.** Read from the 1.18.30 bundled source and
  observed: OpenCode reads `opencode.json` and `opencode.jsonc` from a managed
  config directory, `/etc/opencode` on Linux and
  `/Library/Application Support/opencode` on macOS, and on macOS the
  managed-preferences profile `ai.opencode.managed.plist` under
  `/Library/Managed Preferences`, the user's and then the machine's. It merges
  them above `OPENCODE_CONFIG_CONTENT`, so a key there wins over every key
  Nightgauge sets, the locked keys of § 15 included, and nothing in a run's
  environment moves them. While any of those files exists, an enabled
  dispatch is refused before anything is created, naming the files without
  reading them, unless `inherit_user_config` is on, which accepts the
  machine's config with the operator's own. Both refusals are made by
  `PrepareOpenCodeRun`, which builds the run's config and environment from
  the same read of the machine-tier block, so the opt-in can never read as on
  for the refusal and off for the environment; `nightgauge opencode config`
  runs it too. The doctor's `opencode` row blocks on each, naming what it
  found, through `OpenCodeMachineConfigRefusals`, which makes the same two
  checks on the same inputs, so cap recovery never hops onto a machine that
  refuses every dispatch (#1627).
- **`inherit_user_config`** defaults to `false`: the operator's global
  OpenCode config is not read. The way to turn it on is
  `opencode.inherit_user_config: true` in the machine tier (§ 7), which a
  committed repository config cannot set, and every dispatch it applies to
  says so on stderr. The interim `NIGHTGAUGE_OPENCODE_INHERIT_USER_CONFIG`
  variable is gone (#1625). The opt-in sets `OPENCODE_CONFIG_DIR` to the
  operator's OpenCode config directory (`$XDG_CONFIG_HOME/opencode`, else
  `~/.config/opencode`), which brings back its `opencode.json`,
  `opencode.jsonc` and directories, and `~/.opencode` loads as it does outside
  a run. That config is layered under `OPENCODE_CONFIG_CONTENT`, so every key
  the per-run config sets still wins over it, apart from the `general` and
  `explore` mode entries above: an inherited provider block can add to the
  one Nightgauge injects, such as a header, and can change nothing Nightgauge
  sets in it. The opt-in also lifts the managed-config refusal, and
  a managed config wins over the per-run config, which the stderr line says.
  Stored logins are never inherited either way, because they live in the data
  directory, which stays per run (§ 17). An API key written into the
  operator's config is inherited, and the stderr line says so.
- **The target repository's `opencode.json`, `opencode.jsonc` and
  `.opencode/**`.** `OPENCODE_DISABLE_PROJECT_CONFIG=1` is the intended
  control, so that OpenCode never loads them and Nightgauge reads them
  instead, but it is not set on any spawn yet, so the repository's files
  still load (see the adversarial results below). Keys outside the locked set
  are meant to merge into the per-run config, and a locked key the repository
  sets is meant to be dropped with a warning, once the switch is set. The
  tamper gate records the files' hashes when the worktree is created, and
  `PreDispatch` refuses a dispatch when they have changed since, because a
  stage must not rewrite the config the next stage runs under. #1638 (with
  #1626) sets the switch.
- **Steering.** Observed: that switch also hides the repository's `AGENTS.md`
  and `CLAUDE.md`. The repository's steering therefore reaches OpenCode only
  as `instructions` entries, never because OpenCode finds it, and the per-run
  config carries them whether the switch is set or not (#1626). Each is an
  absolute path, the form observed to load in every configuration: the
  repository's steering file, by the rule Codex and Gemini steering already
  use (`AGENTS.md`, or `CLAUDE.md` without its `@AGENTS.md` import only when
  `AGENTS.md` has no content of its own); every file it imports; and a file in
  the run root's `nightgauge/` directory holding the baseline steering that
  Codex's managed `AGENTS.md` block comes from, the one function both use.
  Nothing is written into the worktree. Imports resolve as Claude Code
  resolves them, relative to the importing file, up to three deep, and no
  file is given twice, so a cycle ends. They stay inside the worktree: a URL,
  a path in the home directory, an absolute path, one whose `..` leaves the
  worktree and a symbolic link out of it are each left out with a warning on
  stderr. Every file Nightgauge reads for the stage, the baseline steering's
  sources and the worktree's MCP files included, is read only when it is a
  regular file inside the worktree once its symbolic links are resolved, and
  only its first MiB. A link out of the worktree, a FIFO or a device is passed
  over with a warning, and the rule falls through to the next source, such as
  `CLAUDE.md` for an `AGENTS.md` linked out of the worktree. So nothing
  Nightgauge gives OpenCode comes from outside the repository. OpenCode's own
  search, which reads the repository's `AGENTS.md` while project config loads,
  is not confined by this: observed, it loads an `AGENTS.md` linked out of the
  worktree, and switching project config off (#1638) ends it. Read
  from the 1.18.30 bundled source, OpenCode loads an absolute entry by
  globbing its last element in its directory, so an entry whose name holds a
  character a pattern could read is left out too. Every path is written with
  its symbolic links resolved, the form of the paths OpenCode finds itself,
  so a file both name loads once.
- **MCP servers.** The per-run config's `mcp` holds the pipeline's MCP
  servers, the ones a Claude stage gets from `.mcp.json` and
  `.claude/settings.json`, and no others (#1626). They are read from the
  forge, not from the worktree and not from the repository on the machine: a
  server is a command OpenCode runs or a URL it sends tool calls to, and a
  stage can write its worktree, so a server one stage added to `.mcp.json`
  would otherwise start in the next without review. A stage can write the
  rest of the repository too, and every worktree of it shares that: the refs
  and objects (a moved `origin/main`, a deleted object, a `refs/replace/`
  entry), the git config that says where origin is and how git reaches it
  (`remote.origin.url`, `url.<x>.insteadOf`, transport settings git would run
  in the orchestrator's environment), and its worktree's `.git` file. So none
  of it is read and no `git` command runs for the servers. The repository is
  the one the pipeline records for the run (the dispatch's target repository,
  `owner/name`; `nightgauge opencode config` takes it as `--repo`), never one
  parsed from the worktree's git config. One GitHub GraphQL query, through
  the pipeline's GitHub client and the identity the workspace config names for
  the repository's owner, returns the default branch, the commit at its head
  and `.claude/settings.json` and `.mcp.json` at that commit, so both files
  come from the commit the answer names; a file that commit does not have
  gives no server, and one it has as a symbolic link is not followed. The
  read, the identity's token included, is bounded to 15 seconds, and a `gh`
  it runs for the token is killed at the deadline. When the read fails, times
  out, or the run records no repository, the stage gets no MCP server and one
  warning on stderr says why; nothing falls back to a local ref. A warning
  names a server only the worktree defines, one it defines differently, and
  one only the default branch defines, and stderr names the repository,
  branch and commit the servers were read at. The servers are what GitHub
  serves for that repository's default branch, and no write a stage makes on
  the machine changes them. Each `${VAR}` becomes OpenCode's `{env:VAR}`, so
  no variable's value is in the content, and a value already holding
  OpenCode's `{env:...}` or `{file:...}` syntax refuses its server, because
  OpenCode would substitute it. OpenCode resolves a
  `{env:VAR}` in its own process by pasting the variable's value into its
  config text before parsing it, unescaped (read from the 1.18.30 bundled
  source, and observed): a quote, a backslash or a control character in the
  value makes the whole config fail to parse, and OpenCode's error prints the
  substituted config, every resolved credential in it, and a `{file:...}` in
  the value is read as a file reference. So each variable a server names is
  checked in the environment OpenCode is spawned with, its value never
  recorded: the run's isolation variables (and `OPENCODE_CONFIG_CONTENT`,
  which always holds a quote) laid over the inherited environment, less the
  variables the spawn withholds. A server one of whose variables holds such a
  value is left out with a warning naming the variable. The builder then
  checks every `{env:VAR}` the finished content holds the same way,
  `{env:ANTHROPIC_API_KEY}` included,
  and refuses the dispatch, naming the variable, when one holds such a value:
  a key read from a file with CRLF line endings ends in a carriage return, and
  would otherwise fail the parse and print every MCP credential beside it. A
  remote server is given `oauth: false`: OpenCode's OAuth flow needs a browser
  login and a callback server on the machine, which a headless stage cannot
  complete. The operator's own servers
  stay out with the rest of their OpenCode config (`inherit_user_config`,
  below); a repository's `opencode.json` can still add a server of its own,
  which OpenCode starts beside these, until the tamper gate closes that route
  (#1638), and until then the tamper-gate warning line says so.
- **`--pure` is never passed.** It would also drop the Nightgauge plugin.
  Plugins are controlled by the per-run `plugin` list (the Nightgauge plugin
  and nothing else) and `OPENCODE_DISABLE_DEFAULT_PLUGINS=1`. Observed by
  #1632, the per-run list controls them only while project config is
  disabled: it is concatenated with the repository's list, and an empty
  per-run list removes none of the repository's plugins, although
  `opencode debug config` then reports `plugin: []`. Separately, and whatever
  the plugin list or `--pure` says, every run starts a background npm install
  of `@opencode-ai/plugin` into each config directory it loads, the run's own
  XDG config directory always among them, and a run that loads a plugin waits
  for it; § 10 lists the request.

#### Adversarial results (#1632)

`internal/execution/adapters/opencode_merge_contract_test.go` (build tag
`opencode_integration`, run in CI against an exact install of 1.18.30) drives
the binary with the repository-supplied fixtures in
[`internal/execution/adapters/testdata/opencode-adversarial/`](../../internal/execution/adapters/testdata/opencode-adversarial/README.md)
and asserts these answers. "Inline" is `OPENCODE_CONFIG_CONTENT`, the layer the
per-run config uses; "project" is the repository's `opencode.json` and
`.opencode/`.

| #   | Question                                                                         | Observed on 1.18.30                                                                                                                                                                                                                                                                                                           |
| --- | -------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Does `--pure` skip a project `.opencode/plugins/*.ts` file and `plugin[]` entry? | Yes, both: neither loads, and the npm plugin is never requested. Without it both load. `--pure` does not stop the background install of `@opencode-ai/plugin` every run starts (§ 10): a run with no plugin at all makes that registry request once it lives a few seconds.                                                   |
| 2   | Does an inline `permission.bash` deny beat a project allow?                      | Yes for `bash: deny` over `bash: allow`, and for `{"rm -rf *": "deny"}` inline over `{"*": "allow"}`. No when the project lists the same patterns first: with `{"rm -rf *": "allow", "*": "allow"}` in the project and `{"*": "allow", "rm -rf *": "deny"}` inline, the merged order puts the deny first and `rm -rf x` runs. |
| 3   | Do `plugin`, `instructions` and `mcp` concatenate or get replaced?               | `instructions` and `plugin` concatenate, the project's first; `mcp` merges by server name, the inline entry winning a name both set. An empty inline list removes nothing. A project `instructions` URL is fetched: with an empty inline list, the run requested it from a loopback stub before its model request.            |
| 4   | Does a project `provider.<key>.options.baseURL` override the injected one?       | No: the inline `baseURL` wins, in the resolved config and in the request. A key the inline block does not set, such as a header, still comes from the project and is sent.                                                                                                                                                    |
| 5   | Does config and rules discovery walk above the worktree root?                    | No. From a worktree at `<checkout>/.nightgauge/worktrees/<repo>-issue-<N>`, neither the checkout's `opencode.json`, `.opencode/` or `AGENTS.md` nor anything above it loads: discovery stops at the git root of its starting directory. Outside any git repository it walks up.                                               |
| 6   | What does `OPENCODE_DISABLE_PROJECT_CONFIG` disable?                             | Every project key the fixture sets (`agent`, `permission`, `instructions`, `plugin`, `mcp`, `provider`), all of `.opencode/` (config, agents, commands, skills, plugins), the repository's `AGENTS.md`, and the loading of those plugins. An inline `instructions` entry with an absolute path still loads.                   |

So a repository config that sets permissions, plugins, providers, MCP servers
or instructions changes nothing about a run only while
`OPENCODE_DISABLE_PROJECT_CONFIG=1` is set (row 6). Until every spawn sets it
(#1626, #1638), the repository's config loads, and rows 2 to 4 are what it can
do: reorder the per-run permission patterns; add plugins, MCP servers and
instructions the per-run lists cannot remove, a remote `instructions` URL among
them, which a run fetches before its model request; and add keys to an injected
provider block. #1638 and #1635 build on these answers.

### 9. Headless posture

Observed: in `opencode run`, a permission that resolves to `ask` is rejected
automatically. OpenCode prints `! permission requested: <permission>
(<pattern>); auto-rejecting`, the tool call fails with "The user rejected
permission to use this specific tool call.", the run ends after that step, and
the process **exits 0**. An `ask` is a silent stop that looks like success.
#1624's captures add four details (see
`internal/execution/testdata/README.md`, § OpenCode): the line carries
terminal escape codes around the `!` even when stderr is not a terminal; it
names the permission, which is `edit` for the write tools, and not the tool;
it is printed for every subagent session as well as the run's own; and the
patterns are the call's input printed unescaped, so a call whose input holds
a newline (a heredoc, a commit message with a body) spreads the notice over
several lines, only the last ending in `); auto-rejecting`.
#1629's subagent capture adds a fifth: a subagent's rejection does not end
the run. The subagent's session ends, the run's `task` call fails with
`Subagent failed (task_id: <session>): ` followed by the rejection message,
and the run goes on and exits 0. That notice is still the first on stderr, so
it decides the stage's marker below, and a stage whose own session finished
its task reports exit 1.

- Permission maps Nightgauge generates contain only `allow` and `deny`, never
  `ask`. That covers the permissions OpenCode defaults to `ask`, such as
  `external_directory`. `allow` was observed to run a tool with no
  auto-approve flag, and `deny` removes the tool from the model's tool list.
- `--auto` and its hidden aliases `--yolo` and `--dangerously-skip-permissions`
  (§ The command) are never emitted. Approval is the map's job, derived from
  the stage's allowed tools (#1638).
- The parser classifies a rejected-permission tool event as a failure, exit
  code notwithstanding (#1624, #1631). It reads the stderr notice as OpenCode
  printed it, before redaction, takes the permission from its first line, and
  ends the stage's stderr with `[adapter-permission-rejected] tool=<permission>`
  when the stage's allowed tools grant the permission and
  `[permission-denied] tool=<permission>` otherwise; an exit-0 run with either
  reports exit code 1. A marker names only a permission 1.18.30 asks for
  itself, and `unknown` for any other, such as an MCP tool's. The patterns are
  the model's own text, and the stage's stderr is what classification reads,
  so the stderr the stage keeps holds the notice without them, and none of the
  lines they span; none of those lines is read as a notice of its own. Being
  unescaped, the patterns decide where the notice seems to end: a line of them
  that itself ends in `); auto-rejecting` ends it early, and nothing tells
  their later lines from what OpenCode prints next. So from the first notice
  on, the stage keeps no stderr line but the parser's own, and a drift marker
  counts the lines it dropped. The first notice decides: OpenCode printed it
  before any rejected input, so its permission alone yields the stage's one
  marker, which is the last line of the stage's stderr. A later notice,
  whether a subagent's or one the patterns forged, is drift only: a drift
  marker counts it, and it yields no marker, so it cannot change the kind the
  last lines of stderr classify as. When the stream shows OpenCode's
  rejection error on the stage's own tool call and stderr named no
  permission, the run still fails, with `[permission-denied] tool=unknown`
  and a drift marker: the event names the tool, and a rejection of
  `external_directory` or `doom_loop` is one the tool's name does not show.
  Nothing else of the transcript is read. #1631 owns the failure kinds.
- The project directory OpenCode uses is the resolved path, so an absolute
  path through a symlinked prefix (such as macOS `/tmp`) reads as an external
  directory. The permission map is built against resolved paths.

### 10. Egress defaults

| Egress                                  | Pipeline default                                                                                                                                                       |
| --------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Session share                           | `share: "disabled"`, `OPENCODE_DISABLE_SHARE=1`, `--share` never emitted                                                                                               |
| Autoupdate                              | `autoupdate: false`, `OPENCODE_DISABLE_AUTOUPDATE=1`                                                                                                                   |
| Model-catalog fetch                     | `OPENCODE_DISABLE_MODELS_FETCH=1`; the registry prices, not the catalog                                                                                                |
| LSP server download                     | `OPENCODE_DISABLE_LSP_DOWNLOAD=1`                                                                                                                                      |
| Default plugins                         | `OPENCODE_DISABLE_DEFAULT_PLUGINS=1`                                                                                                                                   |
| Remote `instructions` and `skills.urls` | refused: only absolute paths inside the worktree or the per-run root; until project config is disabled, a repository `instructions` URL is fetched (§ 8, #1638, #1644) |
| `webfetch`                              | `deny` unless the stage's allowed tools include web fetch                                                                                                              |
| Web search                              | off; its enabling variable is stripped with every inherited `OPENCODE_*`                                                                                               |
| Session-title generation                | `agent.title.disable: true`, so no title request is sent; `small_model` locked (§ 15)                                                                                  |
| Plugin dependency install               | none: every run starts an npm install of `@opencode-ai/plugin` (§ 8, #1644)                                                                                            |

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
- `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` is set as well. Observed on 1.18.30,
  the operator's `~/.agents/skills` load into every run without it, the same
  personal content as `~/.claude/skills` by another path. It also stops
  OpenCode finding a repository's `.agents/skills` by itself, which is § 8's
  rule anyway: Nightgauge renders a stage's skills (#1666), and OpenCode
  discovers nothing.
- The repository's `CLAUDE.md` fallback reaches a run because Nightgauge
  injects it (§ 8, #1626), never because OpenCode finds it: `_PROMPT` hides it
  from OpenCode's own search.
- OpenCode never followed an `@import` in any configuration observed.
  Nightgauge resolves each into an `instructions` entry of its own (§ 8), and
  skips the `@AGENTS.md` import line of a `CLAUDE.md` it gives.

### 12. Pipeline defaults for `snapshot`, `lsp` and `formatter`

| Setting     | Pipeline default                  | Operator may override (machine tier) | Why                                                                                                                           |
| ----------- | --------------------------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| `snapshot`  | `false`                           | yes                                  | The worktree and its git history already undo anything; a snapshot is a second copy of the tree in the per-run data directory |
| `lsp`       | on, for servers already installed | yes, off                             | Diagnostics after an edit add value; the download of new servers is locked off (§ 10)                                         |
| `formatter` | on                                | yes, off                             | Formatting on edit adds value; turn it off where the repository's own hook formats                                            |

The overrides are `opencode.snapshot`, `opencode.lsp` and `opencode.formatter`
in the machine tier (§ 7). The config schema bundled in OpenCode 1.18.30
documents an omitted `lsp` or `formatter` as off, so the per-run config always
sets all three (#1625).

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

_Amendment 2026-09-14 (#1633, as implemented)._ The descriptor is discovered
from the endpoint's server once per process, kept in memory keyed by endpoint
id and model id, and never written to disk; a failed discovery is kept too,
not retried. LM Studio is read from `GET /api/v0/models` and Ollama from
`POST /api/show`. The per-run config (§ 7) takes each limit from the
machine-tier `limit` where it sets one, and otherwise from the descriptor; a
`limit.context` above the discovered loaded window is clamped to it with a
warning, because the server fails a request past it. Observed on LM Studio
0.4.24 and Ollama 0.32.11 (the captures, their field names and their
provenance are in `internal/models/testdata/local-discovery/`), three facts
differ from what #1633 assumed:

- LM Studio's `/api/v0/models` reports the loaded window
  (`loaded_context_length`, never `max_context_length`) and tool use
  (`capabilities: ["tool_use"]`), but no output cap and no reasoning flag. An
  LM Studio descriptor therefore has no `max_output` and no `reasoning`, and
  the per-run output limit for it is the smaller of OpenCode's own
  32 000-token reply cap (what 1.18.30 asks for when `limit.output` is 0) and
  a quarter of the window, unless the machine-tier `limit.output` sets one.
- Ollama's `/api/show` carries `num_ctx` only when the model's Modelfile sets
  it. Without one, Ollama loads the model with a default derived from the
  machine's memory and capped at the trained context, which `/api/show` does
  not report and `/api/ps` reports only while the model is loaded. Such a
  model is unresolved: the operator sets `num_ctx` in its Modelfile, or
  `opencode.limit.context`.
- Ollama reports reasoning (`capabilities` includes `thinking`) and an output
  cap (`num_predict` in `parameters`, when the Modelfile sets one); its
  descriptor carries both.

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

| Capability                       | Disposition                  | Owner or reason                                                                                                                                                                |
| -------------------------------- | ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Skills                           | supported                    | #1666 (install target), rendered per stage                                                                                                                                     |
| Commands                         | supported                    | #1666 (`configs/opencode` templates)                                                                                                                                           |
| Subagents (`task`)               | denied (AC9 fallback)        | #1624 rolls subagent usage into the stage, but the plugin denies `task` unconditionally until AC9 is settled — see the "Nightgauge OpenCode plugin" amendment dated 2026-09-15 |
| Plugins                          | supported, Nightgauge's only | #1635, #1640, #1641, #1642                                                                                                                                                     |
| MCP                              | supported                    | #1626                                                                                                                                                                          |
| Permissions                      | supported                    | #1638                                                                                                                                                                          |
| Sandboxing                       | non-goal                     | OpenCode has none upstream; containment is § 8 isolation, the permission map and the worktree                                                                                  |
| Resume and fork                  | deferred                     | #1643 (session resume within a run)                                                                                                                                            |
| Export                           | supported, sanitized only    | § 22                                                                                                                                                                           |
| Import                           | non-goal                     | a session file or URL is untrusted input with nothing to gain                                                                                                                  |
| Usage (`opencode stats`)         | non-goal                     | usage comes from the stream and the registry (§ 3), not OpenCode's catalog prices                                                                                              |
| `json_schema` output             | deferred                     | #1650                                                                                                                                                                          |
| Variants (`--variant`)           | supported                    | #1643 maps effort to a variant                                                                                                                                                 |
| Compaction                       | supported                    | #1625 (settings), #1641 (events)                                                                                                                                               |
| Worktrees and workspaces         | non-goal                     | Nightgauge owns worktrees; OpenCode's experimental workspaces stay off                                                                                                         |
| Snapshots                        | off by default               | § 12                                                                                                                                                                           |
| LSP                              | supported, installed servers | § 12                                                                                                                                                                           |
| Share                            | non-goal                     | disabled and locked (§ 10)                                                                                                                                                     |
| GitHub agent (`opencode github`) | deferred                     | #1650                                                                                                                                                                          |
| ACP                              | deferred                     | #1650                                                                                                                                                                          |
| `serve` and `run --attach`       | deferred                     | #1650, under § 18's guardrails                                                                                                                                                 |

ADR-020 allows an opt-out of a value-adding feature for footprint and cost, and
already keeps destructive, money-spending and data-exporting features opt-in.
This ADR adds **security** and **privacy** as reasons for a pipeline default to
be off. Each is allowed only with its reason recorded in this table and beside
the setting. A **locked** row cannot be turned back on from any config tier.

| Feature disabled or overridden           | Pipeline setting                                                                                                   | Reason    | Locked or overridable                                                                                                 |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------ | --------- | --------------------------------------------------------------------------------------------------------------------- |
| Dispatch itself                          | `NIGHTGAUGE_EXPERIMENTAL_OPENCODE` gate                                                                            | security  | overridable by environment only, until #1643                                                                          |
| Session share                            | `share: "disabled"`, `OPENCODE_DISABLE_SHARE=1`                                                                    | privacy   | locked                                                                                                                |
| Autoupdate                               | `autoupdate: false`, `OPENCODE_DISABLE_AUTOUPDATE=1`                                                               | security  | locked (§ 20 owns upgrades)                                                                                           |
| Model-catalog fetch                      | `OPENCODE_DISABLE_MODELS_FETCH=1`                                                                                  | privacy   | locked                                                                                                                |
| LSP server download                      | `OPENCODE_DISABLE_LSP_DOWNLOAD=1`                                                                                  | security  | locked                                                                                                                |
| Default and third-party plugins          | `OPENCODE_DISABLE_DEFAULT_PLUGINS=1`; `plugin` lists Nightgauge's only                                             | security  | locked                                                                                                                |
| Repository project config                | `OPENCODE_DISABLE_PROJECT_CONFIG=1`; reviewed merge (§ 8)                                                          | security  | locked; not set yet, so the repository's config still loads until #1638 (with #1626) (§ 8)                            |
| Operator's global OpenCode config        | `inherit_user_config: false`; `~/.opencode` and managed config refused (§ 8)                                       | security  | overridable; locked keys win over all but managed config                                                              |
| Session titles                           | `agent.title.disable: true` (§ 10)                                                                                 | privacy   | locked                                                                                                                |
| A model other than the dispatched one    | `small_model`, every agent's `model`, and on an endpoint or `anthropic` the model's `id` and package, pinned       | security  | locked (`id`/`provider.npm`); `options.model`, `speed`/`fallbacks` and `mcpServers` still route around it until #1638 |
| OAuth and subscription credentials       | never read (§ 17)                                                                                                  | security  | locked                                                                                                                |
| Operator's `~/.claude` prompt and skills | `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1`, `_SKILLS=1`                                                               | privacy   | locked                                                                                                                |
| Operator's `~/.agents/skills`            | `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` (§ 11)                                                                        | privacy   | locked                                                                                                                |
| Other model services' credentials        | every variable the catalog binds to another model service removed; forge and cloud platform credentials kept (§ 8) | security  | locked                                                                                                                |
| Provider base URLs from the environment  | `ANTHROPIC_BASE_URL` and `OPENAI_BASE_URL` removed (§ 8)                                                           | security  | locked                                                                                                                |
| Remote `instructions` and `skills.urls`  | refused                                                                                                            | security  | locked; a repository `instructions` URL is still fetched until project config is disabled (§ 8, #1638)                |
| Inherited `OPENCODE_*` variables         | stripped                                                                                                           | security  | locked                                                                                                                |
| `webfetch`                               | `deny` unless the stage's allowed tools include it                                                                 | privacy   | overridable per stage, through allowed tools                                                                          |
| `ask` permissions                        | never generated (§ 9)                                                                                              | security  | locked                                                                                                                |
| Auto-approve flags                       | never emitted                                                                                                      | security  | locked                                                                                                                |
| Listener, mDNS and CORS                  | `--port`, `--mdns` and `--cors` never passed to `run`                                                              | security  | locked                                                                                                                |
| Session import                           | not used                                                                                                           | security  | locked                                                                                                                |
| Snapshots                                | `snapshot: false`                                                                                                  | footprint | overridable                                                                                                           |

The pinned models are `small_model` and the `model` of every agent: the hidden
`title`, `compaction` and `summary` agents and every subagent the `task` tool
starts. Each of them decides where a model request carrying the stage's prompt
or transcript goes, so none may name a model other than the one the stage
names (§ 17). #1625 writes them into the per-run config, with the mode entry
of every built-in primary agent and, for a declared endpoint or `anthropic`,
the dispatched model's entry, whose `id` and `provider.npm` pin the model
name OpenCode looks up and the package that sends it, when nothing else sets
`options.model` on that same entry (§ 8, § 17). The project-config merge
drops a value the repository sets, with a warning (#1638); a value in an
inherited operator config loses to them like every other locked key (§ 8).
Until #1638, several gaps are disclosed rather than closed: a lower layer's
`mode.general` or `mode.explore` replaces that subagent's model, on the
dispatched provider only, because the content cannot set those two mode
entries without making the subagents primary agents; a lower layer's block
for a hosted provider other than `anthropic`, which the content gives no
block, can send that provider's stage to another model, which the
endpoint-policy warning line names; and, without needing a block or mode
entry at all, a repository can add `options.model` to the dispatched model's
own entry or to an agent's own options, or a variant, and still change the
served model without touching the pinned `id`, can add `options.speed` or
`options.fallbacks` to the same entry on `anthropic` to turn fast mode or a
server-side fallback on, and can give an agent `options.mcpServers` with an
`authorizationToken` of `{env:ANTHROPIC_API_KEY}` to send that key to a URL
of its own choosing — the tamper-gate warning line names all three (§ 8).

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
  empty, Nightgauge never writes or copies an `auth.json` into it, and
  `inherit_user_config` never carries credentials. A dispatch whose run root
  holds one, which only something an earlier stage ran could have written, is
  refused before spawn without the file being read.
- **`OPENCODE_AUTH_CONTENT`.** When it is set, OpenCode reads its JSON as the
  stored logins instead of `auth.json`, and OpenCode exports it, holding every
  login it has stored, to the processes it starts for a workspace (§ Context).
  An inherited value would hand a run the parent's logins however empty its
  data directory is. The adapter withholds it with every other inherited
  `OPENCODE_*` variable (§ 8), `OPENCODE_CONSOLE_TOKEN` included: its
  `WithholdsEnv` hook decides on the name alone, and the manager removes each
  variable it names from the inherited environment before adding the
  adapter's exports, without reading or logging a value.

This is not a pending default. A pipeline run is automated work billed per
token against a key the operator can audit and revoke per machine, and
reversing it takes a superseding ADR. Every other hosted provider likewise
authenticates with its own API-key variable from the environment.

**Platform providers are refused** (#1625). The forge tokens and the cloud
platform credentials stay in every run for the stage's tools (§ 8), and
OpenCode's catalog binds them to providers of its own: `GITHUB_TOKEN` to
`github-copilot`, which serves Claude and other models on the forge's gh
login, a Copilot subscription; `GITLAB_TOKEN` to `gitlab`; the Google Cloud
and AWS credentials to `google-vertex`, `google-vertex-anthropic` and
`amazon-bedrock`, the last two of which serve Claude without
`ANTHROPIC_API_KEY`. Those providers' loaders also read their platform's own
credential chain, application default credentials and SSO profiles
included. Nightgauge cannot tell an API key from a subscription or OAuth
login among them, and they are not the model provider's own API-key
variable. So a model whose provider key is one of § 8's platform providers is
refused before spawn, by `PreDispatch` and by the config builder, and so by
`nightgauge opencode config`, whatever the switch says. The remediation is a
provider with a key of its own, such as `anthropic/*` with
`ANTHROPIC_API_KEY`, or another adapter. `enabled_providers` keeps them out of
every other run (§ 8).

#1616 enforces this section. `PreDispatch` refuses an `anthropic/*` dispatch
before spawn while `ANTHROPIC_API_KEY` is unset or empty, with the enable
switch set or not (§ The enable gate), and names `claude-headless` for a
Claude subscription. The interim refusal of every `anthropic/*` dispatch was
lifted only once both sources above were closed: the per-run data directory
starts empty, and no inherited login-bearing `OPENCODE_*` variable reaches the
run. A login-bearing variable a later version adds is removed with the rest.
Nor can the environment redirect the stage: `ANTHROPIC_BASE_URL` is removed
(§ 8), because it would send the stage and its key to whatever server it
names, a proxy serving a subscription included. Nor can a config change the
`baseURL` or the `id`/`provider.npm` it pins: the `anthropic` block pins its
SDK package and `baseURL` to Anthropic's API root, and its entry for the
dispatched model pins the `id` OpenCode sends and the `provider.npm` it loads
(#1625), so a repository or inherited provider block or model entry that sets
those same keys to another server, model or package loses to it (§ 8).
Observed on 1.18.30, a repository entry mapping `claude-sonnet-5` to another
model id and package resolved to both without that pin. Pinning those keys
does not close every route to another model or server: see the fourth merge
rule of § 8 and the `options.model`, `options.speed`/`options.fallbacks` and
`options.mcpServers` gaps below, none of which sets `id`, `provider.npm` or
`baseURL`.

An entry in any config layer defines its model, so an `anthropic` model the
per-run config cannot pin is refused before spawn, by the config builder and
so by `nightgauge opencode config`. One is a model the 1.18.30 bundled catalog
does not list: the entry would define it with a context limit of 0, which
OpenCode never compacts. The other is a fast-mode entry, such as
`claude-opus-5-fast`, which OpenCode derives from its base model and sends
under the base model's `id` with a `speed` option and an `anthropic-beta`
header. Pinning the entry's own `id` sends a model Anthropic does not serve,
and pinning the base model's `id` drops the option and the header, so the
refusal names the base model. This is a naming refusal, not a control against
fast mode itself: it blocks only a dispatch that names the fast-mode entry
directly. A repository config can still add `options.speed` (or
`options.fallbacks`) to the dispatched base model's own entry and get the
same beta headers without naming a fast-mode entry at all; #1638 is what
closes that route, and until then the tamper-gate warning line names it. The
catalog's `anthropic` models, and the `id` sent for each, are a snapshot read
from the binary
(`internal/execution/adapters/opencode_catalog_anthropic.go`), which
`TestOpenCodeAnthropicModelsMatchTheBinary` re-reads, checking as well that
the pinned entry resolves every other model exactly as the catalog does. The
dispatched model's limits still come from the catalog, and a lower layer's
entry can replace them, which the stage-limits warning line discloses. A
block for another hosted provider can still name another `baseURL`, and send
its stage to another model, which the endpoint-policy warning line discloses
until #1678, #1679 and #1638. An agent's own `options.mcpServers` can give an
MCP server an `authorizationToken` of `{env:ANTHROPIC_API_KEY}` (or any other
variable the run holds), which OpenCode resolves and sends to a URL the
repository names, a route the tamper-gate warning line also discloses until
#1638.

The requirement sees only the model a stage names. The target repository's
`opencode.json`, `opencode.jsonc` and `.opencode/` still load, because the
adapter does not set `OPENCODE_DISABLE_PROJECT_CONFIG` yet (§ 8), and they can
name another model. Read from the 1.18.30 bundled source, two keys send the
stage prompt to the model they name, whatever its provider:

- `small_model` titles every session. `opencode run` gives its session a
  default title, and OpenCode replaces it by sending the stage prompt to the
  `title` agent's model or else to `small_model`, without checking that it is
  on the dispatched provider (§ 10).
- An agent's `model` is what that agent runs on: the `title` agent, the
  `compaction` agent, which summarizes the whole transcript, and a subagent the
  stage starts through the `task` tool, which otherwise runs on the stage's
  model.

Run isolation leaves either one no stored login to use, so a model named that
way reaches its provider only on credentials the run holds or needs none: the
dispatched provider's own key, the forge tokens, the cloud platform
credentials the stage keeps for its tools, credentials a provider's own loader
finds that the catalog does not name, or nothing at all for OpenCode's own
free models (§ 8). The repository, not the operator, may still be what
names it. The per-run config pins both keys, and every built-in agent's model,
to the dispatched model and loads the dispatched provider alone (#1625), and
it wins over the repository's files for every key it sets (§ 8). What the
repository can still do is add an agent of its own, or replace the `general`
or `explore` subagent's model through a mode entry, and either model can only
be on the dispatched provider; add `options.model` to the dispatched model's
own entry or to an agent's own options, or a variant, and change the served
model without touching the pinned `id`; and, on `anthropic`, add
`options.speed` or `options.fallbacks` to that entry, or `options.mcpServers`
with an `authorizationToken` referencing an environment variable the run
holds. The project-config merge drops all of these (#1638), and until then
the enabled-dispatch warning's project-config tamper-gate line names them.

`nightgauge doctor` reports a subscription or OAuth login for `anthropic` in
either source of OpenCode's stored logins as a finding (#1627): `auth.json` in
the operator's OpenCode data directory and `OPENCODE_AUTH_CONTENT` in the
environment the doctor runs in, and also the `auth.json` of any run root that
holds one. For each it reads only each entry's `type`, never a credential
value, and it prints no source's content. The finding
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
max-tested version; the doctor (#1627) and `PreDispatch` enforce it, through
the same functions (`internal/execution/adapters/opencode_preflight.go`). A
refusal is `adapter_incompatible`: it names the installed version and the
manifest's, and its remediation is the managed install,
`npm i --prefix ~/.nightgauge/tools/opencode opencode-ai@<max-tested>`, with a
`binary` pin (§ 7) to what it installs.

- **The binary.** `opencode.binary` pins the binary a dispatch checks and
  spawns and the doctor checks; without it the `opencode` on PATH is used. A
  pin is the absolute path of an executable file. A relative one, a bare
  command name included, is refused and never looked up on PATH: a pin exists
  so the binary cannot move under the pipeline, as a PATH install does when
  OpenCode's TUI updates itself. Every probe of the binary runs in a throwaway
  directory that is its `HOME`, `TMPDIR` and four XDG directories, with no
  credential, under a 20 s timeout that kills its process group. The directory
  is in no git repository, so OpenCode would read `opencode.json`, `.opencode`
  and their plugins from every directory above it, a world-writable `/tmp`
  included. A probe checks the per-run config alone, so every probe sets
  `OPENCODE_DISABLE_PROJECT_CONFIG=1`.
- **Floor: 1.18.30**, the version every observation here was made on. Below
  it, or with a version that cannot be read, dispatch fails closed before
  anything is created.
- **Max-tested: 1.18.30.** Above it, dispatch warns and runs a self-test before
  the first stage on each binary, version and per-run config, and records a
  pass under `~/.nightgauge/opencode/self-test/`, so no later stage repeats it.
  The self-test checks the per-run config and the argv without a model call:
  `opencode debug config` under the `OPENCODE_CONFIG_CONTENT` the builder makes
  for the stage must exit 0 and print a merged config that holds every key the
  content sets, with its value, and `opencode run --help`, which 1.18.30
  prints on stderr, must define every flag `BuildCommand` emits and list each
  value it passes among the option's choices. A failure refuses the stage.
  The behavioural checks of the observation method above (stdin delivery, the
  `--format json` event types, `ask` auto-rejection, the absence of a TCP
  listener and the project-config switch) need a model endpoint, so they are
  #1639's scheduled canary against the newest release rather than a
  dispatch-time check.
- **Why the self-test compares keys.** #1627 assumed that `debug config` exits
  non-zero on an unknown key. Observed on 1.18.30
  (`internal/doctor/testdata/opencode-capture/`), it exits 1 on a value of the
  wrong type in `OPENCODE_CONFIG_CONTENT`, and exits 0 on an unknown top-level
  key, which it drops without a word. A version that stopped accepting a key
  Nightgauge sets would pass on the exit code alone, so the self-test also
  requires every key in the merged output.
- **Endpoints stop at max-tested.** The self-test cannot re-check the reserved
  endpoint ids or the `lmstudio` exception (§ Endpoints). Which provider keys a
  binary bundles and which keys its custom loaders claim are read from its
  bundled catalog and source, and a stub provider observes neither.
  Above max-tested, a dispatch to a model server the operator runs (a declared
  endpoint, or the `lmstudio` or `ollama` key of § 1) is therefore refused
  before spawn, and no endpoint block is written into any run's config. The
  refusal names the installed version and max-tested, and its remediation is a
  `binary` pin to a max-tested build. Hosted dispatch continues under the
  warning and self-test above.
  **The #1639 canary's own leg is the one exception**, and only there: this
  same refusal is what would otherwise keep the daily/PR canary from ever
  driving a real newer release through the endpoint the stub provider serves
  on (`lmstudio`), leaving it unable to tell "new and working" from "new and
  broken." `OpenCodeEndpointAboveMaxTested`'s refusal is skipped when a
  package-level hook, `openCodeCanaryRelax`, is both non-nil and returns true
  for the dispatched model; the self-test below still runs. The hook is set
  only by `opencode_preflight_canary.go`, a file gated behind the `canary`
  build tag no production build (`cmd/nightgauge`, the VS Code extension
  bundle, `scripts/clean-install-e2e.sh`) ever adds, and even then only once
  the explicit `NIGHTGAUGE_CANARY=true` signal `scripts/adapter-canary.sh`'s
  `cmd_opencode_canary` sets is read at call time. A default build's
  `openCodeCanaryRelax` is nil regardless of environment, so the refusal above
  holds for every real dispatch;
  `TestOpenCodeAboveMaxTestedRefusesAnEndpointEvenWithTheCanaryEnvSet`
  (`opencode_preflight_test.go`, no build tag, run by the default
  `go test ./...`) is the regression for that.
- **Drift.** Every dispatch that passes records the binary and version it was
  checked against in `~/.nightgauge/opencode/last-dispatch.json`, and the
  doctor warns when the binary it resolves now reports another version.
- **The doctor's catalog probe.** The doctor runs `opencode models` under the
  per-run config for `opencode.model`. For a declared endpoint's model and an
  `anthropic` model the config writes the model's own entry, so the listing
  holds it by construction and shows only that the binary loads the config;
  the row says so. Any other hosted provider is listed only when one of its
  variables is set, so the probe sets each one the doctor's environment
  holds, and a dispatch keeps, to a placeholder, never to the credential, and
  lists what a dispatch would. When the environment holds none of them, the
  row blocks and names them, because a stage would find no model either.
  With `inherit_user_config` on (§ 8) the listing is not a dispatch's: a
  dispatch also reads the operator's own OpenCode config, and a probe reads
  none of the operator's OpenCode state. Observed on 1.18.30, a lower config
  layer holding such a provider's `options.apiKey` loads it with none of its
  variables set, and one holding a model entry for it adds the model to the
  listing. So with the opt-in, a listing that lacks the model, empty or not,
  is a warning that says the probe left that config out, never a block.
- Raising max-tested re-captures `testdata/opencode-cli/`, the reserved
  endpoint ids and the `lmstudio` exception (§ Endpoints) included, and
  `internal/doctor/testdata/opencode-capture/`, in the same change, and
  re-reads the catalog snapshots `openCodeCatalogVersion` names:
  `TestOpenCodeCatalogSnapshotIsTheMaxTestedVersion` fails until it matches.

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
  whether it succeeded, failed or was cancelled: `Scheduler.runPipeline`'s
  terminal defer deletes it beside the worktree, whatever adapter the run's
  stages used, and a dispatch with no run identity deletes its own root when it
  returns. Nothing from it outlives the run, which is also why session resume
  (#1643) works only within a run. A run that crashes first leaves its root
  behind, so creating a root also deletes every root no stage has used for 7
  days; each stage dispatch refreshes its root's modification time, which is
  what the sweep ages. Deletion refuses a root that is a symbolic link or does
  not resolve directly under `~/.nightgauge/opencode/runs/`, and never follows
  a link inside one.
- **What is kept.** Usage only. The stream (#1624) is the source for the
  run's own session, and it never carries a subagent's steps. After exit the
  parser lists the stage's descendant sessions from the run's own session
  table with `opencode db`, because `session list` lists only root sessions
  and a sanitized export redacts the `task` tool metadata that names a child.
  It reads each descendant's `info.tokens` and `info.cost` from
  `opencode export <session> --sanitize --pure`, at most 64 sessions, each
  process in its own process group under a 10 s timeout. The stage's own
  export is read only for its assistant messages' `providerID` and `modelID`
  (§ 1). Exports are held in memory, and nothing else from one is kept;
  `--sanitize` was observed to redact prompts, replies and tool input while
  keeping those fields. A failed read marks the stage's usage partial; it
  fails the stage only when that leaves its cost budget unverified (§ 3).
- **What those processes run with.** Observed on 1.18.30, `export`
  bootstraps a project from its working directory: from a directory holding
  `.opencode/` it writes there and installs that config's dependencies, and it
  loads that directory's plugins, which a stage can write into its worktree
  with the edit tool alone. Every process the parser starts (`--version`,
  `db`, `export`) therefore runs from the run's root, never the worktree, with
  `--pure`, and with only `PATH`, `HOME`, `TMPDIR`, the four XDG variables
  and the `OPENCODE_DISABLE_*` switches in its environment: no forge token,
  provider key or server password. A stage the operator stopped starts none of
  them; its usage is the stream's, marked partial.
- **Stderr.** `--print-logs --log-level ERROR` limits OpenCode's log to
  errors. Every line the child prints, stderr and stdout alike, is redacted of
  the secrets Nightgauge lets the child hold before it is streamed or kept: the
  server password, `GITHUB_TOKEN`, `GH_TOKEN`, `GITLAB_TOKEN` and every
  credential the catalog binds to the dispatched provider, whichever provider
  it is (§ 8), each become `[REDACTED:<name>]`, matched as they are and as the
  content of a JSON string, since a `--format json` event escapes a tool's
  output. The catalog also binds settings to a provider, and those are not
  secrets: a region, project, location, account, host, endpoint, resource
  name or id (`AWS_REGION`, `GOOGLE_VERTEX_PROJECT`, `DATABRICKS_HOST`), and
  `GOOGLE_APPLICATION_CREDENTIALS`, the path of a credential file. Their
  values stay, so a stage's output keeps every `us-east-1` and an
  organization's name. A variable named as a key, token, secret, password or
  personal access token is always a credential, and one of any other shape is
  treated as one. #1624 then removes every credential of a known shape,
  whatever its source: API keys by their issuers' prefixes, GitHub and GitLab
  tokens, bearer and authorization credentials, a URL's user and password, and
  a credential query parameter, also where a JSON escape or a terminal colour
  code comes right before one. Each string of a JSON event is redacted decoded
  as well as escaped. A secret of no recognizable shape stays, and an
  endpoint's `base_url` is #1678's.

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
  it is persisted (#1678, through the output redaction of § 22).
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

## Failure wording (amendment 2026-09-14)

#1631 assumed that OpenCode reports a failed model request on stderr in the
model server's own words, so that a down local server reads
`ECONNREFUSED 127.0.0.1:1234` and a missing model reads as the server's
not-found error. Observed on 1.18.30 with `--print-logs --log-level ERROR`,
against the stub provider (#1618), one-status servers and an `ollama serve`
on `127.0.0.1`, two of those assumptions do not hold. The captures, their
capture script and the source of every wording are in
[`internal/terminalkind/testdata/opencode/`](../../internal/terminalkind/testdata/opencode/README.md).

| Failure                                       | What 1.18.30 prints on stderr                                                                                                                                                                                                             | Exit             |
| --------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------- |
| A request the server refuses (any 4xx)        | A `level=ERROR` logfmt line, `message="stream error"`, `error.error="AI_APICallError: <message>"`, where the message is the server's own `error.message`                                                                                  | 1                |
| An overflow the server reports                | The same line. OpenCode then tries one compaction, which overflows as well, and the stream's `error` event names the failure `ContextOverflowError`                                                                                       | 1                |
| A 401 with no body                            | `AI_APICallError: Unauthorized`, the HTTP reason phrase                                                                                                                                                                                   | 1                |
| A server that is not listening                | `AI_APICallError: Cannot connect to API: Unable to connect. Is the computer able to access the url?`, after about 60 s of retries. `ECONNREFUSED` never appears                                                                           | 1                |
| A 500                                         | `AI_APICallError: <message>`, after about 70 s of retries                                                                                                                                                                                 | 1                |
| `-m` naming a model the config lacks          | `ProviderModelNotFoundError: Model not found: <provider>/<model>`, before any request                                                                                                                                                     | 1                |
| A model id the loopback LM Studio lacks       | Nothing: with one model loaded, LM Studio answered the request with the loaded model                                                                                                                                                      | 0                |
| A model Ollama 0.32.11 has not pulled         | `AI_APICallError: model '<name>' not found`, Ollama's 404 message, at once                                                                                                                                                                | 1                |
| A stage allowed Read that reaches for `*.env` | OpenCode's own default `ask` rule on `read` for `*.env` and `*.env.*`, auto-rejected headless: `! permission requested: read (<pattern>); auto-rejecting`, which #1624's parser ends with `[adapter-permission-rejected] tool=read` (§ 9) | 0, reported as 1 |

The last line of stderr is always a `message=process` line whose `stack`
repeats `AI_APICallError: <message>`, so the last lines a stage's reason keeps
hold both the wrapper and the server's words. The model's own text is on the
stream, never on stderr.

What changes:

- The OpenCode clauses of the terminal-kind table key on
  `AI_APICallError` together with the server's words, never on the words
  alone, so model prose that says "context length exceeded" matches nothing
  (#1631). The overflow fragments are the ones OpenCode's own recogniser
  matches, and its source names the server each belongs to.
- A down local server classifies on `Cannot connect to API`, not on
  `ECONNREFUSED`.
- A model id the server lacks is a failure only where the server says so.
  Ollama does, and its not-found wording classifies `model_unavailable`. A
  loopback LM Studio with one model loaded does not, so the stage runs on
  another model and nothing classifies. Whether the served-model record (§ 1,
  § 2) shows the substitution was not checked here.
- A `read` rejection under OpenCode's own `.env` guard classifies
  `adapter_permission_rejected` and parks, like a rejection of any other
  granted permission. #1631's acceptance criterion says the rejection is not
  retried, and a retry would let the model, or issue text that asks for the
  file, loop the issue. The remediation names the guard, asks whether the
  issue text sent the stage there, and never loosens a rule that guards
  secret files. Nightgauge generates no permission map yet (#1638), so the
  guard is OpenCode's default and nothing Nightgauge writes changes it.

## Canary tooling corrections (amendment 2026-09-14, round 4)

Two round-3 assumptions about the #1639 canary's own tooling, not about
OpenCode itself, did not hold once reproduced:

- **`scripts/adapter-canary.sh`'s `opencode_canary_failing_line` assumed
  `go test -v`'s line order.** `cmd_opencode_canary` actually runs `go test
-tags canary ... -count=1` with no `-v`, and without `-v` go prints the
  `--- FAIL: TestName` summary BEFORE the failing test's own buffered
  `file.go:N: message` lines, not after — the opposite of round 3's backward
  walk, which found nothing there and fell back to the first `file.go:N:`
  line in the whole file: on any installed version other than the pinned
  1.18.30, that line is realOpenCode's own `t.Logf` "pin relaxed" notice (§
  20 above), never the real failure. The parser now scans both the
  immediately-following block (the shape `-count=1` alone actually prints)
  and the immediately-preceding one (the shape `-v` would print, kept so the
  parser does not regress if the invocation ever adds `-v`), skips the
  "pin relaxed" notice deterministically in whichever order it appears
  relative to the real message, and returns only the failing message's own
  first line — a `t.Fatalf`'s further, more-indented continuation lines carry
  no `file.go:N:` prefix of their own and are never matched. Fixtures
  captured from a real, tiny `go test -tags canaryfixture` run, in both
  orders, are committed at
  `scripts/testdata/adapter-canary-gotest/README.md`, which records exactly
  how they were produced.
- **`OpenCodeEndpointAboveMaxTested`'s casing.** § 20 above named it
  `openCodeEndpointAboveMaxTested` (lower-case initial); the function is
  exported. Wording only — the behavior this section describes was already
  correct.

Neither correction changes OpenCode's own observed behavior recorded
elsewhere in this document; both are about `scripts/adapter-canary.sh`'s own
`go test`-output parsing and this document's own prose.

## Nightgauge OpenCode plugin (amendment 2026-09-15, #1635)

This section states the final design #1635 shipped. The build-out history —
what was found along the way and how the design got here — lived in seven
prior amendment sections in this document; it is now recorded instead in
[nightgauge/nightgauge#1635](https://github.com/nightgauge/nightgauge/issues/1635)'s
own comments, which this section links where a specific decision needs
attribution.

**Handshake.** The adapter deletes any stale sentinel and exports
`NIGHTGAUGE_OPENCODE_PLUGIN_NONCE` before spawn; the plugin's init writes
`.opencode-plugin-<RUN_ID>.json` (nonce, plugin version, hooks) beside
`NIGHTGAUGE_OUTPUT_FILE`. `Manager.RunStage` verifies it twice: at the run's
first `step_start` event — the earliest point any tool call could exist —
and again at exit, comparing the sentinel's mtime against the first
`tool_use`'s own reported start time (`state.time.start`), since 1.18.30 can
log `tool_use` before `step_start` is flushed. Either check failing kills the
stage's whole process group (`killProcessTreeUntilGone`, a bounded,
closely-spaced burst of `SIGKILL`s rather than one delivery: a single
`SIGKILL` can miss a child OpenCode forks in the same instant on macOS, which
does not abort a fork under a pending group-kill signal the way Linux does)
and fails the stage `adapter_incompatible`, forcing a non-zero exit code even
when the CLI itself exited 0, and naming the plugin path and `opencode
--version`. The handshake is keyed on the run's own root identity
(`RunRootRequest.ID`, always present), not the dispatch's possibly-absent
runtime identity, so it arms for every dispatch that actually spawns
opencode, including the autonomous issue-refine dispatch with no runtime
identity of its own.

**Isolation.** `OPENCODE_DISABLE_PROJECT_CONFIG=1` is set on every OpenCode
dispatch (AC2): only the embedded Nightgauge plugin may load, and a target
repository's own `.opencode/plugins/*` and `plugin[]` entries never load
beside it. The cost, until #1638 builds the Go-side reviewed merge, is that a
target repository's own `opencode.json` (its `agent`, `provider`, `mode`,
`permission` and `instructions` keys, not only `plugin`) does not merge into
a dispatch's resolved config at all — 1.18.30 offers no switch narrower than
"every project config file this repository holds."
`TestOpenCodeIntegrationPerRunConfigReachesOpenCode` and
`TestOpenCodeIntegrationAnthropicBlockHoldsItsServer` assert that negative
against the real binary.

**Single plugin load.** The plugin file lives outside the run's OpenCode
config directory's own `plugin`/`plugins` subdirectory
(`.../config/opencode/nightgauge-plugin`, not the auto-scanned
`.../plugin`), which 1.18.30 auto-loads on top of whatever the resolved
config's own `plugin` array names; naming the plugin only in the array, once,
is what keeps every hook — careful-gate included — from firing twice per
tool call. `TestOpenCodeIntegrationPluginLoadsExactlyOnce` asserts the
resolved `plugin` array names the file exactly once, against the real
binary.

**Per-run-root seed, trimmed archive.** opencode 1.18.30 installs the npm
package `@opencode-ai/plugin` into any OpenCode config directory whose
resolved config carries a non-empty `plugin` array, independent of whether a
plugin file imports that package, and every invocation that resolves such a
config waits for that install before doing anything else — offline, or
against an unreachable registry, that wait does not fail fast. Nightgauge
pre-seeds the run's own, freshly-created OpenCode config directory with an
embedded, version-pinned, read-only archive
(`internal/execution/opencodeplugin/depsdata/opencode-ai-plugin-1.18.30.tar.gz`,
`opencodeplugin.WriteDependencies`): no npm binary, no lifecycle script and
no network request ever runs to produce or extract it. The archive holds
exactly four files — `package.json`, `package-lock.json`,
`node_modules/.package-lock.json`, and `@opencode-ai/plugin`'s own
`package.json` version marker — nothing else, not even `dist/`. opencode's
own "is `@opencode-ai/plugin` already installed" check reads only two of
them (`package.json` and `package-lock.json`, by dependency name, never a
version — see the narrowed-AC1 paragraph below); the archive keeps the other
two because they are what a real `npm install` for this package also
produces, and neither opencode's plugin loader nor the Nightgauge plugin
itself (`plugin/nightgauge.js`, `plugin/nightgauge/gates.js`) ever imports
`@opencode-ai/plugin` or anything in its dependency tree at runtime; both
import only `node:*` built-ins and each other. `depsdata/README.md` records
the full provenance, the empirical method, and how to regenerate the archive
for a new pinned opencode version;
`internal/execution/opencodeplugin/depsdata/regenerate` rebuilds it
deterministically from a real, `--ignore-scripts` `npm install`, and
`TestDepsArchiveSizeBudget` fails the build if it grows past 32 KiB
compressed.

**Narrowed AC1: no npm or Bun install runs into any Nightgauge-owned
directory.** opencode 1.18.30 installs `@opencode-ai/plugin` into every
OpenCode config directory its resolved config touches, not only the run's
own: `$HOME/.opencode` when it exists (no XDG variable,
`OPENCODE_DISABLE_PROJECT_CONFIG` nor `OPENCODE_PURE` stops OpenCode reading
it), and, under `opencode.inherit_user_config`, the operator's own
`OPENCODE_CONFIG_DIR`. **Nightgauge never seeds, merges into, or otherwise
writes to either directory** — OpenCode's own install into its own config
directories is the operator's environment, exactly as in the operator's own
OpenCode runs, no different from what happens when the operator runs
`opencode` themselves
([#1635 comment, 2026-09-15T07:16Z](https://github.com/nightgauge/nightgauge/issues/1635)).
`opencodeplugin.OperatorInstallSatisfied` is a READ-ONLY check of whether
such a directory already satisfies opencode's own install check — pulled
from the pinned binary's own `Npm.install` and driven against it directly: a
directory is satisfied if it is not writable, or if `node_modules` exists
and package.json's own dependency names (all four dependency kinds, plus
`@opencode-ai/plugin`) are all present in `package-lock.json`'s root
package entry, by name only, never by version — never a write. An earlier
version of this check (#1635/A11 round 8) required all four files
`depsdata/README.md`'s table lists to exist and the version marker to equal
`DepsVersion` exactly; a later fix round found that did not match the
pinned binary and corrected it (`deps.go`'s `OperatorInstallSatisfied` doc
comment holds the measured cases).

**Bounded and classified operator wait, with its stand-down rule.** Offline,
or against an unreachable registry, a dispatch touching an operator
directory that does NOT already satisfy opencode's own check still waits on
OpenCode's own real install.
`manager.go`'s operator-install-risk watchdog bounds that wait independently
of the stage's own timeout (further capped by whatever remains of the
stage's own context deadline) and fails the dispatch `adapter_incompatible`,
naming the directory and #1787, if it fires. The watchdog arms ONLY for a
directory `OperatorInstallSatisfied` reports unsatisfied at spawn time — a
directory already satisfied gets OpenCode's own local, instant fast path (the
same one a run's own XDG-resolved config directory always gets) and is never
armed at all. Once armed, it stands down on EITHER of two independent
signals, whichever arrives first: the directory BECOMING satisfied (checked
read-only, polled every second or two, never written — proof OpenCode's own
in-flight install is done) or the first output arriving on stdout or stderr
(proof the CLI is not stuck before its first line). This is what keeps the
watchdog from ever capping model latency once OpenCode's own install
completes — a slow first token from a local model prefilling a large prompt
is never mistaken for a hung install
([#1635 comment, 2026-09-15T12:00Z](https://github.com/nightgauge/nightgauge/issues/1635)).
`manager_opencode_operator_install_risk_test.go` covers the bound, the
stage-context cap, the two stand-down paths, and that a fast, unrelated
failure is never misclassified with the watchdog's own marker text.

**`task` denied (AC9).** A bounded spike against 1.18.30 (an embedded plugin
wired as a Node `event`/`tool.execute.before` logger, pointed at a real local
model, given a prompt directing it to call `task` once) could not determine
within its budget whether 1.18.30 calls `tool.execute.before` inside a `task`
(subagent) session. Per AC9's own stated fallback for "not or undecidable",
`gates.js`'s `toolExecuteBefore` denies the `task` tool unconditionally,
careful mode on or off, checked before the careful-gate verb and independent
of `NIGHTGAUGE_BIN` — a subagent session this plugin cannot verify it gates
is worse than no subagent at all. `TestNodeHarnessDeniesTask` is the
red/green coverage. Settling AC9 properly, and lifting the denial, needs
either an upstream answer or a faster local model than the spike had time
for; it remains open.

**Follow-up:** [nightgauge/nightgauge#1787](https://github.com/nightgauge/nightgauge/issues/1787)
tracks a per-run `HOME`, so `$HOME/.opencode` stops being a config directory
at all — removing the operator-install wait entirely rather than only
bounding and classifying it.

## Session-lifecycle events plugin (amendment 2026-09-15, #1641 fix round)

`plugin/nightgauge/session.js` (#1641) shipped against three assumptions this
fix round found wrong on the real 1.18.30 binary, each confirmed with an
offline, loopback-only probe against the pinned binary rather than assumed
from its type declarations.

**The `permission.ask` plugin hook is never called.** 1.18.30 publishes a
permission prompt only as the bus event `permission.asked`
(`{id, sessionID, permission, patterns, metadata, always, tool}`), which
reaches the plugin's `event` hook; the string `"permission.ask"` occurs zero
times among the binary's own `trigger("...")` call sites — every one of the
fourteen hooks it does call is `chat.headers`, `chat.message`, `chat.params`,
`command.execute.before`, `experimental.chat.messages.transform`,
`experimental.chat.system.transform`, `experimental.compaction.autocontinue`,
`experimental.session.compacting`, `experimental.text.complete`, `file.open`,
`shell.env`, `tab.new`, `tool.definition`, or `tool.execute.after`/`.before`.
So AC5's notification and AC6's permission-ask events, both routed through
`permissionAsk` (the `permission.ask` hook), silently never fired in
production; the harness tests passed only because they call `permissionAsk`
directly in Node, bypassing the loader. `event()` now handles
`permission.asked` itself — the throttle and the events-file write both moved
there, reading `properties.permission` (the permission type) only, never
`patterns`/`metadata` (the model-authored command text those carry). The
`permission.ask` export stays, unused in production, only for forward
compatibility should a later opencode version call it directly.
`TestEventPermissionAskedEmitsEventAndThrottlesNotify` is the Node-harness
coverage; `TestPermissionAskEventAgainstRealOpenCode`
(`compaction_stub_test.go`) is the real-binary row, `permission: {bash:
"ask"}` against the #1618 stub.

**`session.idle` wrote only `stop_verify`, never `idle`.** The wire format
(`kind: compaction|idle|stop_verify|permission_ask|skill`) and `events.go`'s
own `EventKinds` always named five kinds; the writer only ever produced four.
`event()` now appends one `idle` line before running `hook stop-verify`.
Covered by `TestEventIdleWritesStopVerifyEvent` and the real-binary
`assertCompactionStubGreen`.

**`child` was hard-coded `false`.** 1.18.30's `Session.Info` carries an
optional `parentID` (visible on `session.created`/`session.updated`'s own
`properties.info.parentID`, and in the bundled client's own reducers —
`case "session.updated": ... r.parentID`), so `event()` now tracks it: a
sessionID first observed with a non-empty `info.parentID` is tagged `child`
in every later event it produces. `TestChildSessionEventsAreTaggedChild`
drives this directly, since `gates.js` still denies the `task` tool
unconditionally (AC9, above) and no run in this codebase can produce a real
child session — a parent-only compaction-stub run's own `session.updated`
traffic was checked directly (this fix round's own probe) and never carried a
`parentID` for its single session, consistent with that denial. **The gap AC6
and AC7 asked to be recorded stands as before: child-session delivery through
this plugin is unverified**, now for a documented reason (AC9's denial)
rather than an unimplemented `child` field.

**#1625's steps cap is not opencode's own hard stop.** AC2 read "a run forced
into compaction ends without it and within #1625's steps cap", and ADR-022
(the section above) assumed "#1625's steps cap remains the hard stop
underneath this either way." On 1.18.30, a step at or past `agent.*.steps`
only appends an assistant nudge message (`let oe=Y.steps??1/0,L=_>=oe;
...messages:[...an,...L?[{role:"assistant",content:lh}]:[]],tools:le` in the
binary's own minified loop) and still passes every tool — the loop does not
stop there. Against `compaction_stub_test.go`'s own deterministic, offline
fixture (`steps: 8`), a green run consistently logs 13 loop steps, not ≤8:
steps 8 through 11 each still execute a bash tool call after the declared cap.
`assertCompactionStubGreen`'s bound (`loopStepsWantMax`) is tightened from an
earlier, cap-agnostic 20 to 16 (13 plus small headroom) to reflect this
measured reality rather than mask it, and its own comment records the
contradiction. AC2's "within #1625's steps cap" should be read as "within the
autocontinue-suppression's own bound", not #1625's literal cap; #1625 should
be read the same way pending its own fix round. The assertion still
meaningfully catches the regression it exists for:
`compactionAutocontinue`'s own suppression removed produced 311+ loop steps
in the same fixture, over 20x the tightened bound.

**CI coverage.** `.github/workflows/ci.yml`'s "OpenCode integration
(opencode-ai@1.18.30)" step ran neither `TestPluginLoadsOnRealOpenCode`
(#1635) nor `TestCompactionAutocontinueSuppressionAgainstRealOpenCode`
(#1641): its package list omitted `./internal/execution/opencodeplugin/` and
its `-run` regex did not match either name, so nothing in CI would have
caught a regression in either. Both, plus the new
`TestPermissionAskEventAgainstRealOpenCode`, are now named in that step.

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
