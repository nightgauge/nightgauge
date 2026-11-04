---
name: integration-audit
description: Cross-repository integration audit. Validates that client API calls match
  platform endpoints, auth flows are aligned, docs are current, and cross-repo
  dependencies are tracked. Run before major epic creation or after platform
  changes.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.2.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task Agent AskUserQuestion
orchestration:
  mode: fanout
  phase: client-extraction
  ceiling: fanout
  units:
    - id: dashboard
      role: client-worker
      promptRef: SKILL.md#angular-acme-dashboard
    - id: flutter
      role: client-worker
      promptRef: SKILL.md#flutter-acme-mobile
    - id: extension
      role: client-worker
      promptRef: SKILL.md#extension-nightgauge
  judge:
    mode: merge
    quorum: 1
    promptRef: SKILL.md#phase-4-gap-analysis
disable-model-invocation: true
---

# Integration Audit

Validate cross-repository integration health across the Nightgauge product
family. This skill catches the class of problems where client apps call API
endpoints that don't exist, auth flows are mismatched, or docs have drifted from
reality.

## When to Use

- **Before creating epics** that span multiple repositories
- **After major platform changes** (new endpoints, auth changes, schema updates)
- **Periodically** (weekly or before sprint planning) to catch drift
- **When a client app reports unexpected 404/401 errors** against the platform

## Outcomes

- Structured gap report: which client API calls have no matching platform
  endpoint
- Auth flow alignment matrix across all clients
- Stale documentation inventory
- Missing cross-repo dependency links
- Actionable issue suggestions for discovered gaps

## Prerequisites

- Docker containers running (`docker ps` shows nightgauge-api healthy)
- All 4 repos cloned in the workspace (`../acme-platform`,
  `../acme-dashboard`, `../acme-mobile`)
- `gh` CLI authenticated

## Orchestration

This skill declares an `orchestration:` frontmatter block (`mode: fanout`) that
fans the Phase 3 client API-call extraction out as one worker unit per client
surface (dashboard/Angular, Flutter, extension), then a merge judge reconciles
their findings against the platform endpoint truth (Phase 4 gap analysis). The
block is consumed by the capability-routed `WorkflowEngine` (epic #3899); see
[docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md). Each
unit's `promptRef` points at the SAME extraction section the prose **Workflow**
below walks, so providers without an orchestration capability extract the three
surfaces sequentially in one agent — the prose stays the portability floor.

## Workflow

### Phase 0: Environment Preflight

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Three-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`
3. **Project environment** — the checks the project declares under
   `preflight.environment` (#101); opt-in, silent when none are declared

The binary discovery cascade below is **provider-neutral** (issue 4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, the development-only repo
`bin/` and worktree canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct
or terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (issue 3262 → issue 4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → gated repo → gated
canonical → `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path, and that every cascade keeps
the `NIGHTGAUGE_DEV_REPO_BIN` gate (#2758): a foreign repository's
`bin/nightgauge` must never run.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (issue 3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → gated repo
# bin → gated canonical-repo bin → ~/go/bin). No VSCode-extension path — see issue 4029.
# Repo bins need NIGHTGAUGE_DEV_REPO_BIN=1 and a verified core checkout (#2758).
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (issue 3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, issue 3262).
if [ -z "${GH_TOKEN:-}" ]; then
  IB_REPO_TOKEN="$("$BINARY" forge auth token 2>/dev/null || true)"
  if [ -n "$IB_REPO_TOKEN" ]; then
    export GH_TOKEN="$IB_REPO_TOKEN"
    export GITHUB_TOKEN="${GITHUB_TOKEN:-$IB_REPO_TOKEN}"
  fi
  unset IB_REPO_TOKEN
fi

# Layer 2: full environment health check via doctor --json
DOCTOR_RESULT=$("$BINARY" doctor --json 2>/dev/null)
DOCTOR_EXIT=$?
if [ "$DOCTOR_EXIT" -eq 2 ]; then
  echo "ERROR: Environment check failed — nightgauge doctor reports broken environment." >&2
  FAILED_CHECKS=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi

# Layer 3: project-declared environment checks (preflight.environment, #101).
# Opt-in: with none declared this prints nothing and continues. The verb
# writes one `[environment-halt]` line per failed check, with its remedy.
if ! "$BINARY" preflight environment >/dev/null; then
  echo "[environment-halt] Project environment check failed — fix the environment, not the code." >&2
  exit 1
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

**Layer 3** halts the stage when any check declared under
`preflight.environment` in the workspace's `.nightgauge/config.yaml` fails
(exit 1) or the declaration is invalid (exit 2). The failure is in the
operator's environment, not the code: do not edit code to work around it, do
not run the remedy yourself, and end the stage. The pipeline records it as
`environment_halt` and holds the issue for an operator.

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


---

### Phase 1: Discover Workspace

1. Identify all repositories in the workspace:
   - `nightgauge` (VSCode extension)
   - `acme-platform` (Cloud API)
   - `acme-dashboard` (Web dashboard)
   - `acme-mobile` (Mobile app)
2. Verify each repo exists at the expected path relative to the workspace root.
3. Skip any repo not found — report it but continue with available repos.

### Phase 2: Platform API Reality Check

Probe the running platform API to determine exactly what endpoints exist.

**Preferred path — deterministic Go binary (binary-first):**

```bash
# Optional: get a test token if ENABLE_TEST_AUTH is set
TOKEN=$(curl -s -X POST http://localhost:3000/v1/auth/test-token \
  -H "Content-Type: application/json" \
  -d '{"githubLogin":"audit-user"}' | jq -r .token)

# Run the probe — emits a 6-category JSON ProbeReport
nightgauge integration probe-platform \
  --base-url http://localhost:3000 \
  --auth-mode jwt --token "$TOKEN" \
  --json > /tmp/integration-probe.json
```

The JSON ProbeReport (schema documented in `docs/GO_BINARY.md` →
`### Integration Operations`) is the input Phase 4 reads. Categorization is
deterministic: status code + body-shape rules below. No prose parsing needed.

**Categorization rules (the verb applies these — table is for reference):**

| HTTP status                  | Body shape                              | Category        |
| ---------------------------- | --------------------------------------- | --------------- |
| 2xx                          | non-empty, non-stub                     | `WORKING`       |
| 2xx                          | empty / `[]` / `{}` / `null` / length<4 | `STUB`          |
| 401                          | any                                     | `AUTH_REQUIRED` |
| 403                          | any                                     | `AUTH_MISMATCH` |
| 404                          | any                                     | `NOT_FOUND`     |
| 5xx                          | any                                     | `BROKEN`        |
| other (3xx, transport error) | any                                     | `BROKEN`        |

**Fallback — inline curl loop** (only if the binary is unavailable):

If `command -v nightgauge` returns nothing, fall back to probing each
endpoint manually with `curl` and recording `{method, path, status, category}`
into the same shape. The endpoint list is maintained in
`configs/integration-platform-endpoints.yaml` and embedded in the binary; if
both are unreachable, the canonical hand-curated list is:

```
AUTH:       GET /v1/auth/me, POST /v1/auth/web/github, POST /v1/auth/web/callback,
            POST /v1/auth/logout, POST /v1/auth/refresh, POST /v1/auth/signout,
            POST /v1/auth/token/refresh, POST /v1/auth/device-code,
            POST /v1/auth/device-token, POST /v1/auth/github
PIPELINES:  GET /v1/pipeline-runs, GET /v1/pipeline-runs/stats,
            GET /v1/pipeline-runs/:id, GET /v1/pipelines/commands/:id,
            GET /v1/pipelines/events/stream, GET /v1/sessions,
            POST /v1/pipelines/trigger, GET /v1/pipelines/slots
QUEUE:      GET /v1/queue, POST /v1/queue/add, DELETE /v1/queue/:id,
            POST /v1/queue/reorder
GITHUB:     GET /v1/github/issues, GET /v1/github/prs, GET /v1/github/epics,
            GET /v1/github/board/items
TEAM:       GET /v1/team/members, GET /v1/team/:id/members
ANALYTICS:  GET /v1/analytics/health, GET /v1/analytics/trends,
            GET /v1/analytics/cost, GET /v1/analytics/dashboard
ADMIN:      GET /v1/billing/subscription, GET /v1/billing/invoices,
            GET /v1/webhooks, GET /v1/api-keys, GET /v1/audit-log,
            GET /v1/events, GET /v1/users/me/notification-preferences
HEALTH:     GET /health, GET /v1/health, GET /ready, GET /v1/health/report
```

### Phase 3: Client API Call Extraction

For each client repository, extract every API call:

#### Angular (`acme-dashboard`)

```bash
# Search all service files, stores, and interceptors
grep -rn "this\.http\.\(get\|post\|put\|patch\|delete\)" src/app/ \
  --include="*.ts" | grep -oP "['\"]\K[^'\"]+(?=['\"])"
grep -rn "fetch\(" src/app/ --include="*.ts"
grep -rn "platformApiUrl" src/app/ --include="*.ts"
```

Record each call as: `{method, path, authType, file, line}`.

#### Flutter (`acme-mobile`)

```bash
# Search all API files and data sources
grep -rn "\.get\|\.post\|\.put\|\.patch\|\.delete" lib/ \
  --include="*.dart" | grep -oP "['\"]\K/[^'\"]+(?=['\"])"
```

Record each call as: `{method, path, authType, file, line}`.

#### Extension (`nightgauge`)

```bash
# Search IPC client and direct fetch calls
# EXT_SRC is the extension repository's source directory (e.g. src/).
EXT_SRC="${EXT_SRC:-src}"
grep -rn "platform\." "$EXT_SRC" \
  --include="*.ts" | grep -v "node_modules"
grep -rn "fetch\(" "$EXT_SRC" \
  --include="*.ts" | grep -v "node_modules"
```

### Phase 4: Gap Analysis

Compare client expectations against platform reality:

1. **Endpoint existence**: For each client API call, check if the platform
   endpoint exists (from Phase 2). Flag any `NOT_FOUND` or `STUB` matches.

2. **Auth method alignment**: For each client API call, verify the auth method
   matches what the platform expects:
   - Client sends JWT cookie → platform must accept cookie auth
   - Client sends Bearer token → platform must accept JWT Bearer
   - Client sends license key → platform must accept license key

3. **Path consistency**: Check for path mismatches where the client and platform
   use different paths for the same resource (e.g., `/pipelines/queue` vs
   `/queue`, `/teams/members` vs `/team/:teamId/members`).

4. **SSE endpoint alignment**: Verify each client's SSE connection URL matches
   an actual platform SSE endpoint.

5. **Response shape compatibility**: For working endpoints, verify the response
   JSON structure matches what the client expects (compare field names).

### Phase 5: Documentation Freshness

Check key documentation files for staleness:

1. **ECOSYSTEM.md** (`nightgauge/docs/ECOSYSTEM.md`):
   - Does the Development Maturity Matrix match reality?
   - Does the API Endpoint Coverage table match Phase 4 results?
   - Are auth flow diagrams accurate?

2. **ARCHITECTURE.md** (each client repo):
   - Does the Platform Integration Status section exist and is it current?
   - Does the tech stack table match actual dependencies?

3. **AGENTS.md** (each repo; `CLAUDE.md` only as its adapter):
   - Are companion repository references current?
   - Are epic references still valid (not closed/superseded)?

4. **OpenAPI spec** (`acme-platform/packages/api/openapi/`):
   - Does the spec include all implemented REST endpoints?
   - Are there spec entries for endpoints that don't exist?

### Phase 6: Cross-Repo Dependency Audit

Check that cross-repo dependencies are properly tracked:

1. **Open epics with cross-repo blockers**: For each open epic in Angular and
   Flutter, check if it has `blockedBy` relationships to platform epics when the
   sub-issues reference platform API endpoints.

2. **Orphaned platform epics**: Check if platform epics that block client work
   are actually open and progressing.

3. **Stale blocking relationships**: Check if any `blockedBy` relationships
   point to closed issues (dependency already satisfied but not unblocked).

### Phase 7: Generate Report

Produce a structured report with these sections:

```markdown
## Integration Audit Report — {date}

### Executive Summary

- X endpoints missing, Y auth mismatches, Z path inconsistencies
- N documentation files stale
- M cross-repo dependencies missing

### Endpoint Gap Matrix

| Client | Endpoint | Platform Status | Auth Match | Action Needed |
| ------ | -------- | --------------- | ---------- | ------------- |

### Auth Flow Alignment

| Flow | Extension | Flutter | Angular | Platform | Status |
| ---- | --------- | ------- | ------- | -------- | ------ |

### Path Mismatches

| Client Path | Platform Path | Affected Files | Fix |

### Stale Documentation

| File | Issue | Suggested Update |

### Missing Cross-Repo Dependencies

| Epic | Missing blockedBy | Suggested Link |

### Recommended Actions

1. [Prioritized list of issues to create or update]
```

### Phase 8: Optional — Create Issues

If the user approves, create GitHub issues for critical gaps discovered:

1. Use `/nightgauge:issue-create` for each gap
2. Link new issues to existing epics where appropriate
3. Set `blockedBy` relationships for cross-repo dependencies

## Decision Rules

- Do NOT create issues automatically — always present findings first and ask
- Prioritize gaps that block end-to-end functionality over cosmetic issues
- Skip probing endpoints if Docker containers are not running — report the skip
  and audit only client-side code and docs
- When a gap is already tracked by an existing open issue, note it rather than
  suggesting a duplicate

## Completion Checklist

- [ ] All 4 repos discovered and scanned
- [ ] Platform API probed (or skip noted if Docker not running)
- [ ] Client API calls extracted from all available repos
- [ ] Gap analysis complete with categorized findings
- [ ] Documentation freshness checked
- [ ] Cross-repo dependencies audited
- [ ] Report generated and presented to user
- [ ] User decision on issue creation recorded
