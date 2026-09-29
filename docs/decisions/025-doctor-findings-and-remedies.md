# Doctor Findings and Remedies — one coded finding, one registered fix, one verified outcome

**Date:** 2026-09-28
**Author:** nightgauge
**Status:** Decided
**Issue:** #2086 (epic #2085)
**Implemented by:** #2088 (registry, Finding model, JSON v2), #2089–#2092 (checks emit findings),
#2093 (remedy engine and `--fix`), #2094 (identity diagnosis), #2095 (interactive CLI), #2096
(IPC), #2097 (Action Center), #2098 (legacy removal), #2099 (VS Code panel), #2100 (docs)
**Consistent with:** [ADR-024](024-data-and-state-layout.md) (reserved `--fix` exit codes 3 and
4; its layout migrations become remedies) and [ADR-015](015-decision-requests.md) (the Action
Center and its verb allowlist)

---

## Context

`RunDoctorWithConfigError` in `internal/doctor/doctor.go` runs every check in sequence and
returns `CheckItem{ok, detail, error}` plus free-text warnings. A fix, when one exists, is prose
inside an error string. The overall status is one word (`degraded` or `broken`), so a leaked
worktree weighs the same as a missing token. The human renderer walks the hand-kept
`doctorCheckOrder` list in `cmd/nightgauge/main.go`, which omits three emitted checks
(`github_identity`, `project_mapping`, `board_population`); they never print.

The epic builds a registry, a remedy engine, an interactive CLI, IPC methods, a VS Code panel
and Action Center cards. All of them exchange the same objects. This ADR fixes those objects
before code is written so the Go, IPC and TypeScript sides cannot drift. A later sub-issue that
needs a field this ADR lacks amends the ADR first.

## Decision

### 1. The Finding

A check returns zero or more findings. No finding means the check passed.

```go
type Finding struct {
    Code        string            `json:"code"`        // "NGD014"; stable, never reused
    Check       string            `json:"check"`       // registry ID, e.g. "worktree_leaks"
    Severity    Severity          `json:"severity"`    // blocker|warning|housekeeping|info
    Title       string            `json:"title"`       // one line, no trailing period
    Cause       string            `json:"cause"`       // plain-language why
    Evidence    map[string]string `json:"evidence"`    // structured, redacted before render
    Docs        string            `json:"docs"`        // "docs/DOCTOR.md#ngd014"
    Fingerprint string            `json:"fingerprint"` // stable across runs
    Remedies    []Remedy          `json:"remedies"`    // may be empty; first is preferred
}
```

- **`code`** is `NGD` plus three digits, assigned once per distinct condition and never reused
  or renumbered. A retired condition keeps its code as a tombstone in `docs/DOCTOR.md`. One check
  may own several codes when it detects distinct conditions.
- **`docs`** is an anchor in `docs/DOCTOR.md` named after the code.
- **`fingerprint`** is `sha256(code + "\x00" + canonical identity keys)`, truncated to 16 hex
  characters. Identity keys are the evidence keys that name the object (a worktree path, a
  branch, a repository), never volatile values such as counts or timestamps. The same problem
  on the same object yields the same fingerprint on every run and every surface.

Rejected: keeping `CheckItem` and adding an optional `remedy` string. It keeps the free-text
contract that the epic exists to remove, and gives the Action Center no stable key.

### 2. Severity

Exactly four values:

| Severity       | Meaning                                 | Exit code effect | Action Center card |
| -------------- | --------------------------------------- | ---------------- | ------------------ |
| `blocker`      | The pipeline cannot run                 | 2                | Yes                |
| `warning`      | It runs degraded                        | 1                | Yes                |
| `housekeeping` | Cleanup; never degrades status          | None             | No                 |
| `info`         | Context, including skipped dependencies | None             | No                 |

Today's checks, as emitted by `RunDoctorWithConfigError` and the two credential checks, map as
follows. `NGD000` is reserved for "check could not complete" (timeout or internal error), always
`warning`, and applies to any check.

| Code   | Check                    | Severity     | Note                                      |
| ------ | ------------------------ | ------------ | ----------------------------------------- |
| NGD000 | (any check)              | warning      | Check could not complete                  |
| NGD001 | `binary`                 | blocker      | Missing or stale binary on `PATH`         |
| NGD002 | `skills`                 | blocker      | Rendered skills tree not found            |
| NGD003 | `gh`                     | blocker      | `gh` CLI missing                          |
| NGD004 | `github_auth`            | blocker      | No valid GitHub credential                |
| NGD005 | `api_user`               | warning      | Authenticated user unresolved             |
| NGD006 | `scopes`                 | blocker      | Required OAuth scopes missing             |
| NGD007 | `rate_limit`             | warning      | Rate limit low                            |
| NGD008 | `github_api_budget`      | warning      | Ledger budget near exhaustion             |
| NGD009 | `github_identity`        | blocker      | Never printed today                       |
| NGD010 | `config`                 | blocker      | `.nightgauge/config.yaml` invalid         |
| NGD011 | `project`                | blocker      | Project owner or number missing           |
| NGD012 | `project_mapping`        | blocker      | Never printed today                       |
| NGD013 | `board_population`       | warning      | Never printed today                       |
| NGD014 | `complexity_model`       | warning      | Remedy: `outcome init`                    |
| NGD015 | `ai_adapter`             | warning      | Zero usable adapters                      |
| NGD016 | `compose_orphans`        | housekeeping |                                           |
| NGD017 | `worktree_leaks`         | housekeeping |                                           |
| NGD018 | `stranded_branches`      | housekeeping |                                           |
| NGD019 | `pipeline_stashes`       | housekeeping |                                           |
| NGD020 | `preserved_wip`          | housekeeping |                                           |
| NGD021 | `orphaned_processes`     | housekeeping |                                           |
| NGD022 | `serve_lease`            | warning      | A stale lease blocks `serve`              |
| NGD023 | `ledger_daemon_coverage` | warning      |                                           |
| NGD024 | `tracked_secrets`        | blocker      | Credential committed under `.nightgauge/` |
| NGD025 | `ci_machine_credentials` | warning      | Machine-tier credential on a CI host      |
| NGD026 | `survival_backlog`       | info         |                                           |
| NGD027 | `survival_coverage`      | info         |                                           |
| NGD028 | `corpus_calibration`     | info         |                                           |
| NGD029 | `scheduled_automations`  | warning      |                                           |

Totals: 29 checks (the epic's "28" predates `ci_machine_credentials`): 11 blocker, 11 warning,
6 housekeeping, 3 info. The migration sub-issues (#2089–#2092) may split a check into further
codes; they append codes and never renumber. Adapter health (`--adapters`, #2092) receives codes
from `NGD100` upward.

Rejected: keeping two levels (required/optional). It is what makes housekeeping look like a
real failure today.

### 3. The Remedy

```go
type Remedy struct {
    ID         string     `json:"id"`         // unique within the finding, e.g. "remove"
    Kind       RemedyKind `json:"kind"`       // auto|confirm|manual
    Summary    string     `json:"summary"`    // one line, imperative
    Preview    string     `json:"preview"`    // exactly what --dry-run prints
    Verb       string     `json:"verb"`       // registered Go verb; empty for manual
    Reversible bool       `json:"reversible"`
    Verify     string     `json:"verify"`     // check ID re-run after apply
    Steps      []string   `json:"steps,omitempty"` // manual only
    Links      []string   `json:"links,omitempty"` // manual only; allowlisted hosts
}
```

- **`auto`**: safe, and reversible or idempotent. Applied by `--fix` without a prompt.
- **`confirm`**: destructive, or visible to others (deletes a branch, touches GitHub). Needs
  `--yes`, interactive consent, `confirm: true` over IPC, or a card option choice.
- **`manual`**: only the operator can act. Carries `steps` and `links`; `verb` is empty.
- **`verb`** names an entry in the closed Go verb registry (`internal/doctor/remedy.go`). It is
  never a shell string. An unregistered verb fails closed and is reported.
- **`verify`** names the owning check. After apply the engine re-runs it; the outcome is `fixed`
  only when the fingerprint is gone, otherwise `still-present`. Apply's own return value is never
  reported as success.

Outcomes are `fixed`, `still-present`, `skipped`, `blocked`, `conflict` and `stale` (IPC and
card only: the fingerprint no longer matches the current scan).

Rejected: remedies as command lines the operator or engine runs. They cannot be previewed,
confirmed per kind, confined, or verified, and they are an injection path.

### 4. Exit codes

| Code | Meaning                                                              |
| ---- | -------------------------------------------------------------------- |
| 0    | Healthy: no blocker or warning                                       |
| 1    | Warnings only                                                        |
| 2    | At least one blocker                                                 |
| 3    | `--fix` conflict: nothing overwritten (reserved by ADR-024, kept)    |
| 4    | `--fix` blocked: a precondition failed at apply time (ADR-024, kept) |

Housekeeping and info never change the exit code. Under `--fix` the code reflects the state
after verification; 3 and 4 take precedence over 0–2.

Rejected: a distinct code per severity count or per check. Callers branch on "can I run", and
PREFLIGHT reads fields, not codes.

### 5. JSON v2 and the migration path

```json
{
  "v": 2,
  "findings": [],
  "summary": { "blocker": 0, "warning": 0, "housekeeping": 0, "info": 0 },
  "healthy": true,
  "exit_code": 0,
  "failed_checks": [],
  "errors": [],
  "warnings": [],
  "install_instructions": ""
}
```

The top-level `healthy`, `exit_code`, `failed_checks`, `errors`, `warnings` and
`install_instructions` are **derived** from `findings[]` with unchanged meaning, so the jq
expressions in `skills/_shared/PREFLIGHT.md` (`.failed_checks[]`, `.errors[]`, `.warnings[]`,
`.install_instructions`) keep working and no skill needs an edit. `failed_checks` and `errors`
come from blockers; `warnings` from warnings; housekeeping and info feed neither.

Migration is a single cut-over: when the registry lands (#2088), `--json` emits v2 only. There
is no `--json-v1`, no version flag and no dual output. Consumers that read `checks` move to
`findings[]` in the same change or in #2098, which deletes `CheckItem`.

Rejected: a v1 compatibility mode. The project is pre-customer; PREFLIGHT is kept whole by the
derived fields, so a second format would buy nothing.

### 6. Interactive CLI library

The interactive CLI (#2095) uses **`golang.org/x/term`** (already in `go.mod`, BSD-3-Clause) for
TTY detection, raw single-key input and width, with a small in-repo renderer for the progress
view, the severity-grouped summary and the `[f]ix [s]kip [d]etails [o]pen [a]ll safe [q]uit`
prompt.

- **License:** no new dependency; nothing new for the license gate.
- **Binary size:** no new module graph.
- **`NO_COLOR`, `TERM=dumb`, non-TTY:** handled in one place; every state carries a text label
  (`BLOCKER`, `WARNING`, `FIXED`), so color and symbols are never the only signal.
- **Screen readers:** line-oriented output with no full-screen redraw or cursor-addressed
  frames. The spinner degrades to one status line per state change when `NO_COLOR` is set or
  the terminal is not interactive.

Rejected: `charmbracelet/huh` with `lipgloss` (MIT). They are well made, but they add bubbletea
and its dependency tree to a single-binary CLI for a prompt with six keys, and huh's
full-screen forms are harder for screen readers than a line prompt. Revisit only if the CLI needs
multi-field forms.

### 7. Action Center (ADR-015)

- A workspace sweep producer, `internal/attention/sweep/doctor.go`, runs the registry and raises
  one **standing card per `blocker` or `warning` finding**, keyed by `fingerprint`. A card
  resolves when a later sweep no longer produces that fingerprint. A sweep error leaves cards
  untouched.
- `auto` and `confirm` remedies become card options on two new allowlisted verbs,
  `doctor.applyRemedy` and `doctor.recheck`, taking only `(fingerprint, remedyId)`. `manual`
  remedies become the card body's steps and links.
- The duplicate producers are removed: `internal/attention/sweep/apibudget.go` (duplicates
  `github_api_budget` with a different lookback) and the doctor-duplicating half of
  `internal/attention/sweep/strandedready.go` (duplicates `board_population` and
  `project_mapping`). Each condition has one threshold, the doctor's.

Rejected: letting both producers stand. One condition would raise two cards with different
thresholds.

### 8. Security

- **Redaction.** Every surface (human, JSON, IPC, webview, card, fix log) passes `evidence`
  through the shared redactor built on `internal/config/redact.go`. Checks never place a raw
  credential in `title` or `cause`. Each surface has a test that seeds a token-shaped value.
- **No shell.** Remedies execute only registered Go verbs. No command text from config, a
  finding, an issue body or an IPC parameter is executed. IPC and card verbs accept only
  `(fingerprint, remedyId)`; no parameter carries a verb name, command or path.
- **Path confinement.** File-writing remedies resolve and symlink-check every path and write
  only inside the workspace and the state directories. Files are created 0600, directories 0700.
- **TOCTOU.** Each verb's `Precondition` runs immediately before `Apply`. If the world changed
  since the scan, the remedy reports `blocked` and does nothing. IPC and cards re-scan the
  fingerprint's check first and return `stale` on mismatch.
- **Link allowlist.** Manual-remedy links open only for `https` URLs on `github.com`,
  `docs.github.com` and `nightgauge.dev`. The URL is passed as a single argument to the OS
  opener, never through a shell. Other hosts render as text and are not opened.

Rejected: allowing any `https` link. Finding text can derive from repository content, so an
open allowlist would let a crafted repository send the operator anywhere.

## Consequences

- Every surface speaks one Finding and Remedy shape; the Go types in `internal/doctor/finding.go`
  are the source, and the IPC client is generated from them.
- `doctorCheckOrder` and `CheckItem` are deleted; the three unprinted checks render.
- Housekeeping stops turning the status "degraded", which changes exit code 1 for workspaces
  whose only findings are cleanup items.
- Codes are a public contract: `docs/DOCTOR.md` must document each code, and a code is never
  reused.
- ADR-024's layout migrations (#2040, #2041) ship as remedies on this engine, not as a second
  `--fix`.
- The link allowlist and verb registry are closed lists; adding to either is a reviewed change.
