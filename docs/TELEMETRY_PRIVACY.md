# Telemetry Privacy

Nightgauge sends nothing about your work to the hosted service until you turn
cloud features on. With them on, run telemetry goes to your account, is on by
default, is disclosed the first time it runs, and you can turn it off at any
time from **Nightgauge: Telemetry Settings** (Command Palette) or with one line
of config. This page says exactly what is sent, when, and why.

## When anything is sent

Run telemetry leaves this machine only when all three hold:

1. **Cloud features are on.** `platform.enabled: true` in your machine-tier
   `config.yaml` ([where it lives](CONFIGURATION.md#global-config-location)),
   or a `NIGHTGAUGE_LICENSE_KEY` or `NIGHTGAUGE_API_KEY` in the environment the
   CLI or VS Code was started from. A repository's `.nightgauge/config.yaml`
   cannot turn it on. Signing in, a license key stored by activating a license
   or starting a trial, and a platform URL do not turn it on.
2. **Telemetry is on.** `platform.telemetry.enabled` is not `false` in the
   machine-tier `config.yaml`. In VS Code, also `nightgauge.telemetry.enabled`
   is not `false` and VS Code's `telemetry.telemetryLevel` is not `"off"`. The
   extension passes those two to the daemon it starts, and tells it at once
   when either changes.
3. **There is an account to send to:** a license key or a signed-in session.

Your own account actions talk to the hosted service whatever these switches
say, and send only what the action needs: signing in or out sends the sign-in
exchange; activating a license sends the key you entered with this machine's
id, hostname and operating system; starting a trial sends your session. None
of them sends anything about your runs.

## Turn it off

```yaml
# the machine-tier config.yaml (~/.nightgauge/config.yaml on macOS)
platform:
  telemetry:
    enabled: false
```

Or in VS Code settings, set `nightgauge.telemetry.enabled` to `false`. Either
one stops everything on this page. Leaving `platform.enabled` off (the
default) stops it too.

## TL;DR

- **Off until you turn cloud features on**, then on by default. A signed-in
  session alone sends nothing.
- **You are told before you have to go looking.** The first time you activate
  the extension you get a notice that states what is shared and offers _Turn
  off_ / _Keep on_. The CLI prints the equivalent notice to stderr on its first
  run with cloud features on.
- **An explicit `false` is never overridden.**
- VS Code's global `telemetry.telemetryLevel = "off"` is honored as a hard
  kill switch, by the extension and by the daemon it starts.
- **A run sends its repository and issue number, the issue title and labels,
  the branch, its timings, token counts, cost and outcome, and a failed
  stage's error message.** The hosted dashboard shows them in your run list and
  run detail. The full list is under [What a run sends](#what-a-run-sends).
- **Never sent:** your source code or file contents, the issue body, issue or
  pull-request comments, secrets, tokens or environment variables; commit SHAs
  only if you set up the audit trail. Two kinds of text the pipeline writes about its own work can
  quote from your files or command output: a failed stage's error message, and
  the reasons and evidence in the `trace` stream (see below).
- You can disable individual streams (`pipeline-run`, `health`,
  `recommendation`, `trace`) without disabling telemetry overall.

## What a run sends

The daemon (`nightgauge serve`, which the extension starts) and the autonomous
scheduler send these for every pipeline run, while the conditions in
[When anything is sent](#when-anything-is-sent) hold.

**Live stage events**, so the hosted dashboard's Pipelines view shows the run
while it is in flight:

- when a stage starts: the run id, the repository (`owner/name`), the issue
  number, the stage and where it runs (`local_cli`); for a run the autonomous
  scheduler started, also the branch and the performance mode;
- while a stage runs: its token and cost estimates so far;
- when a stage completes: its duration, token counts and cost;
- when a stage fails: an error code and the stage's error message (at most
  2,000 characters), which can quote a file name or command output;
- when the run ends: its total duration, the stages that ran and whether it
  succeeded.

**The completed-run record**, which the dashboard's run list, run detail and
cost and token widgets read:

- the run id, repository and issue number;
- when it started and ended, its outcome (`complete`, `failed`, `cancelled`),
  the kind of failure, and whether it was blocked;
- the predicted and actual size (`XS`–`XL`), the predicted and actual model,
  the complexity score and the number of retries;
- the total duration and cost;
- for each stage: its name, attempt, model, adapter, model provider, execution
  path, effort and reasoning settings, duration, token counts, cost and
  whether it succeeded;
- the route the run took through the stages;
- **the issue title** (at most 256 characters) **and labels** (at most 50), so
  the dashboard can show what the run was for without leaving it.

The issue body is not sent. The run keeps it locally, in its own history.

**The queue snapshot**, sent by the autonomous scheduler when its queue
changes, so the dashboard shows what this machine has queued and is working
on: for each queued issue its number, position, priority, status, repository
and title, keyed by this machine's id. Each snapshot replaces the previous one.

**The audit trail**, only if you set it up — a platform URL and key in
`audit.platform_url` and `audit.api_key`, or `NIGHTGAUGE_AUDIT_PLATFORM_URL`
and `NIGHTGAUGE_AUDIT_API_KEY` — and cloud features are on: the extension posts
an event for each pipeline and stage start, completion and failure (the issue
number, stage, model, outcome, duration and a failed stage's error message),
each skill it invokes, and each commit a run validates (its SHA), for the
hosted service's audit and compliance views. `NIGHTGAUGE_AUDIT_ENABLED=false`
turns it off on its own.

**The extension's uploads.** In VS Code the extension also uploads the local
history in these streams:

| Stream           | What it carries                                                                                                                                                                                                                                                |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pipeline-run`   | A copy of the completed-run record above, without the issue title and labels                                                                                                                                                                                   |
| `health`         | Queue, retry and error counters for self-improvement loops                                                                                                                                                                                                     |
| `recommendation` | Whether self-recommendations were accepted or ignored                                                                                                                                                                                                          |
| `trace`          | The run's decision trace: each stage start and exit, phase change, model routing decision (the model chosen, the router's reasoning and the alternatives), skip, escalation, backtrack, retry and gate result (its reason and evidence lines), and the outcome |

Gate reasons and evidence are text the pipeline writes; they can name files in
your repository or quote command output. Turn the `trace` stream off to keep
them on this machine.

**Why.** All of the above feeds the hosted dashboard: your run list and run
detail, the live Pipelines view, cost and token use, the queue across your
machines, and the analytics that tune the pipeline. It is stored with your
account, where the members of your team can see your runs.

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

## What is never sent

No field of any payload above carries:

- source code or file contents;
- the issue body, or issue or pull-request comments;
- repository URLs, clone remotes, or descriptions (the repository is sent
  only as its `owner/name` slug);
- commit SHAs, unless you set up the audit trail;
- secrets, tokens, API keys, OAuth credentials, or environment variables;
- prompts;
- IP addresses (the platform receives the request IP solely for transport;
  it is not retained beyond rate-limiting windows).

Two fields are free text the pipeline writes about its own work: a failed
stage's error message, and the `trace` stream's reasons and evidence. They are
bounded in length but not redacted, so they can quote a file name, a line of
command output or an agent's last words.

## Adapter usage — tiered, and reported to your own account

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
| `off`                | Nothing. The agent heartbeat carries no `usage` field.      |
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

**Every heartbeat carries an instance id** (#2395), whatever the tier: a
random UUID made when the window activates (the daemon makes one per process),
held in memory only, and never derived from a path, host, user or workspace.
The windows of a machine share one platform agent, so the platform uses it to
keep each window's execution profile apart. A reload makes a new one.

## Local skill-usage log (not transmitted)

The PreToolUse(Skill) hook records skill-catalog usage to a local-only file,
`skills/usage.jsonl` in the checkout's git directory
(`nightgauge layout path checkout skills/usage.jsonl`; read with
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

**The run record and the structured streams** (`pipeline-run`, `health`,
`recommendation`) do not go through the redactor. Instead they are assembled
from a fixed, typed schema and validated with `.strict()` — an unknown field is
rejected, never forwarded. Each record can therefore carry only the fields
[What a run sends](#what-a-run-sends) lists, each bounded in length. The issue
body is not among them.

**The live stage events and the `trace` stream** carry text the pipeline
writes about its own work: a failed stage's error message, and gate reasons,
evidence lines and the router's reasoning. They are bounded in length but not
redacted, so treat them as able to quote a file name or command output.

**Consent is checked where data leaves.** The daemon asks the consent in
[When anything is sent](#when-anything-is-sent) before every send and before
every flush of what it buffered while offline; when the answer turns to no,
the buffered items are dropped, not sent.

See
[`RedactionService`](../packages/nightgauge-vscode/src/services/RedactionService.ts),
[`pipelineRunV4Mapper`](../packages/nightgauge-vscode/src/services/telemetry/pipelineRunV4Mapper.ts)
and
[`execution_history_mapper.go`](../internal/platform/execution_history_mapper.go).

## Retention

Telemetry events are retained for at most 90 days for product analytics, then
deleted. Aggregated counters (no per-event row) may be retained longer. Run
records, the issue title and labels included, are your run history in the
hosted dashboard and are kept with your account until you ask for them to be
deleted (below).

## How to opt out

1. **Command Palette → Nightgauge: Telemetry Settings** — opens the
   webview panel where you can toggle the master switch and individual
   streams.
2. **VSCode Settings** — set `nightgauge.telemetry.enabled` to `false`.
3. **VSCode global telemetry** — set `telemetry.telemetryLevel` to `"off"`
   to disable telemetry across all extensions.
4. **CLI and daemon** — set `platform.telemetry.enabled: false` in the
   machine-tier `config.yaml`; the daemon reads it when it starts. Or leave
   `platform.enabled` off: nothing is sent without it.

Turning telemetry off in VS Code takes effect immediately, in the extension and
in the daemon it started. Any events that were already queued in memory are
dropped — no in-flight uploads continue after the toggle flips off.

## How to request deletion

If you have used a paid tier and want your historical aggregate data
deleted, email `privacy@nightgauge.dev` with the email address associated with
your subscription. We will delete all telemetry rows tied to your account
within 30 days of the request.

## Settings reference

| Setting                                      | Type    | Default                                                 | Description                                                  |
| -------------------------------------------- | ------- | ------------------------------------------------------- | ------------------------------------------------------------ |
| `platform.enabled`                           | boolean | `false`                                                 | Cloud features: nothing on this page is sent while it is off |
| `nightgauge.telemetry.enabled`               | boolean | `true`                                                  | Master switch — set `false` to stop all sending              |
| `platform.telemetry.enabled`                 | boolean | `true`                                                  | Same switch for the CLI and the daemon (machine-tier config) |
| `nightgauge.telemetry.streams`               | array   | `["pipeline-run", "health", "recommendation", "trace"]` | Streams the extension may upload when enabled                |
| `nightgauge.telemetry.uploadIntervalMinutes` | integer | `15`                                                    | How often the queue flushes (1–1440 min)                     |
| `platform.telemetry.usage_reporting`         | enum    | `full`                                                  | Allowance reporting: `off` / `minimal` / `full`              |

VSCode's own `telemetry.telemetryLevel` sits above every VS Code row in this
table and over the daemon the extension starts. When it is `"off"`, none of
these settings can cause anything to be sent.

## Questions?

Open an issue at <https://github.com/nightgauge/nightgauge> with the
`privacy` label, or email `privacy@nightgauge.dev`.
