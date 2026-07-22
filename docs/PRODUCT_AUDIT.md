# Product Audit

The `/nightgauge:product-audit` skill provides a comprehensive quality
audit across all Nightgauge repositories. It scans 8 dimensions in
parallel and produces a scored report with actionable findings.

## What It Does

The audit analyzes the product across 8 dimensions:

| #   | Dimension      | Weight | What It Checks                                                                                        |
| --- | -------------- | ------ | ----------------------------------------------------------------------------------------------------- |
| 1   | API Alignment  | 20%    | Client API calls vs. platform endpoints — detects path mismatches, method mismatches, auth mismatches |
| 2   | Epic Lifecycle | 10%    | Stale epics, board status drift, orphaned issues, stale blockers                                      |
| 3   | Documentation  | 10%    | OpenAPI spec freshness, endpoint table accuracy, ECOSYSTEM.md drift                                   |
| 4   | Feature Parity | 15%    | Feature coverage across VSCode, Angular, Flutter, and Platform                                        |
| 5   | Test Coverage  | 20%    | Files below threshold, critical paths uncovered, stub test files                                      |
| 6   | Security       | 15%    | Hardcoded secrets, unvalidated input, unsafe patterns, vulnerable dependencies                        |
| 7   | Dependencies   | 5%     | Outdated packages, known CVEs, ecosystem health                                                       |
| 8   | CI/CD          | 5%     | Disabled workflows, `continue-on-error`, missing coverage enforcement                                 |

## Running the Audit

### Basic Audit (Report Only)

```bash
/nightgauge:product-audit
```

Produces:

- `.nightgauge/audit/PRODUCT_AUDIT_REPORT.md` — human-readable report
- `.nightgauge/audit/product-audit-latest.json` — machine-readable JSON

### Create GitHub Issues for Findings

```bash
# Create issues for high+ severity findings (default)
/nightgauge:product-audit --create-issues

# Create issues for medium+ severity findings
/nightgauge:product-audit --create-issues --severity medium
```

### Auto-Fix Safe Findings

Some findings are safely auto-fixable (stale epics, board status drift, stale
blockers). Preview first with `--dry-run`:

```bash
# Preview what would be fixed
/nightgauge:product-audit --fix --dry-run

# Apply fixes
/nightgauge:product-audit --fix
```

### Quick Mode (Cached Data)

Skip slow operations (test execution, live API probing) and use cached data:

```bash
/nightgauge:product-audit --quick
```

Useful when you need a fast check and don't need fresh test coverage data.

### Run Specific Dimensions Only

```bash
# API alignment and lifecycle only
/nightgauge:product-audit --dimensions 1,2

# Using dimension names
/nightgauge:product-audit --dimensions api,lifecycle

# Security and dependencies
/nightgauge:product-audit --dimensions security,deps
```

### Compare with Previous Audit

```bash
# Compare with most recent previous audit
/nightgauge:product-audit --compare last

# Compare with specific date
/nightgauge:product-audit --compare 2026-03-16
```

### CI Mode

Fail the build if the overall score drops below a threshold:

```bash
/nightgauge:product-audit --ci --threshold 80
```

Exit codes:

- `0` — score at or above threshold, no critical findings
- `1` — score below threshold or critical findings detected

---

## Interpreting Results

### Overall Score

| Score  | Meaning                               |
| ------ | ------------------------------------- |
| 90-100 | Excellent — all dimensions healthy    |
| 75-89  | Good — minor issues only              |
| 60-74  | Fair — some dimensions need attention |
| 45-59  | Poor — significant issues detected    |
| 0-44   | Critical — immediate action required  |

### Finding Severity

| Severity   | Meaning                                        | Action              |
| ---------- | ---------------------------------------------- | ------------------- |
| `critical` | Production risk, data loss, or security breach | Fix immediately     |
| `high`     | Significant impact on users or team velocity   | Fix this sprint     |
| `medium`   | Notable degradation                            | Fix next sprint     |
| `low`      | Cosmetic or minor debt                         | Fix when convenient |

### Confidence Scores

Each finding includes a confidence score (0-100):

| Range  | Meaning                               |
| ------ | ------------------------------------- |
| 90-100 | Confirmed (live probe or exact match) |
| 70-89  | High confidence (static analysis)     |
| 50-69  | Likely correct (heuristic)            |
| 30-49  | Ambiguous — review suggested          |
| 0-29   | Low confidence — informational only   |

Findings below 30% confidence are hidden by default. Use `--verbose` to surface
them.

### Trend Analysis

The report shows how each dimension changed since the previous audit:

- ⬆ **Improving** — score increased since last audit
- ⬇ **Degrading** — score decreased since last audit
- ↔ **Stable** — no significant change (< 2 point delta)
- **First Run** — no previous audit to compare with

---

## Auto-Fixable Findings

Only three categories are auto-fixable (all in the Lifecycle dimension):

| Category             | What It Fixes                                                | How                                             |
| -------------------- | ------------------------------------------------------------ | ----------------------------------------------- |
| `STALE_EPIC`         | Closes open epics where all sub-issues are resolved          | `gh issue close` with explanation comment       |
| `BOARD_STATUS_DRIFT` | Moves closed issues to "Done" on the project board           | GraphQL mutation to update project board status |
| `STALE_BLOCKER`      | Removes blocking relationships where the blocker is resolved | `removeBlockedBy` GraphQL mutation              |

All other findings require manual developer action.

---

## CI Integration

### GitHub Actions Workflow

```yaml
name: Weekly Product Audit

on:
  schedule:
    - cron: "0 9 * * 1" # Monday 9 AM UTC
  workflow_dispatch:
    inputs:
      threshold:
        description: "Minimum score (0-100)"
        default: "75"

jobs:
  audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Checkout sibling repos
        run: |
          git clone https://github.com/acme/platform ../acme-platform
          git clone https://github.com/acme/dashboard ../acme-dashboard
          git clone https://github.com/acme/mobile ../acme-mobile

      - name: Run Product Audit
        run: |
          claude --skill skills/nightgauge-product-audit/SKILL.md \
            --ci --threshold ${{ github.event.inputs.threshold || '75' }} \
            --output-format json
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}

      - name: Upload Audit Report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: product-audit-${{ github.run_id }}
          path: .nightgauge/audit/
          retention-days: 90
```

### Enforcing a Score Threshold on Pull Requests

```yaml
name: PR Quality Gate

on:
  pull_request:
    branches: [main]

jobs:
  quick-audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Quick Audit (Security + Dependencies)
        run: |
          claude --skill skills/nightgauge-product-audit/SKILL.md \
            --dimensions security,deps \
            --ci --threshold 85 \
            --quick
```

---

## Configuration

Customize audit behavior in `.nightgauge/config.yaml`:

```yaml
product_audit:
  enabled: true
  schedule: "0 9 * * 1" # Weekly Monday 9 AM UTC

  dimension_weights:
    api_alignment: 0.20
    lifecycle: 0.10
    documentation: 0.10
    feature_parity: 0.15
    test_coverage: 0.20
    security: 0.15
    dependencies: 0.05
    ci_cd: 0.05

  dimensions:
    test_coverage:
      threshold_percent: 80
      cache_coverage: true

    lifecycle:
      max_stale_days: 30

    feature_parity:
      config_file: ".nightgauge/audit/parity-config.json"
      required_parity_score: 85

  reporting:
    output_format: "both"
    report_retention_days: 90
```

See [docs/CONFIGURATION.md](CONFIGURATION.md) for the complete `product_audit`
configuration reference.

---

## Feature Parity Matrix

The feature parity matrix defines which features each client must implement.
Managed in `.nightgauge/audit/parity-config.json`.

To add a feature to the matrix:

```json
{
  "id": "new_feature.id",
  "name": "New Feature Name",
  "description": "What this feature does",
  "category": "pipelines",
  "required_in": ["vscode", "angular", "flutter"],
  "optional_in": [],
  "detection_hints": {
    "vscode": ["FeatureService", "featureMethod"],
    "angular": ["FeatureComponent", "feature.service.ts"],
    "flutter": ["FeatureScreen", "feature_service.dart"],
    "platform": ["GET /v1/feature/endpoint"]
  },
  "implementation_guide": "path/to/reference/implementation",
  "test_guide": "path/to/reference/tests"
}
```

Coverage levels used by the audit:

| Level     | Score | Meaning                       |
| --------- | ----- | ----------------------------- |
| `FULL`    | 100   | Service + UI + Tests present  |
| `PARTIAL` | 70    | Service + UI, no tests        |
| `STUB`    | 30    | Exists but incomplete         |
| `MISSING` | 0     | Not implemented               |
| `N/A`     | 100   | Not applicable to this client |

---

## Report Files

| File                                          | Description                           |
| --------------------------------------------- | ------------------------------------- |
| `.nightgauge/audit/PRODUCT_AUDIT_REPORT.md`   | Human-readable report (latest)        |
| `.nightgauge/audit/product-audit-latest.json` | Machine-readable JSON (latest)        |
| `.nightgauge/audit/history/`                  | Historical archive for trend analysis |

See `.nightgauge/audit/README.md` for full directory structure and `.gitignore` guidance.

---

## Schema Reference

See [docs/PRODUCT_AUDIT_SCHEMA.md](PRODUCT_AUDIT_SCHEMA.md) for complete JSON
schema documentation including all dimension-specific finding fields.

---

## Author

nightgauge
