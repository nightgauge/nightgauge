# Hook Contract

This document defines the contract for deterministic operations in the
Nightgauge pipeline. These operations are now implemented directly in the
`nightgauge` Go binary — there are no intermediate shell script wrappers.

## Migration complete

Shell scripts that previously lived in
`claude-plugins/nightgauge/hooks/lib/` have been removed. The directory is
now empty. Skills invoke the `nightgauge` Go binary directly.

## Binary resolution

Skills resolve the binary using:

```bash
if [ -f "${BINARY:-}" ]; then
  BINARY="$BINARY"
elif [ -f "bin/nightgauge" ]; then
  BINARY="bin/nightgauge"
elif command -v nightgauge >/dev/null 2>&1; then
  BINARY="nightgauge"
else
  echo "ERROR: nightgauge binary not found. Build it first:" >&2
  echo "  go build -o bin/nightgauge ./cmd/nightgauge/" >&2
  exit 1
fi
```

If the binary is missing, the pipeline exits immediately with an actionable
error message and build instructions.

## Build the binary

```bash
go build -o bin/nightgauge ./cmd/nightgauge/
```

See [docs/GO_BINARY.md](GO_BINARY.md) for full Go binary documentation.

## Required commands (missing binary = pipeline aborts)

| Command                            | Purpose                                                      | Invoked by               | Arguments                                                 | Output format    |
| ---------------------------------- | ------------------------------------------------------------ | ------------------------ | --------------------------------------------------------- | ---------------- |
| `nightgauge ci wait`               | Poll GitHub CI checks for a PR until terminal state          | `CI_GATE.md`             | `<pr-number> [--timeout <seconds>] [--json]`              | JSON (see below) |
| `nightgauge hook check-deps`       | Check that required host tools are available                 | `DEPENDENCY_CHECKING.md` | `<issue-number> [--check-only] [--json]`                  | JSON (see below) |
| `nightgauge epic check-completion` | Detect whether all sub-issues of an epic are complete        | `EPIC_HANDLING.md`       | `<issue-number> [--sweep] [--check-only] [--json]`        | JSON (see below) |
| `nightgauge pr create`             | Create the epic branch → main PR when all sub-issues land    | `EPIC_HANDLING.md`       | `--title <t> --head <b> --base <b> [--body <b>] [--json]` | JSON (see below) |
| `nightgauge project move-status`   | Transition issue board status (in-progress, in-review, done) | all pipeline skills      | `<issue-number> <status> [--json]`                        | JSON (see below) |

### `nightgauge ci wait`

**Exit codes:**

| Code | Meaning                                |
| ---- | -------------------------------------- |
| 0    | All CI checks passed                   |
| 1    | One or more CI checks failed           |
| 2    | Timeout reached before terminal state  |
| 3    | Error (invalid args, binary not found) |

**Output JSON schema:**

```json
{
  "all_passed": true,
  "has_checks": true,
  "failed_count": 0,
  "pending_count": 0,
  "failures": [{ "name": "string", "conclusion": "string" }]
}
```

**Progress lines** (written to stdout during polling, for skillRunner):

```
CI_PROGRESS:{"state":"PENDING","elapsed":30,"pending":2,"completed":1}
```

### `nightgauge hook check-deps`

**Exit codes:**

| Code | Meaning                                   |
| ---- | ----------------------------------------- |
| 0    | All required dependencies available       |
| 1    | One or more required dependencies missing |
| 3    | Error (binary not found or build failed)  |

**Output JSON schema:**

```json
{
  "ok": true,
  "required": [{ "name": "git", "available": true, "version": "2.39.1" }],
  "optional": [{ "name": "node", "available": true, "version": "18.0.0" }],
  "missing": []
}
```

### `nightgauge epic check-completion`

**Arguments:**

- `<issue-number>` — Issue number to check (sub-issue or epic)
- `--sweep` — Optional flag; check all epics in the repository
- `--check-only` — Optional flag; returns status without auto-closing
- `--json` — Output JSON

**Exit codes:**

| Code | Meaning |
| ---- | ------- |
| 0    | Success |
| 1    | Error   |

**Output JSON schema (default mode, returns array):**

```json
[
  {
    "epicNumber": 42,
    "title": "Epic title",
    "complete": true,
    "total": 5,
    "closed": 5,
    "open": 0
  }
]
```

**Output JSON schema (`--check-only` mode):**

```json
[
  {
    "action": "ready-to-close",
    "epic_number": 42,
    "epic_title": "Epic title"
  }
]
```

`action` values: `"ready-to-close"` | `"not-ready"` | `"no-parent"`

### `nightgauge pr create`

**Exit codes:**

| Code | Meaning                                 |
| ---- | --------------------------------------- |
| 0    | Success (PR created or already existed) |
| 1    | Error                                   |

**Output JSON schema:**

```json
{
  "action": "created",
  "pr_url": "#123"
}
```

`action` values: `"created"` | `"already-exists"` | `"failed"`

### `nightgauge project move-status`

**Purpose:** Transitions the GitHub Project board status for an issue.

**Arguments:**

- `<issue-number>` — Issue number
- `<status>` — One of `ready`, `in-progress`, `in-review`, `done`, `blocked`,
  `needs-info`
- `--json` — Output JSON

**Exit codes:**

| Code | Meaning                                  |
| ---- | ---------------------------------------- |
| 0    | Success                                  |
| 1    | Error (invalid status, API failure, I/O) |

### `nightgauge pre-push validate`

**Purpose:** Runs pre-push merge validation gate against the merged state
(feature + target). Validates build, test, vet, security, and static checks.

**Arguments:**

- `<issue-number>` — Issue number (positional, required)
- `--target` (default: `main`) — Target branch to merge against
- `--timeout` (default: `180`) — Timeout in seconds
- `--json` — Output JSON instead of human-readable

**Exit codes:**

| Code | Meaning                                      |
| ---- | -------------------------------------------- |
| 0    | All validation phases passed                 |
| 1    | One or more phases failed (see context file) |

**Output JSON schema:**

```json
{
  "decision": "allow",
  "issue_number": 2609,
  "target_branch": "main",
  "feature_branch": "feat/2609-...",
  "validation_phases": {
    "merged_state": "passed",
    "build": "passed",
    "test": "passed",
    "vet": "passed",
    "security": "passed",
    "static_checks": "passed"
  },
  "critical_findings": 0,
  "context_path": ".nightgauge/pipeline/pre-push-2609.json",
  "started_at": "2026-04-08T00:00:00Z",
  "completed_at": "2026-04-08T00:01:30Z"
}
```

### `nightgauge pre-push install`

**Purpose:** Installs a git pre-push hook into `.git/hooks/pre-push` that calls
`nightgauge pre-push validate` before each push from a pipeline branch.

**Exit codes:**

| Code | Meaning           |
| ---- | ----------------- |
| 0    | Hook installed    |
| 1    | Error (I/O, path) |

## Optional commands (failure = logged warning, execution continues)

| Command                          | Purpose                                          | Invoked by        | When used                                |
| -------------------------------- | ------------------------------------------------ | ----------------- | ---------------------------------------- |
| `nightgauge project sync-status` | Sync the project board Status field for an issue | multiple skills   | After every significant state transition |
| `nightgauge issue close`         | Close issues and sync board status to Done       | pr-merge SKILL.md | After every PR merge                     |

### `nightgauge project sync-status`

When the command fails (e.g., board access is unavailable), a warning is logged
and execution continues. Board sync is informational and does not gate pipeline
progress.

### `nightgauge issue close`

Handles:

- Closing the merged issue via GitHub API
- Syncing the project board Status field to "Done" (does not rely on GitHub's
  built-in project automations, which are unreliable)
- In batch mode: closing all batch issues

When the command fails, the pr-merge skill logs a warning — board status may be
left in "In Progress", but the pipeline completes.

## What happens when the binary is missing

| Classification | Command missing                    | Behavior                                                  |
| -------------- | ---------------------------------- | --------------------------------------------------------- |
| **Required**   | `nightgauge ci wait`               | Pipeline exits with `exit 1` and actionable error message |
| **Required**   | `nightgauge hook check-deps`       | Pipeline exits with `exit 1` and actionable error message |
| **Required**   | `nightgauge epic check-completion` | Pipeline exits with `exit 1` and actionable error message |
| **Required**   | `nightgauge pr create`             | Pipeline exits with `exit 1` and actionable error message |
| **Required**   | `nightgauge project move-status`   | Pipeline exits with `exit 1` and actionable error message |
| **Optional**   | `nightgauge project sync-status`   | Warning logged to stderr; board sync skipped              |
| **Optional**   | `nightgauge issue close`           | Warning logged to stderr; issue closure skipped           |

**Error message format** (required commands):

```
ERROR: nightgauge binary not found.
This binary is required for [PURPOSE]. Build it first:
  go build -o bin/nightgauge ./cmd/nightgauge/
```

**Warning message format** (optional commands):

```
WARNING: nightgauge project sync-status failed. Board sync will be skipped.
```

## Go binary dependency

All pipeline operations depend on the `nightgauge` Go binary. Skills check
for the binary at `bin/nightgauge` first, then fall back to `PATH`. If
neither is available, they exit with code 1 and print build instructions.

Build the binary:

```bash
go build -o bin/nightgauge ./cmd/nightgauge/
```

See [docs/GO_BINARY.md](GO_BINARY.md) for full Go binary documentation.
