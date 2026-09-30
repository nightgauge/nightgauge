# Doctor

`nightgauge doctor` answers one question: can this machine and workspace run the
pipeline, and if not, what exactly is wrong and how is it fixed? Every problem it
finds is a **finding** with a stable code (`NGD014`), a severity, a cause in plain
language, redacted evidence, and zero or more **remedies**. This page is the
reference for all of them: the CLI, the VS Code surfaces, the JSON schema, and one
section per finding code. Every finding's `docs` link points at its code's section
here (`docs/DOCTOR.md#ngd014`).

The design record is [ADR-025](decisions/025-doctor-findings-and-remedies.md).
Machine-state locations (the fix log's directory) follow
[ADR-024](decisions/024-data-and-state-layout.md).

- [Quick start](#quick-start)
- [Severity and exit codes](#severity-and-exit-codes)
- [The CLI](#the-cli)
- [Remedies](#remedies)
- [VS Code](#vs-code)
- [JSON v2](#json-v2)
- [Security](#security)
- [Finding codes](#finding-codes)
- [Adapter health in detail](#adapter-health-in-detail)

## Quick start

```bash
nightgauge doctor                  # on a terminal: a guided repair session
nightgauge doctor --no-interactive # the list report, no prompts
nightgauge doctor --dry-run        # what --fix would do; changes nothing
nightgauge doctor --fix            # apply every safe (auto) remedy, verified
nightgauge doctor --fix --yes      # also apply confirm remedies
nightgauge doctor --only NGD017    # re-run only the check owning a code
nightgauge doctor --json           # JSON v2, for scripts and skills
```

## Severity and exit codes

Exactly four severities exist.

| Severity       | Meaning                                 | Exit code effect |
| -------------- | --------------------------------------- | ---------------- |
| `blocker`      | The pipeline cannot run                 | 2                |
| `warning`      | It runs degraded                        | 1                |
| `housekeeping` | Cleanup; never degrades status          | none             |
| `info`         | Context, including skipped dependencies | none             |

| Exit | Meaning                                                               |
| ---- | --------------------------------------------------------------------- |
| 0    | Healthy: no blocker or warning                                        |
| 1    | Warnings only                                                         |
| 2    | At least one blocker; pipeline skills halt at their preflight         |
| 3    | `--fix` conflict: a remedy refused because nothing may be overwritten |
| 4    | `--fix` blocked: a remedy's precondition failed at apply time         |

Under `--fix` the code is the state **after** verification; 3 and 4 take
precedence over 0–2. Housekeeping and info never change the exit code.

A check whose dependency did not pass (it was skipped, timed out, or raised a
blocker) is reported as an `info` finding under the skipped check's own code, with
the title `<check> skipped: dependency <dep> did not pass`. It is never reported as
passing.

## The CLI

### Report modes

- **Guided repair (default on a terminal).** Plain `nightgauge doctor` shows live
  check progress, then a summary by severity (blockers first, housekeeping
  collapsed to one line), then walks each finding that has a remedy:
  `[f]ix [s]kip [d]etails [o]pen [a]ll safe [q]uit`. A `confirm` remedy shows its
  preview and asks again before applying. A `manual` remedy lists numbered steps
  and waits on `[c]heck again`, which re-runs only that check. The session ends
  with fixed, still present, skipped, not attempted and needs-you counts; the exit
  code is the post-repair state. Ctrl-C stops between remedies, never during one.
- **List report.** With `--no-interactive`, `--json`, or no terminal, doctor never
  prompts and never reads stdin. Findings are grouped by severity, each with its
  code, check, title, cause, evidence and preferred fix, followed by one line per
  check. Output honours `NO_COLOR` and `TERM=dumb`; every state carries a text
  label (`BLOCKER`, `WARNING`, `FIXED`), so color is never the only signal.

### Flags

| Flag                   | Effect                                                                    |
| ---------------------- | ------------------------------------------------------------------------- |
| `--fix`                | Apply remedies: every `auto`, `confirm` only with `--yes` or consent      |
| `--yes`                | With `--fix`, consent to every `confirm` remedy                           |
| `--dry-run`            | Print every remedy's preview; change nothing, write no fix log            |
| `--only <code\|check>` | Run only the checks owning these codes or check IDs, plus dependencies    |
| `--severity <list>`    | With `--fix`/`--dry-run`, act only on findings of these severities        |
| `--history`            | Print the fix log; takes no other remedy flag                             |
| `--json`               | JSON v2 (with `--fix`, the fix report; with `--history`, the log entries) |
| `--no-interactive`     | The list report, even on a terminal                                       |
| `--adapters <list>`    | Also probe these adapters (`all` for every one); see `NGD100`–`NGD111`    |

`--only` without `--fix` is the "check again" step every manual remedy names:
`nightgauge doctor --only NGD036` re-runs just the check that owns `NGD036`. An
unknown code or check ID is an error, never a clean pass. `--yes` and `--severity`
need `--fix` or `--dry-run`. `--fix` on a terminal asks before each `confirm`
remedy; without a terminal and without `--yes` it applies only `auto` remedies.

`nightgauge doctor automation pause <id> --reason "<why>"` records that a stopped
scheduled automation is paused on purpose (its finding becomes `info`, see
[NGD029](#ngd029)); `nightgauge doctor automation resume <id>` clears it. The
record is this checkout's `doctor/automation-pauses.json`
(`nightgauge layout path checkout doctor/automation-pauses.json`).

### `--history`

Each remedy the engine attempted is appended to
`<state dir>/doctor/fix-log.jsonl`. The state directory is
`$NIGHTGAUGE_STATE_HOME`, else `$XDG_STATE_HOME/nightgauge`, else the platform
default (`~/.local/state/nightgauge` on Linux, `~/.nightgauge/state` on macOS,
`%LOCALAPPDATA%\nightgauge\state` on Windows). The directory is 0700, the file
0600, and a symlink in place of either is refused. An entry holds `time`, `code`,
`check`, `fingerprint`, `remedy`, `verb`, `outcome` and a redacted `detail`, never
evidence values. `nightgauge doctor --history` prints it:

```text
Fix log: <state dir>/doctor/fix-log.jsonl
2026-09-29T10:12:03Z  FIXED          NGD017  worktree.sweep        <fingerprint>  verified: worktree_leaks no longer reports it
2026-09-29T10:12:04Z  BLOCKED        NGD018  branch.delete         <fingerprint>  precondition failed at apply time, nothing was done: branch <branch> moved since the scan
```

## Remedies

Each remedy has a kind:

- **`auto`**: safe, and reversible or idempotent. `--fix` applies it without a
  prompt.
- **`confirm`**: destructive or visible to others (deletes a branch, touches
  GitHub, starts a process). Needs `--yes`, interactive consent, `confirm: true`
  over IPC, or a card option.
- **`manual`**: only you can act. It carries numbered steps and, where useful,
  links (opened only on the [allowlisted hosts](#security)).

A remedy's action is a **verb** in a closed Go registry. It is never a shell
string, and nothing read at run time adds one. For each consented remedy the engine
runs, in order:

1. **Preview**: exactly what `--dry-run` prints.
2. **Precondition**: re-derives, immediately before acting, the claim the scan
   made (the branch still points at the scanned tip, the pid still runs the scanned
   command, the model file is still absent). If the world changed, the remedy is
   `blocked` (exit 4) and nothing is done; if acting would overwrite, `conflict`
   (exit 3).
3. **Apply**.
4. **Verify**: the owning check re-runs. The outcome is `fixed` only when the
   finding's fingerprint is gone; otherwise `still-present`, with the check's new
   evidence. Apply's own return value is never reported as success.

Outcomes are `fixed`, `still-present`, `skipped` (no consent, or manual),
`blocked`, `conflict`, and `stale` (IPC and cards only: the fingerprint is not in
the current scan).

| Verb                  | Used by                | What it does                                                     |
| --------------------- | ---------------------- | ---------------------------------------------------------------- |
| `worktree.sweep`      | NGD017                 | Removes only this finding's worktree, honouring live runs        |
| `stash.sweep`         | NGD019                 | Restores only the named stash onto its landed branch, clean tree |
| `branch.delete`       | NGD018                 | Compare-and-delete at the scanned tip                            |
| `wip.prune`           | NGD020                 | Prunes a preserved-WIP ref whose content has landed              |
| `process.terminate`   | NGD021                 | SIGTERM to the single pid, re-verified as your nightgauge binary |
| `serve_lease.reclaim` | NGD022                 | Stops the wedged pid holding the scheduler lease                 |
| `compose.cleanup`     | NGD016                 | Tears down the orphaned compose stack                            |
| `outcome.init`        | NGD014, NGD033         | Writes the baseline complexity model (0600, directory 0700)      |
| `survival.sweep`      | NGD026                 | Finalizes due survival records                                   |
| `binary.build_cli`    | NGD001                 | Runs `make build-cli` in the source checkout                     |
| `github.auth_refresh` | NGD006, NGD034, NGD041 | `gh auth refresh -h github.com -s <scopes>`                      |
| `automation.restart`  | NGD029                 | Starts the autonomous scheduler through the running daemon       |
| `layout.migrate`      | NGD044                 | Moves per-clone and machine state to ADR-024; never overwrites   |

`repo.init` (NGD010, NGD011) is declared but deliberately not registered: the
command is interactive, so `--fix` reports it `blocked` and names the command to
run by hand.

## VS Code

- **Doctor panel.** `Nightgauge: Run Doctor` (`nightgauge.runDoctor`) runs every
  check over the daemon's `doctor.run`, shows each check's progress as it finishes,
  and renders one card per finding, grouped by severity: the code (linked to its
  section here), title, cause and evidence. A card's buttons follow its fix:
  **Fix** applies an `auto` remedy, **Review & fix** shows the preview and asks
  before applying a `confirm` remedy, and **Open** and **Check again** serve
  `manual` remedies. After a fix the card shows the verified outcome without
  reloading the panel. Housekeeping is collapsed, with **Fix all safe**. Adapter
  health is the panel's Adapters group. The panel replaces the former Adapter
  Doctor command.
- **Status bar item.** `Nightgauge: ✓ healthy`, `N warnings` or `N blockers`;
  click it to open the Doctor panel. It follows every doctor result and rescans in
  the background at most every ten minutes, without probing adapters.
- **Action Center cards.** The attention sweep raises one standing card per
  `blocker` or `warning` finding, keyed by its fingerprint; housekeeping and info
  raise none. `auto` and `confirm` remedies are card options; `manual` remedies are
  the card's steps. A card resolves when a later sweep no longer reports its
  fingerprint.

The panel and cards use the daemon's IPC methods: `doctor.run` (with
`doctor.progress` notifications), `doctor.applyRemedy` (takes only a fingerprint
and a remedy ID; a `confirm` remedy needs `confirm: true`), `doctor.recheck` and
`doctor.history`. They drive the same engine as `nightgauge doctor --fix`.

## JSON v2

`nightgauge doctor --json`:

```json
{
  "v": 2,
  "findings": [
    {
      "code": "NGD033",
      "check": "complexity_model",
      "severity": "info",
      "title": "complexity model not yet created at <git-dir>/nightgauge-worktree/complexity-model.yaml",
      "cause": "the model is per-checkout learned state; ...",
      "evidence": { "path": "<git-dir>/nightgauge-worktree/complexity-model.yaml" },
      "docs": "docs/DOCTOR.md#ngd033",
      "fingerprint": "<16 hex characters>",
      "remedies": [
        {
          "id": "init",
          "kind": "confirm",
          "summary": "Write the deterministic baseline now (`nightgauge outcome init`)",
          "preview": "write the deterministic baseline model to <git-dir>/nightgauge-worktree/complexity-model.yaml",
          "verb": "outcome.init",
          "reversible": false,
          "verify": "complexity_model"
        }
      ]
    }
  ],
  "summary": { "blocker": 0, "warning": 0, "housekeeping": 0, "info": 1 },
  "healthy": true,
  "exit_code": 0,
  "failed_checks": [],
  "errors": [],
  "warnings": [],
  "install_instructions": ""
}
```

- `fingerprint` is `sha256(code + NUL + identity keys)`, truncated to 16 hex
  characters. Identity keys name the object (a path, a branch, a repository),
  never a count or timestamp, so the same problem has the same fingerprint on every
  run and every surface.
- `remedies` may be empty; the first is preferred. A manual remedy also carries
  `steps` and may carry `links`.
- `healthy` is `exit_code < 2`. `failed_checks` and `errors` come from blockers,
  `warnings` from warnings; housekeeping and info feed neither. These derived
  fields keep `skills/_shared/PREFLIGHT.md`'s jq expressions working.
- `adapters` (per-adapter health rows) appears only with `--adapters`.

`nightgauge doctor --fix --json` (and `--dry-run --json`) prints the fix report:
`v`, `dry_run`, `results[]`, `counts`, `doctor` (the post-verification result
above) and `exit_code`. Each result has `finding`, `remedy`, `action` (`applied`,
`previewed`, `awaiting-consent`, `manual`, `no-remedy`), `outcome`, `preview`,
`detail`, and `evidence` when the finding is still present. `counts` has `fixed`,
`still_present`, `blocked`, `conflict`, `stale`, `previewed`, `awaiting_consent`,
`manual` and `no_remedy`. `nightgauge doctor --history --json` prints the fix log
entries as an array.

## Security

- **Redaction.** Every surface (human, JSON, IPC, the panel, cards, the fix log)
  passes a finding's title, cause, evidence and remedy text through the shared
  redactor. A credential-shaped value (a GitHub token, a license key) prints as
  `[REDACTED]`, and an evidence key that names a secret is replaced whole.
  Credential findings carry only a redacted prefix and the key name.
- **No shell.** Remedies execute only registered Go verbs. IPC and cards accept
  only `(fingerprint, remedyId)`; no parameter carries a verb, command or path.
- **Path confinement.** File-writing remedies write only inside the workspace and
  the state directory, never through a symlink; files are 0600, directories 0700.
- **Links.** A manual remedy's link opens only for `https` URLs on `github.com`,
  `docs.github.com` and `nightgauge.dev`, passed to the OS opener as one argument.
  Any other link renders as text.

## Finding codes

A code is assigned once per distinct condition and never reused or renumbered;
one check may own several codes. Each section names the owning check, the
severity, the cause and the remedies. Use `nightgauge doctor --only <code>` to
re-run just that check.

### Any check

#### NGD000

**Check could not complete.** Any check · `warning` · no remedy.

The check timed out (each has a deadline, 10s unless it declares its own),
was cancelled, or failed internally. Its condition is unknown, so it is never
reported as passing. Re-run doctor; if it repeats, the evidence names the reason.

### Binary, skills and GitHub

#### NGD001

**The nightgauge binary is missing or stale where the hooks look.** `binary` ·
`blocker` when missing, `warning` when stale.

The hooks resolve `nightgauge` from the current directory in this order:
`$NIGHTGAUGE_BIN`, `PATH`, `<repo root>/bin/nightgauge`, the canonical checkout's
`bin/nightgauge` (worktrees), the VS Code extension bundle VS Code records as
installed, then `~/go/bin/nightgauge`. The result is cwd-dependent, so the same
hook runs different binaries in different repos; the evidence names the resolved
path, the resolving step and the binary's version.

- **Not found:** manual `install` (`go install` or the releases page).
- **Stale** (the resolved binary reports a different version than the recorded
  install): inside a nightgauge source checkout, `auto` `build` runs
  `make build-cli`; elsewhere, manual `reinstall`. An unversioned build (`dev`)
  cannot be compared and is only noted; so is a mismatch while
  `NIGHTGAUGE_BINARY_ISOLATED=1` declares the PATH binary deliberately isolated.

Which bundle counts as installed is VS Code's own record, not the highest version
number: see [GO_BINARY.md](GO_BINARY.md#which-bundle-the-hooks-run--vscodes-record-not-the-biggest-number).

#### NGD002

**The rendered skills tree was not found.** `skills` · `blocker`.

This binary cannot locate `SKILL.md` for the listed stages, so a run needing them
fails before dispatch. Manual `install`: install the bundled skills tree beside
the binary (`<prefix>/bin` next to `<prefix>/skills/`) or point the skills-root
environment variable the finding names at one.

#### NGD003

**`gh` CLI not found.** `gh` · `blocker`.

Operations that shell out to `gh` cannot run. Manual `install`: install the GitHub
CLI, then `gh auth login` or set `GH_TOKEN`.

#### NGD004

**No valid GitHub credential.** `github_auth` · `blocker`.

No GitHub client could be created, or the token was rejected. Manual `login`:
`gh auth login`, set `GITHUB_TOKEN`, or configure `github_auth.app`.

#### NGD005

**Authenticated user unresolved.** `api_user` · `warning`.

`GET /user` returned no login, so actions cannot be attributed. Manual `login`.

#### NGD006

**Required OAuth scopes missing.** `scopes` · `blocker` (`warning` when the scopes
cannot be read).

The classic token lacks `repo`, `project` or another scope pipeline operations
need. `confirm` `gh-refresh` runs `gh auth refresh -h github.com -s <scopes>` for
the account doctor checked (it opens GitHub in a browser); or manual `token`:
issue a token with the missing scopes.

#### NGD007

**GitHub API rate limit low.** `rate_limit` · `warning`.

Fewer than 500 requests remain (critically low under 100); long runs may exhaust
the quota. Manual `wait`: wait for the reset, and find the heavy caller with
`nightgauge api-usage --since 1h --by op`.

#### NGD008

**GitHub API budget near exhaustion.** `github_api_budget` · `warning`.

From the local API ledger: a quota hit zero recently (which presents as an idle
queue), or the projected GraphQL points per hour are past the threshold. Manual
`inspect`: find the repeating caller with `nightgauge api-usage`; reduce polling
or wait.

#### NGD009

**Which identity pipeline traffic is billed to.** `github_identity` · `info` · no
remedy.

Names the personal token or GitHub App that authenticates pipeline traffic and
its GraphQL ceiling. It is always reported, as context.

#### NGD034

**`read:org` missing.** `scopes` · `warning`.

Private organisation memberships are invisible to the token, so discovery may be
incomplete. `confirm` `gh-refresh` adds `read:org`.

#### NGD035

**App commit identity unset.** `github_identity` · `warning`.

`github_auth.app` has no `slug` or `bot_user_id`, so pipeline commits keep the
generic author. Manual `configure`: set both.

#### NGD036

**The GitHub App lacks a required permission.** `github_identity` · `blocker`
(Contents, Issues, Pull requests, organization Projects) or `warning` (Checks,
Actions).

The App's own declaration lacks the permission, so no installation can grant it.
Manual `app-permission`: open the App's permission settings (linked), set the
level, have an owner accept the change, then check again.

#### NGD037

**A permission is pending acceptance.** `github_identity` · same severity as
NGD036.

The App declares the permission but the installation has not accepted it. Manual
`accept`: an owner accepts it on the installation's page (linked); check again.

#### NGD038

**The App installation is suspended.** `github_identity` · `blocker`.

A suspended installation cannot mint tokens, so traffic falls back to a personal
token or fails. Manual `unsuspend`.

#### NGD039

**App permissions unverifiable.** `github_identity` · `warning`.

The App is configured but unusable, its installation was not found, GitHub
refused its JWT, or its permissions could not be read. Manual `investigate`; the
steps depend on which, then check again.

### Configuration and the project board

#### NGD010

**`.nightgauge/config.yaml` invalid or absent.** `config` · `blocker`.

A configuration file exists but was refused (for example a plaintext token), or
this repository was never onboarded (only built-in defaults or a user-global
file loaded). Refused: manual `fix` (correct the file, move credentials out) and
`confirm` `repo-init`. Absent: `confirm` `repo-init`, which `--fix` reports
`blocked` because `nightgauge repo-init` is interactive; run it by hand.

#### NGD011

**Project owner or number missing.** `project` · `blocker`.

The scheduler has no board to poll. `confirm` `repo-init` (run by hand, as for
NGD010).

#### NGD012

**Workspace manifest and runtime config name different boards.**
`project_mapping` · `blocker` (`warning` when a repo cannot be cross-checked).

The scheduler would poll a board the workspace manifest does not name. Manual
`align`: make the workspace manifest and `.nightgauge/config.yaml` agree; check
with `nightgauge project resolve --json`.

#### NGD013

**The board read failed or holds none of the repo's issues.** `board_population` ·
`blocker`.

The configured project could not be read, its items could not be read, or it
holds zero of the repository's open issues (config agreement is not
reachability). Manual `access` (confirm the project exists and the credential can
read it) or `align` (set `project_number`, or add the issues to the board). A
"Could not resolve to a ProjectV2" failure is diagnosed further as NGD040–NGD042.

#### NGD040

**No such project; the owner's projects are listed.** `board_population` ·
`blocker`.

The number names no project this identity can see, and it can see the owner's
other projects. Manual `renumber`: point `project_number` at one of them.

#### NGD041

**The identity cannot see the owner's projects.** `board_population` · `blocker`.

GitHub answers a project the identity may not see as if it did not exist, so the
cause is access, not the number. The remedy follows the reason the evidence
names: a suspended installation (`unsuspend`), an App lacking Projects
(`app-permission`), a pending permission (`accept`), a token lacking `project`
(`confirm` `gh-refresh`, or `token`), or nothing visible (`access`).

#### NGD042

**The project was deleted or is not shared.** `board_population` · `blocker`.

Project numbers are never reused, and the owner has projects numbered above this
one. Manual `renumber`.

### Learning and scheduled automations

#### NGD014

**Complexity model invalid or unsafe.** `complexity_model` · `warning`.

Complexity routing reads this checkout's `complexity-model.yaml`
(`nightgauge layout path checkout complexity-model.yaml`) and falls back
blindly while it cannot be parsed. Invalid: `confirm` `init` moves the invalid
file aside and writes the deterministic baseline (`nightgauge outcome init`). A
symlink or non-regular path is never written through: manual `replace`.

#### NGD033

**Complexity model not yet created.** `complexity_model` · `info`.

The model is per-checkout learned state, bootstrapped on the first outcome record.
`confirm` `init` writes the baseline now; the preview names the file.

#### NGD026

**Survival records pending past twice the window.** `survival_backlog` · `info`.

Nothing has finalized them, and past twice the window they fold to "unobserved".
`auto` `sweep` runs `nightgauge survival sweep`.

#### NGD027

**Survival capture never fired.** `survival_coverage` · `info`.

Runs are recorded but the post-merge capture never wrote a survival record, so
there is no ground truth. Manual `explain`: `nightgauge learn report` names the
gap; the post-merge hook must run with `--pr`.

#### NGD028

**No measurable corpus calibration pairs.** `corpus_calibration` · `info`.

No corpus row carries both a predicted and an actual model. Manual `explain`:
`nightgauge learn report`, and check that the issue's record is found at
recording time.

#### NGD029

**A scheduled automation stopped.** `scheduled_automations` · `warning`; `info`
while a pause is recorded.

It ran before and has since stopped producing evidence, usually a process that
died or a credential that expired. The evidence carries the last run and the
expected interval.

- **Autonomous loop:** while a daemon serving this workspace has a scheduler
  attached, `confirm` `restart` asks it to start the scheduler, the same call as
  `nightgauge autonomous start` and the extension's Start. It then waits up to
  30 seconds for a scan, and verification decides. Doctor never spawns a daemon.
  With no daemon listening, the remedy is manual: start one (`nightgauge serve`
  or the extension) and run `nightgauge autonomous start`, or run
  `nightgauge autonomous run`.
- **Scheduled workflow:** manual `restart`. Only GitHub's scheduler starts it:
  check whether GitHub disabled it (`gh workflow view <file>`,
  `gh workflow enable <file>`) and that its cron is on the default branch.
- Manual `pause`: `nightgauge doctor automation pause <id> --reason "<why>"`
  records the decision; the finding stays visible as `info` until
  `nightgauge doctor automation resume <id>`.

#### NGD030

**A scheduled automation has never run.** `scheduled_automations` · `warning`.

No evidence of a run has ever existed, usually a schedule that was never valid (a
cron on a branch GitHub does not schedule from). Manual `schedule`: make the
schedule valid, then confirm one run.

#### NGD031

**A scheduled automation is unverifiable.** `scheduled_automations` · `warning`.

Its status could not be read (GitHub unreachable, the autonomous state file
unreadable), or an `automations.cadence` entry in the config is malformed. Manual
`probe` or `fix-config`.

### Leaked state (housekeeping)

#### NGD016

**Orphaned docker compose project.** `compose_orphans` · `housekeeping`.

No worktree for its issue exists in any workspace repo, so nothing owns the
stack. `confirm` `cleanup` tears it down.

#### NGD017

**Leaked pipeline worktree.** `worktree_leaks` · `housekeeping`.

Reclaimable (its branch carries nothing the default branch lacks): `auto` `sweep`
removes only this worktree, and never one a live run holds. Stale (uncommitted
changes, or unlanded commits with no merged PR): manual `inspect`, with the paths
that blocked the sweep.

#### NGD018

**Stranded branch held by no worktree.** `stranded_branches` · `housekeeping`.

Merged: `confirm` `delete` deletes it only if it still points at the scanned tip.
Unmerged (unique commits and no merged PR): manual `review`, with
`scripts/branch-merged-check.sh`.

#### NGD019

**Pipeline stash never restored.** `pipeline_stashes` · `housekeeping`.

A stage stashed work (message `nightgauge:<purpose>:<issue>:<stage>`) and was
killed before restoring it. On a landed branch, `auto` `sweep` restores only this
stash, onto a clean tree. Otherwise manual `review`: inspect it and restore it on
its branch with `nightgauge stash sweep --issue <n>`. A stash without the marker is
never touched.

#### NGD020

**Preserved work from a killed stage.** `preserved_wip` · `housekeeping`.

The WIP ref is the only anchor for that work. `confirm` `prune` removes it once
its content has landed; manual `salvage` lists it with `nightgauge wip list`.

#### NGD021

**Orphaned nightgauge process.** `orphaned_processes` · `housekeeping`.

No live sidecar claims the pid, it has run past its age floor, and it has no live
child. `confirm` `terminate` sends SIGTERM to that pid only, after re-checking it
is still your nightgauge binary running the scanned command.

#### NGD032

**A foreign process holds a worktree's directory.** `orphaned_processes` ·
`housekeeping`.

A process that is not a nightgauge binary has its working directory inside a
pipeline worktree, which can block the worktree's removal. Manual `verify`:
confirm what it is and close the session that owns it.

#### NGD022

**A stale serve lease blocks `serve`.** `serve_lease` · `warning`.

The pid holding this checkout's scheduler lease (`serve.lock`, at
`nightgauge layout path checkout serve.lock`) is running but has stopped
heartbeating, so every `nightgauge serve` and `nightgauge autonomous run` here is
refused. `confirm` `reclaim` stops that pid.

#### NGD023

**A daemon's GitHub calls are missing from the ledger.** `ledger_daemon_coverage`
· `warning`.

A daemon serves this workspace and a pipeline ran recently, but none of its calls
reached the ledger. Manual `restart`: restart it with
`nightgauge serve --workspace <root>`.

#### NGD043

**A log directory is over its size cap.** `log_retention` · `housekeeping` or
`warning`.

The check's detail line always reports each log directory's size (the clone's
logs directory, `nightgauge layout path logs`, and the machine state `logs/`)
and the caps in effect: machine-tier `pipeline.logs.max_size_mb` (default 200)
and `pipeline.logs.max_age_days` (default 30). Retention runs at `nightgauge serve`
start, daily while it runs, and at CLI start once a day. Over the cap with files
retention may delete (`housekeeping`): manual `prune`, run
`nightgauge logs prune`. Over the cap with only live files, files written in the
last hour, or files of a run that is not terminal (`warning`): manual `review`,
finish or cancel the runs the evidence names. A log directory that is a symlink
is refused (`warning`): manual `replace`.

#### NGD044

**Per-clone data at an old location.** `layout_migration` · `housekeeping`.

[ADR-024](decisions/024-data-and-state-layout.md) moves per-clone data out of the
working tree, and this build reads each class only at its new location. One
finding per class names the old directory and the exact target: pipeline state,
plans, retros and logs (to the per-clone directory under the git common dir), each
pipeline worktree under the pre-ADR-024 `.nightgauge/worktrees/` or `.worktrees/` (to the worktree
base), and the old recall cache. `auto` `migrate` runs the one migration for the
clone:

- It takes `.migrate.lock` in the per-clone directory and re-scans under it.
- A live daemon of any checkout of the clone blocks it (exit 4). While a run is in
  flight (the in-flight detection `worktree sweep` uses) no file is moved, and the
  worktree that run is on is skipped and reported (exit 4); idle worktrees still
  move, with `git worktree move`.
- Each file moves by a rename on one filesystem, or a synced copy then a delete
  across filesystems, keeping its mode and mtime. A symlink is recreated as a
  symlink, never followed. A target with the same bytes means an earlier move
  finished; the source is deleted. Append-only JSONL (pipeline history,
  `github-api.jsonl`) found at both locations, or left by more than one checkout,
  is merged as the union of its lines, ordered by timestamp. Files git tracks (the template's `.gitkeep`) stay, so
  `git status` is unchanged.
- The old recall cache is deleted, not moved: it rebuilds on next use.
- New directories are created 0700. When nothing is left at an old location, it
  writes the `layout-version` marker last. A second pass changes nothing, and the
  check's detail line reads `layout v1`.

The same migration also runs automatically (ADR-024 § 15): the first
`nightgauge` command on a clone without a current `layout-version` marker runs it
before doing anything else. Once the marker is in place, that costs one file read.
`doctor`, `layout`, `version`, `help`, completion, `hook`, `pre-push` and any
`--dry-run` never trigger it. The automatic run never fails or blocks the command:
a conflict, a run in flight or a live daemon leaves the data where it is, prints
one line to stderr naming `nightgauge doctor --fix`, and the clone is not rescanned
for an hour. A run that moved data prints one line to stderr too.

**Machine state** is reported by the same check, wherever doctor runs: each
class still under `~/.nightgauge` (serve claims, `rate-limit.json`, the GitLab
rate-limit files, `machine-id`, `telemetry-notice-v1`, `usage/`, OpenCode's
`runs/`, `evidence/`, `last-dispatch.json` and `endpoint-slots.json`, and the
machine `logs/`) with its target in the machine-state directory (`nightgauge
layout` prints it as `state`). `migrate` moves them under `.migrate.lock` in that
directory with the mechanics above and writes its own `layout-version` marker:

- `machine-id` is moved byte for byte, ends mode 0600 and is never regenerated.
  The old copy is saved as `~/.nightgauge/machine-id.migrated-<UTC time>` before
  it is removed. A `machine-id` in the default machine-state directory that
  differs from the old one is a conflict (NGD045) even when the machine-state
  directory in use is another one.
- Nothing moves, and nothing is deleted, into a machine-state directory that is
  not where this user's state belongs (NGD046, with the reason): one set by
  `NIGHTGAUGE_STATE_HOME` or `XDG_STATE_HOME` while the default machine-state
  directory for this home exists, or one under the temporary directory when
  `~/.nightgauge` is not. Unset the override to migrate.
- A hint found at both locations (the rate-limit files, the telemetry notice, the
  usage readings, the last-dispatch record, the endpoint slots, a serve claim)
  keeps the copy in the state directory and deletes the old one.
- While a daemon holds a serve lease (a held lock in `~/.nightgauge/serve/`), the
  serve claims and the OpenCode run roots stay and are reported (exit 4); the
  other classes still move.
- The old OpenCode self-test records are deleted; nothing reads them.
- On Linux only, with neither `NIGHTGAUGE_CONFIG_HOME` nor `XDG_CONFIG_HOME` set,
  a legacy `~/.nightgauge/config.yaml` moves to `~/.config/nightgauge/config.yaml`
  (mode 0600, directory 0700). The loader no longer reads the old file. Two
  differing files are a conflict and are never merged, and no finding shows
  either file's content. On macOS `~/.nightgauge/config.yaml` is the machine
  config and never moves; `tools/` never moves.

The same machine-state migration runs automatically at CLI start, under the
same never-fail rule. With `NIGHTGAUGE_STATE_HOME` or `XDG_STATE_HOME` set, the
automatic run only reports what is left; `nightgauge doctor --fix` moves it.

The finding never changes the exit code of plain `nightgauge doctor`.

#### NGD045

**A file exists at both the old and the new location.** `layout_migration` ·
`housekeeping`.

The two copies differ, and the migration never overwrites a file, so it moves
nothing in the clone (or, for machine state such as `machine-id`, nothing in the
machine-state directory) until every conflict is resolved; `--fix` exits 3. Manual
`resolve`: compare the two paths the finding names, keep the one you want at the
new location, delete the other, and re-run `nightgauge doctor --fix`.

An append-only log is never a conflict. That covers pipeline history,
`github-api.jsonl` and the daemon log, including when the main checkout and a
linked worktree each left a copy for the same target: the copies merge as the
union of their lines.

**`machine-id`.** Two different ids are the one conflict with its own command,
because the choice decides which device the platform sees. Each id is a device
the platform may already know, and an id it has not seen is a new device,
counted against the account's machine limit. Doctor therefore never picks a side
and never generates an id. The finding shows both ids (`legacy_id`,
`target_id`) and when each file was last modified (`legacy_modified`,
`target_modified`). `in_use` names the id the running binary uses: the one in the
machine-state directory, unless `NIGHTGAUGE_AGENT_ID` overrides both. The finding
also gives the exact command for each choice (`keep_state`, `keep_legacy`):

```bash
nightgauge doctor resolve machine-id --keep state   # keep the id this build uses
nightgauge doctor resolve machine-id --keep legacy  # keep the id in ~/.nightgauge
```

Keep the id the platform already lists for this machine. It is usually the one
an older build registered and used longest. The command:

- installs the chosen id at `<state>/machine-id` byte for byte, mode 0600;
- saves the other id beside it as `<state>/machine-id.replaced-<UTC time>`
  (mode 0600). The file is never deleted, and no migration or lookup reads it;
- removes `~/.nightgauge/machine-id`, so `nightgauge doctor --fix` moves the rest
  of the machine state and a second run changes nothing.

`--keep` is required. Without a conflict, or with an empty chosen id, the command
changes nothing. It runs under the machine-state migration lock, and `--json`
prints what it did.

#### NGD046

**An old location cannot be migrated safely.** `layout_migration` ·
`housekeeping`.

The old directory is a symlink, resolves outside the checkout (for machine state,
outside `~/.nightgauge` or the machine-state directory), or its new location
cannot be resolved (for example, `pipeline.worktree_base` set in the committed team
config). Nothing there is moved or deleted; the other classes still migrate.
Manual `inspect`: remove the reason the evidence names, then re-run
`nightgauge doctor --fix`.

### Credentials

#### NGD024

**A credential is committed under `.nightgauge/`.** `tracked_secrets` · `blocker`
(`warning` when the scan cannot run).

Treat it as leaked. Manual `rotate`: rotate it at the issuer, stop tracking the
file (`git rm --cached <path>`), and remove it from history. The finding names the
file, line and pattern, with the value redacted to its prefix.

#### NGD025

**A machine-tier credential on a CI host.** `ci_machine_credentials` · `blocker`
(`warning` when the scan cannot run).

On a runner shared between jobs every later job can read it. Manual `rotate`:
remove the keys from the machine file, pass credentials through the job
environment, and rotate them. Only key names are reported.

### Adapters

#### NGD015

**No usable AI coding agent.** `ai_adapter` · `warning`.

No adapter answered the availability probe, so no stage can run. Manual
`install`: install or configure an agent, then
`nightgauge doctor --adapters all` for per-adapter detail. It is a warning, never
a blocker: preflight runs inside an agent session, so it can only fire on a probe
false negative, and a blocker would halt a working run.

The adapter codes below are reported only with `--adapters`; each adapter that is
not usable is one finding, and none is a blocker.

#### NGD100

**Adapter CLI not on `PATH`.** `adapters` · `warning`. Manual `install`: the
install and login commands for that CLI.

#### NGD101

**Adapter below its version floor.** `adapters` · `warning`; `info` while still
usable. Manual `update` to the floor or later.

#### NGD102

**SDK adapter API key unset.** `adapters` · `warning`. Manual `set-key`: names the
variable to set where the pipeline runs, never a value.

#### NGD103

**Unknown or retired adapter name.** `adapters` · `warning`. Manual `rename`: pass
one of the adapters doctor knows.

#### NGD104

**Compat manifests failed to load.** `adapters` · `warning`. Version floors are
not enforced. Manual `reinstall`.

#### NGD105

**A registry-served model is missing from the CLI's catalog.** `adapters` ·
`warning`. The registry declares a model CLI-served that the live catalog does not
list. Manual `reconcile`.

#### NGD106

**The provider rejected the probed model.** `adapters` · `warning` (the adapter
stays usable). A zero-data-retention organization is barred from Covered Models:
manual `retention` (enable retention, or pin a non-Covered model for that band).
Otherwise manual `confirm-model`.

#### NGD107

**OpenCode experimental gate closed.** `adapters` · `warning`. Manual `enable`:
set `NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1`.

#### NGD108

**OpenCode machine-tier `opencode:` block refused.** `adapters` · `warning`.
Manual `fix-config` in `~/.nightgauge/config.yaml`.

#### NGD109

**OpenCode `opencode.binary` pin refused.** `adapters` · `warning`. Manual
`fix-pin`: an absolute executable path, or remove the pin.

#### NGD110

**A dispatch would be refused for another reason.** `adapters` · `warning`.
Manual `resolve`: the cause names the refusal.

#### NGD111

**Usable, with warnings.** `adapters` · `warning`. Manual `review`: act on the
warnings the evidence lists.

## Adapter health in detail

`nightgauge doctor --adapters codex,claude --json` adds an `adapters[]` array of
per-adapter rows beside the findings; the row schema is in
[GO_BINARY.md](GO_BINARY.md#per-adapter-health---adapters).

- **Catalog drift (`cli` adapters with a catalog probe).** Doctor diffs the CLI's
  live model catalog against the registry's served models (NGD105). A model the
  CLI offers but the registry does not mark served is a warning only; a CLI that
  is missing, unauthenticated or unparseable degrades the probe to
  `catalog_warning`. Adapters with no models-listing command say so in
  `catalog_warning`.
- **Model validity.** For `claude`, one cheap request against the current leader
  of the `fable` band distinguishes a retention rejection (NGD106 `retention`)
  from other rejections. It never fails the adapter and runs only under
  `--adapters`.
- **OpenCode.** The row carries a nested `opencode` object: `enabled` (the
  experimental gate, checked first), the version policy (`min_version`,
  `opencode.max_tested`, `opencode.floor_policy`), `opencode.pinned`,
  `opencode.last_dispatch_version`, `catalog`, per-endpoint readiness and loaded
  context (`opencode.endpoints[]`, which name an endpoint by id, never its
  address), the run-scoped directories (`opencode.dirs`), the configured offline
  posture (`opencode.offline`), and stored logins by source and type only
  (`opencode.stored_logins[]`). `warnings` degrade the verdict; `notes` never do.
  The design is [ADR-022](decisions/022-opencode-multi-provider-adapter.md).

To add an adapter to doctor, add it to `adapterSpecs` in
`internal/doctor/adapters.go` with its kind and requirements; a CLI adapter also
gets a compat manifest (`internal/adaptercompat/manifests/<adapter>.json`) for
its version floor. Keep an experimental gate's check first. Chat-only adapters
declare `agentic: false` and are refused by pipeline dispatch; see
[ADAPTER_GUIDE.md](ADAPTER_GUIDE.md).
