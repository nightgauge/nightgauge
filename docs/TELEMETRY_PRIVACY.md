# Telemetry Privacy

Nightgauge telemetry is **opt-in**. No data is sent until you enable it,
and you can change your mind at any time from **Nightgauge: Telemetry
Settings** (Command Palette).

## TL;DR

- Default: **off on every surface**. Telemetry is opt-in whether you run the
  VSCode extension or the CLI/Go scheduler — nothing is sent until you turn it
  on. The extension's `nightgauge.telemetry.enabled` defaults `false`, and the
  CLI/scheduler's `platform.telemetry.enabled` defaults `false` (see
  [CONFIGURATION.md](CONFIGURATION.md)). The first time you activate the
  extension in a workspace, you'll see a modal with three actions: Decline,
  Decide later, Enable.
- VSCode's global `telemetry.telemetryLevel = "off"` is honored as a hard
  kill-switch — Nightgauge never sends data when VSCode telemetry is
  disabled, regardless of the per-extension setting.
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

You can toggle any stream off in the Telemetry Settings panel without
disabling telemetry overall.

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

| Setting                                      | Type    | Default                                        | Description                                            |
| -------------------------------------------- | ------- | ---------------------------------------------- | ------------------------------------------------------ |
| `nightgauge.telemetry.enabled`               | boolean | `false`                                        | Master switch — must be `true` for any data to be sent |
| `nightgauge.telemetry.streams`               | array   | `["pipeline-run", "health", "recommendation"]` | Streams that may submit data when enabled              |
| `nightgauge.telemetry.uploadIntervalMinutes` | integer | `15`                                           | How often the queue flushes (1–1440 min)               |

## Questions?

Open an issue at <https://github.com/nightgauge/nightgauge> with the
`privacy` label, or email `privacy@nightgauge.dev`.
