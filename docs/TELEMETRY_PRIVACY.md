# Telemetry Privacy

Nightgauge sends nothing about your work to the hosted service until you turn
cloud features on. With them on, run telemetry goes to your account, is on by
default, is disclosed the first time it runs, and you can turn it off at any
time from **Nightgauge: Telemetry Settings** (Command Palette) or with one line
of config. This page says exactly what is sent, when, and why.

## When anything is sent

**Cloud features are off by default, and with them off nothing is sent on its
own.** They are on with `platform.enabled: true` in your machine-tier
`config.yaml` ([where it lives](CONFIGURATION.md#global-config-location)), or,
for the CLI and the daemon, a `NIGHTGAUGE_LICENSE_KEY` or `NIGHTGAUGE_API_KEY`
in the environment they were started from. A repository cannot turn them on:
neither the CLI nor the extension reads the `platform` block from a
repository's `.nightgauge/config.yaml` or `config.local.yaml`, and the daemon
logs a warning when it finds one there. Signing in, a license key stored by
activating a license or starting a trial, and a platform URL do not turn them
on either.

With cloud features off, the hosted service hears from this machine only when
you act: signing in or out, activating a license, starting a trial, managing
your subscription, opening a dashboard tab that shows your account's cloud data
(Cost, Health, Runs, Trends, Compliance), or running
`nightgauge pipeline backfill`. Each sends what that action needs: activating a
license sends the key you entered with this machine's id, hostname and
operating system; the backfill uploads the run history you point it at. There
are no health checks, registrations or run data in the background.

**Run telemetry**, everything under [What a run sends](#what-a-run-sends),
also needs:

1. **Telemetry on.** `platform.telemetry.enabled` is not `false` in the
   machine-tier `config.yaml`. In VS Code, also `nightgauge.telemetry.enabled`
   is not `false` and VS Code's `telemetry.telemetryLevel` is not `"off"`. The
   extension passes those two to the daemon it starts, and tells it at once
   when either changes.
2. **An account to send to:** a license key or a signed-in session.

**Cloud features themselves**, under
[What cloud features send](#what-cloud-features-send), follow
`platform.enabled` alone: the telemetry switches do not stop them.

## Turn it off

```yaml
# the machine-tier config.yaml (~/.nightgauge/config.yaml on macOS)
platform:
  telemetry:
    enabled: false
```

Or in VS Code's user settings, set `nightgauge.telemetry.enabled` to `false`.
Either one stops all run telemetry, the daemon's and the extension's. Leaving
`platform.enabled` off (the default) stops everything on this page except the
actions you take yourself.

## TL;DR

- **Off until you turn cloud features on**, then on by default. A signed-in
  session alone sends nothing: with cloud features off, the hosted service
  hears from this machine only when you act.
- **You are told before you have to go looking.** The first time you activate
  the extension you get a notice that states what is shared and offers _Turn
  off_ / _Keep on_. The CLI prints the equivalent notice to stderr on its first
  run with cloud features on. Both are shown again, once, when what is sent
  changes.
- **An explicit `false` is never overridden**, and a repository cannot
  override it: both switches are machine settings (`nightgauge.telemetry.*`
  can be set in user settings only).
- VS Code's global `telemetry.telemetryLevel = "off"` is honored as a hard
  kill switch for run telemetry, by the extension and by the daemon it starts.
- **A run sends its repository and issue number, the issue title, labels and
  the first 8,192 characters of its body, the branch, its timings, token
  counts, cost and outcome, and a failed stage's error message.** The hosted
  dashboard shows them in your run list and run detail. The full list is under
  [What a run sends](#what-a-run-sends).
- **Never sent:** your source code or file contents, issue or pull-request
  comments, secrets, tokens or environment variables; commit SHAs only if you
  set up the audit trail. Three fields carry text as written, and can quote
  whatever it quotes: the issue body excerpt, a failed stage's error message,
  and the reasons and evidence in the `trace` stream (see below).
- You can disable individual streams (`pipeline-run`, `health`,
  `recommendation`, `trace`) without disabling telemetry overall. With
  `pipeline-run` off, no completed-run record is sent, by the extension or by
  the daemon it starts.

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
- **the issue title** (at most 256 characters), **its labels** (at most 50)
  **and an excerpt of its body** (the first 8,192 characters), so the
  dashboard's run detail can show what the run was for without leaving it.
  The excerpt is the issue's own text, sent as written: anything the issue
  says, it carries.

**The run's analytics event**: when a run completes in VS Code, the extension
sends a `pipeline_execution_completed` event through the daemon, with the
run's duration, token totals, per-stage input and output tokens, the model, the
size label, the number of stages, backtracks and model escalations, and whether
it succeeded, for the analytics that tune the pipeline.

**The queue snapshot**, sent by the daemon whenever its queue changes,
including issues you queue from VS Code, so the dashboard shows what this
machine has queued and is working on: for each queued issue its number,
position, priority, status, repository and title, keyed by this machine's id.
Each snapshot replaces the previous one.

**The extension's uploads.** In VS Code the extension also uploads the local
history in these streams:

| Stream           | What it carries                                                                                                                                                                                                                                                |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `pipeline-run`   | A copy of the completed-run record above, without the issue title, labels and body excerpt. Turning this stream off also stops the record the daemon sends                                                                                                     |
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

## What cloud features send

With cloud features on, this machine also talks to the hosted service for the
features themselves. The telemetry switches do not stop these;
`platform.enabled` off does.

- **Registration and heartbeat.** The extension window and the daemon each
  register this machine as an agent: its machine id, the Nightgauge version,
  the repositories (`owner/name`) and workspace it serves, and its execution
  profile (the adapter and model it runs). Each sends a heartbeat every 30
  seconds, with an instance id (below) and, as the tier allows, your adapter
  usage ([Adapter usage](#adapter-usage--tiered-and-reported-to-your-own-account)).
  The daemon registers only with a license key.
- **Remote commands.** Each agent keeps a connection open for the commands the
  dashboard or the app sends it (start a run, cancel, change the throttle), and
  reads the throttle your workspace sets. The daemon reads it when it
  registers, when a throttle command arrives and when its command stream
  reconnects, retrying a failed read on its heartbeat; when the extension has
  handed it your session, it also reads your team list once, to tell which
  team's workspace it serves.
- **The Action Center.** The daemon mirrors the decisions a run is waiting on,
  so the dashboard and the app can show them: for each, its title and the text
  the pipeline wrote, the repository, the issue and the branch.
- **The audit trail**, only if you set it up: a platform URL and key in
  `audit.platform_url` and `audit.api_key`, or `NIGHTGAUGE_AUDIT_PLATFORM_URL`
  and `NIGHTGAUGE_AUDIT_API_KEY`. The extension posts an event for each
  pipeline and stage start, completion and failure (the issue number, stage,
  model, outcome, duration and a failed stage's error message), each skill it
  invokes, and each commit a run validates (its SHA), for the hosted service's
  audit and compliance views. It follows `platform.enabled`;
  `NIGHTGAUGE_AUDIT_ENABLED` overrides that either way.
- **Your session and license.** The extension restores and refreshes your
  session, checks your license and plan, and the daemon checks every minute
  that the service is reachable.

## Private runs

When you are signed in, your runs are readable on the hosted service by the
team that owns the workspace. A **private** run is readable there by you alone.
You choose it when you start the run; it is off by default for every run and
is never remembered, so each run starts as a team run unless you choose
otherwise.

- **Where you choose.** In the extension, every command that starts a run
  (clicking an issue, **Pick Up Issue**, **Run Pipeline with Model**, **Add to
  Pipeline**, an epic's **Add to Pipeline** or **Run All**, retrying a failed
  issue or queue item, and running a single stage that starts a new run) asks
  _Team_ or _Private_, with _Team_ selected. Dismissing the choice cancels the
  start. When the window is not signed in, nothing reaches the hosted service,
  so no choice is offered. From the CLI, `nightgauge run <issue> --private`.
- **What owners and admins still see.** That the private run exists and what
  it cost (who started it, when, the model, tokens and cost), for billing and
  audit. Never its content.
- **What private does not hide.** Work on GitHub. Branches, pull requests,
  issues, comments and board moves follow the repository's own permissions.
- **What carries it.** The daemon sends `visibility: private` on the run's
  first stage event and on every later upload of the run: each live stage
  event, the completion record, and the run's entry in the queue snapshot. A
  team run omits the field. Once a run is private nothing lowers it.
- **Runs that are always team.** Runs the autonomous scheduler picks from the
  board itself, and `nightgauge run --auto`.
- **Runs started from the hosted service.** A run triggered with
  `visibility: private` runs only on one of your own machines, and the run it
  starts carries private on every upload. If it is served by a run of the same
  issue you had already queued, that run becomes private too.
- **Confirmation.** The extension marks a run private, with a _Private_ badge
  on its slot, only when the hosted service confirms it. When the service does
  not confirm it, for example because cloud features or run telemetry are off,
  the extension says so and shows no badge.
- **Resume.** A private run paused by a window reload resumes as a private run.

The hosted service enforces who can read a private run; the core only carries
the choice with the run.

## What is never sent

No field of any payload above carries:

- source code or file contents;
- issue or pull-request comments (the issue body is sent only as the bounded
  excerpt above);
- repository URLs, clone remotes, or descriptions (the repository is sent
  only as its `owner/name` slug);
- commit SHAs, unless you set up the audit trail;
- secrets, tokens, API keys, OAuth credentials, or environment variables;
- prompts;
- IP addresses (the platform receives the request IP solely for transport;
  it is not retained beyond rate-limiting windows).

Three fields carry text as written: the issue body excerpt, a failed stage's
error message, and the `trace` stream's reasons and evidence. They are bounded
in length but not redacted, so they can quote a file name, a line of command
output, an agent's last words, or anything an issue says.

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
[What is never sent](#what-is-never-sent) list above.

**Every heartbeat carries an instance id** (#2395), whatever the tier: a
random UUID made when the window activates (a daemon the window spawned
carries the window's; a daemon started on its own makes one per process),
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

Two independent mechanisms keep payloads free of source and secrets, and
bound the free text they carry:

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
[What a run sends](#what-a-run-sends) lists, each bounded in length. One of
them is free text that is not the pipeline's own: the issue body excerpt,
which the run record carries as the issue says it.

**The live stage events and the `trace` stream** carry text the pipeline
writes about its own work: a failed stage's error message, and gate reasons,
evidence lines and the router's reasoning. They are bounded in length but not
redacted, so treat them as able to quote a file name or command output.

**Consent is checked where data leaves.** The daemon asks the consent in
[When anything is sent](#when-anything-is-sent) before every send and before
every flush of what it buffered while offline; when the answer turns to no,
the buffered items are dropped, not sent. The extension asks before every
upload cycle.

See
[`RedactionService`](../packages/nightgauge-vscode/src/services/RedactionService.ts),
[`pipelineRunV4Mapper`](../packages/nightgauge-vscode/src/services/telemetry/pipelineRunV4Mapper.ts)
and
[`execution_history_mapper.go`](../internal/platform/execution_history_mapper.go).

## Retention

Telemetry events are retained for at most 90 days for product analytics, then
deleted. Aggregated counters (no per-event row) may be retained longer. Run
records, the issue title, labels and body excerpt included, are your run
history in the hosted dashboard and are kept with your account until you ask
for them to be deleted (below).

## How to opt out

1. **Command Palette → Nightgauge: Telemetry Settings** — opens the
   webview panel where you can toggle the master switch and individual
   streams.
2. **VSCode Settings** — set `nightgauge.telemetry.enabled` to `false` in
   your user settings (a workspace cannot set it).
3. **VSCode global telemetry** — set `telemetry.telemetryLevel` to `"off"`
   to disable telemetry across all extensions.
4. **The machine-tier config** — set `platform.telemetry.enabled: false` in
   the machine-tier `config.yaml`. The CLI and the daemon read it when they
   start, and the extension before every upload. Or leave `platform.enabled`
   off: then nothing is sent but what your own actions send.

Turning telemetry off in VS Code takes effect immediately, in the extension and
in the daemon it started. Any events that were already queued in memory are
dropped — no in-flight uploads continue after the toggle flips off.

## How to request deletion

If you have used a paid tier and want your historical aggregate data
deleted, email `privacy@nightgauge.dev` with the email address associated with
your subscription. We will delete all telemetry rows tied to your account
within 30 days of the request.

## Settings reference

| Setting                                      | Type    | Default                                                 | Description                                                         |
| -------------------------------------------- | ------- | ------------------------------------------------------- | ------------------------------------------------------------------- |
| `platform.enabled`                           | boolean | `false`                                                 | Cloud features: off, only your own actions reach the hosted service |
| `nightgauge.telemetry.enabled`               | boolean | `true`                                                  | Master switch — set `false` to stop all run telemetry               |
| `platform.telemetry.enabled`                 | boolean | `true`                                                  | The same switch in the machine-tier config, for every component     |
| `nightgauge.telemetry.streams`               | array   | `["pipeline-run", "health", "recommendation", "trace"]` | Streams that may be sent when enabled (see the stream table)        |
| `nightgauge.telemetry.uploadIntervalMinutes` | integer | `15`                                                    | How often the queue flushes (1–1440 min)                            |
| `platform.telemetry.usage_reporting`         | enum    | `full`                                                  | Allowance reporting: `off` / `minimal` / `full`                     |

VSCode's own `telemetry.telemetryLevel` sits above every VS Code row in this
table and over the daemon the extension starts. When it is `"off"`, no run
telemetry is sent, whatever these settings say.

## Questions?

Open an issue at <https://github.com/nightgauge/nightgauge> with the
`privacy` label, or email `privacy@nightgauge.dev`.
