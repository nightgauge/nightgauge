# Telemetry Privacy

Nightgauge telemetry is **opt-out**. It is on by default, you are told so the
first time it runs, and you can turn it off at any time from **Nightgauge:
Telemetry Settings** (Command Palette) or with one line of config.

## Turn it off

```yaml
# .nightgauge/config.yaml
platform:
  telemetry:
    enabled: false
```

Or in VSCode settings, set `nightgauge.telemetry.enabled` to `false`. Either
one stops everything on this page.

## TL;DR

- Default: **on, on every surface** (changed in #738). The extension's
  `nightgauge.telemetry.enabled` defaults `true`, and the CLI/scheduler's
  `platform.telemetry.enabled` defaults `true` (see
  [CONFIGURATION.md](CONFIGURATION.md)).
- **You are told before you have to go looking.** The first time you activate
  the extension you get a modal that states what is shared and offers _Turn
  off_ / _Keep on_. The CLI prints the equivalent notice to stderr on its first
  run. Neither asks permission, because the answer is already yes — pretending
  otherwise would be the dishonest version of this design.
- **An explicit `false` is never overridden.** If you previously declined, that
  decision was written to your config and this change did not touch it. Only
  operators who had never configured telemetry were moved by the new default.
- VSCode's global `telemetry.telemetryLevel = "off"` is honored as a hard
  kill-switch — Nightgauge never sends data when VSCode telemetry is
  disabled, regardless of the per-extension setting. This now covers **every**
  stream including `adapter-usage`, which was not checking it before #738.
- No payload ever carries source code, file contents, secrets, branch names,
  commit SHAs, or free-form input. The `pipeline-run` stream does include the
  repository slug (`owner/name`) and issue number as correlation keys, so the
  dashboard can show per-repository, per-issue history — see
  [What we collect](#what-we-collect).
- You can disable individual streams (`pipeline-run`, `health`,
  `recommendation`) without disabling telemetry overall.

## What we collect

When the corresponding stream is enabled, we receive aggregate counts and
outcome categories. Examples:

- Stage outcome (`completed`, `failed`, `aborted`)
- Stage duration buckets (round to the nearest second; never sub-second
  timing)
- Token totals per stage (input + output)
- Pipeline outcome category (`productive`, `low-value-loop`, `aborted`,
  `failed`)
- Issue size and type labels (`S`, `M`, `L` / `feature`, `bug`, …)
- Repository slug (`owner/name`) and issue number — the correlation keys the
  `pipeline-run` stream carries so per-repository and per-issue history render
  in the dashboard. No repository URL, clone remote, description, or contents
  are ever sent.

### Workflow-orchestration telemetry (V4)

When a stage fans out through the multi-agent orchestration engine (see
[docs/WORKFLOW_ORCHESTRATION.md](WORKFLOW_ORCHESTRATION.md)), the
`schema_version: 4` outcome payload carries the run's tree as a **nested**,
anonymous `agents[]` array (per-agent provider, status, terminal kind, and token
counts) plus the adversarial `judgeVerdict` (`pass` / `fail` / `uncertain`) — the
same node tree the UI renders, with no prompts, file contents, or identifiers.
The V4 schema preserves `.strict()` (an unknown field is rejected, not silently
forwarded), and these aggregate counters travel only on the **`health`** stream —
the same health-telemetry boundary as every other self-improvement counter, never
a separate channel.

## What we never collect

- Source code or file contents
- Repository URLs, clone remotes, or descriptions (the `pipeline-run` stream
  does send the `owner/name` slug as a correlation key — see
  [What we collect](#what-we-collect))
- Branch names or commit SHAs
- Issue titles, bodies, or comments
- File paths or directory structures
- Secrets, tokens, API keys, OAuth credentials, or environment variables
- IP addresses (the platform receives the request IP solely for transport;
  it is not retained beyond rate-limiting windows)
- Any free-form user input

## Streams

| Stream           | What it carries                                                                   |
| ---------------- | --------------------------------------------------------------------------------- |
| `pipeline-run`   | Per-repository (`owner/name`) outcomes and durations from the Issue → PR pipeline |
| `health`         | Queue, retry, and error counters for self-improvement loops                       |
| `recommendation` | Effectiveness of self-recommendations (accept vs. ignore)                         |
| `adapter-usage`  | How much of your AI provider's allowance is left — see below                      |

You can toggle any stream off in the Telemetry Settings panel without
disabling telemetry overall.

### Adapter usage — tiered, and reported to your own account

The footer and the dashboard webview show how much of your AI provider's
allowance is left (for a Claude Max plan, the five-hour and weekly windows).

`platform.telemetry.usage_reporting` controls whether that picture also reaches
the hosted dashboard, so you can see your allowance across every machine you
run. **This report goes to your own account, not to Nightgauge as product
analytics** — it is the multi-machine view of your own data. That is why it
defaults to `full` rather than off, and it is a materially different disclosure
from the aggregate product telemetry above.

It remains its own switch, because "I share pipeline outcomes" and "I share how
much of my Claude plan I have used" are still different decisions:

| Tier                 | What is sent                                                |
| -------------------- | ----------------------------------------------------------- |
| `off`                | Nothing. The agent heartbeat carries no body at all.        |
| `minimal`            | Allowance windows only — **no monetary figure ever leaves** |
| `full` (**default**) | Additionally the locally-derived per-adapter dollar spend   |

If you want the allowance view across machines but would rather your spend
stayed local, `minimal` is that setting exactly.

`minimal` is defined by what it withholds — money — rather than by which part
of Nightgauge produced a window. Today that split is exact: the dollar figures
are Nightgauge's own rate-card reduction of this workspace's pipeline history,
and the percentages are the provider's own statement of your account's
allowance.

Every switch above it must permit it. If VSCode telemetry is off, or
`platform.telemetry.enabled` is explicitly `false`, nothing is reported
regardless of the tier.

The tier is re-read on every heartbeat, so turning reporting off takes effect
within 30 seconds rather than at the next window reload.

**What a report contains**: the adapter name, the billing arrangement observed,
and for each window a period label, the figure, the ceiling if one is known,
the reset time, and how much the figure can be trusted. The tier itself travels
with the payload, so a surface reading it can tell "no dollar spend" from
"dollar spend withheld" and never present the second as the first.

**What it does not contain**: any account identifier for your AI provider, any
model or prompt detail, and nothing from the
[What we never collect](#what-we-never-collect) list above.

### Local skill-usage log (not transmitted)

The PreToolUse(Skill) hook records skill-catalog usage to an **in-repo**,
local-only file at `.nightgauge/skills/usage.jsonl` (read with
`nightgauge skills usage`). Each line carries only `{ ts, skill, session }`
— the skill's name, an RFC3339 timestamp, and the Claude Code session id. It
records **no** prompt content, file contents, arguments, tokens, secrets, or
personal data, and it is **never sent to the platform** — it stays in the
repository as the single source of truth for which skills are triggering. Delete
the file to clear it; remove the `Skill` matcher from
`claude-plugins/nightgauge/hooks/hooks.json` to stop recording.

## How payloads are bounded

Two independent mechanisms keep payloads free of source, secrets, and
free-form content:

**Ad-hoc analytics events** pass through `RedactionService` in `flushQueue()`
before they reach the platform IPC. The redactor:

1. Removes any field whose key is in the secret-key blocklist (`token`,
   `api_key`, `password`, `secret`, `auth*`, `_debug_*`, …).
2. Drops fields containing values that match secret patterns
   (`sk-…`, GitHub PATs, JWTs, etc.).
3. Truncates string values to a fixed maximum length to bound payload size.

**The structured streams** (`pipeline-run`, `health`, `recommendation`) do not
go through the redactor. Instead they are assembled from a fixed, typed schema
(`schema_version: 4`) and validated with `.strict()` — an unknown field is
rejected, never forwarded. Each record can therefore carry only its
pre-declared fields: aggregate outcomes, duration and token counters, and — for
`pipeline-run` — the `owner/name` slug and issue number as correlation keys. No
source code, file contents, branch names, commit SHAs, or secret values are
among the schema's fields, so they cannot appear in a payload.

See
[`RedactionService`](../packages/nightgauge-vscode/src/services/RedactionService.ts)
and
[`pipelineRunV4Mapper`](../packages/nightgauge-vscode/src/services/telemetry/pipelineRunV4Mapper.ts).

## Retention

Telemetry events are retained for at most 90 days for product analytics, then
deleted. Aggregated counters (no per-event row) may be retained longer.

## How to opt out

1. **Command Palette → Nightgauge: Telemetry Settings** — opens the
   webview panel where you can toggle the master switch and individual
   streams.
2. **VSCode Settings** — set `nightgauge.telemetry.enabled` to `false`.
3. **VSCode global telemetry** — set `telemetry.telemetryLevel` to `"off"`
   to disable telemetry across all extensions.
4. **CLI / Go scheduler** — telemetry is already off by default there
   (`platform.telemetry.enabled: false`). It stays off unless you explicitly set
   it to `true` in `config.yaml` or via `NIGHTGAUGE_PLATFORM_TELEMETRY_ENABLED`.

Disabling telemetry takes effect immediately. Any events that were already
queued in memory are dropped — no in-flight uploads continue after the
toggle flips off.

## How to request deletion

If you have used a paid tier and want your historical aggregate data
deleted, email `privacy@nightgauge.dev` with the email address associated with
your subscription. We will delete all telemetry rows tied to your account
within 30 days of the request.

## Settings reference

| Setting                                      | Type    | Default                                        | Description                                     |
| -------------------------------------------- | ------- | ---------------------------------------------- | ----------------------------------------------- |
| `nightgauge.telemetry.enabled`               | boolean | `true`                                         | Master switch — set `false` to stop all sending |
| `platform.telemetry.enabled`                 | boolean | `true`                                         | Same switch for the CLI / Go scheduler          |
| `nightgauge.telemetry.streams`               | array   | `["pipeline-run", "health", "recommendation"]` | Streams that may submit data when enabled       |
| `nightgauge.telemetry.uploadIntervalMinutes` | integer | `15`                                           | How often the queue flushes (1–1440 min)        |
| `platform.telemetry.usage_reporting`         | enum    | `full`                                         | Allowance reporting: `off` / `minimal` / `full` |

VSCode's own `telemetry.telemetryLevel` sits above every row in this table. When
it is `"off"`, none of these settings can cause anything to be sent.

## Questions?

Open an issue at <https://github.com/nightgauge/nightgauge> with the
`privacy` label, or email `privacy@nightgauge.dev`.
