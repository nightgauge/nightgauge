# Go Binary Architecture

The `nightgauge` CLI is a compiled Go binary that serves as the
deterministic layer of the pipeline. It replaces all shell scripts and external
CLI dependencies (gh, jq, bash 4.x) with a single, zero-dependency binary.

## Installation

Choose the installation method based on your context:

| Context                  | Recommended Method | Command                                                                                                             |
| ------------------------ | ------------------ | ------------------------------------------------------------------------------------------------------------------- |
| Solo developer (macOS)   | Homebrew           | `brew install --cask nightgauge/tap/nightgauge`                                                                     |
| Team / CI (macOS, Linux) | GitHub Releases    | Download the tarball + SHA256 checksum from the [latest release](https://github.com/nightgauge/nightgauge/releases) |
| Go developer             | go install         | `go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest`                                                 |
| VSCode Extension user    | Bundled in VSIX    | Automatic — installed via extension                                                                                 |

The binary is built and tested for macOS (arm64/amd64) and Linux (amd64).
Windows is not currently supported — use WSL2.

### Homebrew (macOS)

```bash
brew tap nightgauge/tap
brew install --cask nightgauge/tap/nightgauge
```

The Homebrew cask is auto-updated on each release via GoReleaser. Supports
both Apple Silicon (arm64) and Intel (amd64) Macs.

### go install

```bash
go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest
```

Requires Go 1.26+. The binary is installed to `$GOPATH/bin/nightgauge`.

### npm postinstall (SDK — in-repo builds only)

> The SDK is **not published to npm**. This postinstall path applies when you
> install dependencies inside a clone of this repository (`npm install` at the
> repo root); it is not an installation method for the binary on its own.

When the SDK package's dependencies are installed, the postinstall script
automatically downloads the matching binary for your platform. No manual
steps required.

The postinstall script:

- Downloads the correct binary from the GitHub Release matching the SDK version
- Verifies SHA256 checksum against `manifest.json`
- Falls back to `go build` if Go is present and download fails
- Never fails `npm install` — warns on failure and exits 0

Set `NIGHTGAUGE_SKIP_POSTINSTALL=1` to skip binary download.

## Why Go

- **Performance**: Single binary, zero cold start, no runtime dependencies
- **IP Protection**: Compiled binary protects orchestration logic
- **Cross-Platform**: Compiles natively for macOS and Linux (Windows
  cross-compiles but is untested/unsupported — use WSL2)
- **Quality**: Static typing, compile-time guarantees, built-in race detector

## Building

```bash
go build -o bin/nightgauge ./cmd/nightgauge/
```

Cross-compile for releases:

```bash
GOOS=darwin GOARCH=arm64 go build -o bin/nightgauge-darwin-arm64 ./cmd/nightgauge/
GOOS=darwin GOARCH=amd64 go build -o bin/nightgauge-darwin-amd64 ./cmd/nightgauge/
GOOS=linux GOARCH=amd64 go build -o bin/nightgauge-linux-amd64 ./cmd/nightgauge/
```

## Testing

```bash
go test ./...                        # All tests
go test ./internal/hooks/            # Hook tests only
go test ./internal/validation/       # Validation tests
go test ./internal/intelligence/...  # Intelligence tests
```

## Package Structure

```text
cmd/nightgauge/          Entry point (Cobra CLI)
internal/
├── github/                   GitHub GraphQL client
│   ├── client.go            Authentication, query execution
│   ├── board.go             Project board operations
│   ├── issues.go            Issue/PR operations
│   ├── labels.go            Repository label CRUD (GraphQL)
│   └── views.go             Project board view operations (GraphQL list, REST create)
├── hooks/                    Claude Code hook implementations
│   ├── gate.go              PreToolUse workflow gate (parses git argv, not substrings)
│   ├── cmdparse.go          Shell-command tokenizer for the guards (#4069)
│   ├── stop.go              Stop verification (PLAN.md)
│   ├── format.go            Format-on-save dispatcher
│   ├── context.go           Session context injection
│   ├── notify.go            Desktop notifications
│   ├── deps.go              Dependency checking
│   └── sanitize.go          Prompt injection detection
├── intelligence/             Pipeline intelligence
│   ├── complexity/          Issue complexity estimation
│   ├── routing/             Model routing decisions
│   ├── tokens/              Token budget and cost estimation
│   ├── learning/            Self-improvement tuning
│   ├── suggestions/         Health-based suggestions
│   ├── failure/             Failure classification
│   └── teams/               Agent team waves, deps, budget
├── ipc/                      IPC server for VSCode
│   └── server.go            JSON-over-stdio protocol
├── orchestrator/             Pipeline scheduling
│   └── scheduler.go         Issue picking and execution
├── platform/                 Platform API client
│   └── client.go            REST client with health polling
├── trace/                    Per-run lifecycle decision trace (ADR 013)
│   ├── events.go            Envelope + kind taxonomy + typed payloads
│   ├── store.go             Per-run JSONL writer/readers (fail-open)
│   └── export.go            Trace + RunRecord + exit-record join
└── validation/               Migration validation
    └── runner.go             Parallel shell/Go comparison
```

## Authentication

The `nightgauge` CLI requires a GitHub token for API operations.

**Auto-detection** (preferred): The binary reads `GITHUB_TOKEN` environment
variable, falling back to `gh auth token` output if available.

**Manual**: Set `GITHUB_TOKEN` environment variable:

```bash
export GITHUB_TOKEN=$(gh auth token)
```

## CLI Command Reference

This section is the canonical reference for all `nightgauge` subcommands.
Skills and plugin commands reference these commands instead of shell scripts.

### Backlog Operations

#### `backlog preflight`

Validates backlog issues are pipeline-ready by running 5 deterministic checks
(Checks 2.1–2.5 from `skills/nightgauge-backlog-preflight`). Implements
audit appendix row B26.

**Usage:**

```bash
nightgauge backlog preflight [flags]
```

**Flags:**

| Flag        | Default | Description                                                              |
| ----------- | ------- | ------------------------------------------------------------------------ |
| `--owner`   | config  | GitHub repository owner                                                  |
| `--repo`    | config  | GitHub repository name                                                   |
| `--status`  | `Ready` | Project board status filter                                              |
| `--focus`   | `all`   | Checks to run: `all`, `labels`, `criteria`, `dependencies`, `greenfield` |
| `--issue`   | `0`     | Single issue number (0 = all issues in `--status`)                       |
| `--project` | config  | Project board number                                                     |
| `--json`    | `false` | Output results as JSON                                                   |

**Exit codes:**

| Code | Meaning                                          |
| ---- | ------------------------------------------------ |
| `0`  | All checks pass (no findings)                    |
| `1`  | Findings found (one or more validation failures) |
| `2`  | Config or IO error                               |

**JSON output schema (`BacklogPreflightReport`):**

```json
{
  "v": 1,
  "owner": "nightgauge",
  "repo": "nightgauge",
  "status": "Ready",
  "focus": "all",
  "findings": [
    {
      "issue_number": 42,
      "issue_title": "Add photo upload",
      "finding_type": "missing_type_label",
      "severity": "high",
      "detail": "#42 \"Add photo upload\" has no type:* label",
      "suggestion": "Add one of: type:feature, type:bug, type:docs, ..."
    }
  ],
  "summary": {
    "total_issues": 10,
    "issues_clean": 9,
    "issues_flagged": 1,
    "by_finding_type": { "missing_type_label": 1 },
    "by_severity": { "high": 1 }
  },
  "generated_at": "2026-04-30T15:00:00Z"
}
```

**Finding types:**

| `finding_type`             | Severity | Description                                   |
| -------------------------- | -------- | --------------------------------------------- |
| `missing_type_label`       | high     | Issue has no `type:*` label                   |
| `missing_size_field`       | medium   | Issue has no Size board field                 |
| `missing_priority_field`   | medium   | Issue has no Priority board field             |
| `weak_acceptance_criteria` | high     | Body < 100 chars or fewer than 2 checkbox ACs |
| `dependency_cycle`         | high     | Circular `blockedBy` relationship detected    |
| `greenfield_warning`       | low      | Missing expected project structure file       |

**Examples:**

```bash
# Validate all Ready issues, JSON output
nightgauge backlog preflight --status Ready --json

# Check labels only (fast — no API calls per issue)
nightgauge backlog preflight --status Ready --focus labels

# Validate a single issue
nightgauge backlog preflight --status Ready --issue 42 --json

# Check project structure (no GitHub calls)
nightgauge backlog preflight --focus greenfield
```

### Preflight Operations

Pre-submission gate verbs that replace the bash + python3 + sed chains in
`skills/pr-preflight/SKILL.md` (audit row B40, skill-survey rows 57-60).
Each subcommand exits non-zero when findings exist, so they chain cleanly in
CI or git pre-push hooks. The exit-code semantics intentionally diverge from
sibling `scan` and `docs` verbs: preflight is a gate, scan is a counter.

```bash
# Validate relative markdown links resolve (wraps `docs check-links`)
nightgauge preflight links --root . [--target FILE] [--section "## Heading"] \
  [--exclude-templates] [--json]

# Validate JSON / YAML file syntax
nightgauge preflight syntax --workdir . [--json]

# Detect committed secrets (wraps `scan secrets`, gate exit codes)
nightgauge preflight secrets --workdir . [--json]

# Fail when a skill SKILL.md contains a direct `gh ` call (forge-abstraction gate)
nightgauge preflight skill-no-direct-gh --root . [--json]

# Fail when a skill file hits a mechanical authoring anti-pattern
nightgauge preflight skill-anti-patterns --root . [--json]

# Fail when a skill embeds a non-portable (VSCode-extension) binary path
nightgauge preflight skill-portability --root . [--json]
```

**Exit codes (uniform across the family):**

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Clean — gate passes                                      |
| 1    | Findings present — gate fails                            |
| 2    | Hard error (unresolvable workdir/root, internal failure) |

**Subcommand details:**

| Subcommand            | Wraps                  | Stable JSON schema (v1)                                                                     |
| --------------------- | ---------------------- | ------------------------------------------------------------------------------------------- |
| `links`               | `internal/docs.Run`    | `v, root, files_scanned, links_total, links_broken, findings, warnings`                     |
| `syntax`              | `internal/preflight`   | `v, workdir, files_scanned, files_invalid, findings[{file, line, format, error}], warnings` |
| `secrets`             | `internal/scan.Run...` | `v, workdir, patterns{generic_kv,...}, total, warnings`                                     |
| `skill-no-direct-gh`  | `internal/preflight`   | `v, root, skills_checked, skills_exempted, findings[{skill, file, line, match}], warnings`  |
| `skill-anti-patterns` | `internal/preflight`   | `v, root, files_checked, findings[{check, file, line, match}], warnings`                    |
| `skill-portability`   | `internal/preflight`   | `v, root, files_checked, findings[{skill_file, line, check, match}], warnings`              |

**`syntax` finding format enum:** `"json"` or `"yaml"`. Empty files are
treated as valid (matches `python3 -m json.tool` and `yaml.safe_load`
behavior). Files larger than 5 MiB are skipped with a warning. Excluded
directories: `.git`, `node_modules`, `vendor`, `dist`, `build`, `coverage`,
`.next`, `out`.

**`skill-anti-patterns` check enum:** `nested_reference` (a supporting file
points at another supporting file), `backslash_path` (a Windows `\` path
separator), `missing_toc` (a long supporting file lacks a `## Contents`
heading). The `skill-no-direct-gh` gate honors an allowlist
(`scripts/lint-skills/allowlist.txt`) for the un-migrated forge tail.

**`skill-portability` check enum:** `vscode_extension_path` (a skill embeds a
hardcoded `~/.vscode/extensions/nightgauge…` binary path that breaks
cross-adapter portability). Scans every `*.md` under `skills/`; skills must
resolve the binary provider-neutrally (`$NIGHTGAUGE_BIN` → PATH → repo bin
→ canonical-repo bin → `~/go/bin`). See docs/SKILL_PORTABILITY.md (#4029).

**No subcommand exposes `--fix`.** No deterministic auto-fix exists for broken
links, invalid syntax, committed secrets, or skill authoring issues — each verb
is read-only and reports findings for a human to resolve. (The
command-wrapper-coupled `skill-versions`/`skill-banners` verbs were retired in
#3876 when the skills-canonical contract removed command wrappers — the skill
SKILL.md is now the single source of truth, so there is nothing to reconcile.)

**Divergence from `scan secrets`:** identical JSON shape, different exit
code. `scan secrets` always exits 0 (counter); `preflight secrets` exits 1
when `total > 0` (gate). Skill authors who want gate semantics should call
`preflight secrets`; rubric scoring should keep calling `scan secrets`.

**Examples:**

```bash
# Single-shot CI gate — chain the read-only checks
nightgauge preflight syntax --workdir . && \
  nightgauge preflight links --root . --exclude-templates && \
  nightgauge preflight secrets --workdir . && \
  nightgauge preflight skill-no-direct-gh --root .

# Inspect specific findings
nightgauge preflight syntax --workdir . --json | jq '.findings[]'
```

### Setup Operations

Project-bootstrap verbs that emit fixed templates with brownfield-safe skips.
Replaces the heredoc-based config emission in
`skills/smart-setup/SKILL.md` (audit row B37). Templates live under
`internal/setup/templates/*` and are embedded at compile time via
`//go:embed`.

```bash
# Emit any combination of tsconfig, vitest, eslint, prettier, ci.yml
nightgauge setup scaffold-tooling --workdir . [--select KEYS] [--dry-run] [--json]
```

**Key flags:**

| Flag        | Meaning                                                                         |
| ----------- | ------------------------------------------------------------------------------- |
| `--workdir` | Project root (default: CWD)                                                     |
| `--select`  | Comma-list: `tsconfig`, `vitest`, `eslint`, `prettier`, `ci`. Empty = all five. |
| `--dry-run` | Report outcomes with `bytes` set to template length but write nothing.          |
| `--json`    | Stable v1 JSON schema (parsed by `smart-setup` Phase 4.5).                      |

**Stable JSON schema (v1):**

| Field      | Type     | Notes                                                                                               |
| ---------- | -------- | --------------------------------------------------------------------------------------------------- |
| `v`        | int      | Always 1; bumping requires field-rename or enum addition.                                           |
| `workdir`  | string   | Absolute path that was scanned.                                                                     |
| `selected` | string[] | Requested template keys after normalization (canonical order).                                      |
| `detected` | object   | `package_json_found`, `node_version`, `has_typescript`, `has_vitest`, `has_eslint`, `has_prettier`. |
| `outcomes` | object[] | One entry per requested key with `key`, `path`, `outcome`, `reason`, `bytes`.                       |
| `warnings` | string[] | Non-fatal scan warnings (missing/malformed package.json, etc.).                                     |

**Closed enums:**

| Field                | Values                                                                            |
| -------------------- | --------------------------------------------------------------------------------- |
| `outcomes[].key`     | `tsconfig`, `vitest`, `eslint`, `prettier`, `ci`                                  |
| `outcomes[].outcome` | `created`, `skipped_existing`, `skipped_missing_dep`, `skipped_disabled`, `error` |

**Brownfield-safety contract:** the verb never overwrites. For ESLint it
also probes `.eslintrc.js` and `.eslintrc.json`; for Prettier it probes
`.prettierrc.json` and `prettier.config.js` before writing the canonical
target. When any probe matches, the outcome is
`skipped_existing` and `path` records the existing variant.

**Template provenance:** `tsconfig.json`, `vitest.config.ts`,
`eslint.config.js`, and `.prettierrc` are byte-for-byte copies of the
SKILL.md Phase 4.5 heredocs. Only `ci.yml.tmpl` takes a substitution —
the Node major version, rendered through Go `text/template` with custom
`<% %>` delimiters so GitHub Actions `${{ ... }}` expressions survive
verbatim.

**Exit codes:**

| Code | Meaning                                                       |
| ---- | ------------------------------------------------------------- |
| 0    | Scan completed (per-file errors land in `outcomes[].outcome`) |
| 2    | Hard error (unresolvable workdir, unknown `--select` key)     |

**Examples:**

```bash
# Scaffold only the CI workflow into a fresh repo
nightgauge setup scaffold-tooling --workdir . --select ci --json | jq '.outcomes[]'

# Dry-run all five templates (reports intended writes, touches nothing)
nightgauge setup scaffold-tooling --workdir . --dry-run --json | jq '.outcomes[].outcome'

# Subset for a TypeScript-only project
nightgauge setup scaffold-tooling --workdir . --select tsconfig,vitest --json
```

### Project Board Operations

```bash
# Add issue to project board (sets Priority, Size from labels)
nightgauge project add <issue-number> [--owner ORG] [--project N]

# Add issue and atomically set Status in a single deterministic call
nightgauge project add <issue-number> --status Ready

# Bulk-add all open issues matching filters to project board
# --bulk: fetches all open issues, adds each to the board (fail-continue)
# --label: filter by label (repeatable); --milestone: filter by milestone title
# --json: returns { total, added, skipped, failed, errors, mode }
nightgauge project add --bulk [--milestone "Sprint 1"] [--label type:feature] [--json]

# Set Priority, Size, and/or Status fields on a board item
nightgauge project set-field <issue-number> --priority P0 --size M --status Ready

# Set date fields on a project board item (ISO 8601 YYYY-MM-DD format)
nightgauge project set-field <issue-number> --start-date 2026-05-01
nightgauge project set-field <issue-number> --target-date 2026-05-15
nightgauge project set-field <issue-number> --start-date 2026-05-01 --target-date 2026-05-15

# Move issue to a status column
nightgauge project move-status <issue-number> <status>

# Sync issue status field (idempotent)
nightgauge project sync-status <issue-number> <status>

# List project board items
nightgauge board list [--status Ready] [--owner ORG] [--project N] [--json]

# List all views for a project board (GraphQL)
nightgauge project view-list --project N [--owner ORG] [--owner-type org|user] [--json]

# Create a project board view (idempotent — returns existing view if name matches)
nightgauge project view-create --name "Ready Items" --layout board --project N \
  [--filter "status:Ready"] [--owner ORG] [--owner-type org|user] [--json]

# Resolve a project by number (org preferred, user fallback)
# Returns: number, owner, owner_type, id, title, url
nightgauge project resolve --number N [--owner ORG] [--json]

# Ensure required project board fields exist (creates missing, adds missing options)
# Manages: Status, Priority, Size (SINGLE_SELECT); Start date, Target date (DATE); Estimate (NUMBER)
nightgauge project ensure-fields --number N [--owner ORG] [--owner-type org|user] [--json]
```

**Status values**: `backlog`, `ready`, `in-progress`, `in-review`, `done`

**`project add --status` notes**: Accepts canonical names (`Backlog`, `Ready`,
`In progress`, `In review`, `Done`) and lowercase aliases (`backlog`, `ready`,
`in-progress`, `in-review`, `done`). Validation runs before any GraphQL call —
unknown values fail fast with no side effects. Atomic: a non-zero exit
indicates either the add or the status assignment failed; the operation is
idempotent on retry. JSON output (`--json`) includes `"status": "<resolved
canonical>"` (empty string when the flag is omitted).

**`project add --bulk` notes**: When `--bulk` is set, a positional issue number is
forbidden (mutually exclusive). Issues are added sequentially (deterministic, rate-limit safe).
`--label` is repeatable (`--label type:feature --label type:bug`). `--milestone` filters
client-side by milestone title. Errors are accumulated — all issues are attempted even if some
fail. Non-zero exit if any issue failed to add. JSON output includes `total`, `added`,
`skipped`, `failed`, `errors`, and `mode: "bulk"`.

**`project set-field --start-date / --target-date` notes**: Accepts ISO 8601 `YYYY-MM-DD`
format only. Validation runs before any API call — invalid formats fail immediately with no
side effects. Date fields must already exist on the project board (use `ensure-fields` to
create them). Can be combined with `--priority`, `--size`, `--status` in a single call.

**ensure-fields notes**: Idempotent — safe to run multiple times. For each required field:
existing fields with all required options are left untouched (reported as `already`);
missing options are added to existing SINGLE_SELECT fields via `updateProjectV2Field` which
replaces the full option set (existing items retain their values). Missing fields are created
via `createProjectV2Field`. JSON output includes `created`, `updated`, `already` arrays and
a `field_ids` map (field name → GraphQL node ID) so consuming skills can extract IDs without
a separate query. Owner type defaults to `org`; pass `--owner-type user` for personal projects.

**View layouts**: `board`, `table`, `roadmap`

**view-create notes**: Uses REST `POST /orgs/{org}/projectsV2/{number}/views` (or
`/users/{user}/...` for user-owned projects) with required header
`X-GitHub-Api-Version: 2026-03-10`. The `--filter` flag sets a server-side filter
query (e.g., `status:Ready is:open`). JSON output includes `id`, `name`, `layout`,
and `status: "created"`.

### Repository Label Operations

```bash
# List all labels for a repository (first 100, GraphQL)
nightgauge label list [--owner ORG] [--repo REPO] [--json]

# Create a label (idempotent — returns existing label if name matches)
nightgauge label create --name "priority:critical" --color ff0000 \
  [--description "..."] [--owner ORG] [--repo REPO] [--json]

# Delete a label by node ID
nightgauge label delete --label-id <node-id> [--owner ORG] [--repo REPO] [--json]
```

**label create notes**: `--color` is a hex string without `#` (e.g., `ff0000`);
defaults to `cccccc` if omitted. All label operations use GraphQL — no `gh` CLI
required. JSON output from `label list` is an array of `{id, name, description, color}`
objects.

### Repository Operations

```bash
# Fetch repository settings (allow_auto_merge and full_name)
nightgauge repo settings --owner ORG --repo REPO [--json]

# Disable allow_auto_merge to restore exclusive pipeline merge control
nightgauge repo disable-auto-merge --owner ORG --repo REPO [--force] [--json]

# Gate: exit non-zero when allow_auto_merge is enabled
nightgauge repo check-auto-merge --owner ORG --repo REPO [--json]
```

**check-auto-merge notes**: Mirrors the `*-gate check` exit-code contract — `0`
on ALLOW (auto-merge disabled), `1` on BLOCK (auto-merge enabled), with the
remediation hint pointing at `repo disable-auto-merge` rendered to stderr. Used
by `pr-create` Phase 0.5 to guard PR creation. JSON output shape:
`{allowed, allow_auto_merge, repository, reason}`. The verb deliberately mirrors
`repo settings` semantics — it does not model branch protection rules.

**disable-auto-merge notes**: Issues `PATCH /repos/{owner}/{repo}` with
`{"allow_auto_merge": false}`. Without `--force`, prompts for confirmation on
stdin; in CI/automation pass `--force`.

### Issue Operations

```bash
# View issue with sub-issues and blocking relationships
nightgauge issue view <number> [--owner ORG] [--repo REPO] [--json]

# List issues, optionally filtered by epic or search query
nightgauge issue list [--epic <number>] [--search "<keywords>"] [--limit N] \
  [--owner ORG] [--repo REPO] [--json]

# Create issue
nightgauge issue create --title "..." --body "..." --labels "type:feature"

# Close issue
# Note: pr-merge SKILL.md verifies close completion via `gh issue view` query
# after this command runs. If the issue remains OPEN within 10s, the pipeline
# halts with: "Issue #N was not closed after merge — run: gh issue close N --reason completed"
nightgauge issue close <number> [--owner ORG] [--repo REPO]

# Edit issue body (full replacement or append)
nightgauge issue edit <number> --body "new full body" [--owner ORG] [--repo REPO] [--json]
nightgauge issue edit <number> --append-body "\n\nappended text" [--owner ORG] [--repo REPO] [--json]

# Create sub-issue under a parent epic
nightgauge issue create-sub <parent-number> "<title>" "<body>"
#   --blocked-by    Comma-separated blocker issue numbers (e.g. --blocked-by 280,290).
#                   Body text "Blocked by #N" is cosmetic and NOT parsed. Default: none.
#   --depends-on    Semantic alias for --blocked-by. Creates addBlockedBy relationships.
#                   Both flags can be used simultaneously; their blocker lists are merged.
#   --wave          Wave number (integer). Embeds "(Wave N)" annotation in the issue body.
#                   Use with output from `epic plan-waves` to annotate execution order.

# Link an existing issue as a sub-issue of a parent epic
nightgauge issue link-sub <parent-number> <child-number>

# Add blocking relationship (blocker blocks blocked)
nightgauge issue add-blocked-by <blocked-number> <blocker-number>

# Remove blocking relationship
nightgauge issue remove-blocked-by <blocked-number> <blocker-number>

# Sync labels from issue metadata
nightgauge issue sync-labels <number> [--owner ORG] [--repo REPO]

# Derive pipeline routing decision (label/board fields → route, complexity, skip stages)
# Wraps the canonical routing.Derive() pure function so issue-pickup and
# feature-planning consume one algorithm instead of duplicated shell prose.
# Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B4.
nightgauge issue route <number> [--owner ORG] [--repo REPO] [--project N] [--json] \
  [--size XS|S|M|L|XL] [--priority P0|P1|P2|P3] \
  [--type feature|bug|docs|refactor|chore|verification|spike]
```

When `--json` is set the verb emits:

```json
{
  "change_type": "code",
  "task_type": "feature",
  "complexity_score": 3,
  "suggested_route": "standard",
  "skip_stages": [],
  "foundation_task": false,
  "documentation_scope": "standard",
  "rationale": "Standard path: M size, code change, complexity 3, high priority. Full pipeline execution.",
  "effective_size": "M",
  "effective_priority": "high"
}
```

Offline mode — pass issue number `0` plus all of `--size`, `--priority`,
`--type` to derive a decision without touching GitHub. Useful for tests and
CI plumbing checks:

```bash
nightgauge issue route 0 --size M --priority P1 --type feature --json
```

```bash
# Infer type:* label from issue title/body/labels (consolidates the keyword
# rules previously duplicated across backlog-preflight Phase 4 and issue-refine
# Phase 2.1). Source priority: existing type:* label > body keywords > title
# keywords > default (type:feature).
# Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B12.
nightgauge issue infer-type <number> [--owner ORG] [--repo REPO] [--json] \
  [--apply] [--apply-default] \
  [--title "..."] [--body "..."] [--labels "type:bug,priority:high"]
```

When `--json` is set the verb emits:

```json
{
  "number": 3070,
  "type": "type:feature",
  "source": "label",
  "applied": false
}
```

`source` is one of `label` (an explicit `type:*` label was present),
`keyword` (matched a token in the title or body), or `default` (fallback to
`type:feature` when nothing matched). `--apply` adds the inferred label;
when `source == "default"` the apply step is skipped unless `--apply-default`
is also passed (the safety opt-in mirrors the "Confirm before applying"
guidance in the consumer SKILL.md).

Offline mode — pass issue number `0` plus at least one of `--title`,
`--body`, or `--labels` to classify without touching GitHub. Useful for
tests and CI plumbing checks:

```bash
nightgauge issue infer-type 0 --title "fix crash on startup" --json
# → {"number":0,"type":"type:bug","source":"keyword","applied":false}
```

````bash
# Parse Markdown acceptance-criteria checkboxes from an issue body and
# return a deterministic verdict (replaces the inline shell parser in
# feature-validate Phase 0.6.2). Lines inside fenced code blocks (``` or
# ~~~) are ignored, and checkbox detection is anchored to start-of-line —
# this removes false positives from technical_notes YAML examples and
# substring-in-prose mentions that the prior shell version counted.
# Audit reference: docs/SKILL_DETERMINISM_AUDIT.md row B14.
nightgauge issue ac-check <number> [--owner ORG] [--repo REPO] [--json] \
  [--body "..."]
````

When `--json` is set the verb emits:

```json
{
  "v": 1,
  "number": 3072,
  "status": "passed",
  "checked_count": 3,
  "unchecked_count": 0,
  "total": 3
}
```

`status` is one of `passed` (all top-level checkboxes checked), `failed`
(at least one unchecked), or `not_applicable` (body contains no top-level
checkboxes). The verb itself always exits `0` on a successful parse —
gating is the **caller's** job (parity with `issue infer-type`). The
`v` field locks the JSON shape at v1; bump only on breaking changes.

Offline mode — pass issue number `0` plus `--body` to parse without
touching GitHub. Useful for tests and CI plumbing checks:

```bash
nightgauge issue ac-check 0 --body "- [x] one\n- [ ] two" --json
# → {"v":1,"number":0,"status":"failed","checked_count":1,"unchecked_count":1,"total":2}
```

### Epic Operations

```bash
# Check epic AC completion (returns JSON with status, checked_count, unchecked_count)
nightgauge epic check-completion <epic-number> [--json]

# Validate epic structure: circular blockers and stale blockers
nightgauge epic validate <epic-number> [--owner ORG] [--repo REPO] [--json]
# JSON output schema: { "epicNumber": N, "title": "...", "repo": "...",
#   "totalSubIssues": N, "valid": true|false,
#   "gaps": [{ "subIssueNumber": N, "subIssueTitle": "...",
#              "gapType": "circular_blocker"|"stale_blocker",
#              "blockerNumber": N, "detail": "..." }] }
# Exit codes: 0 = success (gaps reported in JSON/stdout); non-zero = error fetching epic

# Assess epic sub-issues for batch vs sequential strategy
nightgauge epic assess <epic-number> [--owner ORG] [--repo REPO] [--json]
# JSON output schema: { "strategy": "sequential|parallel|mixed", "reasoning": "...",
#   "estimatedCostUsd": 0.0, "estimatedMinutes": 0.0,
#   "issues": [{ "issueNumber": N, "complexityScore": N, "recommendedModel": "...",
#                "estimatedCostUsd": 0.0, "hasDependencies": false }] }
# Fetches each open sub-issue individually for body+labels; closed sub-issues are skipped.

# Detect lifecycle issues: stale epics, board status drift, orphaned issues, stale blockers
nightgauge epic check-lifecycle <epic-number> [--owner ORG] [--repo REPO] [--project N] [--json]
nightgauge epic check-lifecycle --sweep [--owner ORG] [--repo REPO] [--project N] [--json]
# Single-epic: runs a full audit, then scopes results to findings for <epic-number> only.
# --sweep: returns findings for all open epics and issues in the repo.
# JSON output schema (LifecycleAuditResult):
#   { "dimension": "epic-lifecycle", "repo": "owner/name", "run_at": "...", "fix_mode": false,
#     "findings": [{ "category": "STALE_EPIC|BOARD_STATUS_DRIFT|PREMATURE_DONE|ORPHANED_ISSUE|STALE_BLOCKER",
#                    "severity": "high|medium|low", "issue_number": N, "issue_title": "...",
#                    "issue_state": "OPEN|CLOSED", "board_status": "...", "detail": "...",
#                    "fixed": false, "fix_error": "..." }],
#     "summary": { "total": N, "stale_epics": N, "status_drift": N, "premature_done": N,
#                  "orphaned": N, "stale_blocker": N, "fixed": N, "errors": N } }
# Flags: --project (default 5) sets the project board number for board lookups.
# Note: fix mode is not exposed on this command; use audit lifecycle --fix for auto-remediation.

# Group sub-issues into parallel execution waves based on blockedBy relationships
nightgauge epic plan-waves --sub-issues <N,M,...> [--owner ORG] [--repo REPO] [--json]
# Fetches each listed issue, reads native blockedBy relationships, and runs topological
# sort (Kahn's algorithm) to assign wave numbers. Issues with no internal dependencies
# land in wave 0; issues blocked by wave-0 work land in wave 1, etc.
#
# Deterministic file-overlap serialization (always-on): each issue's predicted
# target files are extracted from its body, and any two SAME-WAVE issues that share
# a top-level EXACT target file are auto-serialized by injecting a blockedBy edge
# (the later issue number depends on the earlier) BEFORE waves are computed — so a
# guaranteed merge conflict (two parallel PRs owning one file) is prevented at
# authoring time. Each injected serialization is reported as an error-severity entry
# in "conflicts". Directory-only overlaps (different files, same dir) are NOT
# serialized — they stay parallel and surface as warning-severity conflicts.
#
# Human-readable output prints, after the waves, one line per injected edge:
#   Serialized #144 after #143 — shared target file lib/pages/journal_entry_page.dart
# The issue-create skill (Phase 3.5) MUST apply every injected edge via
# `nightgauge issue add-blocked-by <later> <earlier>` — the sequencing is computed
# deterministically here, not left to author judgment.
#
# JSON output schema: { "subIssueCount": N,
#   "waves": [{ "waveIndex": 0, "issues": [{ "number": N, "title": "...", "files": [...] }] }],
#   "conflicts": [{ "path": "lib/.../page.dart", "issues": [143, 144], "severity": "error" }] }
# Determinism audit: SKILL_DETERMINISM_AUDIT.md row B23
```

`add-blocked-by` also enforces a parent-epic guard: it rejects the relationship
when the blocker is the parent epic of the blocked issue, preventing circular
dependencies at the source.

### Git Operations

```bash
# Create a feature branch (handles epic detection, lazy epic branch creation)
#
# Two invocation modes:
#   1. Positional name — caller pre-computes the branch name:
#        nightgauge git branch-create feat/123-my-feature [--json]
#   2. --issue N — binary fetches the issue, derives the prefix from labels
#      (bug→fix/, documentation/docs→docs/, refactor→refactor/, test→test/,
#      chore/maintenance→chore/, default feat/), and the slug from the title:
#        nightgauge git branch-create --issue 123 [--json]
#
# `--issue` and a positional name are mutually exclusive.
nightgauge git branch-create [<branch-name> | --issue N] [--json]
```

### PR Operations

```bash
# Create a pull request
nightgauge pr create \
  --title "feat(#N): description" \
  --body "..." \
  --head <branch> \
  --base main \
  [--draft] \
  [--json]

# Merge a pull request with configurable strategy
nightgauge pr merge [pr-number] \
  --owner nightgauge \
  --repo nightgauge \
  --strategy squash|merge|rebase \
  [--delete-branch] \
  [--json]
# JSON output: {"merged":true,"sha":"<commit-oid>","strategy":"squash","branch_deleted":true}

# Wait for PR CI checks to complete
nightgauge pr ci-wait [pr-number] \
  --owner nightgauge \
  --repo nightgauge \
  [--timeout 600] \
  [--poll 30] \
  [--json]
# JSON output: {"prNumber":42,"state":"SUCCESS","total":5,"completed":5,"successful":5,"failed":0,"pending":0,"elapsedSecs":45,"isTerminal":true,"checks":[...]}

# Detect branch rulesets that would block merge; optionally auto-satisfy
nightgauge pr ruleset-precheck <pr-number> \
  [--owner nightgauge] \
  [--repo nightgauge] \
  [--auto-satisfy] \
  [--json]
# Without --auto-satisfy: detect and report blockers only
# With --auto-satisfy: request Copilot review (idempotent), poll until reviewed or ctx expires
# JSON output: {"blockers":["copilot_code_review"],"base_ref":"main","allowed_to_merge":false,"message":"..."}
```

### Pipeline Operations

```bash
# Run the pipeline (auto-pick or explicit issue)
nightgauge run [--auto] [--project N]

# Queue management
# Auto-detects type:epic — expands sub-issues instead of queuing the epic itself
nightgauge queue add <issue-number>
nightgauge queue list
nightgauge queue clear

# Pipeline status
nightgauge status

# Cost estimation
nightgauge cost --complexity <1-10>

# Recorded cost/duration grouped by change_class
nightgauge cost by-class [--days N] [--since YYYY-MM-DD] [--until YYYY-MM-DD]
```

#### `cost by-class`

Reads the recorded pipeline run history and reports cost (p50/p95/mean) and
duration (p50/p95) grouped by the authoritative `change_class` recorded on each
run (#4129). This is the measurement loop that shows trivial changes (docs/config)
cost less than source changes. Runs recorded before the `change_class` field
existed bucket under `unknown`.

```bash
# Default: last 30 days, human-readable table
nightgauge cost by-class

# Wider window, machine-readable
nightgauge cost by-class --days 90 --json

# Explicit date range (--since overrides --days)
nightgauge cost by-class --since 2026-01-01 --until 2026-06-30 --json
```

**Flags:**

- `--days N` — look back this many days of run history (default: `30`)
- `--since YYYY-MM-DD` — start date; overrides `--days` when set
- `--until YYYY-MM-DD` — end date
- `--json` — output JSON (default: human-readable table)
- `--workdir PATH` — project root (default: current working directory)

**JSON output schema:**

```json
{
  "v": 1,
  "runs_analyzed": 42,
  "classes": [
    {
      "change_class": "docs_only",
      "runs": 12,
      "cost_p50_usd": 0.0312,
      "cost_p95_usd": 0.0688,
      "cost_mean_usd": 0.0401,
      "duration_p50_ms": 84000,
      "duration_p95_ms": 142000,
      "total_cost_usd": 0.4812
    }
  ]
}
```

See [GATE_RELAXATION.md § "Measuring pipeline cost"](GATE_RELAXATION.md#measuring-pipeline-cost-nightgauge-cost-by-class)
for how this proves the fast-track win.

#### `pipeline batch-failures`

Extracts pipeline failure rows from `.nightgauge/pipeline/batch-state.json`
AND `.nightgauge/pipeline/history/*.jsonl`, with a context-files fallback.
Replaces ~150 lines of inline Python in `skills/nightgauge-retro/SKILL.md`
Phases 2.1, 2.2, and 2.4 (audit row B29).

```bash
# Default: scan current working directory
nightgauge pipeline batch-failures --json

# Explicit workdir + filters
nightgauge pipeline batch-failures --workdir /path/to/repo --json
nightgauge pipeline batch-failures --since 2026-04-01 --json
nightgauge pipeline batch-failures --issue 3087 --json
nightgauge pipeline batch-failures --all-failures --json
```

**Flags:**

- `--workdir PATH` — project root (default: current working directory)
- `--issue N` — filter to a single issue number (`0` = all)
- `--since YYYY-MM-DD` — lower bound for history JSONL filenames; ignored
  for `batch-state.json` and context-files (no per-row date)
- `--all-failures` — disable `--since` for history (collect all dates)
- `--json` — output JSON (default: human-readable)

**Exit codes:** `0` extract completed (zero rows is not an error); `2` hard
error.

**JSON output schema** (stable v1):

```json
{
  "v": 1,
  "filters": {
    "issue": 0,
    "since": "2026-04-01",
    "all_failures": false,
    "workdir": "/path/to/repo"
  },
  "batch": {
    "batch_status": "partial",
    "batch_started_at": "2026-05-01T12:00:00Z",
    "batch_updated_at": "2026-05-01T13:00:00Z",
    "total_issues": 5
  },
  "batch_failures": [
    {
      "issue_number": 3087,
      "title": "feat: ...",
      "status": "failed",
      "completed_stages": ["pipeline-start", "issue-pickup"],
      "failed_stages": ["feature-planning", "feature-dev"],
      "duration_ms": 480000,
      "token_usage": {},
      "source": "batch-state"
    }
  ],
  "history_failures": [
    {
      "issue_number": 152,
      "title": "feat: ...",
      "outcome": "failed",
      "started_at": "2026-03-31T21:39:23Z",
      "total_duration_ms": 4242109,
      "stage_failures": { "pr-merge": "failed" },
      "estimated_cost_usd": 1.23,
      "source": "history"
    }
  ],
  "context_failures": [
    {
      "issue_number": 100,
      "has_dev_context": false,
      "source": "context-files",
      "inferred_failure": "no pr context found — pipeline likely did not complete"
    }
  ],
  "skipped_records": 0,
  "warnings": []
}
```

The `batch` block is `null` (omitted) when `batch-state.json` is absent.
`source` values: `"batch-state"` | `"history"` | `"context-files"`.

Used by `skills/nightgauge-retro/SKILL.md` Phases 2.1, 2.2, and 2.4.

### Run-State Operations (Issue #3238)

Manages the durable pipeline lifecycle record at
`.nightgauge/pipeline/run-state.json`. Single source of truth for
running / paused / completed / discarded / aborted state. See
[docs/PIPELINE_STATE_SCHEMA.md](PIPELINE_STATE_SCHEMA.md) for the full
schema, lifecycle diagram, and recovery decision tree.

```bash
# Print the current run-state as JSON (or {} when absent)
nightgauge run state get

# Privileged: write a state record (used by tests + recovery flows)
nightgauge run state set --state running --issue 42 --branch feat/x
nightgauge run state set --state paused --reason "user clicked stop"
nightgauge run state set --state aborted --reason "crash" --recoverable

# Transition paused → running and print the resume_from_stage
nightgauge run state resume

# Discard: archive context files, remove worktree, delete branch
nightgauge run state discard --reason "user discard" --repo /path/to/repo

# Detect what the orchestrator should do at start
nightgauge run state detect --branch feat/x --issue 42
# → { kind: "fresh" | "paused" | "aborted" | "running" | "orphaned",
#     choices: [...], state: {...} }
```

The `detect` subcommand is what the autonomous orchestrator calls before
claiming an issue: on `paused`, it logs and skips. The user-driven runner
surfaces `choices` as a quick-pick dialog for the recovery UX (Gap 2).

### Trace Operations (Issue #179 / ADR 013)

Deterministic readers over the per-run lifecycle decision trace — one
append-only JSONL per run at `.nightgauge/pipeline/trace/<run_id>.jsonl`
capturing every stage boundary and every decision with its rationale and
rejected alternatives (model routing, change-class/fast-track, stage skips,
escalations, backtracks, recovery retries, gate results, outcome). The
scheduler emits events fail-open during execution; a trace-write failure never
fails a stage. The event fields and CLI JSON output are the public schema
contract.

```bash
# Show a run's ordered decision timeline (issue number or run id)
nightgauge trace show 179
nightgauge trace show 01890a5d-ac96-774b-bcce-b302099a8057
nightgauge trace show 179 --json

# Export one joined document: trace events + V3 RunRecord + exit records,
# ordered by (ts, producer, seq). Indented by default; --json for compact.
nightgauge trace export 01890a5d-ac96-774b-bcce-b302099a8057 --json
```

The trace is the source of record for **decisions**; outcomes and forensics
stay in the history and exit-records stores, joined by the shared `run_id`
(UUID v7). `trace show <issue>` resolves the most recent traced run for that
issue. Runs pre-dating trace capture simply have no file — consumers degrade
to stage-level data.

### Build Operations

Detects the project build system and runs the build. Consolidates build logic
from skill shell cascades into the deterministic Go layer. Used by
`skills/nightgauge-feature-dev/SKILL.md` Phase 4.1.5.

```bash
# Detect and run project build (go.mod → package.json "build" → skipped)
nightgauge build run

# JSON output (used by pipeline stages — matches build_verification schema in dev-{N}.json)
nightgauge build run --json

# Explicit working directory
nightgauge build run --workdir /path/to/project --json
```

**JSON output schema** (matches `build_verification` in `dev-{N}.json`):

```json
{
  "ran": true,
  "status": "passed",
  "commands": ["npm run build"],
  "output": "...",
  "timestamp": "2026-04-29T12:00:00Z"
}
```

`status` values: `"passed"` | `"failed"` | `"skipped"`

Stale SDK dist auto-healing is built in: if `npm run build` fails with a stale
SDK dist marker, the binary rebuilds the SDK and retries once before reporting
`"failed"`.

---

### Format Operations

Detects the project formatter and runs it at project scope (not per-file).
Distinct from `internal/hooks/format.go` which operates per-file via post-save
hooks. Used by `skills/nightgauge-feature-dev/SKILL.md` Phase 6.3.

```bash
# Detect and run project formatter
# Detection order: npm format → .prettierrc* → dprint.json → go.mod → pubspec.yaml
nightgauge format run

# JSON output
nightgauge format run --json

# Explicit working directory
nightgauge format run --workdir /path/to/project
```

**JSON output schema**:

```json
{
  "ran": true,
  "formatter": "npm run format",
  "output": "...",
  "timestamp": "2026-04-29T12:00:00Z"
}
```

---

### E2E Operations

Detects E2E test frameworks in the project and executes test suites.
Centralizes E2E detection logic from skill shell cascades into the deterministic
Go layer. Closes audit appendix row **B8**.

Detection order: Playwright > Cypress > Vitest > Jest > Go test.

Used by `skills/nightgauge-feature-validate/SKILL.md` Phase 1.2 and Phase 2.1.

#### `e2e detect`

Scans the project for E2E frameworks, config files, and test directories.
No commands are executed — pure file system detection.

```bash
# Detect E2E frameworks in the current directory
nightgauge e2e detect

# JSON output (used by pipeline stages)
nightgauge e2e detect --json

# Explicit working directory
nightgauge e2e detect --workdir /path/to/project --json
```

**JSON output schema**:

```json
{
  "detected": true,
  "frameworks": ["playwright", "cypress"],
  "config_files": ["/path/to/playwright.config.ts"],
  "test_dirs": ["e2e", "tests/e2e"],
  "timestamp": "2026-04-29T12:00:00Z"
}
```

`frameworks` values (in detection precedence order): `"playwright"` | `"cypress"` | `"vitest"` | `"jest"` | `"go"`

**Config file detection**:

| Framework  | Config files checked                                |
| ---------- | --------------------------------------------------- |
| Playwright | `playwright.config.ts`, `.js`, `.mts`, `.mjs`       |
| Cypress    | `cypress.config.ts`, `.js`, `.json`, `cypress.json` |
| Vitest     | `vitest.config.ts`, `.js`, `.mts`, `.mjs`           |
| Jest       | `jest.config.ts`, `.js`, `.json`, `.mjs`            |
| Go         | `go.mod` + at least one `*_test.go` file            |

**Test directory detection**: `e2e/`, `tests/e2e/`, `test/e2e/`

#### `e2e run`

Executes the E2E test suite using the detected (or specified) framework.

```bash
# Auto-detect framework and run
nightgauge e2e run

# JSON output
nightgauge e2e run --json

# Use explicit framework (skips detection)
nightgauge e2e run --framework playwright --json
nightgauge e2e run --framework cypress --json

# Explicit working directory
nightgauge e2e run --workdir /path/to/project --json
```

**`--framework` values**: `playwright` | `cypress` | `vitest` | `jest` | `go`

When `--framework` is omitted, the first framework returned by `e2e detect` is
used.

**JSON output schema**:

```json
{
  "ran": true,
  "status": "passed",
  "framework": "playwright",
  "commands": ["npx playwright test"],
  "output": "...",
  "timestamp": "2026-04-29T12:00:00Z"
}
```

`status` values: `"passed"` | `"failed"` | `"skipped"`

**Exit codes**:

| Code | Meaning                                           |
| ---- | ------------------------------------------------- |
| 0    | Tests passed (or skipped — no framework detected) |
| 1    | Tests failed or command error                     |

**Framework commands**:

| Framework  | Command executed         |
| ---------- | ------------------------ |
| Playwright | `npx playwright test`    |
| Cypress    | `npx cypress run`        |
| Vitest     | `npx vitest run --run`   |
| Jest       | `npx jest e2e`           |
| Go         | `go test -run E2E ./...` |

---

### Integration Operations

#### `integration probe-platform`

Probes platform API endpoints from an embedded YAML manifest and emits a
deterministic 6-category JSON report (WORKING, AUTH_REQUIRED, AUTH_MISMATCH,
NOT_FOUND, BROKEN, STUB). Implements audit appendix row B32 — absorbs the
curl-loop previously inlined in
`skills/nightgauge-integration-audit/SKILL.md` Phase 2.

Categorization is purely status-code + body-shape based. No LLM, no schema
inference. The default manifest mirrors the endpoint list in the
integration-audit skill; the canonical, human-visible copy lives at
`configs/integration-platform-endpoints.yaml` (refresh manually when platform
routes change).

**Usage:**

```bash
nightgauge integration probe-platform [flags]
```

**Flags:**

| Flag          | Default                 | Description                             |
| ------------- | ----------------------- | --------------------------------------- |
| `--base-url`  | `http://localhost:3000` | Platform API base URL                   |
| `--auth-mode` | `none`                  | Auth mode: `jwt`, `license`, or `none`  |
| `--token`     | empty                   | Auth token sent per `--auth-mode`       |
| `--manifest`  | embedded                | Path to a custom endpoint manifest YAML |
| `--timeout`   | `5s`                    | HTTP timeout per request                |
| `--json`      | `false`                 | Output results as JSON                  |

**Exit codes:**

| Code | Meaning                                                           |
| ---- | ----------------------------------------------------------------- |
| `0`  | All results are `WORKING` or `STUB` (no real findings)            |
| `1`  | At least one `AUTH_REQUIRED`/`AUTH_MISMATCH`/`NOT_FOUND`/`BROKEN` |
| `2`  | Config or IO error (manifest load failure, etc.)                  |
| `3`  | Server unreachable (every probe transport-errored)                |

**Categorization rules:**

| HTTP status                  | Body shape                              | Category                       |
| ---------------------------- | --------------------------------------- | ------------------------------ |
| 2xx                          | non-empty, non-stub                     | `WORKING`                      |
| 2xx                          | empty / `[]` / `{}` / `null` / length<4 | `STUB`                         |
| 401                          | any                                     | `AUTH_REQUIRED`                |
| 403                          | any                                     | `AUTH_MISMATCH`                |
| 404                          | any                                     | `NOT_FOUND`                    |
| 5xx                          | any                                     | `BROKEN`                       |
| other (3xx, transport error) | any                                     | `BROKEN` (with `error` detail) |

**JSON output schema (`ProbeReport`):**

```json
{
  "v": 1,
  "base_url": "http://localhost:3000",
  "auth_mode": "jwt",
  "categories": {
    "WORKING": 32,
    "STUB": 2,
    "AUTH_REQUIRED": 1,
    "AUTH_MISMATCH": 0,
    "NOT_FOUND": 4,
    "BROKEN": 1
  },
  "results": [
    {
      "group": "AUTH",
      "method": "GET",
      "path": "/v1/auth/me",
      "resolved_path": "/v1/auth/me",
      "status_code": 200,
      "category": "WORKING",
      "body_preview": "{\"id\":\"...\"}",
      "duration_ms": 14
    }
  ],
  "unreachable": false,
  "generated_at": "2026-05-03T13:55:30Z"
}
```

**Path placeholder substitution:** segments matching `:identifier` (e.g.
`:id`, `:teamId`) are replaced with the literal `probe` before the request is
issued. The verb categorizes route existence, not response content; sentinel
substitution lets `:id`-shaped paths receive a real HTTP response.

**Manifest format:**

```yaml
version: 1
groups:
  AUTH:
    - { method: GET, path: /v1/auth/me }
    - { method: POST, path: /v1/auth/web/github }
  HEALTH:
    - { method: GET, path: /health, auth_mode: none } # per-entry override
```

`auth_mode` on an entry overrides the global `--auth-mode` for that endpoint
only — useful for unauthenticated health probes when running with a JWT
token.

**Examples:**

```bash
# Probe localhost with no auth, human output
nightgauge integration probe-platform

# Authenticated probe, JSON output
nightgauge integration probe-platform \
  --base-url https://api.nightgauge.dev \
  --auth-mode jwt --token "$TOKEN" --json

# Custom manifest
nightgauge integration probe-platform \
  --manifest configs/integration-platform-endpoints.yaml --json
```

---

### CI Operations

CI check operations including PR status polling, log retrieval, CI parity
checking, workflow command discovery, and diff classification for CI fast-track.

```bash
# Poll CI check status for a PR
nightgauge ci wait <pr-number>
nightgauge ci wait 42 --timeout 60 --poll 15
nightgauge ci wait 42 --required-only

# Fetch CI logs
nightgauge ci logs <pr-number>

# Parse CI workflow and discover run-step commands
# Auto-discovers .github/workflows/ci.yml or ci.yaml
nightgauge ci discover-commands
nightgauge ci discover-commands --workflow .github/workflows/ci.yml --json

# Classify a diff into a CI fast-track decision
nightgauge ci classify --base <ref> --head <ref> [--json]
nightgauge ci classify --base origin/main --head HEAD --json
```

#### `ci classify`

Classifies the diff between `--base` and `--head` into a CI fast-track decision
(#4127). The always-running `changes` gate job consumes this to skip the heavy
`build-and-test` steps on documentation-only changes — only the expensive
**steps** are gated, never the required **job** itself, so branch protection
never deadlocks on a skipped required status check.

```bash
# Compare a PR head against its target branch (machine-readable)
nightgauge ci classify --base origin/main --head HEAD --json

# Compare two explicit SHAs
nightgauge ci classify --base "$BASE_SHA" --head "$HEAD_SHA" --json
```

**Flags:**

- `--base REF` — base ref/SHA (**required**)
- `--head REF` — head ref/SHA (default: `HEAD`)
- `--json` — output JSON (default: human-readable line)
- `--workdir PATH` — project root (default: current working directory)

**JSON output schema:**

```json
{
  "change_class": "docs_only",
  "run_heavy": false,
  "jobs": {
    "run_build": false,
    "run_go_tests": false,
    "run_e2e": false,
    "run_vsix_audit": false
  },
  "reason": "change_class=docs_only is fast-trackable — heavy CI jobs skipped"
}
```

`change_class` is one of `docs_only` | `config_only` | `source` | `mixed` |
`empty` (plus `unknown` on fail-open). `run_heavy` is `false` only for
`docs_only` / `empty`; every other class (including `config_only`) runs the full
suite. **Fail-open:** an unclassifiable diff (e.g. a failed `git diff`) returns
`change_class=unknown` with `run_heavy=true`, so CI never under-tests a change it
could not classify.

See [GATE_RELAXATION.md § "CI fast-track"](GATE_RELAXATION.md#2-ci-fast-track-4127)
for the fast-track surface and the `ci_jobs` → workflow-gate mapping.

**`ci discover-commands` JSON output schema**:

```json
{
  "commands": ["npm run format:check", "npm run lint", "npm run build"],
  "workflow_path": ".github/workflows/ci.yml",
  "framework": "node",
  "timestamp": "2026-04-29T12:00:00Z"
}
```

`framework` values: `"node"` | `"go"` | `"flutter"` | `"unknown"`

Falls back to standard commands (`format:check`, `lint`, `typecheck`, `build`,
`test` from `package.json` scripts) when no workflow is found.

```bash
# Run CI parity checks locally (discover + execute)
nightgauge ci parity-check
nightgauge ci parity-check --json
nightgauge ci parity-check --workdir /path/to/project --json
```

**`ci parity-check` JSON output schema**:

```json
{
  "passed": true,
  "commands_run": ["npm run format:check", "npm run lint"],
  "failures": [],
  "timestamp": "2026-04-29T12:00:00Z"
}
```

Each failure entry:

```json
{
  "command": "npm run lint",
  "failure_type": "lint",
  "output": "...",
  "exit_code": 1
}
```

`failure_type` values: `"format"` | `"lint"` | `"typecheck"` | `"build"` | `"test"` | `"unknown"`

Used by `skills/nightgauge-feature-dev/SKILL.md` Phase 6.4 and
`skills/nightgauge-feature-validate/SKILL.md` Phase 2.5.1.

---

### Logs Operations

Deterministic readers over local pipeline session logs in
`.nightgauge/logs/`. Distinct from `nightgauge ci logs <run-id>`,
which downloads CI workflow run logs from GitHub.

#### `logs scan-failures`

Scans `.nightgauge/logs/*_session.log` with the canonical 16-pattern
regex set and emits matched lines per file. Replaces ~80 lines of inline
Python in `skills/nightgauge-retro/SKILL.md` Phase 2.3 (audit row B29).

```bash
# Default: scan current working directory
nightgauge logs scan-failures --json

# Explicit workdir + filters
nightgauge logs scan-failures --workdir /path/to/repo --json
nightgauge logs scan-failures --since 2026-04-01 --json
nightgauge logs scan-failures --issue 3087 --json
```

**Flags:**

- `--workdir PATH` — project root (default: current working directory)
- `--issue N` — filter to logs whose filename contains this issue number
  (`YYYY-MM-DD_NNN_session.log`); `0` = all
- `--since YYYY-MM-DD` — lower bound for the date prefix in the log filename
- `--json` — output JSON (default: human-readable)

**Exit codes:** `0` scan completed (zero matches is not an error); `2` hard
error.

**Filename pattern**: `YYYY-MM-DD[_NNN]_session.log`. The optional `NNN`
issue prefix is parsed when present; logs without it have `issue_number: null`.

Each line is bounded to 300 bytes; per-file matches capped at 50 (mirrors the
existing Python parser's behavior).

**JSON output schema** (stable v1):

```json
{
  "v": 1,
  "filters": {
    "issue": 0,
    "since": "2026-04-01",
    "workdir": "/path/to/repo"
  },
  "log_files_scanned": 12,
  "files_with_signals": 3,
  "log_signals": [
    {
      "log_file": "2026-04-22_3087_session.log",
      "issue_number": 3087,
      "date": "2026-04-22",
      "failure_signals": [
        { "line": 42, "text": "[ERROR] tests failed: 3 failures" },
        { "line": 88, "text": "build failed with rc=2" }
      ]
    }
  ],
  "warnings": []
}
```

The 16-pattern set is the source of truth in
`internal/cmd/scanfailures/scanner.go` (`var FailurePatterns`). Failure
_classification_ (bucketing into one of 7 categories) is owned separately by
`nightgauge failure classify` (audit row B44) and lives in
`scripts/retro/classifiers/failure_classifier.py`.

Used by `skills/nightgauge-retro/SKILL.md` Phase 2.3.

---

### Release Operations

Deterministic verbs for monitoring upstream GitHub releases. Replaces the
inline `gh api` + Python embedded in
`skills/nightgauge-release-watch/SKILL.md` Phases 2–4 (audit row B33).
Output schemas are stable v1 — additive evolution only.

#### `release fetch`

Fetches releases from `GET https://api.github.com/repos/{source}/releases`,
optionally filters to releases strictly newer than `--since` (semver), and
emits a stable v1 JSON document.

**Usage:**

```bash
nightgauge release fetch --source <owner/repo> [flags]
```

**Flags:**

| Flag        | Default           | Description                                               |
| ----------- | ----------------- | --------------------------------------------------------- |
| `--source`  | (required)        | GitHub repo slug to query, e.g. `anthropics/claude-code`  |
| `--since`   | empty (no filter) | Lower-bound semver; only strictly-newer releases are kept |
| `--limit`   | `10`              | Maximum releases to fetch (sent as `per_page`)            |
| `--workdir` | current directory | Reserved for parity with sibling verbs; currently unused  |
| `--json`    | `false`           | Output result as JSON                                     |

**Exit codes:**

| Code | Meaning                                                                |
| ---- | ---------------------------------------------------------------------- |
| `0`  | Fetch completed (zero releases is not an error)                        |
| `2`  | Hard error (bad flag, transport failure, non-2xx status, decode error) |

**Authentication:** the verb sends `Authorization: Bearer <token>` when one is
available — order: `--token` CLI flag → `GITHUB_TOKEN` env var. An absent
token still works for public repos but at the lower 60/hr rate limit.

**JSON output schema (`FetchResult`):**

```json
{
  "v": 1,
  "source": "anthropics/claude-code",
  "since": "2.1.74",
  "limit": 10,
  "fetched_at": "2026-05-03T14:55:30Z",
  "filtered": 2,
  "releases": [
    {
      "tag_name": "v2.1.75",
      "name": "2.1.75",
      "published_at": "2026-04-22T10:00:00Z",
      "body": "## What's changed\n\n- Added foo …",
      "html_url": "https://github.com/anthropics/claude-code/releases/tag/v2.1.75",
      "prerelease": false,
      "draft": false
    }
  ]
}
```

**Examples:**

```bash
# All releases (last 10), no filter
nightgauge release fetch --source anthropics/claude-code --json

# Only releases newer than 2.1.74
nightgauge release fetch --source anthropics/claude-code --since 2.1.74 --json

# Smaller window
nightgauge release fetch --source anthropics/claude-code --limit 5
```

#### `release classify-changes`

Reads a JSON document (a `FetchResult` or a bare `[]Release` for piping
convenience) from `--input` or stdin, walks each release body line-by-line,
and emits one `ClassifiedChange` per `-`-prefixed bullet using the canonical
five-bucket prefix mapping.

**Usage:**

```bash
nightgauge release classify-changes [flags]
```

**Flags:**

| Flag        | Default | Description                                              |
| ----------- | ------- | -------------------------------------------------------- |
| `--input`   | empty   | Path to JSON input; empty = stdin                        |
| `--workdir` | empty   | Reserved for parity with sibling verbs; currently unused |
| `--json`    | `false` | Output result as JSON                                    |

**Exit codes:**

| Code | Meaning                    |
| ---- | -------------------------- |
| `0`  | Classification completed   |
| `2`  | Input read or decode error |

**Classification rules:**

| Body-line prefix (case-insensitive)  | `type`        |
| ------------------------------------ | ------------- |
| `Added …`                            | `feature`     |
| `Fixed …`                            | `fix`         |
| `Breaking …`                         | `breaking`    |
| `Deprecated …`                       | `deprecation` |
| `Improved …` / `Changed …` / default | `improvement` |

`[BRACKETED]` annotations on each line are extracted into `changes[].tags`
in order of appearance. Backticks are stripped from `description`. Lines
that do not start with `-` are ignored, and releases that produce zero
classified changes are dropped (mirrors the `if changes:` guard in the
pre-migration Python).

**JSON output schema:** a top-level array of `ClassifiedRelease` values.
The shape is pinned byte-for-byte to the pre-migration
`/tmp/release-watch-classified.json` so the release-watch skill's Phase 5+
scoring code consumes the new output without changes.

```json
[
  {
    "version": "2.1.75",
    "published_at": "2026-04-22T10:00:00Z",
    "changes": [
      { "type": "feature", "description": "Added foo", "tags": ["VSCode", "SDK"] },
      { "type": "fix", "description": "Fixed bar", "tags": [] }
    ]
  }
]
```

**Examples:**

```bash
# Pipe directly from fetch
nightgauge release fetch --source anthropics/claude-code --json | \
  nightgauge release classify-changes --json

# Re-classify a previously fetched document
nightgauge release classify-changes --input /tmp/release-watch-new.json --json
```

Used by `skills/nightgauge-release-watch/SKILL.md` Phases 3–4.

#### `release notify-findings`

Routes the high-impact `issues_created` findings from a release-watch
creation-log to a Discord **alert sink** (#4058), surfacing new-model /
breaking-change findings beyond the VSCode Discovery tab.

```bash
nightgauge release notify-findings --creation-log <path> [flags]
```

| Flag             | Default                         | Purpose                                                                                   |
| ---------------- | ------------------------------- | ----------------------------------------------------------------------------------------- |
| `--creation-log` | _(required)_                    | Path to a `creation-log-<provider>.json`                                                  |
| `--webhook-env`  | `RELEASE_WATCH_DISCORD_WEBHOOK` | Env var holding the Discord webhook URL; **unset/empty disables the sink**                |
| `--min-score`    | `70`                            | Route only findings with `score >= this` (mirrors `autonomous_discovery.score_threshold`) |
| `--max`          | `3`                             | Cap on findings routed (mirrors the per-release cap)                                      |
| `--dry-run`      | `false`                         | Build the payload and report, but do not POST                                             |
| `--json`         | `false`                         | Emit the `NotifyResult` as JSON                                                           |

**OPT-IN + BEST-EFFORT:** with no webhook env set the command is a clean no-op,
and a webhook delivery failure is reported (exit 0) rather than failing — an
alerting hiccup never breaks the release-watch run. Posts one consolidated
embed per provider-release; the embed timestamp is taken from the log's
`run_started_at` (deterministic).

**Exit codes:** `0` finished (sent / skipped / best-effort delivery failure) ·
`2` hard error (missing/unreadable/unparseable creation-log).

```bash
# Route the claude-code provider's findings (reads the webhook from the env var)
export RELEASE_WATCH_DISCORD_WEBHOOK="https://discord.com/api/webhooks/<id>/<token>"
nightgauge release notify-findings \
  --creation-log .nightgauge/release-watch/creation-log-claude-code.json --json
```

Invoked by the `release-watchdog.yml` workflow after each provider's discovery
run; the webhook URL comes from the `RELEASE_WATCH_DISCORD_WEBHOOK` repository
secret.

---

### Hook Subcommands

Used by Claude Code hook entry points (thin shell wrappers):

```bash
nightgauge hook workflow-gate       # PreToolUse gate (stdin JSON)
nightgauge hook careful-gate        # PreToolUse:Bash — block prod-destructive cmds while /careful is on
nightgauge hook stage-gate          # PreToolUse:Bash — fence analysis stages from git/forge mutations (#4145)
nightgauge hook stop-verify         # Stop hook (PLAN.md check)
nightgauge hook format --file <path>
nightgauge hook inject-context
nightgauge hook notify --message <msg>
nightgauge hook check-deps
nightgauge hook check-version
nightgauge hook sanitize-prompt --input <text>
```

#### `hook stage-gate` — analysis-stage git/forge fence (#4145)

A `PreToolUse:Bash` hook that stops pipeline **analysis** stages from advancing
git/forge state outside their mandate. Keyed on the active stage
(`NIGHTGAUGE_STAGE`, set by the adapters); outside a pipeline stage (env
unset) it is a no-op, and it fails **open** on any parse error.

Why: every stage's `SKILL.md` grants unscoped `Bash`, so an analysis subagent
could run any `git`/`gh` command. In incident #4142 an issue-pickup subagent
opened **and merged** a PR to `main`, bypassing every stage gate.

What it blocks, by stage (scope verified against what each stage legitimately
does — issue-pickup pushes the feature branch, feature-validate commits+pushes
validated work, so those are **not** fenced):

| Stage                                    | Forge PR/MR `create`/`merge`/`ready`, `issue close` | `git commit`/`merge`/`rebase`/`cherry-pick`/`revert` |
| ---------------------------------------- | --------------------------------------------------- | ---------------------------------------------------- |
| `issue-pickup`                           | blocked                                             | blocked                                              |
| `feature-planning`                       | blocked                                             | blocked                                              |
| `feature-validate`                       | blocked                                             | allowed (commits validated work)                     |
| `feature-dev` / `pr-create` / `pr-merge` | allowed                                             | allowed                                              |

Detection matches the real argv (env prefixes stripped, wrappers like `bash -c`
expanded), so words inside commit messages or `--body` payloads never trip it.
Read-only/edit forge verbs (`pr view|list|checks|comment`, `issue view|edit`)
stay allowed for every stage.

### Survival Operations (#4151)

The post-merge **survival outcome model** (spike #4134) records whether merged
code _held up_ on the base branch — distinguishing code that merged-and-survived
from code that merged-and-got-reverted/broke-main. This package
(`internal/intelligence/survival`) is **capture + detection only** and does no
calibration math itself. Feeding finalized verdicts into bias-safe calibration
(penalize proven reverts/breakage, weakly reward finalized survival once
enough data accrues) is implemented separately in
`internal/github/outcome_survival.go` — see
[OUTCOME_RECORDING.md#survival-calibration-issues-4152-4153](OUTCOME_RECORDING.md#survival-calibration-issues-41524153).

```bash
nightgauge survival sweep                 # finalize due pending records (revert/breakage detection)
nightgauge survival sweep --window-days 7 # override the observation window
nightgauge survival list                  # show captured records (JSON)
nightgauge survival list --verdict pending
```

**Capture** is automatic: the pr-merge path appends a `pending` survival record
keyed on the merge commit SHA (the #4133 breadcrumb) for every **single-issue
squash merge** — epic-umbrella PRs are skipped (ambiguous N→1 attribution). The
record is written to `.nightgauge/pipeline/survival-records.jsonl`
(append-only; a terminal line supersedes its pending line on fold).

**Finalize** folds into the autonomous **reconcile sweep** (poll-on-reconcile, no
new cron); `survival sweep` exposes the same pass for manual/CI use. Each due
record (window elapsed) is finalized deterministically:

| Verdict      | Trigger                                                                                     | Signal                      |
| ------------ | ------------------------------------------------------------------------------------------- | --------------------------- |
| `reverted`   | a `This reverts commit <sha>` commit on the base branch                                     | negative (proven)           |
| `broke`      | an ancestry-correlated main-CI failure (descendant of the merge **and** green at the merge) | negative (proven)           |
| `survived`   | window elapsed, no revert/breakage observed                                                 | weak-positive               |
| `unobserved` | never re-observed by `2 × window` (e.g. low-traffic repo)                                   | **none** — never `survived` |

Negative evidence (`reverted`/`broke`) is acted on immediately, regardless of the
window; the positive path is window-gated and ages out to `unobserved` past
`2 × window` so unexercised code is never miscounted as survived. Detection never
treats "main is red" as proof on its own. Window default: 7 days
(`pipeline.survival.window_days`).

### Intelligence Subcommands

```bash
nightgauge learn tune [--workdir <path>]
nightgauge learn audit
nightgauge suggest                  # stdin: findings JSON
nightgauge failure classify --stage <stage> --stderr <text>
nightgauge teams calculate-waves    # stdin: issues + deps JSON
nightgauge teams detect-deps        # stdin: issues JSON
nightgauge teams split-budget       # stdin: issues JSON
nightgauge teams detect-conflicts   # stdin: issues JSON

# Loop effectiveness analysis (Issue #3086, audit row B28)
nightgauge intelligence loop-verdicts [--workdir <path>] [--period <days>]

# Focus-aware proposal ranking (Issue #3086, audit row B28)
nightgauge focus rank --proposals <file> [--lens <name>]
```

#### `nightgauge intelligence loop-verdicts`

Analyzes self-improvement loop effectiveness by reading pipeline data files
and returning deterministic verdicts per loop.

**Usage**: `nightgauge intelligence loop-verdicts [--workdir <path>] [--period <days>]`

**Flags**:

| Flag        | Default | Description                            |
| ----------- | ------- | -------------------------------------- |
| `--workdir` | cwd     | Workspace root for data file discovery |
| `--period`  | 30      | Analysis window in days                |

**Output** (JSON):

| Field            | Type           | Description                                                      |
| ---------------- | -------------- | ---------------------------------------------------------------- |
| `v`              | `1`            | Schema version                                                   |
| `compositeScore` | `int 0-100`    | Weighted sum: closing=+20, stalling=+5, degrading=-10, no-data=0 |
| `healthBand`     | `string`       | `highly-effective` / `working` / `needs-attention` / `urgent`    |
| `loops`          | `[]LoopResult` | Per-loop verdict (loop, verdict, points, reason, evidence)       |
| `period`         | `int`          | Analysis period in days                                          |
| `generatedAt`    | `time`         | UTC timestamp of generation                                      |

**Loop verdicts**: `closing` / `stalling` / `degrading` / `no-data` / `bootstrapping`

**Data files read** (missing files → `no-data` verdict, not an error):

- `.nightgauge/pipeline/assessments/*.json` — skill-drift loop
- `.nightgauge/pipeline/history/outcomes.jsonl` — calibration + cost + reliability loops
- `.nightgauge/health/trends.jsonl` — health-monitoring loop

**Exit codes**: 0 success, 1 error (non-zero workspace root)

**Example output**:

```json
{
  "v": 1,
  "compositeScore": 50,
  "healthBand": "needs-attention",
  "loops": [
    {
      "loop": "skill-drift",
      "verdict": "no-data",
      "points": 0,
      "reason": "no assessment records found"
    },
    {
      "loop": "calibration",
      "verdict": "no-data",
      "points": 0,
      "reason": "no outcome records found"
    }
  ],
  "period": 30,
  "generatedAt": "2026-05-16T00:00:00Z"
}
```

---

#### `nightgauge focus rank`

Ranks improvement proposals by focus lens keyword alignment and adjusts
priorities using the same rules as `continuous-improvement` Phase 4.

**Usage**: `nightgauge focus rank --proposals <file> [--lens <name>]`

**Flags**:

| Flag          | Default     | Description                                                      |
| ------------- | ----------- | ---------------------------------------------------------------- |
| `--proposals` | (required)  | Path to JSON array of proposal objects                           |
| `--lens`      | active lens | Lens name; defaults to active lens from `.nightgauge/focus.yaml` |

**Proposal input schema** (array of):

```json
{
  "id": "ci-001",
  "category": "skill-fix",
  "priority": "medium",
  "loop": "skill-drift",
  "loopVerdict": "degrading",
  "title": "...",
  "description": "..."
}
```

**Output** (JSON):

| Field       | Type         | Description                                                               |
| ----------- | ------------ | ------------------------------------------------------------------------- |
| `v`         | `1`          | Schema version                                                            |
| `lens`      | `string`     | Lens name applied                                                         |
| `proposals` | `[]Proposal` | Re-ranked proposals with `focus_aligned` and `focus_keywords_matched` set |

**Priority adjustment rules** (same as SKILL.md Phase 4):

- Aligned + degrading loop → bump priority one tier up
- Not aligned + stalling loop → bump priority one tier down
- Hard constraint: degrading loop proposals never go below `high`
- Hard constraint: reliability/security category proposals always retain at least `high`

### Pipeline Gates

Preflight gates invoked by pipeline skills. Each emits structured JSON via
`--json` and uses the convention `0=pass, 1=block, 2=config error`.

### Size Prediction

```bash
# Predict complexity size label for an issue from its metadata
nightgauge size predict <issue-number> [--owner ORG] [--repo REPO] [--json]
# Fetches the issue, runs it through the complexity.Estimator, and returns a Score.
# JSON output schema: { "value": N, "sizeLabel": "XS|S|M|L|XL",
#   "confidence": "low|medium|high", "reasoning": "..." }
# Human-readable output: "Issue #N: M (score=6/10, confidence=high) — detailed description"
# Determinism audit: SKILL_DETERMINISM_AUDIT.md row B23
```

### Pipeline Gates

```bash
# Issue size gate — invoked by issue-pickup
nightgauge size-gate check --issue <N> [--config <path>] [--json]

# Baseline-CI dependency gate — invoked by issue-pickup (Issue #3004)
nightgauge baseline-gate check --issue <N> [--branch main] [--config <path>] [--json] [--pause-queue=true]
nightgauge baseline-gate promote [--branch main] [--config <path>] [--json]

# Scope-drift gate — invoked by pr-create Phase 2.6 for type:docs / type:chore (Issue #3040)
nightgauge scope-drift check --issue <N> [--config <path>] [--workdir <path>] [--issue-type docs|chore] [--json]

# Version-downgrade gate — invoked by feature-validate Phase 2.6 (Issue #3042)
nightgauge version-downgrade check [--issue <N>] [--baseline main] [--config <path>] [--workdir <path>] [--allow-override] [--json]
```

**`scope-drift check` flags**:

| Flag           | Description                                                                                        |
| -------------- | -------------------------------------------------------------------------------------------------- |
| `--issue`      | GitHub issue number to evaluate (required).                                                        |
| `--config`     | Path to `config.yaml`. Default: `.nightgauge/config.yaml`.                                         |
| `--workdir`    | Workspace root for locating `.nightgauge/pipeline/dev-{N}.json`. Default: cwd.                     |
| `--issue-type` | Override inferred issue type (`docs` \| `chore`). When set, skips the type-label lookup on GitHub. |
| `--json`       | Emit JSON `{ status, allowed, drifted_files, allowed_files, reason, ... }`.                        |

The gate reads `dev-{N}.json.files_changed` (created + modified) and matches
each path against `pipeline.scope_drift_gate.allowlist_docs` /
`allowlist_chore`. Drift events emit a `scope_drift_detected` pipeline event
(best-effort) and append to `.nightgauge/audit/scope-drift-stats.json`.
See [docs/CONFIGURATION.md#pipelinescope_drift_gate-issue-3040](CONFIGURATION.md#pipelinescope_drift_gate-issue-3040).

**`version-downgrade check` flags**:

| Flag               | Description                                                                                                |
| ------------------ | ---------------------------------------------------------------------------------------------------------- |
| `--issue`          | GitHub issue number for label bypass + `dev-{N}.json` lookup. Optional — gate runs without it for CLI use. |
| `--baseline`       | Baseline branch to compare against. Default: `main`.                                                       |
| `--config`         | Path to `config.yaml`. Default: `.nightgauge/config.yaml`.                                                 |
| `--workdir`        | Workspace root for file lookup. Default: cwd.                                                              |
| `--allow-override` | Force-bypass the gate (equivalent to `allow_downgrade=true` in `dev-{N}.json`).                            |
| `--json`           | Emit JSON `{ status, allowed, bypassed, downgrades[], reason, ... }`.                                      |

The gate reads the working tree's `tsconfig*.json` (current directory only,
non-recursive) and `package.json`, fetches the baseline versions of the same
files via `git show <branch>:<path>`, and reports any of:

- `compilerOptions.target` moved to a lexicographically-older value
- `compilerOptions.lib` lost an entry or replaced one with a smaller entry of
  the same family (e.g. `ES2022` → `ES2021`)
- `dependencies` / `devDependencies` / `peerDependencies` range minimum
  decreased (semver)
- `engines.node` minimum decreased (semver)

Bypassed when the issue carries the configured bypass label (default
`version:downgrade-allowed`) or when `dev-{N}.json` sets
`allow_downgrade: true`. Default config is **disabled** — opt in via
`pipeline.version_downgrade_gate.enabled: true`.

### Knowledge Base Operations

```bash
# Scaffold a workspace-level knowledge directory
nightgauge knowledge workspace-create <category> <slug> [flags]

# Examples
nightgauge knowledge workspace-create product my-feature
nightgauge knowledge workspace-create cross-repo auth-flow --repos nightgauge,acme-platform
nightgauge knowledge workspace-create product my-feature --json
nightgauge knowledge workspace-create product my-feature --workdir /path/to/workspace
```

**Flags:**

| Flag              | Description                                                                       |
| ----------------- | --------------------------------------------------------------------------------- |
| `--repos <names>` | Repo names for YAML frontmatter (repeatable; also accepts comma-separated values) |
| `--json`          | Output result as JSON                                                             |
| `--workdir <dir>` | Starting directory for workspace detection (default: cwd)                         |

**Categories:** `product`, `cross-repo`

**Workspace detection:** walks up from cwd (or `--workdir`) until `.vscode/nightgauge-workspace.yaml` is found.

**Idempotent:** re-running returns the existing path without overwriting files. JSON output includes `"skipped": true`.

**Output (text):**

```
Created: .nightgauge/knowledge/product/my-feature
  + PRD.md
  + decisions.md
```

**Output (JSON):**

```json
{
  "knowledge_path": ".nightgauge/knowledge/product/my-feature",
  "prd_path": ".nightgauge/knowledge/product/my-feature/PRD.md",
  "decisions_path": ".nightgauge/knowledge/product/my-feature/decisions.md",
  "skipped": false,
  "files_created": ["PRD.md", "decisions.md"]
}
```

=======

# Remove knowledge directories containing only boilerplate content

nightgauge knowledge prune-empty [--issue N] [--json]

# Regenerate PRD.md for an issue from the latest GitHub issue body

nightgauge knowledge regenerate <issue-number> [--knowledge-path PATH] [--dry-run] [--json]

````

**prune-empty notes**: Scans `.nightgauge/knowledge/` and removes
issue-scoped directories whose `.md` files are all boilerplate (< 30 non-boilerplate
characters). Use `--issue N` to limit to a single issue. `--json` outputs
`{ "pruned": ["..."] }`.

**regenerate notes**: Fetches the issue body from GitHub (`gh issue view`) and
rewrites `PRD.md` with fresh `## Summary`, `## Acceptance Criteria`, and
`## Technical Notes` content. Existing YAML frontmatter (`created`, `tags`,
`related_issues`) is preserved; `updated` is bumped. `decisions.md` is never
touched. Use `--dry-run` to preview without writing. `--json` outputs
`{ "regenerated": true, "files_updated": [...], "prd_updated": true, "decisions_preserved": true, "timestamp": "..." }`.

### Validation and Health

```bash
nightgauge validate [--category hooks] [--json]
nightgauge health trends [--limit N] [--json]
nightgauge health gate-metrics [--json]
```

### Health Subcommands

Read health data written by the SDK's `HealthTrendsWriter` and gate evaluation
hooks.

#### `health trends`

```bash
nightgauge health trends [--limit N] [--json]
```

Reads the last N entries from `.nightgauge/health/trends.jsonl`. Malformed
lines are skipped with a warning to stderr (non-fatal).

| Flag | Default | Description |
|------|---------|-------------|
| `--limit N` | 50 | Last N entries. `0` returns all. |
| `--json` | false | Output as JSON array. |

**JSON output schema:**

```json
[
  {
    "schema_version": "1",
    "timestamp": "2026-04-29T10:30:00Z",
    "run_id": "run-abc123",
    "issue_number": 3070,
    "overall_score": 85.5,
    "dimensions": {
      "token_economics": 0.9,
      "cost_health": 0.85
    },
    "significant_findings": ["improving success rate trend"]
  }
]
```

#### `health gate-metrics`

```bash
nightgauge health gate-metrics [--json]
```

Reads `.nightgauge/health/gate-metrics.jsonl`, groups by `gate_name`, and
computes hit rates. Output is deterministically sorted by gate name.

| Flag | Default | Description |
|------|---------|-------------|
| `--json` | false | Output as JSON array. |

**JSON output schema:**

```json
[
  {
    "gate_name": "size-gate",
    "invocations": 145,
    "catches": 8,
    "skipped": 0,
    "hit_rate": 0.055,
    "average_duration_ms": 234.5
  }
]
```

### Doctor — Environment Health Check

```bash
nightgauge doctor [--json]
```

Performs a full pre-flight environment check for pipeline operations. Every
pipeline skill calls this as Phase 0 preflight via `skills/_shared/PREFLIGHT.md`.

**Checks performed**:

| Check key     | What it verifies                                | Required?  |
| ------------- | ----------------------------------------------- | ---------- |
| `binary`      | `nightgauge` reachable via PATH            | warning    |
| `gh`          | `gh` CLI reachable via PATH                     | warning    |
| `github_auth` | GitHub token valid and authenticated            | required   |
| `api_user`    | `GET /user` returns non-empty login             | required   |
| `scopes`      | Token has `repo`, `project`, `read:org` scopes  | required   |
| `rate_limit`  | API requests remaining (warn < 500, warn < 100) | warning    |
| `config`      | `.nightgauge/config.yaml` parseable        | required\* |
| `project`     | `project_number` and `owner` set in config      | required\* |

\* Downgraded to warning for fresh repositories (no `config.yaml`).

**Exit codes**:

| Code | Meaning  | Description                                                |
| ---- | -------- | ---------------------------------------------------------- |
| 0    | Healthy  | All required checks pass, no warnings                      |
| 1    | Degraded | Required checks pass; optional items have warnings         |
| 2    | Broken   | One or more required checks failed; skills halt at Phase 0 |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable after
first merge. Skills parse `healthy`, `exit_code`, `errors[]`, and
`install_instructions`.

```json
{
  "v": 1,
  "healthy": true,
  "exit_code": 0,
  "checks": {
    "binary": { "ok": true, "detail": "/usr/local/bin/nightgauge" },
    "gh": { "ok": true, "detail": "/usr/local/bin/gh" },
    "github_auth": { "ok": true, "detail": "authenticated as octocat" },
    "api_user": { "ok": true, "detail": "octocat" },
    "scopes": { "ok": true, "detail": "repo, project, read:org" },
    "rate_limit": { "ok": true, "detail": "remaining: 4823/5000" },
    "config": { "ok": true, "detail": "configuration loaded" },
    "project": { "ok": true, "detail": "project 42 (owner: nightgauge)" }
  },
  "warnings": [],
  "errors": []
}
```

**Example broken output** (missing GitHub auth):

```json
{
  "v": 1,
  "healthy": false,
  "exit_code": 2,
  "checks": {
    "binary": { "ok": true, "detail": "/usr/local/bin/nightgauge" },
    "gh": { "ok": true, "detail": "/usr/local/bin/gh" },
    "github_auth": {
      "ok": false,
      "error": "GitHub client could not be created — check GITHUB_TOKEN env var or run `gh auth login`"
    },
    "api_user": { "ok": false, "error": "skipped: no authenticated client" },
    "scopes": { "ok": false, "error": "skipped: no authenticated client" },
    "rate_limit": { "ok": false, "error": "skipped: no authenticated client" },
    "config": { "ok": true, "detail": "configuration loaded" },
    "project": { "ok": true, "detail": "project 42 (owner: nightgauge)" }
  },
  "warnings": [],
  "errors": ["GitHub authentication failed — set GITHUB_TOKEN or run `gh auth login`"]
}
```

#### Per-adapter health (`--adapters`)

```bash
nightgauge doctor --adapters codex,claude --json
nightgauge doctor --adapters all          # check every known adapter
```

Adds an `adapters[]` section to the report with deterministic, side-effect-light
facts per execution adapter (Issue #4031). This is **opt-in** — the default
`doctor` run omits the section entirely, so existing skill preflight is
unchanged and fast. An unhealthy adapter is surfaced as a **warning** (exit
code 1), never a required failure: an optional adapter the operator does not use
being uninstalled must not break the environment verdict.

Per adapter the doctor reports:

| Kind   | Adapters                         | `installed` means         | Extra facts                            |
| ------ | -------------------------------- | ------------------------- | -------------------------------------- |
| `cli`  | claude, codex, gemini, copilot   | binary on PATH            | `version`, `version_ok`, `min_version` |
| `sdk`  | claude-sdk, gemini-sdk           | API-key env set           | —                                      |
| `http` | ollama, lm-studio                | local-model env set       | —                                      |

For `codex`, an `mcp` sub-object reports whether `$CODEX_HOME/config.toml` exists
and whether the nightgauge MCP managed block is present.

Auth status (`codex login status`, `claude auth status`, …) is intentionally
**not** probed here — it lives in the SDK adapters and is layered on by the
VSCode **Adapter Doctor** (see [ADAPTER_DOCTOR.md](ADAPTER_DOCTOR.md)). The
`adapters[]` section is the deterministic half; the extension adds auth + a UI.

The `version`/`min_version` floors **mirror** the SDK `MIN_KNOWN_VERSION`
constants (`packages/nightgauge-sdk/src/cli/adapters/*Adapter.ts`); a Go
test guards them against drift.

```json
{
  "v": 1,
  "healthy": true,
  "exit_code": 1,
  "checks": { "...": "..." },
  "warnings": ["adapter \"codex\" not ready: Install the codex CLI and ensure it is on PATH."],
  "errors": [],
  "adapters": [
    {
      "adapter": "codex",
      "kind": "cli",
      "binary": "codex",
      "installed": false,
      "version_ok": false,
      "min_version": "0.111.0",
      "mcp": {
        "config_path": "/Users/me/.codex/config.toml",
        "config_present": true,
        "managed_block": true
      },
      "ok": false,
      "remediation": "Install the codex CLI and ensure it is on PATH."
    },
    {
      "adapter": "claude",
      "kind": "cli",
      "binary": "claude",
      "installed": true,
      "path": "/usr/local/bin/claude",
      "version": "2.1.179",
      "version_ok": true,
      "ok": true
    }
  ]
}
```

### Scan — Dependency Audit

```bash
nightgauge scan deps [--workdir DIR] [--ecosystems nodejs,python,go,rust] [--include-vulns=true|false] [--json]
```

Runs per-ecosystem vulnerability and outdated-package scans by shelling out to
`npm`, `pip-audit`/`pip`, `govulncheck`/`go list`, and `cargo audit`/`cargo
outdated`. Replaces the bash + jq audit chain duplicated across `health-check`,
`security-audit`, and `dep-modernize` (audit row **B3**).

The verb is **non-fatal by design** — when an ecosystem's tooling is not on
PATH, the scan records `available: false` and an entry in `errors[]` instead
of failing. `npm audit` exits non-zero when vulnerabilities are found; that
exit code is treated as informational, not an error.

**Auto-detection** picks an ecosystem when its lockfile/manifest is present in
`--workdir`:

| Ecosystem | Detection files                                  | Audit tool    | Outdated tool    |
| --------- | ------------------------------------------------ | ------------- | ---------------- |
| nodejs    | `package.json`                                   | `npm audit`   | `npm outdated`   |
| python    | `requirements.txt`, `pyproject.toml`, `setup.py` | `pip-audit`   | `pip list`       |
| go        | `go.mod`                                         | `govulncheck` | `go list -m -u`  |
| rust      | `Cargo.toml`                                     | `cargo audit` | `cargo outdated` |

**Flags**:

| Flag                   | Default | Behavior                                                                            |
| ---------------------- | ------- | ----------------------------------------------------------------------------------- |
| `--workdir DIR`        | cwd     | Directory to scan.                                                                  |
| `--ecosystems LIST`    | _all_   | Comma-separated subset. Unknown values exit 2.                                      |
| `--include-vulns=BOOL` | `true`  | Skip the audit (vuln) step when false; outdated step still runs. Useful in slow CI. |
| `--json`               | `false` | Emit JSON instead of human-readable output. Skills always set this.                 |

**Exit codes**:

| Code | Meaning                                                             |
| ---- | ------------------------------------------------------------------- |
| 0    | Scan completed (vulnerabilities may be present — counts, not gates) |
| 1    | Every detected ecosystem reported unavailable tooling (warning)     |
| 2    | Hard error (e.g. invalid `--ecosystems` value, internal failure)    |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge. All four ecosystems always appear in `ecosystems` even when
undetected, so skills can pin to fixed jq paths. `vulnerabilities` is `null`
when the audit step did not run (tool absent, `--include-vulns=false`, or
ecosystem not detected).

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "ecosystems": {
    "nodejs": {
      "detected": true,
      "available": true,
      "vulnerabilities": { "critical": 0, "high": 1, "moderate": 3, "low": 0 },
      "outdated": 12,
      "errors": []
    },
    "python": {
      "detected": true,
      "available": false,
      "vulnerabilities": null,
      "outdated": 0,
      "errors": ["pip-audit not on PATH"]
    },
    "go": {
      "detected": false,
      "available": false,
      "vulnerabilities": null,
      "outdated": 0,
      "errors": []
    },
    "rust": {
      "detected": false,
      "available": false,
      "vulnerabilities": null,
      "outdated": 0,
      "errors": []
    }
  },
  "totals": { "critical": 0, "high": 1, "moderate": 3, "low": 0, "outdated": 12 },
  "warnings": []
}
```

**Severity mapping**: severities below moderate (info, none) and unknown values
fold into `low` to keep the schema closed. Tools that don't expose a per-vuln
severity (currently `pip-audit` and `govulncheck`) fold every finding into
`moderate` and `high` respectively — a safe default that errs toward visible
counts. Skills that need precise severity should not consume those buckets.

**Examples**:

```bash
# Full scan — all detected ecosystems with vulns + outdated
nightgauge scan deps --json

# Outdated-only sweep (skip slow audit calls in CI)
nightgauge scan deps --include-vulns=false --json

# Narrow to two ecosystems
nightgauge scan deps --ecosystems nodejs,python --json

# Pipe directly to jq path
nightgauge scan deps --json | jq '.totals'
```

### Scan — Ecosystem Detection

```bash
nightgauge scan ecosystem [--workdir DIR] [--json]
```

Detects which language ecosystems (nodejs, python, go, rust, java) are present
in `--workdir` and whether the project is a monorepo (npm/yarn/pnpm
workspaces, Cargo workspace, `go.work`). Replaces the bash + jq file-existence
chain duplicated across `health-check`, `security-audit`, `refactor-rewrite`,
and `dep-modernize` Phase 0 (audit row **B1**).

The verb is **non-fatal by design** — malformed manifests and unparseable
workspace declarations are recorded in `warnings[]` rather than causing the
scan to fail. Detection is pure file existence + tiny TOML/JSON parsing; no
subprocess, no network.

**Detection rules**:

| Ecosystem | Manifest files                                   | Lockfile (first match wins)                                                            |
| --------- | ------------------------------------------------ | -------------------------------------------------------------------------------------- |
| nodejs    | `package.json`                                   | `package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`, `pnpm-lock.yaml`, `bun.lockb` |
| python    | `pyproject.toml`, `setup.py`, `requirements.txt` | `poetry.lock`, `Pipfile.lock`, `uv.lock`, `requirements.txt`                           |
| go        | `go.mod`                                         | `go.sum`                                                                               |
| rust      | `Cargo.toml`                                     | `Cargo.lock`                                                                           |
| java      | `pom.xml`, `build.gradle`, `build.gradle.kts`    | _none_ (Maven and Gradle have no canonical single lockfile by default)                 |

**Monorepo discriminators**:

| `monorepo_kind`     | Trigger                                                     |
| ------------------- | ----------------------------------------------------------- |
| `nodejs-workspaces` | `package.json` has top-level `workspaces` (array or object) |
| `cargo-workspace`   | `Cargo.toml` has a `[workspace] members = [...]` table      |
| `go-workspace`      | `go.work` exists with `use ( ./… )` directives              |
| `mixed`             | More than one of the above markers present                  |
| `""`                | None of the above (`is_monorepo=false`)                     |

**Flags**:

| Flag            | Default | Behavior                                                            |
| --------------- | ------- | ------------------------------------------------------------------- |
| `--workdir DIR` | cwd     | Directory to scan.                                                  |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this. |

**Exit codes**:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Scan completed                                           |
| 2    | Hard error (e.g. unresolvable workdir, internal failure) |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge. `lockfiles` is always populated for all five ecosystems
even when undetected, so skills can pin to fixed jq paths. `lockfile`
(singular) is the lockfile of the first alphabetically detected ecosystem,
provided as a convenience for single-ecosystem repos.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "ecosystems": ["go", "nodejs"],
  "is_monorepo": true,
  "monorepo_kind": "nodejs-workspaces",
  "packages": ["packages/foo", "packages/bar"],
  "lockfile": "go.sum",
  "lockfiles": {
    "nodejs": "package-lock.json",
    "python": "",
    "go": "go.sum",
    "rust": "",
    "java": ""
  },
  "warnings": []
}
```

**Examples**:

```bash
# Detect ecosystems + monorepo kind in current directory
nightgauge scan ecosystem --json

# Pipe directly to jq paths
nightgauge scan ecosystem --json | jq '.ecosystems'
nightgauge scan ecosystem --json | jq -r '.monorepo_kind'

# Read into shell arrays for downstream phases
ECO_JSON=$(nightgauge scan ecosystem --json --workdir "$ASSESS_PATH")
ECOSYSTEMS=( $(echo "$ECO_JSON" | jq -r '.ecosystems[]') )
IS_MONOREPO=$(echo "$ECO_JSON" | jq -r '.is_monorepo')
PACKAGES=( $(echo "$ECO_JSON" | jq -r '.packages[]') )
```

### Scan — Secret Pattern Detection

```bash
nightgauge scan secrets [--workdir DIR] [--json]
```

Scans `--workdir` for the six fixed secret patterns codified in the
`security-audit` skill — generic key/value secrets, PEM private key headers,
AWS access keys, JWT/bearer tokens, embedded connection strings, and committed
`.env` files. Replaces the inline `grep -rn ... | wc -l` chains in
`security-audit` Phase 2.2 (audit row **B41**).

The verb is **non-fatal by design** — unreadable files and oversize-skips are
recorded in `warnings[]` rather than causing the scan to fail. Detection is
pure regex over file content; no subprocess, no network. Counts are
**line-based** (each pattern increments at most once per matching line,
mirroring `grep -rn ... | wc -l` behavior) so scoring rubrics calibrated
against the prior implementation remain valid.

**Patterns**:

| `patterns` key      | What it detects                                                                                       | Example regex (excerpt)                                   |
| ------------------- | ----------------------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| `generic_kv`        | Quoted assignments to `api_key`, `password`, `token`, `auth_token`, `access_key`, `private_key`, etc. | `(api[_-]?key\|secret\|password\|...)\s*[:=]\s*['"]…['"]` |
| `pem_private_key`   | PEM private-key headers (RSA / EC / DSA / OPENSSH).                                                   | `BEGIN (RSA \|EC \|DSA \|OPENSSH )?PRIVATE KEY`           |
| `aws_access_key`    | AWS access key IDs (`AKIA…`).                                                                         | `AKIA[0-9A-Z]{16}`                                        |
| `jwt_bearer`        | Long quoted strings assigned to `jwt_secret` or `bearer`.                                             | `(jwt[_-]?secret\|bearer)\s*[:=]\s*['"]…['"]`             |
| `connection_string` | DB URLs with embedded credentials (mysql, postgres, mongodb, redis, amqp).                            | `(mysql\|postgres\|...)://[^:@\s]+:[^@\s]+@`              |
| `dotenv_files`      | Bare `.env` files (`.env.example` / `.env.sample` / `.env.template` excluded).                        | _path-based_                                              |

False-positive filters mirror the SKILL.md exactly: a broad
case-insensitive filter (`example|placeholder|your[_-]?|<|>|REPLACE|TODO|test|mock|fake|dummy`)
suppresses `generic_kv` and `jwt_bearer` matches; a narrower case-sensitive
filter (`example|localhost|127.0.0.1|REPLACE|TODO`) suppresses
`connection_string` matches. The `pem_private_key` and `aws_access_key`
passes have no FP filter — those tokens are treated as hard signals
regardless of surrounding text.

Per-pattern file-extension allowlists also mirror the SKILL.md `--include`
lists. The `aws_access_key` pass intentionally has no allowlist (every file
is in scope) because the original `grep -rn` invocation omitted `--include`.

Excluded directories (pruned at walk time): `.git`, `node_modules`, `vendor`,
`dist`, `build`, `coverage`. Files larger than 5 MiB are skipped with a
warning.

**Flags**:

| Flag            | Default | Behavior                                                            |
| --------------- | ------- | ------------------------------------------------------------------- |
| `--workdir DIR` | cwd     | Directory to scan.                                                  |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this. |

**Exit codes**:

| Code | Meaning                                                     |
| ---- | ----------------------------------------------------------- |
| 0    | Scan completed (matches may be present — counts, not gates) |
| 2    | Hard error (e.g. unresolvable workdir, internal failure)    |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge. `patterns` is always populated for all six pattern keys
even when zero matches were found, so skills can pin to fixed jq paths
without null-handling.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "patterns": {
    "generic_kv": 3,
    "pem_private_key": 0,
    "aws_access_key": 1,
    "jwt_bearer": 0,
    "connection_string": 0,
    "dotenv_files": 0
  },
  "total": 4,
  "warnings": []
}
```

**Examples**:

```bash
# Scan current directory and pipe to jq
nightgauge scan secrets --json | jq '.patterns'

# Read into shell variables for downstream scoring
SECRETS_JSON=$(nightgauge scan secrets --workdir "$ASSESS_PATH" --json)
SECRET_GENERIC=$(echo "$SECRETS_JSON" | jq -r '.patterns.generic_kv')
SECRET_TOTAL=$(echo "$SECRETS_JSON" | jq -r '.total')
```

### Scan — Debt Markers (TODO/FIXME/HACK/XXX)

```bash
nightgauge scan debt [--workdir DIR] [--json]
```

Walks `--workdir` counting `TODO`, `FIXME`, `HACK`, `XXX` comment markers in
files matching the source-extension allowlist (`.ts`, `.tsx`, `.js`, `.jsx`,
`.py`, `.go`, `.rs`, `.java`, `.kt`). Replaces the inline `grep -cE 'TODO|FIXME|HACK|XXX' | awk`
chain in `health-check` Phase 3.1 and the equivalent pass in `refactor-rewrite`
Phase 2.2 (audit row **B5**). This PR migrates `health-check` Phase 3.1 as the
proof consumer; the `refactor-rewrite` migration is deferred under the same
B5 banner.

The verb is **non-fatal by design** — oversize files (>5 MiB) and unreadable
files surface as `warnings[]` entries rather than errors. Counts are
**line-based**: each marker increments at most once per matching line,
mirroring `grep -cE 'TODO|FIXME|HACK|XXX' file | awk '{sum+=$NF}'` behavior so
existing scoring rubrics (e.g. `<5 markers → 90-100 quality`,
`>100 markers → 0-29`) remain calibrated.

Word boundaries are enforced (`\bTODO\b`) so `TODOIST` does NOT match `TODO`.
The original `grep -cE` does not enforce boundaries; rubric tolerances are
wide enough that the slight tightening keeps scoring calibrated. `Files`
counts the number of source files containing at least one marker line —
useful when the rubric weights "spread across many files" differently than
"concentrated in one".

Excluded directories (pruned at walk time): `.git`, `node_modules`, `vendor`,
`dist`, `build`, `coverage`.

**Flags**:

| Flag            | Default | Behavior                                                            |
| --------------- | ------- | ------------------------------------------------------------------- |
| `--workdir DIR` | cwd     | Directory to scan.                                                  |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this. |

**Exit codes**:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Scan completed                                           |
| 2    | Hard error (e.g. unresolvable workdir, internal failure) |

**JSON output** (`--json`): Schema version `v: 1` — field names and marker
keys are stable after first merge. `markers` is a fixed-shape object so all
four marker keys plus `total` are always present.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "markers": {
    "todo": 109,
    "fixme": 20,
    "hack": 12,
    "xxx": 14,
    "total": 155
  },
  "files": 15,
  "warnings": []
}
```

**Examples**:

```bash
# Total marker count via jq
nightgauge scan debt --json | jq -r '.markers.total'

# Read into shell variables for the health-check rubric
DEBT_JSON=$(nightgauge scan debt --workdir "$ASSESS_PATH" --json)
DEBT_TOTAL=$(echo "$DEBT_JSON" | jq -r '.markers.total')
DEBT_FILES=$(echo "$DEBT_JSON" | jq -r '.files')
```

### Scan — Test/Source Ratio

```bash
nightgauge scan tests [--workdir DIR] [--json]
```

Walks `--workdir` counting test files (basenames matching `*.test.*`,
`*.spec.*`, `*_test.*`, `test_*`) versus source files (same extension
allowlist as `scan debt`, minus test files). Pure path classification — no
file content is read. Replaces the parallel-Glob test/source counting in
`health-check` Phase 2.2 and the equivalent inline pass in `refactor-rewrite`
Phase 2.1 (audit row **B5**).

The ratio is `float64`, set to `0` when `source_files == 0` (explicit
zero-source guard, not NaN). A file matching a test pattern is NEVER also
counted as a source file.

Excluded directories (pruned at walk time): `.git`, `node_modules`, `vendor`,
`dist`, `build`, `coverage`.

**Flags**:

| Flag            | Default | Behavior                                                            |
| --------------- | ------- | ------------------------------------------------------------------- |
| `--workdir DIR` | cwd     | Directory to scan.                                                  |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this. |

**Exit codes**:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Scan completed                                           |
| 2    | Hard error (e.g. unresolvable workdir, internal failure) |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "source_files": 750,
  "test_files": 757,
  "test_to_source_ratio": 1.0093333333333334,
  "warnings": []
}
```

**Examples**:

```bash
# Three-line read for the health-check rubric
TESTS_JSON=$(nightgauge scan tests --workdir "$ASSESS_PATH" --json)
TEST_FILE_COUNT=$(echo "$TESTS_JSON" | jq -r '.test_files')
SOURCE_FILE_COUNT=$(echo "$TESTS_JSON" | jq -r '.source_files')
TEST_RATIO=$(echo "$TESTS_JSON" | jq -r '.test_to_source_ratio')
```

### Test — Inventory and Risk Scoring

Two deterministic verbs absorb the inline Glob + grep + git log shell from
`skills/nightgauge-test-scaffold/SKILL.md` Phases 1 (Steps 1.1–1.4) and
Phase 3 (Steps 3.1–3.5). Implements audit row **B39**.

The scoring tables (criticality boosts, branching/commits/importer buckets,
priority thresholds) reproduce the SKILL.md prose verbatim — they are part
of the v1 contract. Both verbs share the source-extension allowlist and
excluded-dir set used by `scan tests`: extensions `.ts`, `.tsx`, `.js`,
`.jsx`, `.py`, `.go`, `.rs`, `.java`, `.kt`; pruned dirs `.git`,
`node_modules`, `vendor`, `dist`, `build`, `coverage`.

#### `test inventory`

```bash
nightgauge test inventory [--workdir DIR] [--json]
```

Walks `--workdir`, classifies each file as source or test (basename
patterns `*.test.*`, `*.spec.*`, `*_test.*`, `test_*`), derives the
test→source mapping by stripping the test suffix, and lists source files
with no matching test. Pure path classification — no file content is read.
Paths in the result are workdir-relative with POSIX-style separators so
the output can pipe directly into `test risk-score --files`.

**Flags**:

| Flag            | Default | Behavior                                                            |
| --------------- | ------- | ------------------------------------------------------------------- |
| `--workdir DIR` | cwd     | Directory to scan.                                                  |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this. |

**Exit codes**:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Inventory completed                                      |
| 2    | Hard error (e.g. unresolvable workdir, internal failure) |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "counts": {
    "source_files": 750,
    "test_files": 312,
    "untested_files": 438
  },
  "source_files": ["src/a.ts", "src/b.ts"],
  "test_files": ["src/a.test.ts"],
  "test_to_source_mapping": { "src/a.test.ts": "src/a.ts" },
  "untested_files": ["src/b.ts"],
  "warnings": []
}
```

#### `test risk-score`

```bash
nightgauge test risk-score (--files PATH | --stdin) [--workdir DIR] [--json]
```

Scores each input file by combining four sub-scores:

- **business_criticality** — case-insensitive substring match against the
  Phase 3.1 priority order (payment/billing/checkout `+40`, auth/session
  `+35`, router/handler/controller `+25`, middleware/interceptor `+20`,
  service/repository `+15`, util/helper `+5`). First match wins.
- **complexity** — count of branching keywords (`if`, `else`, `switch`,
  `case`, `for`, `while`, `try`, `catch`, `&&`, `||`); bucketed `0–5 →
  +5`, `6–15 → +15`, `16–30 → +25`, `31+ → +35`.
- **change_frequency** — `git -C <workdir> log --since="6 months ago"`
  line count for the file; bucketed `0–2 → +0`, `3–5 → +10`, `6–15 →
  +20`, `16+ → +30`. Non-git workdirs return `0` and emit a single
  deduped warning.
- **dependency_depth** — count of files within the source allowlist that
  contain the file's basename-stem as a substring (excluding the file
  itself); bucketed `0–1 → +0`, `2–5 → +10`, `6–10 → +20`, `11+ → +30`.
  This is an approximation, NOT an import-graph traversal.

Composite is `min(100, sum)`; priority is `critical` (80–100), `high`
(60–79), `medium` (40–59), `low` (0–39). Entries are sorted by score
descending, then by file path ascending, for stable ordering.

**Input**: `--files PATH` reads newline-delimited paths from a file
(blank lines and lines starting with `#` are ignored). `--stdin` reads
the same format from standard input. Paths may be absolute or
workdir-relative.

**Flags**:

| Flag            | Default | Behavior                                                                  |
| --------------- | ------- | ------------------------------------------------------------------------- |
| `--files PATH`  | —       | Path to a newline-delimited file list (mutually exclusive with `--stdin`) |
| `--stdin`       | `false` | Read newline-delimited file list from stdin                               |
| `--workdir DIR` | cwd     | Project root for `git log` + importer scans.                              |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this.       |

**Exit codes**:

| Code | Meaning                                          |
| ---- | ------------------------------------------------ |
| 0    | Scoring completed                                |
| 2    | Hard error (bad flag, input read error)          |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "entries": [
    {
      "file": "src/checkout.ts",
      "business_criticality": 40,
      "complexity": 25,
      "change_frequency": 20,
      "dependency_depth": 10,
      "score": 95,
      "priority": "critical"
    }
  ],
  "warnings": []
}
```

**Examples**:

```bash
# Pipe inventory's untested list straight into risk-score
nightgauge test inventory --workdir . --json | \
  jq -r '.untested_files[]' > /tmp/untested.txt
nightgauge test risk-score --files /tmp/untested.txt --workdir . --json | \
  jq '.entries[0:5]'

# Or via stdin
nightgauge test inventory --json | jq -r '.untested_files[]' | \
  nightgauge test risk-score --stdin --json | \
  jq -r '.entries[] | select(.priority=="critical") | .file'
```

### Scan — Linter / Formatter Tooling

```bash
nightgauge scan tooling [--workdir DIR] [--json]
```

Stat-probes `--workdir` for canonical linter and formatter config files at
the repo root. Linters: `eslint`, `ruff`, `golangci`, `clippy`, `flake8`,
`pylint`, `checkstyle`. Formatters: `prettier`, `editorconfig`, `black`,
`ruff_format`. Additionally, when `pyproject.toml` exists at root, the verb
detects `[tool.ruff]` / `[tool.black]` / `[tool.ruff.format]` sections via
anchored multiline regex (line-start match against TOML section headers).

Replaces the linter/formatter probe chains in `health-check` Phase 3.2 and
the equivalent inline pass in `refactor-rewrite` Phase 2.2 (audit row
**B5**).

Detection rules mirror the SKILL.md sources exactly — no new linters or
formatters are added in this verb. Probes are O(linterCount +
formatterCount) `os.Stat` calls plus one bounded `pyproject.toml` read
(capped at 1 MiB). No directory walk.

All keys in `linters` / `formatters` are pre-populated with `false` so
consumer jq paths never resolve to null. The convenience booleans
`linter_present` / `formatter_present` are `true` when ANY corresponding
key is `true`.

**Flags**:

| Flag            | Default | Behavior                                                            |
| --------------- | ------- | ------------------------------------------------------------------- |
| `--workdir DIR` | cwd     | Directory to probe.                                                 |
| `--json`        | `false` | Emit JSON instead of human-readable output. Skills always set this. |

**Exit codes**:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Scan completed                                           |
| 2    | Hard error (e.g. unresolvable workdir, internal failure) |

**JSON output** (`--json`): Schema version `v: 1` — field names and the
linter / formatter keys are stable after first merge.

```json
{
  "v": 1,
  "workdir": "/abs/path/scanned",
  "linters": {
    "eslint": true,
    "ruff": false,
    "golangci": false,
    "clippy": false,
    "flake8": false,
    "pylint": false,
    "checkstyle": false
  },
  "formatters": {
    "prettier": true,
    "editorconfig": true,
    "black": false,
    "ruff_format": false
  },
  "linter_present": true,
  "formatter_present": true,
  "warnings": []
}
```

**Examples**:

```bash
# Boolean check for the health-check rubric
TOOLING_JSON=$(nightgauge scan tooling --workdir "$ASSESS_PATH" --json)
LINTER_PRESENT=$(echo "$TOOLING_JSON" | jq -r '.linter_present')
FORMATTER_PRESENT=$(echo "$TOOLING_JSON" | jq -r '.formatter_present')

# Per-tool detection
echo "$TOOLING_JSON" | jq -r '.linters.golangci'
```

### Docs — Markdown Link Validation

```bash
nightgauge docs check-links [--root DIR] [--target FILE] [--section NAME] [--exclude-templates] [--json]
```

Walks `--root` for `*.md` files and verifies every relative Markdown link
resolves to an existing path. Replaces the bash + `dirname` + `grep` link
chain duplicated across `docs-write` Phase 7 and `update-docs` Phase 4.5
(audit row **B6**). This PR migrates `docs-write` Phase 7 as the proof
consumer; the `update-docs` Phase 4.5 migration is deferred under the same
B6 banner.

The verb is **non-fatal by design** — missing files become entries in
`findings[]`; unreadable files become entries in `warnings[]`. Hard input
errors (unresolvable root, target outside root) exit 2.

External links (`http://`, `https://`, `mailto:`, `tel:`, `ftp://`, `//`)
and in-page anchors (`#section`) are ignored — skills excluded the same
set from their grep patterns. Code-fence content (` ``` ` and `~~~`) is
skipped, matching the AWK fence-toggle filter the skills already used.
Anchors are recorded verbatim in `findings[].anchor` but the anchor target
is not validated in v1.

**Auto-skipped directories** (never descended into): `node_modules`,
`.git`, `dist`, `build`, `coverage`, `.next`, `out`.

**Flags**:

| Flag                  | Default | Behavior                                                                                              |
| --------------------- | ------- | ----------------------------------------------------------------------------------------------------- |
| `--root DIR`          | cwd     | Directory tree to scan.                                                                               |
| `--target FILE`       | _none_  | Restrict validation to a single markdown file (relative to `--root`, or absolute). Errors if outside. |
| `--section NAME`      | _none_  | Restrict validation to links inside the named heading subtree (case-insensitive, ATX headings).       |
| `--exclude-templates` | `false` | Skip `*/skills/*/SKILL.md` and `*/claude-plugins/*/commands/*` files (template content).              |
| `--json`              | `false` | Emit JSON instead of human-readable output. Skills always set this.                                   |

**Exit codes**:

| Code | Meaning                                                  |
| ---- | -------------------------------------------------------- |
| 0    | Scan completed, no broken links                          |
| 1    | Scan completed, one or more broken links found           |
| 2    | Hard error (e.g. unresolvable root, target outside root) |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge. `reason` is a closed enum: `file_not_found`,
`outside_root`, `unreadable`.

```json
{
  "v": 1,
  "root": "/abs/path/scanned",
  "files_scanned": 42,
  "links_total": 318,
  "links_broken": 1,
  "findings": [
    {
      "file": "docs/GO_BINARY.md",
      "line": 612,
      "link": "../missing/FILE.md",
      "resolved": "/abs/path/missing/FILE.md",
      "anchor": "",
      "reason": "file_not_found"
    }
  ],
  "warnings": []
}
```

**Examples**:

```bash
# Validate every relative link in the repo (excluding template files)
nightgauge docs check-links --root . --exclude-templates --json

# Validate links inside a specific section of one file
nightgauge docs check-links --target docs/ARCHITECTURE.md --section "Pipeline Lifecycle" --json

# Pipe directly to jq for the broken-link list
nightgauge docs check-links --root . --json | jq '.findings[] | "\(.file):\(.line)  \(.link)  [\(.reason)]"'

# CI: fail the pipeline when broken links appear
nightgauge docs check-links --root . --exclude-templates  # exit 1 on broken
```

### Docs — Snapshot Diffing

```bash
nightgauge docs snapshot-diff --snapshot <snapshot.json> --urls <urls.txt> [--json]
```

Fetches each URL in `--urls`, computes its sha256 hash, and compares against
hashes recorded in `--snapshot`. Replaces the bash + curl + sha256sum chain
in `docs-watch` Phase 4 (audit row **B34**).

**Flags**

| Flag           | Required | Description                                    |
| -------------- | -------- | ---------------------------------------------- |
| `--snapshot`   | yes      | Path to existing snapshot JSON (`pages` object)|
| `--urls`       | yes      | Text file with one URL per line                |
| `--json`       | no       | Emit stable JSON (default: human-readable)     |

**JSON output schema** (v1 — stable):

```json
{
  "v": 1,
  "new":     [{ "url": "...", "hash": "..." }],
  "changed": [{ "url": "...", "hash": "...", "old_hash": "..." }],
  "removed": [{ "url": "..." }],
  "warnings": ["fetch https://...: HTTP 404"]
}
```

Fetch failures for individual URLs are appended to `warnings[]` and skipped
— they do not cause a non-zero exit. Hard input errors (missing files,
malformed JSON) exit 2.

**Exit codes**: `0` completed, `2` hard error.

**Examples**

```bash
# docs-watch Phase 4 — detect content changes
nightgauge docs snapshot-diff \
  --snapshot /tmp/docs-watch-snapshot.json \
  --urls /tmp/docs-watch-urls.txt \
  --json \
  > /tmp/docs-watch-diff.json

# Summarise result with jq
nightgauge docs snapshot-diff --snapshot snap.json --urls urls.txt --json | \
  jq '{new: (.new|length), changed: (.changed|length), removed: (.removed|length)}'
```

### Docs — Pattern Detection

```bash
nightgauge docs detect-patterns --files <glob> [--json]
```

Expands `--files` glob and searches each matched file for keywords belonging to
a closed set of 7 architectural pattern slugs. Replaces the inline bash grep
loop in `docs-write` Phase 1.5 Step 1.5.1 (audit row **B35**).

**Flags**

| Flag      | Required | Description                                    |
| --------- | -------- | ---------------------------------------------- |
| `--files` | yes      | Glob pattern to match source files             |
| `--json`  | no       | Emit stable JSON (default: human-readable)     |

**Pattern slug reference table**

| Slug               | Keywords                                                       |
| ------------------ | -------------------------------------------------------------- |
| `event-system`     | `EventEmitter`, `on(`, `.emit(`, `_onDid`, `vscode.EventEmitter` |
| `auth-security`    | `authenticate`, `authorize`, `middleware`, `guard`, `validateToken` |
| `service-pattern`  | `class.*Service`, `class.*Manager`, `class.*Provider`          |
| `repo-storage`     | `class.*Repository`, `class.*Store`, `db.query`, `prisma.`    |
| `config-system`    | `config`, `settings`, `schema`, `zod`, `Config`               |
| `pipeline-workflow`| `stage`, `orchestrat`, `pipeline`, `PipelineOrchestrator`     |
| `ipc-transport`    | `stdio`, `ipc`, `socket`, `exec`, `spawn`                     |

Only slugs with at least one matching file appear in the output.

**JSON output schema** (v1 — stable):

```json
{
  "v": 1,
  "patterns": [
    { "slug": "event-system", "files": ["packages/extension/src/EventBus.ts"] }
  ],
  "warnings": []
}
```

Unreadable files append a warning and are skipped — they do not cause a
non-zero exit. Invalid glob syntax exits 2.

**Exit codes**: `0` completed (including zero matches), `2` hard error (invalid
glob or missing `--files`).

**Examples**

```bash
# docs-write Phase 1.5 — detect patterns in TypeScript source
nightgauge docs detect-patterns --files "packages/**/*.ts" --json | jq .

# Human-readable summary
nightgauge docs detect-patterns --files "internal/**/*.go"

# Parse matched files for a specific slug
nightgauge docs detect-patterns --files "src/**/*.ts" --json | \
  jq '.patterns[] | select(.slug == "service-pattern") | .files[]'

# Glob matching no files — exits 0 with empty patterns
nightgauge docs detect-patterns --files "nonexistent/*.go" --json
```

### Docs — Version Consistency

```bash
nightgauge docs version-consistency [--root DIR] [--json]
```

Detect project type from root directory markers and validate that version
references in markdown files match the authoritative source-of-truth version.
Replaces the bash project-type detection and version extraction prose in
update-docs Phase 4.6 (audit row **B36**).

**Project type detection order**: `package.json` (nodejs) → `pyproject.toml` /
`setup.py` (python) → `Cargo.toml` (rust) → `go.mod` / `VERSION` (go) →
`*.csproj` (dotnet) → `skills/` directory (skills).

**Flags**:

| Flag | Default | Description |
|------|---------|-------------|
| `--root DIR` | CWD | Directory tree to scan |
| `--json` | false | Emit JSON output (parsed by skills) |

**Exit codes**: `0` = no mismatches, `1` = mismatches found, `2` = hard error.

**JSON schema (v1)**:

```json
{
  "v": 1,
  "root": "/abs/path",
  "project_type": "nodejs|python|go|rust|dotnet|skills|unknown",
  "source_file": "package.json",
  "source_version": "1.2.3",
  "mismatches": [
    {
      "file": "docs/INSTALL.md",
      "line": 42,
      "context": "Install version: 1.2.2 ...",
      "found_version": "1.2.2",
      "expected_version": "1.2.3"
    }
  ],
  "mismatches_count": 1,
  "warnings": []
}
```

**Examples**:

```bash
# Validate version consistency in current directory
nightgauge docs version-consistency --root . --json | jq '.'

# List mismatches only
nightgauge docs version-consistency --root . --json | \
  jq '.mismatches[] | "\(.file):\(.line) expected \(.expected_version) got \(.found_version)"'

# Human-readable output
nightgauge docs version-consistency --root .
```

### Docs — Freshness Check

```bash
nightgauge docs check-freshness [--root DIR] [--json]
```

Walk `--root` for `*.md` files, extract `Updated: YYYY-MM-DD` metadata lines,
and compare each documented date against the most recent git commit date for
that file. Files whose git commit date is newer than their documented date are
flagged as stale. Replaces the bash + git log prose in update-docs Phase 4.8
(audit row **B36**).

**Patterns detected** (case-insensitive, code fences skipped):

- `Updated: 2026-01-22`
- `**Updated**: 2026-01-22`
- `| Updated | 2026-01-22 |`

**Flags**:

| Flag | Default | Description |
|------|---------|-------------|
| `--root DIR` | CWD | Directory tree to scan |
| `--json` | false | Emit JSON output (parsed by skills) |

**Exit codes**: `0` = no stale dates, `1` = stale dates found, `2` = hard error.

**JSON schema (v1)**:

```json
{
  "v": 1,
  "root": "/abs/path",
  "files_scanned": 42,
  "files_with_updated_metadata": 15,
  "stale_findings": [
    {
      "file": "docs/SETUP.md",
      "line": 3,
      "documented_date": "2026-01-15",
      "git_date": "2026-04-20",
      "days_stale": 95
    }
  ],
  "stale_count": 1,
  "warnings": []
}
```

**Examples**:

```bash
# Check for stale Updated: dates
nightgauge docs check-freshness --root . --json | jq '.'

# List stale files only
nightgauge docs check-freshness --root . --json | \
  jq '.stale_findings[] | "\(.file):\(.line) (\(.days_stale) days stale)"'

# Human-readable output
nightgauge docs check-freshness --root .
```

### Pipeline Analysis

```bash
nightgauge pipeline aggregate [--runs N] [--since YYYY-MM-DD] [--until YYYY-MM-DD] [--issue N] [--include analysis] [--json] [--workdir DIR]
```

Aggregate per-stage durations, token counts, model usage, and per-run cost
metrics from `.nightgauge/pipeline/history/YYYY-MM-DD.jsonl`. Replaces the
~300 lines of inline-Python aggregation duplicated across `pipeline-audit`,
`pipeline-health`, `retro`, and `continuous-improvement` (audit row **B2**).
This PR migrates `pipeline-audit` Phase 2.1 as the proof consumer; the other
three skills are deferred under the same B2 banner.

The verb is **non-fatal by design** — zero records is not an error, missing
history directories return an empty schema, and unknown `--include` values
produce a warning rather than failing.

**Flags**:

| Flag                 | Default | Behavior                                                                               |
| -------------------- | ------- | -------------------------------------------------------------------------------------- |
| `--runs N`           | `0`     | Limit to last N runs by `recorded_at`. `0` means unbounded.                            |
| `--since YYYY-MM-DD` | _none_  | Lower bound (lexicographic filename pre-filter + per-record `started_at` check).       |
| `--until YYYY-MM-DD` | _none_  | Upper bound (forward-compat with pipeline-health's `--until-date`).                    |
| `--issue N`          | `0`     | Filter to a single issue number. `0` means all issues.                                 |
| `--include LIST`     | `""`    | Optional analysis blocks (comma-separated). Currently only `analysis` is recognized.   |
| `--json`             | `false` | Emit JSON instead of human-readable output. Skills always set this.                    |
| `--workdir DIR`      | cwd     | History root (the `.nightgauge/pipeline/history` directory is resolved under it). |

**`--include analysis`** adds the size-accuracy / weekly-trend block that the
pipeline-audit skill needs (Issue #1591). Other consuming skills do not need
this block — leave the flag off to skip the extra work.

**Exit codes**:

| Code | Meaning                                                                         |
| ---- | ------------------------------------------------------------------------------- |
| 0    | Aggregate completed (zero records is not an error — emits empty schema)         |
| 1    | History directory missing AND `--workdir` was explicit (callers may ignore)     |
| 2    | Hard error (invalid flag value such as a malformed `--since`, internal failure) |

**JSON output** (`--json`): Schema version `v: 1` — field names are stable
after first merge. The `analysis` block is omitted entirely when
`--include analysis` is not set.

```json
{
  "v": 1,
  "runs_analyzed": 12,
  "date_from": "2026-04-15",
  "date_to": "2026-04-22",
  "filters": { "runs": 12, "since": "", "until": "", "issue": 0 },
  "runs": [
    {
      "issue_number": 42,
      "title": "feat: add foo",
      "outcome": "complete",
      "total_duration_ms": 813341,
      "started_at": "2026-04-22T10:00:00Z",
      "total_input": 304,
      "total_output": 30158,
      "total_cache_read": 5350629,
      "total_cache_creation": 154560,
      "estimated_cost_usd": 3.56,
      "labels": ["type:feature"],
      "size": "M",
      "type": "feature",
      "priority": "high",
      "skipped_stages": []
    }
  ],
  "stage_metrics": {
    "feature-dev": {
      "status": { "complete": 12 },
      "duration_stats": {
        "count": 12,
        "median": 120000,
        "mean": 130000,
        "p90": 180000,
        "min": 50000,
        "max": 220000
      },
      "token_stats": {
        "input": { "count": 12, "median": 100, "mean": 110, "p90": 200, "min": 50, "max": 250 },
        "output": {
          "count": 12,
          "median": 2000,
          "mean": 2100,
          "p90": 3500,
          "min": 1000,
          "max": 4000
        },
        "cache_read": {
          "count": 12,
          "median": 400000,
          "mean": 410000,
          "p90": 600000,
          "min": 100000,
          "max": 700000
        },
        "cache_creation": {
          "count": 12,
          "median": 20000,
          "mean": 22000,
          "p90": 35000,
          "min": 10000,
          "max": 40000
        }
      },
      "models": { "claude-sonnet-4-6": 10, "claude-opus-4-7": 2 },
      "model_sources": { "auto": 8, "config": 4 }
    }
  },
  "model_usage": {
    "by_stage": { "feature-dev": { "claude-sonnet-4-6": 10 } },
    "by_source": { "feature-dev": { "auto": 8 } }
  },
  "analysis": {
    "size_baselines": {
      "S": {
        "count": 5,
        "median_cost": 1.0,
        "avg_cost": 1.1,
        "min_cost": 0.5,
        "max_cost": 1.5,
        "median_duration_ms": 200000,
        "avg_duration_ms": 220000
      }
    },
    "size_accuracy_rates": { "S": { "total": 5, "within_range": 5, "accuracy_pct": 100.0 } },
    "oversized": [],
    "undersized": [],
    "weekly_accuracy": [{ "week": "2026-W17", "total": 12, "accurate": 10, "accuracy_pct": 83.3 }],
    "runs_with_size": 10,
    "runs_without_size": 2
  },
  "warnings": []
}
```

**Schema notes**:

- `runs[]` is the per-run array (renamed from the previous Python aggregator's
  `run_metrics`). `stage_metrics.<stage>` collapses the previous
  `stage_durations` / `stage_statuses` / `stage_tokens` into a single nested
  object.
- `duration_stats` and `token_stats` use linear interpolation between adjacent
  ranks for `p90` (matches numpy.percentile default). `median` is the average
  of the two middle values for even-length lists (matches Python's
  `statistics.median`).
- The aggregator does **not** recompute cache hit rate. Skills that need it
  should read `V2StageTokens.CacheHitRate` from the source records (or compute
  it inline) — drift between Python copies of that formula was the failure
  mode this verb was introduced to prevent.
- `analysis.weekly_accuracy` uses Go's `time.ISOWeek`, which produces the same
  year/week values as Python's `datetime.isocalendar()` for ISO 8601 dates.

**Examples**:

```bash
# Aggregate last 30 runs with the size-accuracy block
nightgauge pipeline aggregate --runs 30 --include analysis --json

# Filter to a single issue across all history
nightgauge pipeline aggregate --issue 3060 --json

# Date-bounded sweep
nightgauge pipeline aggregate --since 2026-04-01 --until 2026-04-22 --json

# Skill consumer pattern (matches health-check Phase 1.1's || fallback)
nightgauge pipeline aggregate \
  --runs "${RUNS_LIMIT}" \
  ${SINCE_DATE:+--since "${SINCE_DATE}"} \
  ${ISSUE_FILTER:+--issue "${ISSUE_FILTER}"} \
  --include analysis \
  --workdir . \
  --json > /tmp/audit_extracted.json 2>/dev/null \
  || echo '{"v":1,"runs_analyzed":0,"runs":[],"stage_metrics":{},"model_usage":{"by_stage":{},"by_source":{}},"warnings":["pipeline aggregate failed"]}' > /tmp/audit_extracted.json
```

### Modernize — Assessment Aggregation

#### modernize aggregate-findings

```bash
nightgauge modernize aggregate-findings [--workdir DIR] [--out FILE] [--json]
```

Reads the three `.nightgauge/` assessment reports (health, security,
test scaffold), applies severity normalization, deduplicates overlapping
findings, and outputs a single stable JSON structure. Replaces the shell+jq
extraction previously inlined in modernize-plan SKILL.md Phase 2.1–2.4
(audit row **B31**).

**Input files** (read from `--workdir/.nightgauge/`):

| File | Produced by |
| ---- | ----------- |
| `health-report.json` | `/nightgauge:health-check` |
| `security-audit.json` | `/nightgauge:security-audit` |
| `test-scaffold-report.json` | `/nightgauge:test-scaffold` |

At least one input file must be present. Missing files are listed in
`sources_missing` and do not cause a non-zero exit.

**Flags**:

| Flag | Default | Behavior |
| ---- | ------- | -------- |
| `--workdir DIR` | cwd | Project root containing `.nightgauge/` |
| `--out FILE` | — | Write JSON output to file instead of stdout |
| `--json` | `false` | Emit JSON to stdout (skills always set this) |

**Severity normalization** (health-check only — security-audit passes through):

| Health-check status | Canonical severity |
| ------------------- | ------------------ |
| `critical` | `critical` |
| `poor` | `high` |
| `fair` | `medium` |
| `good` | `low` |
| `excellent` | `info` |
| _(unknown)_ | `info` |

**Deduplication**: key = `source_dimension + "::" + lowercase(title)`. When
two findings share a key, the one with the longer `recommendation` string is
kept; the other's ID is recorded in `merged_from`.

**Exit codes**:

| Code | Meaning |
| ---- | ------- |
| 0 | Success |
| 2 | Hard error (no inputs found, I/O failure, malformed JSON) |

**Output schema (v1)**:

```json
{
  "v": 1,
  "sources_read": ["health-check", "security-audit"],
  "sources_missing": ["test-scaffold"],
  "findings": [
    {
      "id": "health-check::dependency_health::0",
      "title": "Outdated lodash",
      "description": "lodash is 4 major versions behind",
      "recommendation": "Run npm update lodash",
      "source": "health-check",
      "source_dimension": "dependency_health",
      "severity": "high",
      "merged_from": []
    }
  ],
  "summary": {
    "total_findings": 42,
    "after_dedup": 38,
    "by_severity": {"critical": 2, "high": 8, "medium": 15, "low": 10, "info": 3},
    "deduplication_rate": 0.095
  },
  "generated_at": "2026-05-16T12:00:00Z"
}
```

**Skill consumer** (modernize-plan Phase 2.1–2.4):

```bash
AGGREGATE_OUT=$(mktemp /tmp/aggregate-findings-XXXXXX.json)
nightgauge modernize aggregate-findings \
  --workdir "$ASSESS_PATH" \
  --out "$AGGREGATE_OUT"
cat "$AGGREGATE_OUT" | jq '.summary'
```

---

### Configuration

```bash
nightgauge config show [--key <dotted.path>] [--json] [--raw]
```

Renders the merged effective configuration loaded by `internal/config.Load`,
emitted in the canonical on-disk YAML schema regardless of which on-disk
format (nested or legacy flat) was used. Replaces brittle `grep | awk` and
`yq` patterns scattered across SKILL.md files (audit row B11).

**Flags**:

| Flag           | Default | Behavior                                                                                        |
| -------------- | ------- | ----------------------------------------------------------------------------------------------- |
| `--key <path>` | _none_  | Print only this dotted path (e.g. `project.number`, `autonomous`). Omit to print full config.   |
| `--json`       | `false` | Emit JSON instead of YAML. Combinable with `--key`.                                             |
| `--raw`        | `false` | Strip YAML quoting and trailing newline from a scalar leaf. Requires `--key` on a scalar value. |

**Key syntax**: dotted mapping paths only (e.g. `project.owner`,
`autonomous.scan_interval`). Sequence indexing is not supported — sub-documents
containing sequences are emitted as YAML/JSON sub-trees.

**Exit codes**:

| Code | Meaning                                                              |
| ---- | -------------------------------------------------------------------- |
| 0    | Success — value or sub-document printed to stdout                    |
| 1    | Failure — config could not be loaded, key not found, or invalid args |

When `--key` resolves to nothing, `key not found: <path>` is printed to stderr
and the binary exits 1. This makes it safe to use as a deterministic value
source in shell scripts with a `||` fallback:

```bash
PROJECT_NUMBER=$(nightgauge config show --key project.number --raw 2>/dev/null \
  || grep "number:" .nightgauge/config.yaml | awk '{print $2}')
```

**Examples**:

```bash
# Full effective config as YAML
nightgauge config show

# A single scalar value (shell-friendly)
nightgauge config show --key project.number --raw

# A sub-document as JSON
nightgauge config show --key autonomous --json
```

> Source attribution (`[default|global|project|env]` annotations), env-var
> overlay, and global-config merging are intentionally out of scope for this
> verb. Those richer views remain in the user-invocable
> `nightgauge-config-show` skill.

```bash
nightgauge config init --owner <login> [--owner-type org|user] [--repo <name>] \
  [--project <N>] [--out <path>] [--force] [--no-fetch] [--json]
```

Renders the canonical `.nightgauge/config.yaml` template (audit row
B10). Replaces the inline YAML heredocs in `nightgauge-repo-init` Phase
6 and `smart-setup` Step 5.8 with one deterministic verb so both skills
agree on the exact template shape — header, comments, ordering, and the
nested `project.fields.{status,priority,size}` schema understood by the Go
parser.

**Modes**:

- **Offline** (no `--project`, or `--no-fetch`): emit the template with
  `<PROJECT_NUMBER>` / `<*_OPTION_ID>` tokens intact. Suitable for
  dev-container bootstrap or before the GitHub project exists.
- **Online** (`--project N`): query GitHub once via the existing
  `internal/github.ProjectService.SnapshotFields` cache, substitute the
  discovered project ID + Status/Priority/Size field IDs, and emit a
  fully-populated config.

**Flags**:

| Flag              | Default                        | Behavior                                                                        |
| ----------------- | ------------------------------ | ------------------------------------------------------------------------------- |
| `--owner <login>` | _required_                     | GitHub project owner (org or user login)                                        |
| `--owner-type`    | `org`                          | `org` or `user` — must match the project's owner type                           |
| `--repo <name>`   | _empty_                        | Default repo name; empty emits the `<REPO_NAME>` placeholder                    |
| `--project <N>`   | `0`                            | Project V2 number; `0` emits placeholders for every project ID                  |
| `--out <path>`    | `.nightgauge/config.yaml` | Output path; use `-` to write to stdout                                         |
| `--force`         | `false`                        | Overwrite an existing file at `--out`                                           |
| `--no-fetch`      | `false`                        | Skip GitHub queries even when `--project` is set; emit field-ID placeholders    |
| `--json`          | `false`                        | After writing, print `{"path":"...","wrote":true}` to stdout (machine-readable) |

**Exit codes**:

| Code | Meaning                                                                       |
| ---- | ----------------------------------------------------------------------------- |
| 0    | Success — file written (or printed to stdout when `--out -`)                  |
| 1    | Failure — missing required flag, GraphQL error, file exists without `--force` |

**Examples**:

```bash
# Offline: placeholder template safe to commit
nightgauge config init --owner nightgauge --repo nightgauge

# Online: fully populated against GitHub project board #1
nightgauge config init --owner nightgauge --owner-type org \
  --repo nightgauge --project 1

# Replace an existing config (matches "Replace with fresh config" prompt)
nightgauge config init --owner nightgauge --project 1 --force

# Dry run — emit to stdout for inspection
nightgauge config init --owner nightgauge --project 1 --out -
```

> The verb is intentionally init-only. The "Update field IDs only" branch of
> the `nightgauge-repo-init` Phase 6 prompt continues to be handled by
> the AI layer using `Edit` on the `project:` block — encoding a
> diff-and-merge mode in the verb is tracked under audit row B9
> (`config sync-fields`).

### knowledge — Knowledge Base Operations

```bash
# Scaffold a knowledge directory for an issue
nightgauge knowledge scaffold --issue-number N --title "Issue title" \
  [--knowledge-enabled true|false] [--workspace-scoped true|false] \
  [--criteria "Criterion"] [--json]

# Prune empty (boilerplate-only) knowledge directories
nightgauge knowledge prune [--dry-run] [--json]

# Generate the knowledge index (README.md)
nightgauge knowledge index [--json]
nightgauge knowledge index --cross-repo --workspace --limit 20 --json
nightgauge knowledge index --cross-repo --limit 5 --json
nightgauge knowledge index --workspace --json

# Render the ## Knowledge section for a PR body
nightgauge knowledge render-pr-section --issue N [--workdir <path>]

# Rank ADR graduation candidates for an issue (read-only)
nightgauge knowledge graduate-candidates <issue> [--min-score N] [--json]

# Graduate an ADR — auto-mode (default ritual) or manual override
nightgauge knowledge graduate <issue> --auto \
  [--adr-index N] [--dry-run] [--all-candidates] \
  [--base <branch>] [--repo owner/name] [--forge github] [--json]
nightgauge knowledge graduate <issue> --section <docs#anchor> --adr ADR-NNN [--json]
```

**scaffold** creates `.nightgauge/knowledge/features/{N}-{slug}/` with
`PRD.md` and `decisions.md` template files. Idempotent — safe to re-run;
returns `"skipped": true` when the directory already exists. `--criteria` is
repeatable to inject acceptance criteria into `PRD.md`.

When `--knowledge-enabled false` is passed, the command exits 0 immediately and
returns `{"skipped": true, "skip_reason": "knowledge.enabled=false in config"}`.
This allows consuming shell scripts to gate scaffolding on the config flag
without running Node/Python interpreters. The `--workspace-scoped` flag is
accepted for symmetry but does not affect per-issue scaffold behavior (it gates
workspace-level operations such as `workspace-init`).

**prune** removes knowledge directories whose `.md` files contain only
boilerplate content (no real text beyond headings, table structure, and HTML
comment placeholders). Use `--dry-run` to preview what would be deleted. Mirrors
the `KnowledgeService.pruneEmpty()` threshold: ≥30 non-whitespace chars required
to be considered substantive.

**index** generates `.nightgauge/knowledge/README.md` as a table-of-contents
listing all entries grouped by category. Matches the output of
`KnowledgeService.generateIndex()`.

Optional flags extend the JSON output with cross-repository and workspace-level
knowledge context:

- `--cross-repo` — reads `.vscode/nightgauge-workspace.yaml` and enumerates
  `.md` files (excluding `README.md`) under each sibling repo's
  `.nightgauge/knowledge/` directory. Repos whose knowledge directory is
  absent are silently skipped. No-op when the workspace config file is missing.
- `--workspace` — enumerates top-level `.md` files under
  `.nightgauge/knowledge/{product,cross-repo,architecture}/` in the current
  repo. Categories with no qualifying files are omitted.
- `--limit N` (default 20) — maximum entries per repository (for `--cross-repo`)
  or cumulative cap across all categories (for `--workspace`).

When `--json` is combined with `--cross-repo` or `--workspace`, the output
includes additional fields:

```json
{
  "index_path": ".nightgauge/knowledge/README.md",
  "cross_repo_knowledge": [
    { "repo": "platform", "path": "path/to/knowledge", "entries": ["features/1-slug/PRD.md"] }
  ],
  "workspace_kb": [
    {
      "namespace": "product",
      "path": ".nightgauge/knowledge/product",
      "entries": ["overview.md"]
    }
  ]
}
```

`cross_repo_knowledge` and `workspace_kb` are only present in the output when
the respective flag is set. Existing callers that do not pass these flags see no
change in output.

**render-pr-section** emits the Markdown `## Knowledge` block for the PR body
of the given issue. Walks `.nightgauge/knowledge/features/{N}-*/` and
emits one bullet per top-level `.md` file (excluding `README.md` and
`_template.md`). Well-known filenames (`PRD.md`, `decisions.md`, `outcomes.md`)
render with fixed descriptions in deterministic order; remaining files render
with title-cased labels in case-insensitive alphabetical order. Prints nothing
and exits 0 when the directory is missing or contains no qualifying entries.
Consumed by `pr-create` Phase 1.7 to replace a fixed-dictionary bash loop.

**record-outcome** appends a structured `## Outcome` Markdown block to the
knowledge base file for the given issue. Prefers `decisions.md` when it
exists; otherwise creates and writes to `outcomes.md`. Idempotent — re-running
with the same issue number is a no-op when the outcome block already exists
(detected by the `**Issue**: #N` marker). When no knowledge directory is found
for the issue, a minimal one is created under
`.nightgauge/knowledge/features/{N}-outcome/`.

```bash
nightgauge knowledge record-outcome \
  --issue N \
  --status complete|partial|failed \
  --duration MINS \
  --tokens N \
  --cost USD \
  [--what-went-well "bullet points or prose"] \
  [--what-didnt "bullet points or prose"] \
  [--lessons-learned "bullet points or prose"] \
  [--workdir <path>] \
  [--json]
```

| Flag | Type | Required | Description |
| --- | --- | --- | --- |
| `--issue` | int | yes | GitHub issue number (locates knowledge path) |
| `--status` | string | yes | Outcome status: `complete`, `partial`, `failed` |
| `--duration` | int | no | Pipeline duration in minutes |
| `--tokens` | int | no | Total tokens used |
| `--cost` | float | no | Estimated cost in USD |
| `--what-went-well` | string | no | Narrative: what went well (agent-provided) |
| `--what-didnt` | string | no | Narrative: what didn't go well (agent-provided) |
| `--lessons-learned` | string | no | Narrative: lessons learned (agent-provided) |
| `--workdir` | string | no | Workspace root (default: cwd) |
| `--json` | bool | no | Output result as JSON |

**Exit codes**: `0` success (outcome appended or idempotent no-op); `1` error
(invalid status, file permission failure, etc.).

**JSON output** (when `--json` is set):

```json
{
  "issue_number": 42,
  "knowledge_path": ".nightgauge/knowledge/features/42-my-feature",
  "target_file": ".nightgauge/knowledge/features/42-my-feature/decisions.md",
  "appended": true,
  "file_created": false,
  "date_recorded": "2026-04-28",
  "status": "complete",
  "duration_mins": 30,
  "tokens": 5000,
  "cost_usd": 1.23
}
```

Consumed by `skills/nightgauge-retro/SKILL.md` Phase 9 to replace ~200
lines of bash/Python procedural logic with a single deterministic call. The
skill owns narrative content (`--what-went-well`, `--what-didnt`,
`--lessons-learned`); the binary owns the deterministic file-append operation.

#### knowledge graduate-candidates

```bash
nightgauge knowledge graduate-candidates <issue> \
  [--workdir <path>] [--min-score N] [--json]
```

Score every ADR block in the per-issue `decisions.md` against a structural +
telemetry rubric and print the ranked subset that meets the threshold.
Strictly read-only — never mutates `decisions.md`. Consumed by
`skills/nightgauge-retro/SKILL.md` Phase 9 to populate the "Graduation
Candidates" section of the retro summary.

| Flag          | Type | Required | Default | Description                                |
| ------------- | ---- | -------- | ------- | ------------------------------------------ |
| `<issue>`     | int  | yes      | —       | GitHub issue number (positional)           |
| `--workdir`   | str  | no       | cwd     | Workspace root                             |
| `--min-score` | int  | no       | 4       | Minimum score to qualify as a candidate    |
| `--json`      | bool | no       | false   | Emit Result as JSON (skips human format)   |

**Scoring rubric** (per AC #3596):

| Signal                                                                 | Score | Reason string          |
| ---------------------------------------------------------------------- | ----- | ---------------------- |
| `recall_hit` events from ≥2 distinct future issues on this `decisions.md` | +3    | `recall_hits:N`        |
| No `packages/`, `internal/`, or `src/` paths in `**Decision**`         | +2    | `general_language`     |
| Contains pattern keyword (`always`, `never`, `MUST`, `every`, `all`, `any service`) | +2    | `pattern_language`     |
| `**Consequences**` filled (>30 non-whitespace chars, no template marker) | +1    | `filled_consequences`  |
| `<!-- graduated-to: ... -->` marker present                            | −2    | `already_graduated`    |
| Title contains `issue`, `#NNNN`, or `this PR`                          | −1    | `issue_specific_title` |

`MUST` is matched case-sensitively (RFC 2119 convention); other keywords are
case-insensitive and word-bounded. Recall-hit attribution is computed at
`decisions.md` file granularity (not per-ADR) because the telemetry event
schema does not carry an ADR anchor — see ADR-001 in the issue #3596
decisions for the rationale and the future-work pointer.

**`suggested_dest` derivation**: tokenize the ADR Title and optional
`**Tags**:` line, score every `docs/*.md` file by how many tokens appear in
its filename (case-folded), and pick the highest score (alphabetical
tie-break). Fallback `docs/KNOWLEDGE_BASE.md` when nothing scores.

**Exit codes**: `0` success (with or without candidates); `1` invalid issue
argument or missing `decisions.md`.

**JSON output**:

```json
{
  "issue": 3596,
  "decisions_path": ".nightgauge/knowledge/features/3596-feature/decisions.md",
  "candidates": [
    {
      "adr_title": "Always parameterize SQL queries",
      "adr_index": 1,
      "score": 8,
      "signals": ["recall_hits:3", "general_language", "pattern_language", "filled_consequences"],
      "suggested_dest": "docs/CODE_STANDARDS.md"
    }
  ]
}
```

Sorted by score desc, then `adr_index` asc. `candidates` is `[]` (never
`null`) when nothing qualifies.

**Telemetry**: emits one `EventStats` event with `Scope: "issue:N"` and
`Path: <decisions-rel-path>` on success — matches the existing pattern from
`knowledge stats` and `knowledge validate` (see ADR-002 in the issue #3596
decisions for why a dedicated `EventGraduateScore` type was deferred).

#### knowledge graduate

```bash
# Auto-mode (default ritual)
nightgauge knowledge graduate <issue> --auto \
  [--adr-index N] [--dry-run] [--all-candidates] \
  [--base <branch>] [--repo owner/name] [--forge github] \
  [--owner <org-or-user>] [--project N] [--owner-type org|user] \
  [--workdir <path>] [--json]

# Manual override (legacy ritual)
nightgauge knowledge graduate <issue> \
  --section <docs-path>#<anchor> --adr ADR-NNN \
  [--workdir <path>] [--json]
```

| Flag               | Type   | Required          | Default                | Description                                                                                                |
| ------------------ | ------ | ----------------- | ---------------------- | ---------------------------------------------------------------------------------------------------------- |
| `<issue>`          | int    | yes               | —                      | GitHub issue number (positional)                                                                           |
| `--auto`           | bool   | no                | `false`                | Run end-to-end graduation (branch + commit + push + PR + labels + project sync)                            |
| `--adr-index`      | int    | no (auto mode)    | 0 (top score)          | Specific ADR index to graduate                                                                             |
| `--dry-run`        | bool   | no                | `false`                | Print planned changes without touching the filesystem or forge                                             |
| `--all-candidates` | bool   | no                | `false`                | Open one PR per qualifying candidate                                                                       |
| `--base`           | string | no                | repo default branch    | Base branch for the graduation PR                                                                          |
| `--section`        | string | yes (manual only) | —                      | Destination docs section, e.g. `docs/ARCHITECTURE.md#sse-pipeline-events`                                  |
| `--adr`            | string | yes (manual only) | —                      | ADR anchor to graduate (e.g. `ADR-001`)                                                                    |
| `--json`           | bool   | no                | `false`                | Emit `AutoGraduateResult` (auto) or `result` (manual) as JSON                                              |
| `--workdir`        | string | no                | cwd                    | Workspace root override                                                                                    |

**Auto-mode behavior**:

1. Calls `graduation.Candidates(issue)` (the same scoring used by
   `knowledge graduate-candidates`).
2. Selects the candidate by `--adr-index`, or the highest-scoring
   candidate; on a tie returns `status: tie_unresolved` (non-zero exit).
3. For each selected candidate:
   1. Reads the source ADR block via `knowledge.ReadADRBlock`.
   2. If the source already carries a `<!-- graduated-to: -->` marker,
      returns `status: already_graduated` and looks up the existing open
      PR via `ListOpenPRsForBranch` for `docs/graduate-<issue>-adr-NNN`.
   3. Derives a destination anchor (`kebab(<title>)`, suffixed `-2`,
      `-3`, … on collision) by scanning existing `## ` headings in the
      destination doc.
   4. Creates `docs/graduate-<issue>-adr-NNN` from `--base`.
   5. Appends the rendered section to the destination doc:
      ```markdown
      ## <ADR title>

      <!-- graduated-from: <decisions.md path>#adr-NNN -->

      <verbatim Decision block>

      _Source: issue #<N>, ADR <title>_
      ```
   6. Writes the source-side marker via `knowledge.WriteBacklink`
      (idempotent).
   7. Commits as `docs(#<N>): graduate <ADR title> to <docs-path>`.
   8. Pushes the branch via `git.Service.PushBranch`.
   9. Creates the PR with the deterministic body (links source ADR,
      destination doc, score, signals, reviewer checklist).
   10. Calls `UpdatePR(Labels: ["type:docs", "priority:medium", "size:S"])`.
       `forge.ErrUnsupported` is non-fatal (warning only).
   11. Adds the PR to the project board and sets
       `Status=Ready` via `Project.AddItem` + `SetSingleSelectField`.

**Exit codes**:

| Code | Meaning                                                                       |
| ---- | ----------------------------------------------------------------------------- |
| 0    | One or more PRs created, or every selected candidate was already graduated     |
| 1    | `no_candidates`, `tie_unresolved`, or one or more candidates errored          |

**JSON output (auto mode, `--json`)**:

```json
{
  "issue": 1234,
  "decisions_path": ".nightgauge/knowledge/features/1234-foo/decisions.md",
  "status": "created",
  "dry_run": false,
  "per_candidate": [
    {
      "adr_index": 1,
      "adr_anchor": "ADR-001",
      "adr_title": "Always validate input at API boundaries",
      "destination_doc": "docs/CODE_STANDARDS.md",
      "destination_anchor": "always-validate-input-at-api-boundaries",
      "branch": "docs/graduate-1234-adr-001",
      "pr_number": 9001,
      "pr_url": "#9001",
      "pr_node_id": "PR_kwDO...",
      "labels_applied": ["type:docs", "priority:medium", "size:S"],
      "board_synced": true,
      "status": "created"
    }
  ]
}
```

For `--dry-run --json`, `status` is `"dry_run"`, `pr_number` and friends
are zero/empty, and `planned_append` contains the rendered Markdown
that would be appended to the destination doc.

**Telemetry**: emits one `EventGraduate` event per processed candidate
with `Mode: "auto"`. Manual mode emits one event with `Mode: "manual"`.
Aggregators that filter by mode can slice automation coverage without
losing the unified graduate count.

#### knowledge recall

Find and rank prior knowledge base decisions by BM25 semantic similarity.

```bash
nightgauge knowledge recall "<query>" \
  [--scopes local,cross-repo,workspace] \
  [--limit N] \
  [--update-cache] \
  [--workdir <path>] \
  [--json]
```

| Flag             | Type   | Default                          | Description                                                     |
| ---------------- | ------ | -------------------------------- | --------------------------------------------------------------- |
| `--scopes`       | string | `"local,cross-repo,workspace"`   | Comma-separated scope filter: `local` (issue-level ADRs), `cross-repo` (sibling repo KB), `workspace` (workspace categories) |
| `--limit`        | int    | `10`                             | Maximum results to return                                       |
| `--update-cache` | bool   | `false`                          | Force cache rebuild before querying                             |
| `--workdir`      | string | cwd                              | Workspace root override                                         |
| `--json`         | bool   | `false`                          | Output `RecallResult` as JSON                                   |

**Scoring**: BM25 (Okapi BM25, `k1=1.5` `b=0.75` defaults) with:
- **Path boost** (×1.5): when a query term appears as a substring of the document path
- **Tag boost** (+0.5× per match): when a query term exactly matches a frontmatter tag

**Tie-breaking**: identical scores are broken by lexicographic path order (deterministic).

**Graduated ADR de-duplication**: when a `decisions.md` has a `<!-- graduated-to: docs/path.md -->` marker and the graduation target also appears in results, the source is suppressed. The stable `docs/` location always wins. If the target is below `--limit`, the source appears with `"graduated": true`.

**Cache**: index stored at `.nightgauge/knowledge/.recall-cache/index.jsonl` (JSONL, gitignored). Mtime-based invalidation per file; full rebuild on `--update-cache` or BM25 parameter change.

**BM25 config keys** (in `.nightgauge/config.yaml`):
```yaml
knowledge:
  recall:
    bm25_k1: 1.5   # term frequency saturation (default 1.5)
    bm25_b: 0.75   # document length normalization (default 0.75)
```

**JSON output shape** (`RecallResult`):
```json
{
  "query_id": "uuid",
  "query": "BM25 scoring",
  "hits": [
    {
      "rank": 1,
      "score": 4.117,
      "path": ".nightgauge/knowledge/features/42-bm25/decisions.md",
      "kind": "issue",
      "issue_number": 42,
      "tags": ["bm25", "scoring"],
      "snippet": "Use BM25 with k1=1.5 and b=0.75 defaults...",
      "graduated": false
    }
  ],
  "total_hits": 3
}
```

**Telemetry**: emits one `recall` event per query with `query_summary` (truncated to 200 chars), `recall_id` (UUID), and `result_count`. `recall_hit` events are intentionally NOT emitted by the binary — downstream skills emit them when they consume a hit (per the contract in this section).

---

All subcommands accept `--workdir` to override the workspace root
(default: `cwd`); `scaffold`, `prune`, `index`, `record-outcome`,
`graduate`, `graduate-candidates`, and `recall` also accept `--json` for
machine-readable output.

#### Knowledge Telemetry

Every knowledge subcommand emits one JSONL event to
`.nightgauge/pipeline/history/knowledge-events.jsonl` at its success
path. Skills and downstream stages can also emit events for operations that
happen outside the binary via `knowledge telemetry record`.

Telemetry is enabled by default once `knowledge.enabled: true` is set in
`.nightgauge/config.yaml`. Opt out with `knowledge.telemetry.enabled:
false`. When `knowledge.enabled` is false the telemetry emitter is forced
off regardless of the nested flag — no surprise files appear in projects
that have not opted into the KB.

**Event schema** (one JSON object per line):

| Field           | Type     | Description                                                          |
| --------------- | -------- | -------------------------------------------------------------------- |
| `timestamp`     | string   | RFC3339, UTC                                                         |
| `type`          | string   | One of: `scaffold`, `read`, `write`, `recall`, `recall_hit`, `graduate`, `prune`, `index`, `validate`, `stats` |
| `stage`         | string   | `NIGHTGAUGE_STAGE` env var or `"unknown"`                       |
| `scope`         | string   | `issue:N`, `workspace`, or `repo:<topic>` (omitempty)                |
| `issue_number`  | int      | When scope is `issue:N` (omitempty)                                  |
| `path`          | string   | Knowledge file path (kept as-is, omitempty)                          |
| `query_summary` | string   | Truncated to 200 chars; `<redacted>` when `NIGHTGAUGE_TELEMETRY_REDACT_QUERIES=1` (omitempty) |
| `recall_id`     | string   | Correlator returned by a recall (omitempty)                          |
| `hit_index`     | int*     | Zero-based index of the recall result used (omitempty)               |
| `result_count`  | int*     | Numeric result count for the operation (omitempty)                   |
| `duration_ms`   | int      | Operation duration in milliseconds (omitempty)                       |
| `status`        | string   | `success` or `failure` (omitempty)                                   |
| `error_kind`    | string   | Error class when status=failure (omitempty)                          |
| `mode`          | string   | Operational mode for events with more than one (e.g. `manual`/`auto` for `graduate`) (omitempty) |

**When each event fires**:

| Event type    | Source                                                                          |
| ------------- | ------------------------------------------------------------------------------- |
| `scaffold`    | `knowledge scaffold`, `knowledge workspace-create`, `knowledge workspace-init`  |
| `read`        | `knowledge render`, `knowledge render-pr-section`, ad-hoc skill calls           |
| `write`       | `knowledge new`, `knowledge record-outcome`, ad-hoc skill calls                 |
| `recall`      | Ad-hoc — emitted by callers wrapping a knowledge recall                          |
| `recall_hit`  | Ad-hoc — caller reports it used result index N (see "Recall hits" below)         |
| `graduate`    | `knowledge graduate`                                                            |
| `prune`       | `knowledge prune`                                                               |
| `index`       | `knowledge index`                                                               |
| `validate`    | `knowledge validate`                                                            |
| `stats`       | `knowledge stats`, `knowledge graduate-candidates`                              |

**Recall hits**: `recall_hit` is NOT emitted by the binary because the binary
returns and exits before the caller decides which results were useful. The
metric `recall_hit_rate` will therefore read 0 until skills opt into
emitting `recall_hit` events (tracked under epic #3590).

```bash
nightgauge knowledge telemetry record \
  --type=<event-type> \
  [--scope=issue:N|workspace|repo:T] \
  [--issue=N] \
  [--path=<file>] \
  [--query=<text>] \
  [--recall-id=<id>] \
  [--hit-index=<N>] \
  [--result-count=<N>] \
  [--duration-ms=<ms>] \
  [--status=success|failure] \
  [--error-kind=<class>] \
  [--stage=<override>] \
  [--workdir=<root>] \
  [--json]
```

**Stats `--stale` flag**: `knowledge stats --stale [--stale-days=30]` joins
the KB tree with telemetry events and lists every `decisions.md` whose last
`read` or `recall_hit` event is older than the threshold (or never).
`--json` returns `{threshold_days: N, stale: [{path, last_read_at?,
days_since_read}]}`. Default threshold is 30 days.

**Pipeline aggregate roll-up**: `nightgauge pipeline aggregate --json`
includes an additive `knowledge` block under the top-level result:

```json
{
  "knowledge": {
    "events_total": 42,
    "by_type":  { "read": 30, "scaffold": 5, "graduate": 2, "recall": 4, "recall_hit": 1 },
    "by_stage": { "feature-dev": { "read": 20 }, "feature-validate": { "recall": 4 } },
    "by_scope": { "issue:42": 10, "workspace": 5 },
    "recall_hit_rate": 0.25
  }
}
```

The schema is additive — existing readers (`stage_metrics`, `runs`,
`recovery`, etc.) are unaffected and the `v` schema version remains `1`
per ADR-006 (issue #3592).

### IPC Server

```bash
nightgauge serve [--platform-url <url>] [--api-key <key>]
```

The IPC server exposes all Go binary capabilities to the VSCode extension via
JSON-over-stdio (same pattern as LSP). The extension calls methods by writing
newline-delimited JSON to stdin; responses arrive on stdout.

**Platform namespace IPC methods** (require Go binary started with `--api-key`
or `NIGHTGAUGE_PLATFORM_API_KEY` env var):

```bash
# Platform connectivity status
{"id":1,"method":"platform.status"}

# Current license features (from cache or community tier)
{"id":2,"method":"platform.license"}

# Validate a license key and bind machine
{"id":3,"method":"platform.validateLicense","params":{"licenseKey":"ib_live_...","machineId":"sha256-hash"}}

# Resolve skill content for a pipeline stage
{"id":4,"method":"platform.resolveSkill","params":{"skillId":"feature-dev","model":"sonnet","complexityScore":5}}

# Submit analytics event (fire-and-forget)
{"id":5,"method":"platform.submitAnalytics","params":{"eventType":"stage.complete","payload":{"stage":"feature-dev"}}}

# Usage dashboard summary
{"id":6,"method":"platform.getUsageSummary"}

# Team members
{"id":7,"method":"platform.getTeamMembers"}

# Create billing portal session URL
{"id":8,"method":"platform.createPortalSession"}

# Platform API health check
{"id":9,"method":"platform.healthCheck"}
```

#### Per-Operation Identity Resolution

In multi-repo workspaces where different repos use different GitHub identities,
the IPC server auto-resolves the correct `*gh.Client` for each operation based
on the target `(owner, repo)` pair.

**How it works:**

1. The VSCode extension calls `workspace.registerRepo` once per repo during
   workspace initialization, mapping `(owner, repo)` → filesystem path.
2. On each IPC call that includes `owner` and `repo` params, the
   `ClientResolver` loads that repo's `.nightgauge/config.yaml` and
   resolves the token via the standard priority chain (config → GITHUB_TOKEN
   env → gh CLI).
3. Resolved clients are cached by `"owner/repo"` key. Cache entries are
   invalidated on:
   - Config file mtime change (detected on each `Resolve()` call)
   - Token fingerprint change (SHA256[:8] of resolved token)
   - Explicit `Invalidate()` call (triggered on HTTP 401 from GitHub API)
4. When no registry entry exists for `(owner, repo)`, the default server
   client is used (backward compatible).

**Backward compatibility:** If a request includes an explicit `GitHubUser`
field, the legacy `clientForUser()` path is used. The resolver is only
consulted when `GitHubUser` is empty. This is handled by the
`resolveClientForRequest(ctx, githubUser, owner, repo)` helper.

**Register repos:**

```json
{
  "id": 1,
  "method": "workspace.registerRepo",
  "params": { "owner": "nightgauge", "repo": "nightgauge", "path": "/path/to/repo" }
}
```

**Debug logging:** On cache miss, a single log line is emitted:
`IPC ClientResolver: resolved identity for owner/repo (user="username", token=...XXXXXXXX)`

**CLI single-shot path:** The `main.go` → `NewClientFromConfig` path is
unaffected by this change. Per-operation resolution only applies to the
long-running IPC server.

**Platform offline behavior**: When the platform is unreachable, the Go binary
automatically falls back to cached data (license: 7-day grace period, skills:
bundled free tier). All platform IPC methods succeed even offline — they return
degraded/community data rather than errors. The `platform.status` method
indicates the current connectivity mode (`online`, `degraded`, or `offline`).

## Hook Integration

Claude Code hooks remain as 3-line shell wrappers that `exec` the Go binary:

```bash
#!/bin/bash
exec nightgauge hook workflow-gate "$@"
```

This is required because Claude Code's hook system expects shell scripts. All
business logic lives in the compiled Go binary.

## Dependencies

The Go binary eliminates these external runtime dependencies:

| Removed              | Replaced By                                                          |
| -------------------- | -------------------------------------------------------------------- |
| `gh` (GitHub CLI)    | `internal/github/` (native GraphQL + REST)                           |
| `gh label`           | `nightgauge label` (GraphQL — `internal/github/labels.go`)      |
| `gh api .../views`   | `nightgauge project view-*` (REST — `internal/github/views.go`) |
| `jq`                 | `encoding/json` (stdlib)                                             |
| `bash 4.x+`          | Go stdlib                                                            |
| `awk`, `sed`, `date` | Go stdlib                                                            |
| `curl`               | `net/http` (stdlib)                                                  |

Only `git` remains as an external dependency (for worktree/branch operations).

## Error Handling

### IPC Panic Recovery

The IPC server (`internal/ipc/server.go`) is a long-running process that handles
concurrent JSON-RPC requests from the VSCode extension. To prevent a single bad
request from crashing the entire server and losing all active pipeline state,
defer-based panic recovery is applied at two levels:

**1. Per-request handler (`handleRequest`)** — Every incoming request is
dispatched through `handleRequest`, which wraps handler execution in a
`defer/recover`. When a handler panics:

- The panic value and a full stack trace are logged at `WARNING` level.
- An `ipc.panic` event is emitted to VSCode so the extension can log the
  recovery (extension does not need to take any action).
- An `ErrInternal` (`-32603`) JSON-RPC error is returned for the failed request.
- All other requests continue processing normally.

**2. Main scanner loop (`Run`)** — The outer stdin scanner loop is also wrapped
with a `defer/recover`. A panic here (e.g., in `bufio.Scanner` or JSON
unmarshaling) would otherwise terminate the entire server process.

**Panic logging format:**

```
WARNING: PANIC in IPC handler "method.name" (id=N): <panic value>
Stack trace:
goroutine N [running]:
...
```

**`ipc.panic` event shape:**

```json
{
  "event": "ipc.panic",
  "data": {
    "context": "method.name",
    "message": "<panic value as string>"
  }
}
```

The VSCode extension already handles all RPC errors uniformly; no extension
changes are required when a handler panics.

**Reusable middleware** — `internal/ipc/middleware.go` exposes `recoverPanic`
and `logPanicRecovery` for use in any new IPC entry points added in the future.

### Classification Strategy

The `nightgauge` CLI uses `internal/intelligence/failure.Classifier` to
classify GitHub API errors into actionable categories. The classifier analyzes
error message text and exit codes to determine whether an error is transient,
deterministic, or an auth/permission failure.

| Category        | Examples                          | Retryable | User Action          |
| --------------- | --------------------------------- | --------- | -------------------- |
| `transient`     | Rate limit (429), network timeout | Yes       | Wait and retry       |
| `infra`         | Connection refused, DNS failure   | Yes       | Check connectivity   |
| `permission`    | 401 Unauthorized, 403 Forbidden   | No        | `gh auth login`      |
| `resource`      | Token context length exceeded     | No        | Reduce request scope |
| `deterministic` | Merge conflict, type error        | No        | Fix the root cause   |
| `unknown`       | Unrecognized error text           | —         | Check error details  |

### Retry Guidance Conventions

Key service-call error paths (e.g., `issue view`, `project sync-status`,
`pr merge`) wrap GitHub API errors with the `enrichError()` helper in
`cmd/nightgauge/main.go`. Enriched messages follow these conventions:

- **Transient / infra errors**: append `(transient — wait and retry)` so
  pipeline orchestrators and users know to back off before retrying.
- **Auth errors**: append `(auth error — run: gh auth login, or set
GITHUB_TOKEN)` with the exact remediation command.
- **Other errors**: returned as-is — no annotation added for deterministic or
  unknown failures, to avoid misleading the user.

### Silent Ignore Policy

The `_ =` pattern is permitted only in two situations:

1. **Cobra flag registration** — `_ = cmd.MarkFlagRequired(...)` and
   `_ = cmd.Flags().Set(...)` never return errors when the flag name is a
   compile-time constant that matches a flag registered on the same command.
   Every such site carries the comment:
   `// cobra MarkFlagRequired never errors for known flags`
   or `// cobra flag.Set never errors for known flags`

2. **Best-effort operations** — operations that are explicitly non-fatal by
   design (e.g., log rotation via `os.WriteFile`). Every such site carries an
   explicit comment: `// log rotation is best-effort; failure is non-fatal`

All other `_ =` patterns are considered bugs. The `printJSON()` function writes
to stdout, which can fail if stdout is closed; its errors must be logged via
`fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)`
rather than silently discarded.
````
