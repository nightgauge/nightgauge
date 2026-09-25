# Adapter Doctor

The **Adapter Doctor** answers "is my adapter set up correctly?" on demand,
instead of letting misconfiguration (CLI not installed, not logged in, stale
version, MCP not provisioned) surface as a mid-run pipeline failure.

It exists in two complementary layers, each the authority for the part it owns —
there is no duplicated probing logic.

| Layer                             | Consumer                    | Reports                                                                 |
| --------------------------------- | --------------------------- | ----------------------------------------------------------------------- |
| Go `nightgauge doctor --adapters` | Skills (Phase 0), CLI users | Deterministic binary presence + version (vs floor) + Codex MCP config   |
| VSCode **Adapter Doctor** command | IDE users                   | The Go facts **plus** auth status, per-stage routing, remediation, a UI |

## VSCode command

Run **"Nightgauge: Adapter Doctor"** from the command palette
(`nightgauge.adapterDoctor`). It opens a webview reporting, for every
adapter the pipeline resolves to (per stage + the global default):

- **Install / Version** — CLI binary on PATH and its version vs the known floor
  (CLI adapters); API-key configured (SDK adapters); local-model env set (HTTP
  adapters). Sourced from the Go binary.
- **Auth** — authenticated or not, with the failure reason. Sourced from the SDK
  `runAdapterAuthPreflight` (`codex login status`, `claude auth status`, the
  Gemini key/ADC cascade, the Copilot token/CLI cascade).
- **Codex MCP** — whether `$CODEX_HOME/config.toml` exists and carries the
  nightgauge managed MCP block.
- **How to fix** — a concrete remediation per failure (e.g. ``Run `codex
login`.``), merged from the Go binary's install/version hints and the SDK's
  per-adapter `suggestedFix`.

A second table shows **per-stage resolution**: which adapter + model each of the
six executable stages resolves to (`issue-pickup` … `pr-merge`), the resolution
source (env / stage-config / global-config / default), and — for Codex — the
concrete model the tier maps to (e.g. `opus → gpt-5.5`). Each stage is flagged
`ok` / `warn` / `error` based on the resolved adapter's health.

"Re-run checks" recomputes the report in place.

### How the data is merged

```
resolveStageAdapter / getStageModel   → per-stage adapter + model (TS resolvers)
        │
        ├── distinct adapters ──► Go `doctor --adapters … --json`  → binary/version/MCP
        │                     └─► SDK runAdapterAuthPreflight       → auth + suggestedFix
        ▼
   merged AdapterDoctorReport → webview (per-adapter + per-stage tables)
```

When the Go binary cannot be resolved, the panel shows a warning and falls back
to auth-only readiness (the SDK auth probe itself surfaces a missing CLI as a
`BINARY_NOT_FOUND` failure), so the command degrades gracefully.

## Go CLI (`doctor --adapters`)

For skill preflight and headless use, the deterministic half is available
directly:

```bash
nightgauge doctor --adapters codex,claude --json
nightgauge doctor --adapters all
```

See [GO_BINARY.md → Doctor → Per-adapter health](GO_BINARY.md#per-adapter-health---adapters)
for the `adapters[]` schema and the per-kind semantics.

### The always-on `ai_adapter` row (#862)

`--adapters` reports per-adapter DETAIL and stays opt-in. But one question is
part of environment health and is asked on **every** `doctor` run, with or
without the flag: **can this machine run a pipeline stage at all?**

The `ai_adapter` check answers it. It walks `AllAdapterNames()` and
short-circuits on the first usable adapter, so the common case costs one
`lookPath` plus one `--version` spawn rather than one per registered adapter.

| Outcome                     | Row | Verdict                                          |
| --------------------------- | --- | ------------------------------------------------ |
| At least one adapter usable | ✓   | unchanged — no warning, no exit-code change      |
| Zero adapters usable        | ✗   | warning → `degraded` (exit 1). **Never exit 2.** |

Before this row existed, a machine with no coding agent and no API key reported

```
Status: healthy — environment ready for pipeline operations
```

and exited 0, then failed at the first stage. Adapter health was reachable only
through `--adapters`, which a first-run user has no reason to pass.

**Why it is warning-only, and why that is not timidity.**
[`skills/_shared/PREFLIGHT.md`](../skills/_shared/PREFLIGHT.md) halts a skill
immediately on exit 2, and PREFLIGHT runs _inside_ an agent session. If it is
executing, an adapter is already running — so the only way this check can fire
mid-run is a **probe false negative**: a CLI below the known-version floor, or
one that does not answer `--version` the way the probe expects. Under exit 2
that would halt a run that was working fine. The defect being fixed is the
false `healthy`, not the exit code.

The degraded summary line is special-cased for this row, because the generic
one ("pipeline will run but some features may be limited") is false in exactly
this case.

### Local model servers

The doctor has no adapter row for a local model server: the `lm-studio` and
`ollama` adapters, and their HTTP reachability and catalog checks, were removed
(#2128). A local model runs through `opencode`, whose rows are below.

### CLI catalog drift detection (`cli` kind, #551, #604)

For `cli`-kind adapters that wire a catalog probe (grok via `grok models`),
doctor also diffs the CLI's own live model catalog against the registry's
`transports.cli.served` facts for that provider — the detection half of the
#532 class, where the registry declared a model CLI-served that the live CLI
catalog never actually offered. A confirmed missing model fails the adapter
(`ok: false`) and names the provider, the concrete model id, and the
transport; the inverse (the CLI catalog offers a model the registry does not
mark served) is a warning only. A CLI that is not installed, not
authenticated, or whose catalog output cannot be parsed degrades the probe to
`catalog_warning` and never fails the adapter on that basis alone — see
[GO_BINARY.md → CLI catalog drift detection](GO_BINARY.md#cli-catalog-drift-detection-551-604)
for the full field reference.

`claude`, `codex`, `gemini`, and `copilot` have no catalog probe wired —
captured evidence (`internal/doctor/testdata/no-catalog-cli-probes/`) showed
none of them expose a models-listing command today. Each reports this
explicitly via `catalog_warning: "no catalog probe: <reason>"` once its own
baseline health passes, rather than leaving `catalog`/`catalog_warning` both
empty and unexplained; it never affects `ok`.

Enforcement at model-selection time (rejecting an unserved model before
spawn) is a separate, already-shipped mechanism
(`internal/models.CheckTransportServed`, #579); this probe only detects and
reports.

### Covered-Model retention block (`cli` kind, #1274)

A catalog listing answers "does this model exist"; it cannot answer "may THIS
organization use it". Anthropic's frontier models are **Covered Models**: an
organization or workspace configured for zero data retention is barred from
them, and every request returns `400 invalid_request_error` with
`your organization or workspace must have data retention enabled` — on a
machine where the CLI is installed, current, and authenticated. Nothing in
that failure looks like a bad model id, so reporting it as a generic
model-validity failure sends the operator hunting for a typo in an id that is
spelled correctly.

`doctor --adapters` therefore runs one **model-validity probe** for the
`claude` adapter: the cheapest non-interactive request against the registry's
current leader of the `fable` band (today `claude-fable-5-1`). It reports
`model` (the id probed) and `model_ok`, and on the retention rejection it
names the remediation — enable 30-day data retention for the organization or
workspace, or pin a non-Covered model such as `claude-opus-5` for the stages
that route to the `fable` band. Any other rejection gets the generic
"confirm the id is served to this account" advice plus the CLI's own output.

Two deliberate boundaries:

- **It never fails the adapter.** A band the org cannot reach is a real
  finding, but every other band still dispatches, so `ok` is untouched and the
  finding surfaces as `model_ok: false` plus a remediation — the same
  never-a-hard-failure rule the catalog probe follows.
- **It runs only under `--adapters`.** The always-on `ai_adapter` row shares
  the same probe plumbing but is wired without the model spawn, so a plain
  `nightgauge doctor` stays free. The probe targets a registry BAND rather
  than a literal id, so registering a new band leader re-points it on the same
  commit.

### OpenCode adapter health (#1627)

```bash
nightgauge doctor --adapters opencode --json
```

`opencode` is a `cli`-kind adapter with its own nested `opencode` object on
the adapter row (`AdapterHealth.OpenCode`, non-nil only for this adapter).
Every row name below is the JSON field a skill or operator greps for; see
[ADR-022](decisions/022-opencode-multi-provider-adapter.md) for the design
this check enforces.

- **`warnings` and `notes` (on the adapter row).** `warnings` lists the
  findings that leave the adapter usable but that the operator should act
  on, such as a version below a warn floor or a stored login; each one
  degrades the doctor's verdict. `notes` lists facts a reader of the row
  needs and never changes the verdict: the version floor and the version
  Nightgauge is tested up to, a binary that changed since the last dispatch,
  the run directories, the offline posture, and whether the catalog probe
  ran.
- **`opencode.enabled`** — whether `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1` is set
  in the process environment. While it is not, no other OpenCode check runs:
  there is nothing to check for a dispatch that never happens.
- **Version policy (`version_ok`, `min_version`, `opencode.max_tested`,
  `opencode.floor_policy`).** Below the compat manifest's floor, or when the
  version cannot be read, the row blocks — the floor policy is
  `fail_closed`. `opencode.max_tested` is the version Nightgauge is tested up
  to and is information only: a newer binary neither blocks nor warns, and
  dispatches, to a hosted model or a model server the operator runs, exactly
  as a tested one does (ADR-022 § 20, 2026-09-25 amendment).
- **`opencode.pinned`** — whether `opencode.binary` (the machine-tier config
  key) pins the binary an absolute path resolves, rather than falling back to
  whatever `opencode` resolves on `PATH`.
- **`opencode.last_dispatch_version`** — the version the last dispatch on
  this machine was checked against, when one was recorded. The row warns when
  the binary it resolves now reports a different version — a PATH install
  can change under the pipeline the way OpenCode's own TUI updates itself
  (observed on opencode 1.18.30).
- **The catalog probe (`catalog`, `catalog_warning`).** Runs `opencode models`
  under the per-run config built for `opencode.model`. For a declared
  endpoint's model or an `anthropic` model, the per-run config writes the
  model's own entry, so the probe shows only that the binary loads the
  config; any other hosted provider is listed only when one of its variables
  is set in the doctor's own environment (observed on opencode 1.18.30), and the row blocks and names them
  when none is.
- **Local-provider reachability and loaded context (`opencode.endpoints[]`,
  one `OpenCodeEndpointReadiness` per declared endpoint).** Each entry probes
  the endpoint's server and reports whether it is ready, and, for the
  endpoint `opencode.model` dispatches to, whether the discovered or declared
  context window covers the injected `limit.context`. An endpoint on another
  machine, reached over plain `http`, is a separate warning naming the
  endpoint id — never its `base_url`, host, or address, which appear in no
  record this check produces (ADR-022 § Endpoints).
- **The `limit.context 0` warning.** A model whose `limit.context` neither
  the machine-tier `limit` nor server discovery can supply is refused before
  spawn (a declared endpoint) or warned about (the always-probed default),
  because OpenCode never compacts a session whose context limit is 0
  (observed on opencode 1.18.30) — the
  stage would run into the server's loaded window instead.
- **Effective config/data dirs (`opencode.dirs`).** `config`, `data`, `cache`,
  and `state` are the run-scoped OpenCode directories a dispatch isolates
  under `~/.nightgauge/opencode/runs/<run_id>/`; `operator_config` is set
  only when `inherit_user_config` is on, naming the operator's own OpenCode
  config directory a run then also reads.
- **The offline posture (`opencode.offline`).** A statement about
  configuration only — a run fetches no model catalog, never autoupdates or
  shares a session, downloads no LSP server, and enables only the provider it
  dispatches to — never a claim about verified network egress. It names
  whether `opencode.model` resolves to an endpoint on this machine, an
  endpoint on another machine, or a hosted provider, or says the model is
  unset.
- **The OAuth-type anthropic flag (`opencode.stored_logins[]`, each a
  `{source, provider, type}`).** Flags a subscription or OAuth login OpenCode
  holds for a provider, found in a run's own directories or (with
  `inherit_user_config`) the operator's. It names only where the login is
  and its type, never its content, and the finding states that a pipeline
  run never uses it: an `anthropic/*` stage through OpenCode authenticates
  only with `ANTHROPIC_API_KEY`.

### Binary self-check cascade (#277)

The `binary` check in the default (non-adapter) `nightgauge doctor` output
reports whether the `nightgauge` binary itself is resolvable, via a
six-step cascade (mirrors `claude-plugins/nightgauge/hooks/lib/guard.sh`):

1. `$NIGHTGAUGE_BIN` (only when it points at an executable file)
2. `PATH` (`exec.LookPath("nightgauge")`)
3. `<repo-root>/bin/nightgauge` (`git rev-parse --show-toplevel`)
4. `<canonical-repo-root>/bin/nightgauge` (`git rev-parse --git-common-dir`,
   for worktree checkouts)
5. `~/.vscode/extensions/nightgauge.nightgauge-vscode-*/dist/bin/nightgauge`
   — the bundle VSCode **records** as installed (#356), never the first glob
   match and never the biggest version number
6. `~/go/bin/nightgauge`

This is implemented once, in Go, at `internal/doctor/binary_resolve.go`
(`ResolveBinary`), and is the canonical implementation the cascade is
specified against; `internal/doctor/binary_resolve_test.go` pins
`guard.sh`'s resolution order against it for the five filesystem-based
steps, so the two cannot silently drift. Since #356 it also pins the
**order** itself: `TestResolveBinary_Precedence` and
`TestResolveBinary_GuardShParity_Precedence` satisfy each adjacent pair of
steps simultaneously and assert the earlier one wins, in both
implementations. The per-step tests alone could not catch a reorder — each
isolates the cascade so exactly one step is satisfiable.

### What the `binary` check reports (#356)

The cascade is **cwd-dependent**: steps 3 and 4 shell out to `git rev-parse`
in the caller's directory, so the same hook script legitimately runs
different binaries in different repos. Inside a nightgauge checkout the
hooks use `bin/nightgauge`; two directories over they use the extension
bundle. Before #356 nothing reported that, which let a merged hook fix sit
inert everywhere except the repo it was built in.

`checks.binary.detail` therefore reads as _what the hooks resolve from
here_, and carries:

- the resolved path and the resolving step, e.g.
  `hooks resolve /…/dist/bin/nightgauge from this directory (via vscode_extension)`
- the binary's own version, from one bounded `<path> version` exec — note
  the verb is `version`; there is no `--version` flag. The exec lives in
  `doctor` (on demand) and deliberately **not** in `guard.sh`, which runs on
  every tool call.
- when an earlier cascade step wins and the recorded bundle is runnable, that
  bundle's binary path and version from a second bounded exec. A mismatch
  reports both paths, both complete version strings, and the winning step.
- the extension-bundle inventory — how many bundle directories are on disk,
  which one VSCode **records** as installed, and whether that recorded bundle
  is the step-5 selection — **even when an earlier step wins**, so running
  `doctor` from inside a checkout still tells you what other repos will use. The
  inventory is also carried on the not-found path, where "2 bundle dir(s) on
  disk, none runnable" is a different problem from "nothing installed".

The `binary` check is **warning-only** — it contributes to `warnings` but
never sets `exit_code` to `2`, and never appears in `failed_checks`. A
missing binary can only be observed by running `doctor` in the first place,
so it cannot be a hard-required check. An extension-only install (binary
resolvable via step 5 but never on `PATH`) reports
`checks.binary.ok == true`, not a false "not found".

Three distinct warning-level outcomes exist and they are not interchangeable:

| Outcome             | `ok`    | `install_instructions` | Meaning                                                                                        |
| ------------------- | ------- | ---------------------- | ---------------------------------------------------------------------------------------------- |
| unresolved          | `false` | populated              | no step matched — installing something is the fix                                              |
| diverging bundle    | `false` | **empty**              | step 5 resolved a bundle VSCode's install record does not confirm — installing changes nothing |
| cross-step mismatch | `false` | **empty**              | an earlier step and the recorded bundle report different binary versions                       |
| resolved            | `true`  | empty                  | normal                                                                                         |

A diverging bundle names the **recorded** version, the **resolved** version and
the resolved path in `checks.binary.error`, matching the `[stale-binary]` line
`guard.sh` writes to its side-channel log — and it keeps its full `detail`
alongside that error. This is the outcome an operator is actually
investigating, so the resolving step and the resolved binary's own version (the
two facts that answer "is this really an old build?") are reported here, not
only on healthy machines.

A cross-step mismatch is also warning-only and never enters `failed_checks`.
Matching versions, no usable install record, or no runnable recorded bundle
produce no cross-step finding. The comparison uses complete command output
rather than trying to order versions; a different build is actionable even
when version ordering would be ambiguous.

#### The install record is the selection authority (#356)

Step 5 does not rank version numbers, and neither does this check. VSCode
records exactly one installed directory per extension in
`~/.vscode/extensions/extensions.json` (`relativeLocation`), and that record —
not the biggest-parsing directory name — decides which bundle the hooks run.

Ranking versions is wrong in three ways this project actually reaches:
maintainer dev-installs are permanently `0.1.<epoch>` and lose to any leftover
`0.2.x` release directory; RC bundles (`…-0.2.0-rc.22`, `…-0.2.0-rc.23`) tie
under every dotted-numeric comparator and fall back to first-glob-match; and a
`….vsctmp` partial-install orphan can out-parse the real install. Consequently
a **recorded downgrade is healthy** — `doctor` reports `ok: true` and says
nothing about the higher-numbered directory next to it.

Divergence is reported only when the record cannot be honored (recorded bundle
absent or not executable) or when there is no usable record and several bundles
are on disk. See
[docs/GO_BINARY.md](GO_BINARY.md#which-bundle-the-hooks-run--vscodes-record-not-the-biggest-number)
for the full rule and for the parsing-parity contract between `guard.sh` and
`internal/doctor/binary_resolve.go`.

`DoctorResult.FailedChecks` (`failed_checks` in JSON) lists every check
name that contributed a required failure (`exit_code == 2`), in the order
added — e.g. `["github_auth"]`. `skills/_shared/PREFLIGHT.md`'s exit-2
branch reads this field to print the failing check name(s) alongside
`.errors[]`, instead of leaving the stage agent to guess which check broke.

## Agentic capability gate (#57)

Every adapter declares whether it drives a real agentic tool loop
(`agentic` on the SDK `ICliAdapter`; `Agentic()` on the Go `SkillRunner`).
Chat-completion-only adapters — the TypeScript `gemini-sdk` and
`openai-compatible` — cannot edit files, run shell commands, or call `gh`, so
**pipeline dispatch rejects them** with remediation at every entry point:
the SDK CLI preflight (`runAdapterPreflightChecks`), the VSCode prerequisite
check (primary, fallback walker, and auto-router enumeration), and the Go
`Manager.RunStage`. They remain first-class for the eval harness / judge /
summarization surfaces, which do not run these gates. (The Go `gemini-sdk`
adapter is agentic — it spawns the gemini CLI — unlike its chat-only
TypeScript namesake.)

## Extending to a new adapter

1. Add the adapter to the Go `adapterSpecs` table (`internal/doctor/adapters.go`)
   with its `kind` and binary/env requirements. A CLI adapter also gets a compat
   manifest (`internal/adaptercompat/manifests/<adapter>.json`), and its spec
   reads the version floor and floor policy from it.
2. Ensure the SDK adapter implements `validateAuth()` (the auth layer is
   automatic via `runAdapterAuthPreflight`).
3. Declare its agentic truth: `agentic` on the SDK adapter class and
   `Agentic()` on the Go adapter (#57) — `false` bars it from pipeline
   dispatch while keeping it available to eval surfaces.
4. Add a display name to `SDK_ADAPTER_DISPLAY` in
   `packages/nightgauge-vscode/src/commands/adapterDoctor.ts`.
5. If the adapter ships behind an experimental gate the way `opencode` does
   (`enabled` in its nested health object), keep the gate check first: every
   other check is meaningless for a dispatch that is refused before it
   starts.

## Related

- [ADAPTER_GUIDE.md](ADAPTER_GUIDE.md) — adapter selection, auth, troubleshooting
- [ADAPTER_MATRIX.md](ADAPTER_MATRIX.md) — verified per-adapter capability matrix
- [MCP_INTEGRATION.md](MCP_INTEGRATION.md) — Codex MCP provisioning
- [GO_BINARY.md](GO_BINARY.md) — `doctor` CLI reference
- [decisions/022-opencode-multi-provider-adapter.md](decisions/022-opencode-multi-provider-adapter.md) — the OpenCode adapter's full design record
