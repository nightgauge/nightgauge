# Spike #662: What Usage and Quota Signal Each Execution Adapter Exposes

**Issue**: #662
**Status**: Complete
**Date**: 2026-08-17

## Executive Summary

**Overall verdict: adopt — narrowly, for one adapter.** No execution adapter exposes a
queryable "how much quota do I have left" API _reachable from the non-interactive mode
nightgauge actually dispatches through_. That qualifier matters: Claude Code, Codex CLI,
and Gemini CLI each ship a genuine, vendor-documented, interactive usage command
(`/usage`, `/status`, `/stats model` respectively) that reports real provider-side
quota — but all three are slash commands in an interactive TUI session, and Anthropic's
own documentation states plainly that "in print mode you cannot type slash commands."
Nightgauge dispatches every one of these CLIs in exactly that non-interactive mode.
So the working assumption that shaped `UsageLimitsService.ts` — "no upstream API is
available for Claude Code Max quota" — is subtly wrong: an upstream API-equivalent
_exists_, it is simply unreachable by the calling convention nightgauge uses. Only one
adapter has a second, independent path around that wall: the `claude` CLI's own
`--output-format stream-json` output carries a structured, provider-reported utilization
signal that nightgauge's TypeScript execution path already parses — today only to decide
when to pause and retry a stage, never to answer "what does the user's quota look like."
That is the one adopt. Every other adapter in `ExecutionAdapterSchema` gets **defer** (a
real but too-weak-to-map signal exists: `codex`, `grok`) or **skip** (no signal exists,
or none has been observed: `gemini`, `gemini-sdk`, `copilot`, `lm-studio`, `ollama`).
The epic's local-telemetry provider (#658) remains the correct baseline for all eight
adapters; this spike adds exactly one enrichment on top of it, not eight.

| Adapter      | Official/vendor mechanism                                                         | Reachable headlessly?                             | Signal nightgauge can actually use today                                                                                                                   | Verdict   | Confidence                                            |
| ------------ | --------------------------------------------------------------------------------- | ------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- | ----------------------------------------------------- |
| `claude`     | `/usage` slash command (real plan usage bars + reset times)                       | **No** — print mode has no slash commands         | `rate_limit_event` stream envelope: `utilization`, `resetsAt`, `rateLimitType`, `status` — an independent, undocumented second channel that _is_ reachable | **adopt** | reverse-engineered, unofficial; measured only mid-run |
| `codex`      | `/status` (real % remaining + reset date, both windows)                           | **Unconfirmed** for `codex exec`                  | plain-text boundary-crossing message only, no number, no reset time                                                                                        | **defer** | low; headless-capture not yet attempted               |
| `grok`       | none documented anywhere (interactive or not)                                     | n/a                                               | plain-text boundary-crossing pattern match, not confirmed to have fired on real output                                                                     | **defer** | low                                                   |
| `gemini`     | `/stats model` (documented, but accuracy disputed in a filed bug)                 | **No** — interactive slash command                | no adapter-side detection code exists at all                                                                                                               | **skip**  | unverified; no working CLI to probe headlessly        |
| `gemini-sdk` | same as `gemini`                                                                  | same as `gemini`                                  | same as `gemini`                                                                                                                                           | **skip**  | unverified                                            |
| `copilot`    | none — by design, plan-level figures live only on the web billing page            | n/a (nothing to reach)                            | per-invocation premium-request **count** only (accounting, not quota)                                                                                      | **skip**  | confirmed by vendor docs                              |
| `lm-studio`  | none — no billing relationship exists                                             | n/a                                               | no billing/quota concept; not dispatched by the pipeline at all (`Agentic()==false`)                                                                       | **skip**  | n/a                                                   |
| `ollama`     | none for the local server; Ollama Cloud has plan limits with no shipped query API | n/a (adapter targets the local server, not Cloud) | no billing/quota concept for the local server; not dispatched by the pipeline at all (`Agentic()==false`)                                                  | **skip**  | n/a                                                   |

## 1. Methodology and what "verified" means here

Every claim below is grounded in one of three kinds of evidence, cited inline:

1. **Adapter source and stream parsers in this repository** — `internal/execution/adapters/*.go`,
   `internal/execution/stream.go` (the Go binary's dispatch and token accounting), and the
   parallel TypeScript execution path (`packages/nightgauge-vscode/src/utils/tokenParser.ts`,
   `skillRunner.ts`, `packages/nightgauge-sdk/src/cli/adapters/*.ts`) that the VS Code
   extension uses for its own dispatch. **These are two separate execution paths with
   different capabilities** — see §2.1.
2. **A captured `--help` output** for a CLI, following the same discipline
   `internal/doctor/adapters.go` already established for the (unrelated) catalog-probe
   question in #551/#604: a real, non-interactive, time-bounded invocation, committed
   verbatim, not a claim from memory. Where this spike could not perform its own capture,
   it cites the doctor package's existing captures
   (`internal/doctor/testdata/no-catalog-cli-probes/`, `claude-help.txt` 2.1.233 and
   `codex-help.txt` 0.145.0, both captured 2026-08-16) and greps them for
   `usage|limit|quota|rate.?limit|remaining` — this is adjacent evidence (it was
   captured to answer a _catalog-listing_ question, not a _quota_ question), so it is
   marked as such everywhere it appears below, not treated as a direct answer.
3. **Public vendor documentation**, fetched live where reachable. Every citation below
   states the source; a claim with no reachable primary source is marked **unverified**
   rather than asserted.

Where these disagree, the source with the higher evidentiary bar wins: a captured
invocation beats a doc page, and a doc page beats "no adapter code parses this, so it
probably doesn't exist" — an absence in this codebase is evidence about what nightgauge
currently _consumes_, not proof about what a CLI's wire format _contains_. Each finding
below says explicitly which of the two it is.

### 1.1 Precedent this spike follows, and why it isn't sufficient on its own

`internal/doctor/adapters.go` (#551, generalized in #604) is the closest existing
precedent for "interrogate an adapter about itself": it shells each CLI's own
catalog-listing command and diffs the result against the model registry. It is useful
evidence here for two reasons, and insufficient for one:

- **Useful**: it already required — and got — real captured `--help` output for `claude`
  and `codex`, and it already recorded that `gemini` and `copilot` had **no working CLI
  on the capture machine** as of 2026-08-16 (`testdata/no-catalog-cli-probes/README.md`).
  That absence is itself evidence: this spike inherits it rather than re-deriving it, and
  flags every `gemini`/`copilot` finding below as correspondingly less certain.
- **Insufficient**: it was scoped to a _model-listing_ command, not a _usage/quota_
  command. A `grep -i models` finding nothing in `claude-help.txt` says nothing about
  whether the same `--help` text mentions usage or limits — so this spike re-greps the
  same captured files for the quota-relevant vocabulary independently (§1, item 2) rather
  than inheriting #551/#604's conclusion by association.

## 2. Findings by adapter

### 2.1 A structural fact that applies to every finding below: two execution paths

Nightgauge dispatches through **two independent implementations** of the same eight
adapters, and they do not have the same capabilities:

- **The Go binary** (`internal/execution/adapters/*.go`, accumulated by
  `internal/execution/stream.go`'s `TokenAccumulator`) is what the headless pipeline
  scheduler spawns. Its `RunResult` struct
  (`internal/execution/adapters/adapter.go:82-114`) carries `InputTokens`,
  `OutputTokens`, cache-token fields, `PremiumRequests` (copilot), and `ServedModel` —
  and nothing else. There is no rate-limit or quota field anywhere in this struct, in
  `TokenAccumulator`, or in any of the eight `internal/execution/adapters/*.go` files (a
  full grep for `usage|quota|remaining|rate.?limit` across that package returns exactly
  one hit — a code comment in `gemini.go:42` about _token_-usage events, unrelated to
  quota). **This confirms the research already done before this spike was dispatched.**
- **The TypeScript execution path** the VS Code extension uses for its own dispatch
  (`packages/nightgauge-vscode/src/utils/skillRunner.ts`,
  `packages/nightgauge-vscode/src/utils/tokenParser.ts`,
  `packages/nightgauge-sdk/src/cli/adapters/*.ts`) is a **separate implementation that
  parses more of the Claude CLI's stream than the Go binary does** — including a
  structured rate-limit envelope the Go side has no equivalent for (§2.2). This is not
  duplicated effort so much as it is unevenly-distributed effort: the TS path was built
  out for the extension's own live-run pause/retry logic, years before this epic's
  `UsageProvider` was conceived, and nobody has yet connected the two.

**Why this matters for the epic**: `UsageProvider` (#658) is a VS Code extension
(TypeScript) service. The one real signal this spike found already lives in the
TypeScript layer nightgauge's own extension code will run in — it does not require
touching the Go binary, adding a new CLI flag, or reverse-engineering anything new. It
requires wiring an existing internal signal to a new external-facing consumer.

### 2.2 `claude` (and its CLI-vs-API-key split) — adopt

**The officially documented mechanism, and why nightgauge cannot reach it.** Anthropic
documents a real usage-reporting surface: the `/usage` slash command shows live
subscription-plan usage bars — a rolling five-hour window and a weekly window, each with
a reset time — for Pro/Max/Team/Enterprise subscribers, sourced from a server-side usage
endpoint (`code.claude.com/docs/en/costs`, fetched live for this spike). This is exactly
the mechanism the epic's Technical Notes ask about. It is also, by Anthropic's own
documentation, **interactive-only**: a GitHub issue discussion from Anthropic states
plainly that "in print mode you cannot type slash commands... built-in interactive
commands like `/config` and `/login` do not work headless," and the CLI reference
(`code.claude.com/docs/en/cli-reference`) documents no `claude usage` subcommand and no
`--usage` flag for `-p`/print-mode invocations. Nightgauge dispatches `claude` exclusively
in print mode (`-p`, both `claude.go` and `claude_sdk.go` — see below) — so the
documented, supported mechanism exists and is real, but is not reachable from the
integration nightgauge actually has. This is the load-bearing reason the reverse-engineered
signal below matters: it is not a second-best option chosen out of laziness, it is the
_only_ option reachable from a non-interactive dispatch.

**The `anthropic-ratelimit-*` HTTP headers are a different, unrelated mechanism.**
Anthropic's rate-limits documentation (`platform.claude.com/docs/en/api/rate-limits`)
scopes those headers strictly to the Messages API under organization-level, API-key
billing (RPM/ITPM/OTPM usage tiers) — a different axis than the per-seat
subscription-plan windows `/usage` reports. No Anthropic-authored source found for this
spike states whether an OAuth-authenticated Claude Code Max session receives these
headers at all when the CLI talks to Anthropic's backend internally; that specific
sub-point is **unverified**, not confirmed either way.

**`ccusage`, the known community tool, does not change this.** `ccusage`'s own
documentation describes it as reading local usage data from coding-agent CLIs — parsing
local transcript/JSONL files for cost and token estimates. It has no connection to
Anthropic's servers and reports no real quota figure; it is local accounting, the same
category `LocalTelemetryUsageProvider` (#658) already covers, not a source this spike
needed to treat separately.

**What "claude" resolves to.** The `ExecutionAdapterSchema` value `"claude"` is not one
Go adapter — `packages/nightgauge-vscode/src/config/schema.ts:1742-1750` documents it
mapping to either `claude-sdk` (API key) or `claude-headless` (CLI/OAuth auth),
selected by `auth_provider` (`AuthProviderSchema`: `max` | `bedrock` | `vertex`).
`internal/execution/adapters/registry.go:33-34` shows the Go-side factories
(`claude-headless` → `claude.go`, `claude-sdk` → `claude_sdk.go`) and the alias
(`"claude"` → `"claude-headless"`). Both `claude.go` and `claude_sdk.go` build
near-identical commands — `-p --output-format stream-json --verbose` — so both paths
produce the same wire format; the finding below applies to whichever one is dispatched.

**The signal.** The Claude CLI's `--output-format stream-json` output carries a message
type the accumulated Go-side `TokenAccumulator` does not parse at all, but the
TypeScript `tokenParser.ts` does
(`packages/nightgauge-vscode/src/utils/tokenParser.ts:130-171`, Issue #2573):

```
{"type":"rate_limit_event","rate_limit_info":{...}}
```

parsed into:

```ts
export interface RateLimitEventData {
  resetsAt: number; // Unix epoch seconds
  rateLimitType: string; // e.g. "seven_day", "daily", "five_hour"
  utilization: number; // 0-100, percentage of the bucket used
  status: string; // "allowed" | "allowed_warning" | "limited"
  isUsingOverage: boolean;
  overageStatus?: string;
  overageDisabledReason?: string;
}
```

This is a **real, provider-reported utilization figure with a reset time and a named
bucket** — the closest thing to "remaining quota" any adapter in this survey exposes,
and by a wide margin the strongest candidate to satisfy the acceptance criterion asking
whether Claude Code's subscription-plan windows are obtainable at all. `rateLimitType`
values observed in this codebase (`five_hour`, `seven_day`, `daily`) line up with the
session-window / weekly-window structure the epic describes for Claude Code Max plans
— see `internal/orchestrator/autonomous.go:4208` ("Model-specific usage caps reset on
Anthropic's rolling windows") and `internal/orchestrator/model_fallback_test.go:45-46`
("weekly tier cap (Claude Code Max plans)").

**What it is not.** `rate_limit_event` does **not** carry a per-model-family breakdown.
The interface above has no model or model-family field — `rateLimitType` names a
_window_ (five-hour, daily, seven-day), not a _model_. The epic's Technical Notes ask
specifically whether windows are obtainable "per model family"; the honest answer for
this mechanism is **no** — it reports bucket-level utilization, not
opus-vs-sonnet-vs-fable-level utilization. Treat that part of the epic's assumption as
unconfirmed, not merely under-specified.

**Freshness — mid-run only, today.** This event arrives on the live stdout stream of an
_already-running_ `claude -p --output-format stream-json` invocation
(`packages/nightgauge-vscode/src/utils/skillRunner.ts:6436-6448`). There is no
`claude`-CLI command this spike found (nor did the doctor package's captured
`claude-help.txt`, 2.1.233, grepped in §1 item 2, turn one up) that returns the current
utilization when the CLI is **not** running a request. The status bar's stated need to "sample at rest"
(`docs/spikes/662-*.md` issue text, Technical Notes) is **not met by this mechanism as
observed** — a provider built on it would have to persist the last-seen reading to disk
and serve it with a staleness caveat (`confidence: "estimated"`, an explicit
"as of" timestamp) rather than `confidence: "measured"` between runs. That degrades
gracefully and is a solvable design problem, not a blocker to `adopt` — but it is not
free, and a provider built on this signal is architecturally obligated to expose the
staleness rather than silently presenting a stale number as current.

**Stability risk — high, and already demonstrated.** This is not a documented public
API surface as far as this spike could determine (§2.6); it is a wire-format detail of
the CLI's own `stream-json` output, discovered and captured by nightgauge engineers
through observed behavior (Issue #2573), and it has **already changed shape once**
within this codebase's own history: `tokenParser.ts:424-432` explicitly preserves a
"flat-fields" fallback parse path "for older CLI builds and existing tests that still
write the flat shape," because the real Claude CLI moved the fields from the envelope's
top level to a nested `rate_limit_info` object at some point (#3386 in this repo's
history). A second data point: `#3386`'s original hypothesis about `overageStatus`
behavior "did NOT manifest in practice" and had to be corrected by `#3448` after
observing real session logs
(`packages/nightgauge-vscode/src/utils/tokenParser.ts:154-163`). Two independent
correction cycles on the same field in this repo's history is itself the finding: this
is a live, still-settling, unofficial interface, not a stable contract. A provider built
on it should be written defensively (unknown fields ignored, missing fields treated as
absent-not-zero) and should not be presented to a user as more authoritative than it is.

**A second, weaker Claude signal exists and is already partially consumed.** Separate
from the structured envelope, the Claude CLI also surfaces an actually-hit session or
usage limit as plain text inside a `{type:"result", is_error:true}` envelope — e.g.
_"You've hit your session limit · resets 10:30am (America/Denver)"_ — which nightgauge
already regex-detects
(`isAnthropicSessionLimit`, `tokenParser.ts:1188-1196`) and parses a reset time out of
(`parseSessionLimitResetsAt`, `tokenParser.ts:1206-1260`, handling `"resets 3pm"` /
`"resets 10:30am (America/Denver)"` forms). This is strictly a subset of what the
structured `rate_limit_event` already gives when present (it fires only at the boundary,
carries no percentage, and its reset-time extraction is a best-effort regex over
natural-language text) — worth naming because it's a second, independently-observed
confirmation that Anthropic's CLI communicates quota state as a first-class concern of
its own output, but it should not be built as a separate provider; the structured event
is the better source when both are available.

**Verdict: adopt.** Map `rate_limit_event` onto the `UsageWindow` shape from #658 as:
`used` = `utilization` (as a percentage, or converted against an assumed 100 baseline),
`limit` = 100 (or null if the mapping is judged too lossy), `resetsAt` = `resetsAt`,
`scope` derived from `rateLimitType` (`five_hour` → `session`/`rolling`, `seven_day` →
`weekly`, `daily` → `daily`), `confidence` = `"measured"` only while a same-run reading
exists, degrading to `"estimated"` for a cached last-seen value. The follow-up ticket
below is scoped to exactly this: consume the signal `skillRunner.ts` already parses;
build nothing new against the CLI itself.

### 2.3 `codex` — defer

**A real, official quota signal exists — same interactive-only wall as Claude.** The
Codex CLI's `/status` command reports a provider-server-reported percentage-remaining
and reset date for both its five-hour and weekly windows (e.g. "62% left, resets Jul
9") — confirmed through OpenAI's own `openai/codex` repository (issues and PRs
discussing `/status` accuracy, including a fix titled "stale `/status` rate limits in
active TUI sessions"), a first-party source even though the terser official docs page
under-describes the command. Like Claude's `/usage`, this is an interactive TUI command;
this spike found no confirmation that an equivalent figure is available from `codex
exec` (the non-interactive mode `internal/execution/adapters/codex.go` actually
dispatches) — that is recorded as **unconfirmed**, not as a negative, since it was not
independently tested by shelling `codex exec` and inspecting its NDJSON stream for a
status/quota event type the way Claude's `rate_limit_event` was found (§2.2). A follow-up
worth doing before committing to `defer` reasoning below: capture real `codex exec --json`
output and grep it for a status/quota-shaped event, the same discipline this spike
applied to Claude.

**What it reports for a completed run.** `internal/execution/adapters/codex.go` and the
Go stream parser (`internal/execution/stream.go:312-390`, `ParseCodexStreamLine`) read a
`turn.completed` event's `usage` object: `input_tokens`, `cached_input_tokens`,
`output_tokens`. This is per-turn accounting (#4027), not quota — the same class of
figure every token-metered adapter reports for the work it just did.

**What it reports at exhaustion.** `docs/FAILURE_TAXONOMY.md:596-599` documents a bare
Codex quota line — _"usage limit reached for this account"_ — that names no model and
carries no machine-parseable reset time. Nightgauge's cross-adapter extension
(`session-usage-limit-quota`, same doc, lines 592-607) catches this via a generic
word-bounded `/\b(?:session|usage)\s+limit\b/i` match on the CLI's plain-text output —
the same regex class used for Claude's fallback text path (§2.2), but for Codex there is
**no structured envelope underneath it** the way there is for Claude; this word-boundary
match against plain text _is_ the entire signal
(`docs/FAILURE_TAXONOMY.md:604-607`: _"SkillRunner normalises this shape to
`[rate-limit-quota-exhausted]` only on the Claude stream-json result-envelope path, so
for plain-text and Codex runs this branch **is** the routing"_).

**Why this is `defer`, not `skip` or `adopt`.** A real signal exists — the boundary
crossing is detected and already drives pipeline retry/cooldown behavior — but it is a
**boolean, not a magnitude**: no percentage, no reset time, no bucket name. The
`UsageWindow` shape from #658 requires a `used` value; a boundary-only signal cannot
honestly populate one without fabricating a number the epic's own acceptance criteria
explicitly forbid ("never a fabricated percentage"). Building a `codex` provider today
would mean inventing a shape the signal doesn't support, or leaving `used` unset in a way
the current `UsageWindow` schema doesn't accommodate. `defer` records that the signal is
real and worth a design pass — likely "surface a boolean blocked/not-blocked state
alongside the window list, distinct from a window" — rather than pretending it maps
cleanly today.

**`codex login status` and `codex --help`.** The doctor package's captured
`codex-help.txt` (codex-cli 0.145.0, 2026-08-16,
`internal/doctor/testdata/no-catalog-cli-probes/codex-help.txt`) lists 27 subcommands
including `login`, `doctor`, and `debug`, and its top-level flags. Grepping that capture
for `usage|limit|quota|rate.?limit|remaining` (excluding the boilerplate `Usage: codex
...` header line) returns **no matches** — no usage/quota subcommand or flag is
advertised in `--help`. This is adjacent evidence (captured for the catalog-listing
question, §1.1), not a direct probe of `codex login status`'s output, which this spike
did not capture — see the external-research findings in §2.6 for what could be confirmed
independently.

### 2.4 `gemini` and `gemini-sdk` — skip

**A documented mechanism exists, and its accuracy is independently disputed.** Google's
own Gemini CLI documentation (`github.com/google-gemini/gemini-cli`,
`docs/resources/quota-and-pricing.md`, fetched live for this spike) states that the
`/stats model` slash command shows session token usage plus "information about the
limits associated with your current quota." This is, like Claude's `/usage` and Codex's
`/status`, an interactive command — nightgauge's `gemini.go`/`gemini_sdk.go` both
dispatch non-interactively, so the same headless-unreachability finding applies here too.
Unlike Claude and Codex, though, this spike also found a filed, maintainer-unanswered bug
report (google-gemini/gemini-cli#17081) describing `/stats` showing available
quota at the same moment the API was actually rejecting requests as exhausted — i.e., an
independent report that even the interactive, documented figure may be stale or
inaccurate in practice. This spike did not resolve that dispute; it is recorded as a
reason for extra caution if `gemini`'s verdict is ever revisited, not as a reason to
prefer one side.

Both adapters (`internal/execution/adapters/gemini.go`,
`internal/execution/adapters/gemini_sdk.go`) build nearly identical commands
(`--output-format stream-json`, `resolveGeminiModel`) and both are token-metered:
the Go parser (`internal/execution/stream.go:394-522`, `ParseGeminiStreamLine`) reads
`input_tokens`/`output_tokens`/`cached` from `stats` or `result.usage` — again,
per-run accounting.

**No quota-detection code exists for Gemini anywhere in this repository.** A grep across
`packages/nightgauge-sdk/src/cli/adapterQuery.ts` (the module hosting the Codex and Grok
output summarizers) for `quota|rate.?limit|429|resource.exhausted|usage.limit` returns
no hits, and no `geminiStream.ts`-equivalent quota parser exists alongside `grokStream.ts`.
This is an absence-of-evidence finding, not evidence of absence: it says nightgauge has
never needed to detect a Gemini quota message (plausibly because it has not been
observed in practice, or because the CLI reports it differently, e.g. a non-zero exit
code the wrapper already treats as a generic failure) — not that no such message exists
on the wire.

**The doctor package's own capture attempt found no CLI to probe at all.**
`internal/doctor/testdata/no-catalog-cli-probes/README.md:58-65`: as of 2026-08-16, no
`gemini` binary was on the capture machine's `PATH` and no global npm package provided
one, so the doctor evidence directory (deliberately) contains no `gemini --help`
capture — "capturing one would mean fabricating it." This spike inherits that same
limitation and did not have a working `gemini` CLI to interrogate directly either.

**Verdict: skip**, with the explicit reason that no mechanism was found to adopt, not
that one was found and rejected. Revisit if a `gemini`-specific quota message is ever
observed in production logs, or if a future doctor catalog-probe capture (#604) turns up
a `gemini`-native usage command.

### 2.5 `copilot` — skip

**What it reports.** The Copilot CLI does not emit token counts at all
(`internal/execution/stream.go:520-573`, `ParseCopilotStreamLine`) — it prints a
plain-text stats footer whose only billing figure is a **premium-request estimate for
the current invocation**: `"Total usage est: N Premium requests"`. This is explicitly
documented in this codebase as accounting, not quota:
`internal/execution/stream.go:107-111` ("PremiumRequests is the copilot billing unit
... Zero for token-metered adapters"), and
`internal/execution/adapters/copilot.go:78-80` confirms the `-s` (silent) flag is
deliberately **not** passed specifically so this footer remains available.

**The one adjacent figure is a cost estimate, not a quota.**
`packages/nightgauge-sdk/src/cli/adapterQuery.ts:391-409` documents a hardcoded
per-premium-request cost estimate — GitHub Copilot's own published individual-plan price
of $10/month for 300 premium requests, giving ≈$0.033/request (rounded up to $0.04) —
used to convert the observed count into a dollar figure for nightgauge's own local
accounting. That is provider-published _pricing_, already covered by the "provider
pricing that is publicly published by the vendor is fine" allowance; it is not a
provider-reported _remaining allowance_, and nightgauge does not query GitHub for one
anywhere in this codebase.

**GitHub's own documentation confirms this is by design, not a gap nightgauge could close
by parsing more carefully.** GitHub's Copilot CLI documentation
(`docs.github.com/en/copilot/how-tos/copilot-cli/use-copilot-cli/overview`, fetched live
for this spike) documents the `/usage` command and the session footer as reporting
**current-session** figures only (premium-request estimate, duration, code changes,
token breakdown); the monthly/plan-level remaining allowance is visible exclusively on
`github.com/settings/billing`, on the web, never in any CLI output. Unlike Claude, Codex,
and Gemini, there is no _interactive-but-unreachable_ richer command being missed here —
the CLI itself, by design, never carries the plan-level figure at all, interactively or
otherwise.

**No monthly-remaining figure is surfaced anywhere.** No code in this repository parses
a "you have used N of M premium requests this billing period" figure from Copilot CLI
output, and the doctor package's own capture attempt found the same absence of a working
CLI to interrogate as it did for Gemini: the only `copilot` binary on the 2026-08-16
capture machine's `PATH` was a VS Code extension bootstrap shim that fails immediately
without a real CLI installed
(`internal/doctor/testdata/no-catalog-cli-probes/README.md:67-85`,
`copilot-help.txt`).

**Verdict: skip.** The per-run premium-request count already flows into local
accounting via the existing `TokenTracker`
(`packages/nightgauge-sdk/src/tracking/TokenTracker.ts:27,52`) — the same category of
signal the #658 local-telemetry provider already covers. There is no distinct
GitHub-reported _remaining_-allowance mechanism this spike found to build a separate
provider around.

### 2.6 `grok` — defer

**What it reports for a completed run.** `internal/execution/adapters/grok.go` and
`internal/execution/stream.go:581-639` (`ParseGrokStreamLine`) read a structured usage
object off `streaming-json` output: `input_tokens`, `output_tokens`,
`cache_read_input_tokens`, `cache_creation_input_tokens`, `reasoning_tokens` — real,
structured, per-run accounting, the richest token breakdown of any adapter surveyed
here. None of these fields describe a remaining allowance.

**What it reports at exhaustion.** `packages/nightgauge-sdk/src/cli/adapters/grokStream.ts:40-51`
defines a plain-text pattern match, `QUOTA_RE`:

```
/\b(weekly (usage )?pool|usage pool exhausted|usage (limit|paused)|quota (exhausted|exceeded)|rate limit)\b/i
```

which the CLI query helper turns into a boolean
(`packages/nightgauge-sdk/src/cli/adapterQuery.ts` → `cliQueryHelper.ts:308-310`,
`[rate-limit-quota-exhausted] grok usage pool exhausted`). Same shape as Codex (§2.3): a
boundary-crossing detector, no numeric magnitude, no reset time. Unlike Codex's
equivalent pattern — which traces to a real production incident of misrouted bare quota
lines (`docs/FAILURE_TAXONOMY.md`, #3792) — this spike found no equivalent trail
confirming `QUOTA_RE` has fired against real observed Grok CLI output; it is recorded
here as an implemented, defensive detector, not a confirmed-observed one.

**Grok's own official CLI reference documents no usage surface at all.** `docs.x.ai`'s
CLI reference and headless-scripting pages (`docs.x.ai/build/cli/reference`,
`docs.x.ai/build/cli/headless-scripting`) — the full documented command, flag, and
`json`/`streaming-json` output-schema inventory, fetched live for this spike — contain no
usage, cost, quota, or rate-limit command, flag, or output field of any kind. Where
Claude, Codex, and Gemini each have _some_ vendor-documented usage surface even if
unreachable from headless mode (§2.2–2.4), Grok Build CLI has none documented at all;
`QUOTA_RE`'s pattern match is nightgauge's only signal, official or otherwise.

**`grok models`, the one adapter with a genuine catalog probe, does not extend to
quota.** Grok is the one adapter in this survey with a real, wired, evidence-backed CLI
catalog command (`internal/doctor/adapters.go:538-597`, `parseGrokCatalog`, grok CLI
1.0.4, captured 2026-08-15, `internal/doctor/testdata/grok-catalog/`) — but that command
lists available _models_, not usage, consistent with the documentation finding above.

**Verdict: defer**, for the same structural reason as `codex`: a real, boolean,
already-consumed signal exists; it does not map onto `UsageWindow`'s required `used`
figure without fabrication, and a provider is only worth building once that mapping
question is designed, not before.

### 2.7 `lm-studio` and `ollama` — skip, and "usage" means something different for them

The issue's Technical Notes ask this to be treated honestly rather than silently
dropped, so: both adapters are **local HTTP runtimes with genuinely no billing or quota
concept** — `internal/doctor/adapters.go:166-167` classifies them `kindHTTP`, resolving
a model against a locally-configured OpenAI-compatible server
(`http://localhost:11434/v1` for Ollama, `http://localhost:1234/v1` for LM Studio by
default) with no account, no plan, and no provider-side usage tracking of any kind to
query. There is no vendor to ask "how much quota is left" — the concept does not apply.

**They are also not dispatched by the pipeline at all today**, which the epic's
`UsageProvider` scope should account for independently of the quota question:
`internal/execution/adapters/ollama.go:29-31` and `lmstudio.go:25-31` both report
`Agentic() bool { return false }` — the bridge "bottoms out in the TypeScript
[Ollama/LmStudio]Adapter — fetch/SSE chat completion with zero tool handling," and both
are explicitly "barred from pipeline dispatch (#57); remains available for eval/judge
surfaces." A `UsageProvider` for either would today only ever apply to eval/judge runs,
never to a pipeline stage a status-bar meter is watching.

**What "usage" honestly means for these two**: not spend, not a subscription window, but
local **capacity** — VRAM/RAM headroom, whether the configured model is loaded, whether
the server is reachable at all. `internal/doctor/adapters.go`'s existing `kindHTTP`
health check (`ServerReachable`, `ModelOK` via a live `/models` catalog probe,
`probeLocalServer` — `internal/doctor/adapters.go:707-746`) already answers exactly this
question, deterministically, with no LLM involved, today. If the epic's model is ever
extended to local runtimes, the honest framing is "server/model health," reusing that
existing doctor mechanism — not a `UsageWindow` with a fabricated `limit`. This spike
records that as the explicit reasoning, not a silent omission.

**Don't mistake Ollama's concurrency knobs for a quota signal.** Ollama's own
documentation (`docs.ollama.com/faq`, fetched live for this spike) describes
`OLLAMA_NUM_PARALLEL` and `OLLAMA_MAX_LOADED_MODELS` as governing concurrent-request and
model-loading capacity based on locally available RAM/VRAM — a hardware ceiling, not a
metered or billed allowance. They resemble a "limit" superficially; they are not one in
the sense this survey cares about, and a provider must not conflate them.

**Ollama Cloud (a separate, hosted product) does have plan-based limits, with no shipped
API to query them.** Distinct from the local server this adapter talks to, Ollama's
hosted offering has session/weekly usage limits by plan tier — but a live quota-check API
for it is an **open, unshipped feature request** as of this spike
(ollama/ollama#16448 and ollama/ollama#15663), not a mechanism nightgauge's
`ollama` adapter could adopt even
if it wanted to, since the adapter targets the local server
(`NIGHTGAUGE_OLLAMA_BASE_URL`, default `http://localhost:11434/v1`), not the hosted
product.

## 3. What this means for the epic's working assumption

The comment that shaped this epic —
_"No upstream API is available for Claude Code Max quota — tracking is local and
budget-based"_ — is **not fully accurate, in an instructive way**. It was only ever
asked about one adapter, and the honest finding generalizes across three of the eight:
Claude Code, Codex, and Gemini CLI each ship a real, vendor-documented, provider-backed
usage command (`/usage`, `/status`, `/stats model`). None of them is a myth and none of
them is local accounting wearing a costume — they are genuine upstream APIs. What is
true, and what the original comment actually captured even if it named the wrong reason,
is that **none of the three is reachable from a non-interactive dispatch**, and
nightgauge dispatches every CLI in this survey non-interactively by construction (`-p`,
`exec`, headless flags — never an interactive TUI session). So "no upstream API is
available" should be corrected to "no upstream API is available _to a headless caller_" —
a narrower and more precise claim, and one that opens exactly one door the original
framing closed: Claude's `--output-format stream-json` carries a second, independent,
undocumented channel that _is_ reachable headlessly (§2.2), and nightgauge's own
TypeScript execution path already parses it for an unrelated purpose. That is the one
`adopt`. Nothing found for Codex or Gemini suggests an equivalent headless-reachable
channel exists today — but nothing found rules one out either, since neither `codex exec`
nor headless `gemini` was captured and inspected the way `claude -p` effectively already
has been (via `skillRunner.ts`'s existing parsing). That is this spike's single sharpest
recommendation for future work: the same "capture the actual headless stream and grep it"
discipline that surfaced Claude's signal has not yet been applied to Codex or Gemini, and
until it is, `defer`/`skip` on those two means "not yet looked for the right way," not
"confirmed absent."

For the remaining four adapters (plus `gemini-sdk`, which shares `gemini`'s finding
exactly — same CLI, same absence of adapter-side detection), the assumption holds up
cleanly: `copilot` by design
never reports plan-level figures anywhere, in any mode (§2.5); `grok` has no documented
usage surface at all, interactive or otherwise (§2.6); and `lm-studio`/`ollama` have no
quota concept because there is no vendor billing relationship to query in the first place
(§2.7).

**The most important limitation of this survey to disclose plainly**: two of the eight
adapters (`gemini`, `copilot`) could not be probed against a _live, installed_ CLI on this
machine — neither by this spike nor by the doctor package's own #604 evidence-gathering
effort a day earlier — because no working installation was available on the capture
machines used. For those two, and for `codex`'s and `grok`'s headless-mode question
above, this document's verdicts rest on vendor documentation and this repository's own
adapter code, not on an independent live capture the way Claude's finding does. Every
`skip`/`defer` verdict resting on that gap should be read as "no mechanism was found with
the evidence available," not "a mechanism was found and rejected." A future revisit with
working CLIs installed, applying the same live-capture discipline §2.2 used for Claude,
could change several of these verdicts; nothing in this document should be read as
foreclosing that.

## Recommendations

```yaml recommendations
spike: 662
recommendations:
  - id: claude-usage-provider
    action: adopt
    # Materialized by hand as issue 709 and shipped as
    # ClaudeRateLimitUsageProvider. See the amendment section of
    # docs/decisions/018-adapter-usage-quota-model.md for what the
    # at-rest/staleness requirement below forced on the model.
    status: shipped
    title: "feat(vscode): claude UsageProvider from the existing rate_limit_event stream"
    type: feature
    priority: medium
    size: M
    labels: ["area:vscode"]
    body: |
      Implement UsageProvider for the claude adapter (covering both the
      claude-headless CLI/OAuth path and the claude-sdk API-key path) by
      consuming the rate_limit_event envelope tokenParser.ts and
      skillRunner.ts already parse from `claude --output-format stream-json`
      (packages/nightgauge-vscode/src/utils/tokenParser.ts:130-171,
      skillRunner.ts:6436-6448) — do not add any new CLI invocation or flag.

      Map RateLimitEventData onto UsageWindow (#658): used = utilization,
      limit = 100, resetsAt = resetsAt, scope derived from rateLimitType
      (five_hour -> session/rolling, seven_day -> weekly, daily -> daily).
      There is no per-model-family breakdown in this signal — do not
      populate modelFamily from it.

      This signal is only observed while a claude invocation is actively
      streaming. Persist the last-seen reading (per window/bucket) to disk
      so the status bar can sample "at rest" between runs, and set
      confidence: "measured" only for a same-run reading, "estimated" for a
      cached one, with an explicit as-of timestamp surfaced to the caller.

      This is an unofficial, reverse-engineered wire-format detail that has
      already changed shape once in this codebase's history (#3386's
      flat-to-nested rate_limit_info move). Parse defensively: unknown
      fields ignored, missing fields treated as absent rather than zero,
      and do not present this to the user as more authoritative than a
      locally-observed reading of the CLI's own output.
    depends_on: []

  - id: codex-usage-provider
    action: defer
    title: "spike/design: codex usage-boundary signal — does not fit UsageWindow as-is"
    type: chore
    priority: low
    size: S
    labels: ["area:vscode"]
    body: |
      Codex's only CONFIRMED quota signal in the mode nightgauge dispatches
      (`codex exec`, non-interactive) is a plain-text boundary-crossing
      message ("usage limit reached for this account") with no percentage
      and no machine-parseable reset time (docs/FAILURE_TAXONOMY.md:596-607).
      It is already detected and drives pipeline retry/cooldown behavior,
      but UsageWindow requires a `used` figure this signal cannot honestly
      supply without fabricating a number.

      Codex CLI's interactive `/status` command DOES report a real,
      provider-server-reported percentage-remaining and reset date for both
      the five-hour and weekly windows (confirmed via openai/codex's own
      issue tracker) — but this spike could not confirm whether an
      equivalent is reachable from `codex exec`'s non-interactive NDJSON
      stream. Before deciding this stays deferred, capture real
      `codex exec --json` output (the same discipline this spike applied to
      `claude -p --output-format stream-json`, which is how the
      claude-usage-provider signal was found) and grep it for any
      status/quota-shaped event type. If one exists, this becomes an adopt
      the same way claude-usage-provider did. If not, design how a boolean
      blocked/not-blocked signal should surface alongside (not inside) the
      UsageWindow list, or decide it is not worth surfacing separately from
      local accounting.
    depends_on: []

  - id: grok-usage-provider
    action: defer
    title: "spike/design: grok usage-boundary signal — does not fit UsageWindow as-is"
    type: chore
    priority: low
    size: S
    labels: ["area:vscode"]
    body: |
      Same structural finding as codex: grok's only quota signal is a
      plain-text boundary-crossing regex match (QUOTA_RE in
      packages/nightgauge-sdk/src/cli/adapters/grokStream.ts:40-43), no
      percentage, no reset time. Defer for the same reason as
      codex-usage-provider — needs a boolean-signal design, not a
      UsageWindow mapping, before a provider is worth building.
    depends_on: []

  - id: gemini-usage-provider
    action: skip
    title: "gemini usage provider — no mechanism found"
    type: chore
    priority: low
    size: XS
    labels: ["area:vscode"]
    body: |
      No quota-detection code exists for gemini or gemini-sdk anywhere in
      this repository, and no working gemini CLI was available to either
      this spike or the #604 doctor evidence-gathering effort to interrogate
      directly (internal/doctor/testdata/no-catalog-cli-probes/README.md).

      Gemini CLI does document an interactive `/stats model` command that
      reports quota-related info, but (a) it is an interactive slash
      command, unreachable from nightgauge's non-interactive dispatch, same
      as Claude's /usage and Codex's /status, and (b) an open,
      maintainer-unanswered bug report (google-gemini/gemini-cli#17081)
      describes it showing available quota at the same moment the API was
      actually exhausted — its accuracy is independently disputed even for
      interactive use. Recorded as "no mechanism found," not "found and
      rejected" — revisit if a quota message is ever observed in
      production, or a future catalog-probe capture surfaces a
      gemini-native usage command.
    depends_on: []

  - id: copilot-usage-provider
    action: skip
    title: "copilot usage provider — accounting only, no remaining-allowance signal"
    type: chore
    priority: low
    size: XS
    labels: ["area:vscode"]
    body: |
      The Copilot CLI's stats footer reports a premium-request count for
      the current invocation only (internal/execution/stream.go:520-573);
      no monthly-remaining figure is surfaced anywhere, and no working
      standalone Copilot CLI was available to probe further
      (internal/doctor/testdata/no-catalog-cli-probes/README.md). The
      per-run count already flows into local accounting via TokenTracker;
      there is no distinct provider-reported remaining-allowance mechanism
      to build a separate provider around.
    depends_on: []

  - id: lm-studio-usage-provider
    action: skip
    title: "lm-studio usage provider — no quota concept; not pipeline-dispatched"
    type: chore
    priority: low
    size: XS
    labels: ["area:vscode"]
    body: |
      Local HTTP runtime with no billing or quota concept
      (internal/doctor/adapters.go kindHTTP), and Agentic() is false
      (internal/execution/adapters/lmstudio.go:25-31) — barred from
      pipeline stage dispatch (#57), so a status-bar meter never observes
      it in the pipeline's normal operation. If ever revisited, "usage"
      should mean local server/model health via the existing doctor
      kindHTTP probe, not a fabricated UsageWindow.
    depends_on: []

  - id: ollama-usage-provider
    action: skip
    title: "ollama usage provider — no quota concept; not pipeline-dispatched"
    type: chore
    priority: low
    size: XS
    labels: ["area:vscode"]
    body: |
      Same finding as lm-studio-usage-provider: local HTTP runtime, no
      billing/quota concept, Agentic() is false
      (internal/execution/adapters/ollama.go:25-31), barred from pipeline
      dispatch (#57). Skip for the same reason.
    depends_on: []
```
