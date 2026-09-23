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
- **The machine's managed OpenCode config refuses an enabled dispatch**
  unless the operator has opted into their own OpenCode config (§ 8), because
  run isolation cannot keep it out of a run. The refusal is made where the
  run's config and environment are built from the machine-tier block
  (`PrepareOpenCodeRun`), after the gate and before anything is created.
  Before #1787, a populated `~/.opencode` refused a dispatch the same way;
  since #1787 a non-inheriting run's own per-run `HOME` keeps it out
  structurally instead, so there is nothing left to refuse.
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
  is not passed to OpenCode. #1652's stage budgets (ADR-023 Q8) now bound
  every stage on its stream (its steps, its wall clock and its tokens), and
  the row's line says so, but the row stays: it is also the disclosure that a
  hosted model's limits come from OpenCode's catalog and that a lower config
  layer can replace them, which no stage budget changes. The USD watchdog
  cannot stop a stage while its subagents spend, because their steps never
  reach the stream (§ 3); that row stays with #1748 until a change bounds
  them while the stage runs.
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
  enabled-dispatch warning says so (the `subagent cost` row). That gap is
  currently unreachable, not merely unenforced: `gates.js` denies every
  `task` tool call unconditionally as AC9's fallback (see the "Nightgauge
  OpenCode plugin" amendment), so no subagent session exists to spend past
  the budget while the stage runs. The row stays because the gap is real and
  becomes live again the moment AC9 is settled and the denial is lifted
  (#1748).
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
local model that still ends a session caught in a loop. Since #1652 every
stage has one, its `max_turns` stage budget (ADR-023 Q8), whose zero-cost
default is the same 200. A hosted stage whose `max_turns` is set to -1
passes none, so it gets these 200 steps. The steps cap is not a hard stop (#1811), so the manager also counts
`step_finish` events and stops the stage itself. A dispatch to an
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
- **The home directory (#1787).** OpenCode 1.18.30 reads two operator
  locations from `home` whatever the XDG variables say:
  `OPENCODE_DISABLE_EXTERNAL_SKILLS=1` closes `~/.agents/skills` and
  `~/.claude/skills`; `~/.opencode` is a config directory to OpenCode: it loads
  its `opencode.json`, `opencode.jsonc`, and `agent`, `command`, `mode`,
  `plugin`, `tool` and `skill` directories under either spelling. Neither
  `OPENCODE_DISABLE_PROJECT_CONFIG` nor `OPENCODE_PURE` stops that read.
  Before #1787, moving `HOME` would have moved every other tool the stage
  starts with it, so the adapter only ever _detected_ a populated
  `~/.opencode` and refused the dispatch — safe, but leaving the wait for
  OpenCode's own install into it in place whenever the operator's real
  `~/.opencode` held anything to install against.

  Since #1787, a non-inheriting run gets a private `home/` inside the same
  per-run root `EnsureOpenCodeRunRoot` already builds (alongside `config/`,
  `data/`, `cache/` and `state/`), populated by the same
  symlink-the-operator's-entries pattern `config/` uses
  (`linkOperatorConfig`/`linkOperatorConfigEntry`, generalized as
  `linkOperatorHome`), withholding only `.opencode`. `HOME` is then pointed at
  `home/` for the dispatch (`OpenCodeIsolationEnv`). Because `home/.opencode`
  never exists, OpenCode never finds config to install against and never
  waits — without hiding any other operator state a stage's tools resolve via
  `$HOME` (`.gitconfig`, `.netrc`, `.git-credentials`, `.ssh`, `.aws`,
  `.config`, ...), which keeps flowing through unchanged symlinks, exactly as
  `linkOperatorConfig` already does for `$XDG_CONFIG_HOME`. The refusal
  (`openCodeHomeConfigRefusal`) is removed as dead code: once a non-inheriting
  run's `HOME` structurally guarantees an empty `.opencode`, the condition it
  checked can never be observed true.

  `inherit_user_config` is unchanged by this: it still leaves `HOME` untouched
  entirely, so `~/.opencode` (and the machine's managed config) load exactly
  as they do outside a run.

  **Trade-off, carried over from `config/`'s own:** a symlinked entry is
  bidirectional — a tool that _writes_ through one of the forwarded entries
  (an SSH `known_hosts` append, a git credential store, an AWS SSO cache
  write) writes into the operator's real file, exactly as `config/`'s
  forwarding already accepts for `~/.gitconfig`'s XDG-side entries. #1787
  extends where that risk already applied, from `$XDG_CONFIG_HOME` to
  `$HOME`; it does not change the risk model itself.

  Owning changes: `internal/doctor/opencode.go`'s `checkOpenCode` no longer
  blocks a dispatch on a populated `~/.opencode` (only the machine's managed
  config still does); `OpenCodeMachineConfigRefusals` and `PrepareOpenCodeRun`
  make the same one check instead of two; `operatorInstallRisk`
  (`opencode_plugin_deps.go`) resolves the dispatch's _own_ `HOME` (its `Env`,
  falling back to the operator's real one only when `HOME` is left untouched)
  rather than always the operator's real one, so the operator-install-risk
  watchdog can no longer arm for a non-inheriting run's `$HOME/.opencode` — it
  structurally cannot exist.

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
  machine's config with the operator's own. The refusal is made by
  `PrepareOpenCodeRun`, which builds the run's config and environment from
  the same read of the machine-tier block, so the opt-in can never read as on
  for the refusal and off for the environment; `nightgauge opencode config`
  runs it too. The doctor's `opencode` row blocks on it, naming what it
  found, through `OpenCodeMachineConfigRefusals`, which makes the same check
  on the same inputs, so cap recovery never hops onto a machine that refuses
  every dispatch (#1627). Before #1787 this was two refusals (the home
  directory's above, and this one); since #1787 only this one remains.
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

| Capability                       | Disposition                                   | Owner or reason                                                                                                                                                                                                                                                                                      |
| -------------------------------- | --------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Skills                           | supported                                     | #1666 (install target), rendered per stage                                                                                                                                                                                                                                                           |
| Commands                         | supported                                     | #1666 (`configs/opencode` templates)                                                                                                                                                                                                                                                                 |
| Subagents (`task`)               | denied (AC9 fallback)                         | #1624 rolls subagent usage into the stage, but the plugin denies `task` unconditionally until AC9 is settled — see the "Nightgauge OpenCode plugin" amendment dated 2026-09-15; #1748 verified the denial also closes the `subagent cost` gap (§ 3) as long as it stands, and left this row in place |
| Plugins                          | supported, Nightgauge's only                  | #1635, #1640, #1641, #1642                                                                                                                                                                                                                                                                           |
| MCP                              | config: supported; tool calls: blocked closed | #1626 wires the per-run `mcp` config; a repository's own MCP server tool call is blocked closed under `[nightgauge-gate:unknown-tool]` until a mapping exists — see the "OpenCode plugin gate parity" amendment dated 2026-09-15 (round 2, #1640)                                                    |
| Permissions                      | supported                                     | #1638                                                                                                                                                                                                                                                                                                |
| Sandboxing                       | non-goal                                      | OpenCode has none upstream; containment is § 8 isolation, the permission map and the worktree                                                                                                                                                                                                        |
| Resume and fork                  | deferred                                      | #1643 (session resume within a run)                                                                                                                                                                                                                                                                  |
| Export                           | supported, sanitized only                     | § 22                                                                                                                                                                                                                                                                                                 |
| Import                           | non-goal                                      | a session file or URL is untrusted input with nothing to gain                                                                                                                                                                                                                                        |
| Usage (`opencode stats`)         | non-goal                                      | usage comes from the stream and the registry (§ 3), not OpenCode's catalog prices                                                                                                                                                                                                                    |
| `json_schema` output             | non-goal                                      | #1650: unusable on 1.18.31 — a `json_schema` prompt makes the session unreadable (see the amendment dated 2026-09-21)                                                                                                                                                                                |
| Variants (`--variant`)           | supported                                     | #1643 maps effort to a variant                                                                                                                                                                                                                                                                       |
| Compaction                       | supported                                     | #1625 (settings), #1641 (events)                                                                                                                                                                                                                                                                     |
| Worktrees and workspaces         | non-goal                                      | Nightgauge owns worktrees; OpenCode's experimental workspaces stay off                                                                                                                                                                                                                               |
| Snapshots                        | off by default                                | § 12                                                                                                                                                                                                                                                                                                 |
| LSP                              | supported, installed servers                  | § 12                                                                                                                                                                                                                                                                                                 |
| Share                            | non-goal                                      | disabled and locked (§ 10)                                                                                                                                                                                                                                                                           |
| GitHub agent (`opencode github`) | non-goal                                      | #1650: an externally-driven agent that duplicates and races Nightgauge's own intake, branch, worktree, routing and PR lifecycle, under GitHub's credentials rather than § 17's                                                                                                                       |
| ACP                              | non-goal                                      | #1650: an editor-driven surface with a human in the loop — the inverse of the pipeline's direction of control. Its `--port`/`--mdns`/`--cors` flags carry § 18's listener hazard                                                                                                                     |
| `serve` and `run --attach`       | deferred                                      | #1650, under § 18's guardrails — and blocked on the isolation collision recorded in the amendment dated 2026-09-21: an attached run's config, permission map and XDG isolation are the **server's**, not the run's                                                                                   |

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
| Operator's global OpenCode config        | `inherit_user_config: false`; per-run `HOME` keeps `~/.opencode` out (#1787), managed config refused (§ 8)         | security  | overridable; locked keys win over all but managed config                                                              |
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

### Endpoints narrowing (amendment 2026-09-20, #1678)

Verified against the maintainer's actual machine on 2026-09-20: two
OpenAI-compatible endpoints, neither LM Studio nor Ollama. The amendment
above still specs id syntax, the reserved-id catalog check, complete provider
blocks, the `endpoint` wire label, the LAN/`allow_lan`/private-network rules
and forwarding endpoints unchanged. This amendment narrows three things
#1678 actually implements them against:

- **`openai-compatible` is the primary and only required kind.** `lm-studio`
  and `ollama` are optional labels on an `opencode.endpoints[]` entry, with
  no protocol-specific readiness probe of their own; every declared entry,
  whatever its label, is checked with one generic `GET {base_url}/models`
  probe (`OpenCodeEndpoint.Legacy` distinguishes the pre-existing
  single-flat-key endpoint, which keeps the LM-Studio/Ollama-specific
  probes untouched, from a declared entry, which always gets the generic
  one). Implementing per-server protocol clients for servers Nightgauge does
  not control would duplicate knowledge OpenCode itself already has.
- **The loaded-vs-declared-context doctor warning is dropped, not
  implemented.** No generic OpenAI-compatible `/models` response carries a
  loaded-context field, and a server-reported number half-trusted is worse
  than the operator's own declared `limit.context` and `limit.output`. A
  declared entry is therefore never probed for its loaded context: its
  `limit.context` and `limit.output` must be set in config, and doctor
  surfaces the entry's declared `max_concurrency` as "slots" instead — a
  declared capacity, never measured.
- **The config field is `provider`, holding the already-normalized value
  directly** (`provider: lm-studio`), not `kind`; this was already correct in
  the prose above (§ Endpoints' own code sample) but the issue's original
  technical notes drifted from it, so this reconciles the two. Reserved-id
  collision refusal reuses the catalog `internal/execution/adapters` already
  captures from the max-tested binary for env-var withholding
  (`openCodeCatalogEnv`, § 20) rather than a separate fixture: it is the same
  213-key snapshot, kept in sync by the same re-capture obligation, so a
  second copy would only be one more thing to drift.
- **Declared `models[].variants`** are carried through unfiltered into the
  generated provider block (opaque passthrough, disabled entries included);
  #1643's `--variant` mapping is what reads them, not this layer.

Read-only consistency check against ADR-012 and ADR-013: neither exists in
this repository's `docs/decisions/` (the sequence skips from 011 to 015), so
there is nothing here for this amendment to conflict with; if either is a
private ADR in `nightgauge-internal`, that check is that repository's to run.

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
it — but since #1787 a non-inheriting run's `HOME` is its own per-run `home/`,
whose `.opencode` is never created, so this path is only ever in play under
`opencode.inherit_user_config`, where `HOME` stays the operator's real one),
and, under `opencode.inherit_user_config`, the operator's own
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
OpenCode's own real install. Since #1787 this can only be `OPENCODE_CONFIG_DIR`
under `opencode.inherit_user_config` (or, under the same setting, the
operator's real `$HOME/.opencode`); a non-inheriting run's own per-run `HOME`
keeps `$HOME/.opencode` structurally out of reach, so this watchdog can no
longer arm for one.
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

**Resolved:** [nightgauge/nightgauge#1787](https://github.com/nightgauge/nightgauge/issues/1787)
gave a non-inheriting run its own per-run `HOME`, so `$HOME/.opencode` stops
being a config directory to it at all — removing the operator-install wait
entirely for that case, rather than only bounding and classifying it. See
"The home directory" above.

## OpenCode plugin gate parity: 1.18.30 divergences from AC assumptions (amendment 2026-09-15, round 2, #1640)

#1640 extended the plugin's `gates.js` (workflow-gate, stage-gate,
`command.execute.before` sanitize-prompt, and a pinned `TOOL_CLASSIFICATION`
fail-closed table) beyond the "Nightgauge OpenCode plugin" amendment above's
careful-gate and `task` denial. AC7 requires recording, here, any observed
divergence from that work's stated assumptions before continuing; this
section is that record, written from a fix round that traced three of them
against the pinned 1.18.30 binary's own minified source rather than
inference.

**`apply_patch` vs `edit`/`write` is a per-model switch, not a capture
disagreement.** An earlier version of `gates.js`'s own comments described two
tool-surface captures — the static `opencode debug agent build` dump and the
real dispatch stream captures this ADR's § 6 already recorded — as
"disagreeing" on whether `edit`/`write` or `apply_patch` is the file-mutation
tool. They do not disagree: 1.18.30's `ToolRegistry.tools({...defaultModel,
agent})` enables `apply_patch` and disables `edit`/`write` exactly when

```
modelID.includes("gpt-") && !modelID.includes("oss") && !modelID.includes("gpt-4")
```

and offers `edit`/`write` (not `apply_patch`) for every other model family.
`debug agent build`'s empty-`HOME` default model happened to match that rule;
the real dispatch captures used models that did not. Consequence: any
OpenCode dispatch whose model id matches this rule — for example an
`openai/gpt-5*` id admitted from OpenCode's bundled catalog — is offered only
`apply_patch` for file mutation, which `gates.js` classifies `"blocked"`
(below), so every tool-based file edit in that stage fails outright rather
than merely being screened differently. This is a real gap for any pipeline
stage dispatched against such a model, not a documentation nit; it is not
fixed here.

**`apply_patch`'s argument shape is known, not "never observed."** Its input
is a single `{patchText}` string; the pinned binary's own patch parser reads
`*** Add File:`, `*** Update File:`, `*** Delete File:` and `*** Move to:`
headers to recover each path a patch touches. `gates.js` still blocks it
(kind `"blocked"`) because that shape has never been mapped to a Claude-shaped
`file_path` payload for workflow-gate, not because the shape is unknown.

**`command.execute.before`'s prompt has already had its shell substitutions
run.** 1.18.30's command-template pipeline substitutes `$ARGUMENTS` into the
template, then runs every `` !`...` `` span in the COMBINED text through the
invoking user's own shell, and only afterwards calls
`trigger("command.execute.before", ...)` with the already-substituted,
already-shelled-out text. No `tool.execute.before` fires for those shell
spans. Consequence, confirmed by an offline probe against the real binary (a
plugin logging and throwing from both hooks, a command template containing
`` !`touch ...` ``, and a `--command argshell` invocation with a `` !`...` ``
span inside the caller-supplied argument): the shell commands run and their
side effects land BEFORE the plugin ever sees the command, and a plugin
throw aborts the prompt but not the shell command that already ran. A
subagent (`subtask: true`) command's expansion also arrives as a
`{type:"subtask", prompt}` output part, not `{type:"text"}` — `gates.js`'s
`commandExecuteBefore` (#1640) reads only `type:"text"` parts today, so a
subtask expansion is not currently screened by sanitize-prompt at all. This
is not reachable from a Nightgauge dispatch today: the adapter never passes
`--command` (only `run --format json ...`) and sets
`OPENCODE_DISABLE_PROJECT_CONFIG=1`, so no config a Nightgauge run loads can
define a command template in the first place. The guardrail here, until a
future issue changes that: `command` must stay out of every config a
Nightgauge run loads, and a `{type:"subtask"}` part needs its own
sanitize-prompt coverage before command templates are ever enabled.

**MCP tool calls: correcting § 15's "MCP: supported" row.** § 15's
capability table names MCP `supported` (#1626), which is accurate for the
per-run config's `mcp` block (§ 8) reaching a dispatch. It does not mean
every MCP-sourced tool call is gated the way a first-party tool is. 1.18.30's
`tool.execute.before` also fires for three kinds of id `TOOL_CLASSIFICATION`
did not list before this fix round:

- a repository's own MCP server tools, dispatched via `CodeMode.invokeChildTool`
  with `tool: <server>_<local-tool-name>` (the server/tool key `Ah0`'s own
  code builds);
- opencode's own read-only MCP resource tools, `list_mcp_resources`,
  `list_mcp_resource_templates` and `read_mcp_resource` — opencode itself
  groups these under the same `"read"` permission category as `read`/`glob`/
  `grep` (confirmed against the binary's own permission-mapping code); and
- code-mode child tool invocation itself (`CodeMode.invokeChildTool`).

Before this fix round every one of these threw `[nightgauge-gate:unknown-tool]`,
so an OpenCode stage with an MCP server configured silently lost that
server's tools entirely. The fix round classifies the three read-only
resource tools `"passthrough"` (they only list or read resource metadata a
configured server advertises). It deliberately does NOT classify a
repository's own `<server>_<tool>` ids: this plugin has no reliable way to
distinguish a read-only MCP tool from a mutating one by id alone, and #1626's
per-run config already resolves per-repository server definitions the plugin
does not currently see. **Until a mapping is built (tracked as a follow-up,
not yet filed as its own issue), a repository's own MCP server tool calls
stay blocked closed under `[nightgauge-gate:unknown-tool]`** — narrower than
§ 15's "MCP: supported" row implies for a stage running under the OpenCode
adapter specifically; the Claude Code side is unaffected (`hooks.json` never
gates an MCP tool call at all).

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

## OpenCode does not await plugin hook promises (amendment 2026-09-15, #1810)

Two assumptions underneath the session-lifecycle plugin turned out to be
wrong. Both were settled by probing the pinned 1.18.30 binary directly, not
by reading its source or reasoning from the plugin type declarations.

**OpenCode does not await the promise a plugin's `event` hook returns, and a
one-shot `opencode run` exits within ~10 ms of publishing `session.idle`.** A
probe plugin registered as the only plugin in the run, whose `event` hook
`await`ed a bounded 1200 ms sleep and then wrote a marker file, never reached
the line after its `await`; the run's total wall clock was identical
(1.53 s vs 1.51 s) to the same plugin returning immediately. A continuation
scheduled with `.then()` landed only at a 0 ms delay (9 ms after the hook
returned) and was already lost at 20 ms. So on the terminal event, nothing
the plugin schedules — an `await`, a `.then()`, a timer — can be relied on to
run at all, and no timer of the plugin's can bound a child it spawned either.

That is what made #1641's fix-forward shape (record the verdict from a
`.then()` continuation on an in-process spawn) produce `got 0 stop_verify
events, want exactly 1` against
`TestCompactionAutocontinueSuppressionAgainstRealOpenCode`. Awaiting the verb
inline instead is no better, and for the same reason.

**Decision.** The `stop_verify` verdict is written by the process that
computes it. `session.js` spawns `nightgauge hook stop-verify --emit-event
--session-id <id> [--child]` detached and `unref`'d, and does not wait on it
at all; that child appends its own `stop_verify` line through
`opencodeplugin.AppendRunEvent`, honouring the same 1 MiB cap, the same
single `truncated` sentinel and the same retention contract (ids, counts and
verdict codes — never `EvaluateStopHookOutput`'s `Reason`, which is
plan-derived text), and bounds its own evaluation at 5 s, recording
`verdict: "timeout"` rather than hanging. The one verdict the plugin still
owns is `no_bin`, which it writes synchronously after a synchronous
executability check, because on a terminal event there is no later tick in
which to learn it from `spawn`'s asynchronous `'error'` event.

The consequence for readers, #1653 included, is that a run's events file
finalizes shortly AFTER the OpenCode CLI exits. `events.go` grows
`WaitForRunEvent` for exactly that, and both the real-binary suite and the
Node-harness suite read through it.

**The "5 s hook verb" was a test-harness artifact, not production cost.**
`composeStageEnv` exported `NIGHTGAUGE_BIN` from `os.Executable()`, which
under `go test` is the Go TEST binary — and the plugin SPAWNS that value.
Running `internal/execution`'s own test binary as `… hook stop-verify
--workdir X` re-runs the entire suite: measured at 104.5 s of wall clock,
forking git into other tests' temp directories, exiting 1, and orphaned past
the test that started it once the CLI died with the plugin's 5 s bound. A
real `nightgauge hook stop-verify` answers in ~70 ms warm (0.47 s cold). The
manager now resolves that export through an injectable `hostExecutable`, and
every integration case that dispatches the real CLI points it at a real
`nightgauge` build (`useRealNightgaugeBinary`).
`TestOpenCodeIntegrationHostBinaryIsARealNightgauge` asserts it by running
the exact argv the plugin runs.

**CI masking.** The regression reached `main` green because
`.github/workflows/ci.yml`'s "OpenCode integration (opencode-ai@1.18.30)"
step is guarded only by `if: needs.changes.outputs.run_heavy != 'false'`.
GitHub Actions skips a step with no `always()`/`failure()` condition once an
earlier step in the same job has failed, so on a run where the main Go test
step failed first, the OpenCode integration step never executed and its
result was never part of the verdict. Filing that separately; it is not
fixed here.

## The permission map's pattern matching (amendment 2026-09-15, #1638)

§ 9 assumed opencode 1.18.30 resolves a `permission` object's patterns the
same way for every key, matching the last rule the merged config carries
against a request's own absolute path. #1638's own bounded probes against the
pinned binary (a scripted, in-process OpenAI-compatible stub scripting real
`read`/`edit`/`bash` tool calls, `internal/execution/adapters/opencode_guard_integration_test.go`
and the isolated probes its history records) found three narrower rules
instead, each load-bearing for the permission map #1638 generates:

- **A pattern with no glob metacharacter is never matched for `edit`.**
  `edit: {"*": "allow", "secret.txt": "deny"}`, with `secret.txt` the exact
  relative path a tool call's own `filePath` carried, still ran the edit: the
  literal pattern was never consulted. The same object with the pattern
  spelled `**/secret.txt` or `secret.*` — a real glob, differing only in
  carrying a `*` — denied it. Every pattern #1638 sets on `edit` or `read`
  beyond the bare `*` default is therefore built to carry a real wildcard;
  the static backstop entries already did (`*.env`, `**/.ssh/**`, and the
  rest), and a dynamic directory pattern is built as `<dir>/**`.
- **A pattern that starts with `/` is never matched for `edit`, only for
  `external_directory`.** `edit: {"*": "allow", "/a/b/**": "deny"}` ran an
  edit whose `filePath` was exactly `/a/b/c`; stripping the leading `/` from
  the identical pattern denied it. `external_directory`'s own patterns need
  the leading `/` (an unprefixed directory pattern in that key was never
  observed to match anything): a probe of `external_directory: {"*": "deny",
"/a/b/**": "allow"}` let a read under `/a/b/` through, and the same pattern
  without its leading `/` refused it. So a directory pattern's two uses need
  two different strings: `openCodeDirPatterns` (opencode_guard.go) keeps the
  leading separator for `external_directory`'s allow-list, and
  `openCodeEditDenyPatterns` strips it for `edit`'s deny entries — the
  NIGHTGAUGE_SKILL_DIR read-only pair (§ "external_directory allow-list")
  uses one function for each half. Once both defects are avoided — a real
  wildcard, no leading `/` — `edit`'s own last-matching-rule-wins behaves as
  § 9 assumed: a specific `deny` added after a `*: allow` was consistently
  denied in every probe, in both directions (a specific `allow` after a
  `*: deny` was consistently let through). `bash`'s own patterns, matched
  against the command string rather than a path, were never observed to need
  either correction: `bash: {"*": "allow", "rm -rf *": "deny"}` denied `rm
-rf /tmp/x` in the same probe run that found `edit`'s literal-pattern gap.
- **`external_directory` needs a directory's path in more than one form.**
  A macOS `t.TempDir()` skill directory under `$TMPDIR` (itself under `/var`,
  a symlink to `/private/var`) was reported by a real tool call's `filePath`
  unresolved (`/var/folders/.../skill/_includes/note.md`), even though §
  9's own last bullet ("the permission map is built against resolved
  paths") predicted the resolved form. An allow-list built only from
  `filepath.EvalSymlinks`'s output did not match it; one carrying both the
  given and the resolved form (when they differ) matched either way.
  `openCodeDirPatterns` returns both, which is also why the six stage
  skills' `/tmp` literals were already emitted in both `/tmp` and
  `/private/tmp` forms before this amendment (the same macOS gap, generalized
  here to every directory pattern the permission map builds, not only
  `/tmp`'s).

None of the three findings changes § 9's `ask`-vs-`auto-rejecting` account:
a `deny` match, unlike an `ask` one, was never observed to print the
`! permission requested: ... auto-rejecting` stderr notice — it fails the
tool call directly, visible in `--format json` stdout as an errored
`tool_use` event, with the rejected permission's name inside the event's own
`error` text, never on stderr. Every generated map is `allow`/`deny` only
(§ 9, AC1), so an enabled dispatch's stderr carries that notice only when a
target repository's own project config or a lower layer adds an `ask` entry
this map does not already override — the project-config tamper gate (§ 8)
is the control that closes that route. #1624's own failure classification
(`OpenCodeAutoRejectMarker`, `internal/execution/opencode_usage.go`) reads
stderr for the notice; whether it also needs a stdout-based path for a plain
`deny` rejection, now that every dispatch's own map never emits `ask`, is
package `execution`'s own tested concern, raised here as a finding rather
than settled by this change.

## Correction to the pattern-matching amendment (#1638 fix round, same day)

A same-day review of the amendment above, run against the same pinned
binary but in a real **git worktree** rather than the amendment's own bare
`t.TempDir()` fixture, found its first two bullets describe an artifact of
that fixture, not opencode's actual rule. `TestOpenCodeIncludesReadAllowed`
(the amendment's own probe) never had a `.git` in its project directory; a
git worktree is what every real dispatch actually runs in
(`Manager.RunStage`'s own worktree setup).

**The corrected rule.** opencode 1.18.30's `edit`, `write` and `read` tools
all ask permission with `patterns:[path.relative(Instance.worktree, file)]`
(bundled source: `n.ask({permission:"edit",patterns:[qo.relative(y.worktree,u)]...})`).
`Instance.worktree` is the git repository's top-level
(`git rev-parse --show-toplevel`) for a git repository, and `/` — the whole
filesystem — for a directory that is not one. The amendment's fixture had no
`.git`, so `Instance.worktree` there really was `/`, and a pattern's absolute
path with its leading `/` stripped happens to equal the correct
worktree-relative form in that one case, by coincidence: `filepath.Rel("/",
"/a/b/c")` is `"a/b/c"`, the same string stripping the slash gives. Read
against a real git worktree instead, the identical, slash-stripped pattern
never matches: the file's actual relative path is the git top-level's
relative form, `../../…/skill/_includes/note.md` for a skill directory
outside the worktree, which an anchored `Users/…/skill/**` pattern never
matches. `TestProbeA9SkillEditInGitWorktree` (the fix round's own probe,
folded into `internal/execution/adapters/opencode_guard_integration_test.go`
as `TestOpenCodeIncludesEditDeniedInGitWorktree`) reproduced this directly: an
`edit` of `NIGHTGAUGE_SKILL_DIR/_includes/note.md` succeeded in a git
worktree under the pre-fix map, with the file's content actually rewritten —
AC2 broken in exactly the shape every real dispatch runs in. The amendment's
first bullet ("no glob metacharacter") does not hold up either: the same
git-worktree probe denied the edit with a literal, no-wildcard relative
pattern once it was the CORRECT relative form, so the missing wildcard was
never the defect — the absolute-vs-relative path was.

`external_directory`'s own patterns are unaffected by this correction: they
are matched against the file's absolute path (or, for a directory a shell
command's argument resolves into, `dirname(file)`), never against a
worktree-relative form, which is why the amendment's third bullet (needing
both the given and the resolved directory form) still holds, and why
`external_directory`'s allow-list entries correctly keep their leading `/`
while `edit`'s deny entries must not have one relative to `/` — or, in a real
git worktree, must instead be the file's path relative to the worktree's own
top-level, with `../` segments where the directory is outside it.

**The fix.** `openCodeWorktreeRelativeDirPatterns` (`opencode_guard.go`)
replaces `openCodeEditDenyPatterns`: it resolves the git top-level of
`RunOptions.WorktreeDir` by walking its ancestors for a `.git` entry — never
a `git` subprocess, so the permission-map builder stays pure — and computes
the deny pattern relative to that top-level (or to `/` when `WorktreeDir` is
empty or not a git repository, preserving the amendment's fixture-only
behaviour for a non-git test double). The same function now also denies edit
of `NIGHTGAUGE_BIN`'s own directory: the external_directory allow-list
already let a stage read there, but nothing had ever denied `edit`, so any
Edit-granted stage — every one of the six stage skills — could plant an
executable in the running nightgauge binary's own directory, typically a
`PATH` entry, before this fix
(`TestOpenCodeBinDirEditDenied`/`TestProbeA9BinDirWritable`).

**A second, narrower correction: nested secret files.** The read/edit
backstop's `*.env`/`.env*` entries, matched the same worktree-relative way,
only ever match a ROOT-level file (opencode's pattern matching has no
implicit `**` prefix the way a shell glob does). A nested secret such as
`apps/web/.env.local` fell through to the backstop's own `*`/`.env`-shaped
entries and reached `read: "*": "allow"` unmatched. `**/*.env` and
`**/.env*` were added to `openCodeSecretDenyBackstop` to close it
(`TestOpenCodeNestedDotEnvDenied`/`TestProbeA9NestedEnvLocalRead`; a routed
#1752 comment on the issue asked for exactly this coverage).

**AC3 closed, not just raised.** The finding this document's previous
section left open — a plain `deny` rejection has no stderr notice, so
`OpenCodeAutoRejectMarker`'s stderr-based classification never fires for
one — turned out to have an existing fallback already built for it:
`openCodeRun.finish` (`internal/execution/opencode_usage.go`) already
classifies a run as rejected, never success, whenever the stream shows a
`RejectedToolCalls > 0` and stderr named no permission. That fallback simply
never fired, because `RejectedToolCalls`'s own match
(`internal/execution/stream.go`) was an exact-string comparison against
`"The user rejected permission to use this specific tool call."` — the
`ask`-and-auto-rejected text — and a `deny` match's real text is different:
`"The user has specified a rule which prevents you from using this specific
tool call. Here are some of the relevant rules […]"` (bundled source,
confirmed against the pinned binary). `openCodeIsRejectedToolError` now
matches either text as a PREFIX (the ruleset list, and an interactive
rejection's own feedback text, both trail their respective prefixes
dynamically), on both the Go and the TS/SDK parser
(`packages/nightgauge-sdk/src/cli/adapters/opencodeStream.ts`, which
`internal/execution/testdata/opencode_stream_expected.json` binds to the same
fixtures) — `TestOpenCodeDenyRejectedNeverSuccess` and the TS suite's own run
over the newly captured `opencode_deny_rejected_stream.jsonl` are the
red/green coverage. #1631's own `PermissionRejectedMarker`/
`PermissionDeniedMarker` split still cannot name the specific permission a
plain tool_use error refused (the event names the tool, not the permission),
so the fallback's marker is always `tool=unknown`; a drift marker records why,
exactly as it already did for the `ask`-with-lost-stderr case this fallback
was originally built for.

**The tamper gate's base ref (AC6).** `openCodeProjectConfigTamperCheck`
compared the worktree only to `HEAD`, on the premise that a fresh worktree's
`HEAD` is the base branch's tip. That premise fails for every stage after the
first in a worktree `Manager.RunStage` reuses across a run: a stage with
`Bash` can commit its own tamper, and `git status` alone never sees a
difference from `HEAD` once it has. `openCodeProjectConfigTamperCheck` now
runs three legs — the existing `git status` leg; `git diff --name-only
<merge-base-with-the-resolved-base-ref>` for a change already committed on
top of the base branch's tip (`openCodeTamperGateBaseRef`, a local,
subprocess-free mirror of `internal/execution/worktree_sweep.go`'s own
`resolveBaseRef`/`detectDefaultBranch`, duplicated rather than imported
because `internal/execution` imports this package and the reverse would
cycle); and `git ls-files -v`, because `git update-index
--skip-worktree`/`--assume-unchanged` hides a working-tree edit from both the
status and the diff legs entirely (`TestOpenCodeTamperGateCommittedChange`,
`TestOpenCodeTamperGateSkipWorktree`).

**AC4's `/tmp` allow-list: found dead, left dead, recorded (AC8).** A
probe read Tool.assertExternalDirectory's own bundled source: every
`external_directory` request — the file tool's own out-of-worktree check,
and each directory a bash command's argument resolves into — asks with
`patterns:[path.join(dirname(file), "*")]`, never the file's own path.
`Wildcard.match` then compares that literal `dirname/*` string against each
rule. None of `openCodeTmpAllowList`'s 16 per-file entries (`/tmp/planning_tmp.json`
and the rest) can ever equal `/tmp/*`, so every one is dead: a Read-granted
stage's read of any of those exact, allow-listed files is refused
(`TestProbeA9TmpAllowListRead`, reproduced and left red on purpose — this is
the one finding this fix round does NOT close in code). The only pattern
`external_directory`'s own matching can ever honour at this granularity is
the directory itself, `/tmp/*` (and `/private/tmp/*`): opencode 1.18.30 has
no mechanism to allow-list one file inside a shared directory without
allowing every file directly in it. Setting that pattern would fix AC4's
letter but open exactly the hazard this ADR's own Security constraints name
first: `/tmp` shared with other processes. That is a product decision, not a
code defect this file's ownership can make unilaterally — per this
document's own § 9 "stop and record" rule (the issue's AC8) — so this fix
round leaves the allow-list exactly as it was (a list of the literal paths,
inert against `external_directory`) and records the finding here instead of
silently widening exposure. The follow-up direction, not yet an issue: move
the six stage skills' `/tmp` scratch files into a directory scoped to one
run (`RunRoot`'s own `tmp` subdirectory is the obvious candidate — already
present, already per-run) and allow-list that directory instead of
`/tmp` itself, closing AC4 without the shared-directory hazard.

## Second fix round: AC3 tool naming, AC4's /tmp allow, #1752's widening, NIGHTGAUGE_BIN, tamper-gate fail-closed (amendment 2026-09-15, #1638 fix round, stages D/E)

A delegated fix round closed four review findings the round above left open
or recorded rather than fixed, plus one trivial low, each with red/green
Go coverage and, where the finding is about opencode's own runtime
behaviour, a bounded probe against the pinned 1.18.30 binary.

**AC3, finally closed with a real permission name, not `tool=unknown`.**
The round above's "AC3 closed, not just raised" section left the fallback
marker (`openCodeRun.finish`'s own path, hit whenever the stream shows a
`RejectedToolCalls > 0` and stderr named no permission — a "deny" match's
own shape, since it never prints the stderr notice) naming
`openCodeUnknownPermission` ("unknown") unconditionally, because "the event
names the tool, not the permission." That reasoning undersold what the
event actually carries: a rejected `tool_use` event's own `part.tool` is
the opencode tool name the model called (`"bash"`, `"edit"`, `"read"`,
`"apply_patch"`, …) — not the specific permission sub-category
(`external_directory` vs. the base permission) that ultimately refused it,
but a real, useful name all the same, and the one AC3's own Verification
bullet asks for (`[adapter-permission-rejected]` for a granted tool,
`[permission-denied]` for one that is not). `OpenCodeStream.RejectedTool`
(`internal/execution/stream.go`) now records the FIRST rejected tool_use
event's own `part.tool`; `openCodeToolRejectionPermission`
(`internal/execution/opencode_usage.go`, and its TS twin in
`opencodeStream.ts`) maps `write`/`apply_patch` to `edit` — 1.18.30 has no
permission of its own for either — and passes every other name through
unchanged; `openCodeRejectionMarker(tool, allowed)` then classifies exactly
as it already does for a stderr-sourced permission name.
`TestOpenCodeDenyRejectedNeverSuccess` and `TestOpenCodeAutoRejectMarker`
(Go) and the TS suite's own two cases now assert the EXACT marker for both
an allowed and a not-allowed tool set, not merely that one of the two
markers is present.

**AC4's `/tmp` allow-list: the per-file design confirmed impossible,
replaced with a directory-level allow.** The round above's own bounded
probe (`Tool.assertExternalDirectory`'s bundled source) already found every
`external_directory` request is `patterns:[path.join(dirname(file), "*")]`,
never the file's own path, and recorded the per-file allow-list as dead
without fixing it, "a product decision, not a code defect this file's
ownership can make unilaterally." This fix round's own decision: the
per-file design is impossible on 1.18.30, full stop, so the directory-level
allow — literally `/tmp/*` and `/private/tmp/*`
(`openCodeTmpDirAllowPatterns`, `opencode_guard.go`) — is the only shape
that can ever work, replacing `openCodeTmpAllowList`'s sixteen dead
entries. The accepted cost: every stage's Read/Edit-governed tool calls can
now reach any OTHER file directly under `/tmp` or `/private/tmp`, not only
the ones its own skill uses — shared `/tmp` readability and writability by
`read`/`edit`, the hazard the round above named and declined to open. Two
things bound that cost: the secret and project-config deny-list backstops
(`openCodeSecretDenyBackstop`, `openCodeProjectConfigDenyBackstop`) are
still checked LAST and still win over this allow wherever they apply
(`*.env`, `**/.ssh/**`, `opencode.json*`, …, whether or not the file
happens to sit under `/tmp`); and the `external_directory` check itself is
only ever a lexical backstop, not a sandbox — a bounded probe this fix
round ran (a bash `mv` moving a file OUT of an allow-listed `/tmp`
directory, across two different external directories in one command) found
it refused even with an explicit allow for the source directory, while a
single-path bash `cat` or `cp` of the same file was correctly allowed or
refused by the matching directory-level rule — so bash redirection or a
multi-path command is not reliably covered by this control at all, allowed
or denied, and was never claimed to be. `TestOpenCodeTmpDirAllowLetsAStageReadAndCatFromTmp`
(a real-binary probe against actual files under `/tmp`, not a hand-patched
allow entry) is the closure; `TestOpenCodeTmpAllowListCoversStageSkills`
now asks whether a scanned `/tmp/...` literal's own `dirname(literal)+"/*"`
request (`openCodeTmpRequestPattern`) is covered by
`openCodeTmpDirAllowPatterns`, not whether the literal string itself is in
a list — the six stage skills' own `/tmp` literals are all flat, directly
under `/tmp`, so this holds today; its red companion introduces a NESTED
`/tmp/sub/dir/...` literal, which this flat pair does not cover, and does
fail. **Follow-up, not yet its own issue:** move the six stage skills'
`/tmp` scratch files to a directory scoped to one run (`RunRoot`'s own
`tmp` subdirectory, already present, already per-run) and allow-list that
instead of `/tmp` itself, closing the shared-directory cost this amendment
accepts rather than removing it.

**#1752's widening: `*.env.*` as an infix, not only a prefix or suffix.**
The round above's nested-secret fix (`**/*.env`, `**/.env*`) still only
ever matches a filename that STARTS with `.env` or literally ends in
`.env`. A file such as `config/prod.env.local` — `.env` as an INFIX, the
name neither starts nor ends with it — matched none of those patterns, at
any depth, and opencode 1.18.30's own bundled default `read` guard already
treats `*.env.*` as a shape worth an `ask` rule in its own right (the
Failure wording amendment above, "A stage allowed Read that reaches for
`*.env`"), independent evidence this is a real secret shape, not a
speculative widening. `*.env.*` and `**/*.env.*` are added to
`openCodeSecretDenyBackstop`, paired the same root/nested way as the
existing `.env`-prefixed entries. `TestOpenCodeNestedDotEnvDenied` now
builds every tool path from `filepath.EvalSymlinks(dir)` (matching what a
real dispatch event actually carries, not the given, unresolved form),
asserts BOTH `apps/web/.env.local` and `config/prod.env.local` are denied,
and includes a control read of `config/plain.txt` — a name with no secret
shape at all — that MUST complete, proving the deny is targeted rather than
a broader nested-read regression.

**NIGHTGAUGE_BIN removed from the `external_directory` allow-list.** A scan
of the six stage skills' own committed text for a concrete Read, `cat` or
`cd` of a path under `NIGHTGAUGE_BIN` found none: every reference is
`BINARY="${NIGHTGAUGE_BIN:-}"` followed by running `$BINARY` (bash
execution — never gated by `external_directory` at all, the same "lexical
backstop, not bash redirection" limitation this amendment's AC4 section
records) or `export PATH="$(dirname "$BINARY"):$PATH"` (a shell variable
assignment, never a filesystem read). The allow-list entry bought no stage
skill anything it uses, while letting a Read tool call inspect the running
nightgauge binary's own directory — a self-hosted checkout's own build
output, or any sibling files an operator placed beside the binary.
`openCodeExternalDirectoryAllowList` no longer takes a `binDir` parameter
at all; `openCodeWorktreeRelativeDirPatterns`' own edit-deny backstop
(finding 8, round above) is unaffected and still denies EDITING the
directory as defense in depth, now redundant with `external_directory`'s
own `"*": "deny"` default rather than the allow-list's own carve-out.
`TestOpenCodePermissionMapNeverAllowsBinDir` is the unit coverage;
`TestOpenCodeBinDirCpDenied` is the real-binary probe the issue's own item
4 asked for — a bash `cp` planting a file into `NIGHTGAUGE_BIN` is refused,
naming `external_directory` in the rejected `tool_use` event, unlike the
multi-path `mv` probe above.

Unrelated to this dir, but bearing on when it could ever be reopened for a
GPT-family dispatch: `apply_patch`'s own `*** Move to:` header (§ 6, and
the `#1640` amendment above) names a patch's destination path, but nothing
in this repository's `apply_patch` handling reads that header today —
`gates.js` classifies the whole tool `"blocked"` (the `#1640` amendment),
so no Move-to destination ever reaches a workflow-gate or a permission
check at all while that classification stands.
[nightgauge/nightgauge#1808](https://github.com/nightgauge/nightgauge/issues/1808)
tracks mapping `apply_patch`'s `patchText` to Claude-shaped file payloads;
until that includes gating a Move-to destination the same way an `edit` or
`write` tool call's own path is gated, `apply_patch` must stay `"blocked"` —
lifting the block first would let a Move-to destination bypass every
path-shaped control this ADR and `gates.js` both rely on, this
`external_directory` allow-list included.

**Trivial low: the tamper gate now fails CLOSED on an unverifiable
worktree.** `openCodeProjectConfigTamperCheck` treated `git status`
reporting "not a git repository" as "nothing to compare, so allow it" —
backwards for a gate whose entire job is refusing a worktree it cannot
verify. A `worktreeDir` git reports is not a repository (a dispatch config
naming a path outside any checkout, or one whose `.git` a prior stage
removed or corrupted) now REFUSES, naming the reason
(`TestOpenCodeTamperGateNonGitWorktreeFailsClosed`); a `git status`
invocation that fails for any OTHER reason (a corrupted index, tested by
`TestOpenCodeTamperGateGitStatusFailsClosed`) already refused before this
fix and is unchanged. `worktreeDir == ""` (no worktree named at all — never
a real dispatch, only a caller that supplies none, including the dozens of
this package's own `PreDispatch` tests that construct a `RunOptions` with
no `WorktreeDir` to test something else entirely) is unaffected: it is
still skipped, not refused, since there is no worktree to have lied about.
Every fixture across `internal/execution`, `internal/execution/adapters`
and `cmd/nightgauge` that dispatched through a plain, non-git `t.TempDir()`
worktree to test something OTHER than the tamper gate itself now
git-initializes it first (`gitInitOneCommitWorktree`,
`gitInitTestWorktree`, `isolateOpenCodeVerb`'s own fixture) — the same
shape every real dispatch's worktree already has by construction.

## Correction to the `/tmp` allow-list: `*` crosses `/`, so `/tmp/*` was the whole tree, not one level (#1638 fix round, same day)

The second fix round above recorded the `/tmp` allow-list's accepted cost as
"every OTHER file directly under `/tmp` or `/private/tmp`, not only the ones
its own skill uses" and asserted the backstops "still win... whether or not
the file happens to sit under `/tmp`." Both sentences assumed opencode
1.18.30's `Wildcard.match` never lets a configured `*` cross a `/`, the same
assumption this document's "Correction to the pattern-matching amendment"
section above already had to retract once for `edit`'s own patterns. It does
not hold for `external_directory` either: the bundled matcher (`o.replace(/[.+^${}()|[\]\\]/g,"\\$&").replace(/\*/g,".*").replace(/\?/g,".")`,
tested anchored `^...$`) turns a configured `*` into the regex `.*`, which
matches across `/` exactly like a real glob's would. A bounded probe against
the pinned binary (`TestOpenCodeTmpDirAllowDoesNotReachANestedFile`,
`opencode_guard_integration_test.go`) found a NESTED file's own
`external_directory` request (`dirname(file)+"/*"`, two directories below
`/tmp`) matched the configured `/tmp/*` entry, and both a `read` of it and a
`write` of a sibling at the same depth completed — the accepted cost was the
whole `/tmp` and `/private/tmp` trees, at any depth, not one level. A flat
`/tmp` symlink whose target is outside every allow-listed directory matched
the same way, so the sentence above claiming the secret and project-config
deny-list backstops "win... whether or not the file happens to sit under
`/tmp`" does not hold for that shape either: those backstops are lexical on
the path string a tool call reports, which does not resolve a symlink to its
target — a pre-existing property of a lexical backstop, not something this
correction changes or closes.

`openCodeTmpDirAllowPatterns` is now `/tmp/?` and `/private/tmp/?` (`?`
becomes the regex `.`, exactly one character, never `/`), not `/tmp/*`.
Opencode's own `external_directory` request for a FLAT file directly under
`/tmp` is always the literal six-character string `/tmp/*` — never the
file's own name (§ "AC4's `/tmp` allow-list" above) — and that string's only
character after `/tmp/` is the literal `*`, which `/tmp/?` matches; a
NESTED file's request has more than one character there and does not match.
The accepted cost is now what the second fix round's own prose intended:
every stage's Read/Edit-governed tool calls can reach any OTHER file
directly under `/tmp` or `/private/tmp`, never a nested one. The flat-symlink
gap above is unaffected by this narrowing (the symlink's own path is still
flat), and stays open — the same follow-up recorded below (moving the six
stage skills' `/tmp` scratch files to a per-run scratch directory) is what
would close it, by removing the need for this allow entry at all.

`openCodeTmpCoverageMissing` (`opencode_guard_test.go`) is now
`openCodeWildcardMatch` (`opencode_guard.go`), opencode's real matcher, not
the plain map lookup the second fix round above described as "whether a
scanned `/tmp/...` literal's own `dirname(literal)+"/*"` request... is
covered by `openCodeTmpDirAllowPatterns`" — that description was accurate
about the INTENT, but the lookup it shipped with was exact-string equality
against the (still-`/tmp/*`-shaped) source list, which happened to agree
with the real matcher only because the pre-narrowing pattern was identical
to the request string it needed to match. Swapping in `/tmp/?` without this
change would have made the drift test (`TestOpenCodeTmpAllowListCoversStageSkills`)
report the six stage skills' own flat literals as uncovered, a false red;
`TestOpenCodeTmpAllowListCoverageFailsOnANewLiteral`'s own red companion is
unchanged in shape, still a nested literal the flat pattern does not cover.

`TestOpenCodeOutsideReadRejected` and `TestOpenCodeBinDirCpDenied` each gain
a `TMPDIR=/tmp`-forcing companion
(`TestOpenCodeOutsideReadRejectedUnderTmpdirTmp`,
`TestOpenCodeBinDirCpDeniedUnderTmpdirTmp`). With `TMPDIR` unset — this
repository's own `ubuntu-latest` CI default, and the common Linux developer
setup — Go's `t.TempDir()` places every fixture these two tests treat as
"outside the allow-list" under `/tmp` itself, one level down; against the
pre-narrowing `/tmp/*` pattern both tests' own probes completed instead of
erroring, a platform-dependent gap this document's own prior amendments did
not carry a leg for. Forcing `TMPDIR=/tmp` reproduces the shape on any
platform, including this document's own macOS-based probes above, whose
default temp root (`/var/folders/...`) never exercised it.

## OpenCode plugin edit hooks: `tool.execute.after` argument shape (amendment 2026-09-15, #1642)

#1642's own AC7 requires recording, here, any observed divergence from its
stated assumptions before continuing. The issue's assumptions read
`tool.execute.after` receives the tool's args and a mutable `output.output`;
`edit`/`write` args are `filePath`/`oldString`/`newString`/`content`" without
saying which side of the call — `input` or `output` — carries `args`. A
bounded, offline probe against the pinned 1.18.30 binary (a logging plugin
loaded in place of `plugin/nightgauge/edit.js`, driven through the #1618
stub's `tool-edit-stop` script and a temporary `write-then-stop` fixture, no
live model and no network egress) settles both halves:

**`args` live on `input`, not `output` — the opposite of `tool.execute.before`.**
`tool.execute.before`'s `args` are on `output.args` (gates.js, already
documented). `tool.execute.after`'s own `input` instead carries
`{tool, sessionID, callID, args}` directly — captured verbatim off the real
binary:

```json
{"hook":"after","input":{"tool":"edit","sessionID":"ses_...","callID":"call-stub-tool-edit-stop-0","args":{"filePath":"calc.py","newString":"return a - b","oldString":"return a + b"}},"output":{"metadata":{...},"title":"calc.py","output":"Edit applied successfully."}}
```

and the same shape for `write`, with `args: {content, filePath}`. `output`
itself carries `{title, output, metadata}`, confirming the issue's own
`output.output` assumption. `edit.js` reads `input.args`, not `output.args`,
accordingly.

**Correction (fix round, 2026-09-15): the capture's `filePath: "calc.py"` is
an artifact of the stub fixture, not of 1.18.30's own argument shape.** The
`{tool, sessionID, callID, args}` envelope above is genuine, but the
`tool-edit-stop`/`write-then-stop` stub fixture that produced it supplies its
own relative `filePath` value; the installed 1.18.30 binary's own tool
schemas (read via `strings` on the pinned binary) require an ABSOLUTE
`filePath` for both tools — write's own schema text reads "The absolute path
to write... must be absolute, not relative", edit's reads "The absolute path
to the file to modify" — and this repository's own real-model captures
(`internal/execution/testdata/opencode_stream_local_capture.jsonl`,
`..._remote_capture.jsonl`, `..._subagent_capture.jsonl`) each show an
absolute `filePath` on a real `edit` call, e.g.
`/tmp/nightgauge-fixture/repo/calc.py`. `edit.js`'s first cut refused every
absolute path outright (mirroring `internal/hooks/format.go`'s
`ValidateFilePath`), which left format/check-version/test-quality dead
against a real model; it now resolves an absolute `filePath` against the
run's cwd the way `format.go`'s own `relativizeHookPath` resolves one for
the Claude Code hook path, and refuses only a path that resolves outside it.

**Mutating `output.output` in place reaches the model's next turn.** The
probe's `tool.execute.after` appended a marker string to `output.output`
in place (no reassignment of `output` itself, no return value). The very
next `POST /v1/chat/completions` request the stub-provider received carried
that marker, byte for byte, as the `tool`-role message's own `content` —
not merely visible within the hook's own scope, and not silently dropped
before the session's own transcript is built. This confirms the issue's
"so the model sees them" half of AC5 without qualification.

## Dispatch-time symlink resolution for `read` (amendment 2026-09-20, #1816)

§ "The permission map's pattern matching" (amendment 2026-09-15, #1638)
already documents that opencode 1.18.30 matches `external_directory` (and
every other permission key) lexically: it compares a tool call's `filePath`
string against the generated glob patterns, never resolving symlinks at
match time. That amendment's own generator-side fix
(`openCodeDirPatterns`/`openCodeWorktreeRelativeDirPatterns`) resolves
symlinks too, but only **once, at config-generation time**, to compute the
pattern strings opencode's matcher later applies lexically. A symlink
planted **after** the config is written — at any point during the stage's
own run — is invisible to that one-time resolution: opencode's matcher is a
pinned third-party binary this repository does not control, and no
config-generation-time fix can catch a runtime-planted symlink. #1816 (an
out-of-scope finding from #1638's own adversarial review) is this gap made
concrete: a symlink placed directly under an allow-listed directory
(`NIGHTGAUGE_SKILL_DIR`, the context/output file directories, or the `/tmp`,
`/private/tmp` scratch literals), pointing anywhere outside every
allow-listed directory, is matched by the allow pattern on the link's own
lexical path — its resolved target is never checked — and the read
completes.

The fix cannot live in the permission-map generator, so it lives at the one
enforcement surface this repository does control with access to the actual
filesystem at the moment of the tool call: the Nightgauge OpenCode plugin's
own `gates.js` (`tool.execute.before`, § "Nightgauge OpenCode plugin"
above), which already runs `nightgauge hook <verb>` as a real subprocess
gate for `bash` and `edit`/`write`. `read` was classified `"passthrough"`
there (no gate at all, opencode's own lexical permission map the only
check); it is now `"read"`, dispatching to a new verb, `nightgauge hook
external-directory-gate` (`internal/hooks/external_directory_gate.go`).

The new gate resolves the file's real location with `filepath.EvalSymlinks`
against the **full file path**, not merely its directory (`filepath.Dir`)
before resolving — resolving only the directory misses a symlink that is
the file's own last path component, which is exactly this issue's attack
shape. It then checks the resolved directory for containment in the
worktree or in `internal/opencodeallow.RootsFromEnv`'s allow-listed roots
(the same roots `internal/execution/adapters`'
`OpenCodeExternalDirectoryAllowRoots`/`openCodeExternalDirectoryAllowList`
project into glob patterns for the config, kept as one shared, tested
source of truth so the plain-directory and glob-pattern views can never
diverge — `TestOpenCodeExternalDirectoryGateRootsMatchAllowList`). The two
packages cannot share this logic via a direct import: `internal/hooks`'
own tests import `internal/execution/opencodeplugin` (to drive the embedded
plugin end to end), and `internal/execution/adapters` also imports
`internal/execution/opencodeplugin` (to embed the plugin tree into a
dispatch), so `internal/hooks` importing `internal/execution/adapters`
directly would close an import cycle
(`opencodeplugin -> hooks -> adapters -> opencodeplugin`). The roots logic
instead lives in a standalone leaf package, `internal/opencodeallow`, that
neither side needs to route through the other to reach.

`printPreToolUse` (`cmd/nightgauge/hookoutput.go`) already gives this gate
AC2 ("classified like any other permission rejection") for free: a `Block`
decision renders as the identical `hookSpecificOutput.permissionDecision:
"deny"` shape `gates.js`'s `runGateVerb` already parses into a marker-prefixed
throw for every other gate here, so this refusal needed no new
classification code.

Like every other lexical fallback this document records, resolving only
`glob`/`grep`/`list`/`webfetch` etc. stays out of scope: the issue's own ACs
and verification section are specifically about a `read` through a planted
symlink, and broadening to every path-bearing tool would need its own probe
per tool this issue's evidence does not cover.

## Routing, cap-hop, `--variant` and session resume (amendment 2026-09-20, #1643)

§ "Consequences" below already named the direction — `ProviderFor(adapter,
model)` replacing `ProviderForAdapter` at every caller that needs the serving
provider — as a consequence of this ADR before a caller had actually moved.
#1643 is that move: `internal/orchestrator/cap_recovery.go` (`nextCapProvider`,
`capProviderOf`) and `internal/orchestrator/dispatch_envelope.go`
(`resolveDispatchThinking`) now call `ProviderFor`, so an `opencode` candidate
configured against the just-capped provider is skipped in a cap-hop walk, and
opencode's dispatched model resolves the correct provider for thinking-default
attribution. `nextCapProvider`'s skip check needed a new injection point,
`CapRecoveryInput.CandidateModel`, because a chain candidate's adapter name
alone cannot answer "which model would this candidate run" the way the
stage's own already-resolved model can — the scheduler's default reads the
machine-tier `opencode:` block's flat `Model` field; a nil resolver (every
call site and test before this issue) falls back to `ProviderForAdapter`'s
answer unchanged.

**Deviation from the plan, per this issue's own AC 8 escape valve**: the issue
named `opencode models --verbose` as the source for per-model declared
variants (the effort → `--variant` mapping). No captured fixture of that
flag's output against opencode 1.18.30 exists (`testdata/opencode-cli` holds
only `run --help` and `version` — see its README), so the doctor's catalog
probe (`internal/doctor/opencode.go`) is **not** extended to parse it: doing
so would mean shipping an unverified parser against a CLI shape nobody has
observed, exactly what AC 8 says to stop and record rather than do. The
`--variant` mapping instead reads only the config-declared source that was
already end-to-end wired for a different purpose
(`config.OpenCodeEndpointModel.Variants` → `openCodeModelJSON.Variants` in
`internal/execution/adapters/opencode_config.go`, itself commented "#1643's
--variant mapping is what reads it"): `openCodeVariantsForModel`
(`internal/execution/adapters/opencode.go`) looks up the dispatched model's
declared endpoint entry and `BuildCommand` emits `--variant <effort>` only
when the dispatched `RunOptions.Effort` is itself one of that model's declared
variants — never a guessed rung, and nothing for a model that declares no
variants at all (effort records "not applicable" for those, same as every
other adapter with no Go-visible effort evidence, § "Provider-aware call
sites" above / `dispatch_envelope.go`'s `#580` convention). Reopen the
`--verbose` catalog-probe extension once a real capture exists; until then this
is the complete, honest source.

Session resume (`RunOptions.ResumeSessionID`, `-s <id>` in `BuildCommand`)
follows § 8's reading of the retention window unchanged: the run's own data
directory, not a separate TTL. `RuntimeState.RecordStageOpenCodeSession` /
`StageOpenCodeSession` mirror `RecordStageServedModel`'s pattern, and the
scheduler (`resolveResumeSessionID`, `internal/orchestrator/dispatch_envelope.go`)
passes the recorded id to a retry of the SAME stage only when adapter, model
AND worktree all still match the attempt that recorded it — any of the three
changing (a cap-hop, a tier descent, a worktree switch) starts a fresh
session. The id itself is captured by reading back the same events file
(`opencodeplugin.EventsPath`/`ReadRunEvents`) the session-lifecycle plugin (§
"Session-lifecycle events plugin" amendment above, #1641) already writes to —
no new writer, only a new reader. `-s` and `--variant` are both validated
against a fixed, non-guessable shape before they ever reach argv
(`openCodeSessionIDRE`, `openCodeRejectsFlagValue`), mirroring
`OpenCodeModelArg`'s existing reject-on-leading-dash guard for `-m`: never a
session id, variant or model value read from config, a prompt or model
output, only one this run itself recorded or the operator declared.

## Server mode measured: `serve`, `run --attach`, the HTTP approver and `json_schema` (amendment 2026-09-21, #1650)

Observed against **opencode 1.18.31**, one patch above this ADR's floor and max-tested.
The full evidence, per-experiment commands and reap log are in
[`docs/spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md`](../spikes/1650-opencode-server-mode-warm-serve-run-attach-http-permission.md).
§ 15's disposition rows for `json_schema`, the GitHub agent, ACP and
`serve`/`run --attach` are updated above by this amendment.

**An attached run's isolation is the server's, not the run's.** This is the finding that
governs every other server-mode decision. With `OPENCODE_CONFIG_CONTENT`, all four XDG
base directories, `HOME`, `TMPDIR` and the permission map of a `run --attach` all pointed
at a second sandbox, the model request went to the **server's** declared endpoint and the
transcript was written to the **server's** session database (249856 bytes) while the
attached run's stayed empty (4096 bytes). The attached `run` is a thin HTTP client; the
server is the agent. Everything § 8, § 9, § 15 and § 17 build — per-run XDG roots, the
per-run config, the permission map, the pinned models, the one credential — is therefore
a property of the **spawn**. A server shared across stages collapses them into one
identity, one credential set, one permission map and one transcript database, and a
server per stage saves nothing (a 1217 ms boot against a ~1.05 s per-stage startup
saving, measured on the #1618 stub). `serve`/`run --attach` stays **deferred** behind
that collision, and behind a prefill measurement no machine here could take.

**§ 9's auto-rejection is a property of `run`, not of the agent.** Headless
`opencode run` rejects an `ask` automatically and exits 0. Routed through the server, an
unanswered `ask` **hangs indefinitely** — a `bash` tool part was still
`{"status":"running"}` at t+29 s, never auto-rejected and never timed out. The § 15 row
"`ask` permissions — never generated" is what keeps this unreachable today, and it must
stay locked: under server mode an `ask` fails **open into a hang**, not closed.

**An HTTP approver works, through the v1 pair only.** `permission.asked` arrives on a
directory-scoped `GET /event?directory=<worktree>` (the unscoped stream carries only
`server.connected` and `server.heartbeat`), and
`POST /session/{sessionID}/permissions/{permissionID}` with `{"response":"reject"}`
returns 200 in 3 ms, after which the model sees
`"The user rejected permission to use this specific tool call."` The reply body admits no
reason, both v2 pending-ask list endpoints report the ask as absent while it is pending,
and the v2 reply route 404s a v1-issued ask. The two API generations are **not
interoperable** on this build.

**`json_schema` output is a non-goal.** `POST /session/{id}/prompt`, the only route that
declares `format` and returns a synchronous body, is **not routed** on 1.18.31 and serves
the web UI's HTML; the v2 `POST /api/session/{id}/prompt` has no `format` field at all.
`prompt_async` accepts `format`, persists it, and then `GET /session/{id}/message`
returns **400** for the whole session —
`Expected OutputFormatJsonSchema, got {"type":"json_schema","schema":{"type":"object"},"retryCount":0}`
— rejecting a value byte-for-byte identical to what the server's own published
`OutputFormatJsonSchema` component declares valid. A `json_schema` prompt makes the
transcript unreadable, and `retryCount` is not enforced. Stage output contracts stay on
the `--format json` stream and § 2's parsing.

**§ 18 holds, and gains two corrections.** Plain `opencode run` without `--port` or
`--attach` opened **no** TCP listening socket on 1.18.31, measured mid-request. Two
details differ from what § 18's guardrails assumed:

- `--hostname` **already defaults to `127.0.0.1`** on `serve`, `acp` and the top-level
  command. The guardrail is the default; pass it explicitly anyway so an inherited
  default can never move it.
- `serve` has **no `--password` flag**. Authentication comes solely from
  `OPENCODE_SERVER_PASSWORD` in the environment, so the secret has no argv path — the
  property § 18 wants. Nothing should add one. (`run --attach` does take `-p`/`--password`,
  defaulting to the same variable.)
- `opencode acp` carries `--port`, `--hostname`, `--mdns` and `--cors` too. § 18's
  guardrails apply to it unchanged.

Confirmed as stated: `serve` is unauthenticated unless `OPENCODE_SERVER_PASSWORD` is set
(with it unset, `/session`, `/config` and `/api/permission/request` answered 200 to any
local caller; with it set, every probe answered 401), `--mdns` "defaults hostname to
0.0.0.0", the OpenAPI surface is 162 paths of OpenAPI 3.1.0, and `info.version` is a
static `"1.0.0"` that never tracks the release — so no compat check may key on it.

**Recorded, not characterised.** A plain `run` whose only model endpoint was a loopback
stub, with `OPENCODE_DISABLE_MODELS_FETCH`, `_AUTOUPDATE`, `_LSP_DOWNLOAD`,
`_DEFAULT_PLUGINS` and `_SHARE` all set and no credentials, held 28 established outbound
connections to 104.16.0.0/16:443 alongside the loopback one, reproduced across two runs.
The peer was not identified and nothing is asserted about the content; § 10's egress
defaults and `scripts/opencode-egress-check.sh` own the question.

## The worktree's other forms in `external_directory` (amendment 2026-09-22, #1651)

A live feature-dev run on a worktree reached through `/tmp` (a symlink to
`/private/tmp` on macOS) had a tool call naming a `/tmp/...` path refused.
OpenCode compares a path to its instance directory lexically, and that
directory is the resolved one, so the `/tmp` form of a worktree file asked
`external_directory`, whose allow-list had no entry for it.

- The allow-list now leads with the worktree itself, in every form a tool
  call can name it: as given, resolved through symlinks, and the resolved form
  re-rooted on `/tmp` when `/tmp` is a symlink to its prefix
  (`openCodePathForms`). `EvalSymlinks` cannot find that last form, because
  the symlink points at the path, not out of it. The NIGHTGAUGE_SKILL_DIR and
  context-directory entries and the edit-deny directory patterns use the same
  forms.
- An edit through another form of the worktree asks with a path relative to
  the resolved worktree (`../../../tmp/wt/opencode.json`), which the bare
  project-config backstop does not match. The edit map therefore also denies
  `opencode.json*` and `.opencode/**` under each such relative prefix.

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
