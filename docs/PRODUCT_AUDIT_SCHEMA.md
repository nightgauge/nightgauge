# Product Audit Schema Reference

Complete schema documentation for the `/nightgauge:product-audit` skill
output format. JSON Schema files are in `schemas/`.

## Schema Files

| File                                    | Purpose                                                 |
| --------------------------------------- | ------------------------------------------------------- |
| `schemas/product-audit-report-v1.json`  | Top-level audit report format                           |
| `schemas/product-audit-finding-v1.json` | Individual finding format (base + dimension extensions) |

---

## Top-Level Report Schema

**File**: `schemas/product-audit-report-v1.json`

```json
{
  "$schema": "https://nightgauge.dev/schemas/product-audit-report-v1.json",
  "schema_version": "1.0",
  "timestamp": "2026-03-23T14:30:00Z",
  "run_id": "audit-20260323-143000",
  "workspace_root": "/Users/you/repos/acme",
  "overall_score": 78,
  "overall_score_trend": { ... },
  "dimensions": [ ... ],
  "critical_findings": [ ... ],
  "trend_analysis": { ... },
  "execution_metadata": { ... }
}
```

### Top-Level Fields

| Field                 | Type                     | Required | Description                            |
| --------------------- | ------------------------ | -------- | -------------------------------------- |
| `schema_version`      | string                   | yes      | Always `"1.0"`                         |
| `timestamp`           | ISO 8601                 | yes      | Audit completion time                  |
| `run_id`              | string                   | yes      | Unique run ID: `audit-YYYYMMDD-HHMMSS` |
| `workspace_root`      | string                   | yes      | Absolute path to workspace root        |
| `overall_score`       | integer 0-100            | yes      | Weighted overall score                 |
| `overall_score_trend` | ScoreTrend               | no       | Trend vs. previous audit               |
| `dimensions`          | DimensionResult[]        | yes      | Per-dimension scores and findings      |
| `critical_findings`   | CriticalFindingSummary[] | yes      | Rollup of critical-severity findings   |
| `trend_analysis`      | TrendAnalysis            | no       | Historical trend data                  |
| `execution_metadata`  | ExecutionMetadata        | yes      | How the audit was run                  |

### ScoreTrend Object

```json
{
  "previous_score": 72,
  "delta": 6,
  "direction": "improving",
  "days_since_last_audit": 7
}
```

| Field                   | Type            | Values                                          |
| ----------------------- | --------------- | ----------------------------------------------- |
| `previous_score`        | integer \| null | 0-100 or null if no prior audit                 |
| `delta`                 | integer \| null | Positive = improving, negative = degrading      |
| `direction`             | string          | `improving`, `degrading`, `stable`, `first_run` |
| `days_since_last_audit` | integer \| null | Days since previous audit                       |

### DimensionResult Object

```json
{
  "name": "api_alignment",
  "index": 1,
  "score": 85,
  "weight": 0.20,
  "finding_count": 3,
  "critical_count": 0,
  "high_count": 1,
  "medium_count": 2,
  "low_count": 0,
  "status": "completed",
  "duration_seconds": 47,
  "findings": [ ... ]
}
```

| Field              | Type           | Values                                          |
| ------------------ | -------------- | ----------------------------------------------- |
| `name`             | string         | See [Dimension Names](#dimension-names)         |
| `index`            | integer        | 1-8                                             |
| `score`            | integer        | 0-100                                           |
| `weight`           | number         | 0.0-1.0 (sum of all weights = 1.0)              |
| `finding_count`    | integer        | Total findings in this dimension                |
| `critical_count`   | integer        | Critical severity findings                      |
| `high_count`       | integer        | High severity findings                          |
| `medium_count`     | integer        | Medium severity findings                        |
| `low_count`        | integer        | Low severity findings                           |
| `status`           | string         | `completed`, `skipped`, `timeout`, `error`      |
| `skip_reason`      | string \| null | Present when `status != completed`              |
| `duration_seconds` | number \| null | Time taken for this dimension                   |
| `findings`         | Finding[]      | See [Finding Base Schema](#finding-base-schema) |

### Dimension Names

| Index | Name             | Weight | Description                             |
| ----- | ---------------- | ------ | --------------------------------------- |
| 1     | `api_alignment`  | 0.20   | Client API calls vs. platform endpoints |
| 2     | `lifecycle`      | 0.10   | Epic and issue lifecycle health         |
| 3     | `documentation`  | 0.10   | Doc accuracy vs. codebase reality       |
| 4     | `feature_parity` | 0.15   | Feature coverage across all clients     |
| 5     | `test_coverage`  | 0.20   | Test coverage and quality               |
| 6     | `security`       | 0.15   | Security vulnerabilities and patterns   |
| 7     | `dependencies`   | 0.05   | Outdated and vulnerable packages        |
| 8     | `ci_cd`          | 0.05   | CI/CD workflow integrity                |

### CriticalFindingSummary Object

```json
{
  "id": "lifecycle-001-stale-epic-130",
  "dimension": "lifecycle",
  "severity": "critical",
  "summary": "Epic #130 'Auth Client Alignment' has been open 45 days with all sub-issues resolved"
}
```

### TrendAnalysis Object

```json
{
  "audits_in_history": 4,
  "score_history": [68, 72, 75, 78],
  "improving_dimensions": ["api_alignment", "documentation"],
  "degrading_dimensions": ["test_coverage"],
  "stable_dimensions": ["lifecycle", "feature_parity", "security", "dependencies", "ci_cd"],
  "recommendations": [
    "Test coverage is declining — prioritize test writing in next sprint",
    "API alignment improved significantly — continue focused reviews"
  ]
}
```

### ExecutionMetadata Object

```json
{
  "mode": "full",
  "dimensions_run": [
    "api_alignment",
    "lifecycle",
    "documentation",
    "feature_parity",
    "test_coverage",
    "security",
    "dependencies",
    "ci_cd"
  ],
  "dimensions_skipped": [],
  "duration_seconds": 287,
  "repos_scanned": 4,
  "repos_available": ["nightgauge", "acme-platform", "acme-dashboard", "acme-mobile"],
  "repos_missing": [],
  "issues_created": 5,
  "issues_auto_fixed": 3,
  "skip_reasons": {},
  "cli_flags": {
    "create_issues": true,
    "fix": false,
    "quick": false,
    "threshold": 75
  }
}
```

---

## Finding Base Schema

**File**: `schemas/product-audit-finding-v1.json`

All findings share a common base schema. Dimension-specific fields are added
as additional properties.

```json
{
  "id": "api-001-flutter-path-mismatch",
  "severity": "high",
  "category": "PATH_MISMATCH",
  "confidence": 95,
  "repo": "acme-mobile",
  "dimension": "api_alignment",
  "detail": "Flutter calls non-existent endpoint /v1/pipelines/{issue}/run; platform has /v1/pipelines/trigger",
  "auto_fixable": false,
  "suggested_action": "Update Flutter API client to use POST /v1/pipelines/trigger",
  "files": [
    {
      "path": "lib/core/network/api/pipeline_api.dart",
      "line": 42,
      "lines_of_context": 3,
      "code_snippet": "final response = await dio.post(\n  '/v1/pipelines/{issue}/run',\n  data: {...}\n);"
    }
  ],
  "related_findings": [],
  "external_references": [],
  "metadata": {
    "detected_at": "2026-03-23T14:32:00Z",
    "detection_method": "static_analysis",
    "manual_review_required": false
  }
}
```

### Base Finding Fields

| Field                 | Type                | Required | Description                                          |
| --------------------- | ------------------- | -------- | ---------------------------------------------------- |
| `id`                  | string              | yes      | Stable ID: `{dimension_abbr}-{seq:03d}-{short-slug}` |
| `severity`            | string              | yes      | `critical`, `high`, `medium`, `low`                  |
| `category`            | string              | yes      | Machine-readable category (see categories below)     |
| `confidence`          | integer             | yes      | 0-100 certainty score                                |
| `repo`                | string \| null      | no       | Source repository                                    |
| `dimension`           | string \| integer   | yes      | Dimension name or index                              |
| `detail`              | string              | yes      | Human-readable description                           |
| `auto_fixable`        | boolean             | yes      | Whether `--fix` can safely remediate this            |
| `suggested_action`    | string              | yes      | Developer remediation guidance                       |
| `files`               | FileLocation[]      | yes      | Affected files with location data                    |
| `related_findings`    | string[]            | no       | IDs of related findings                              |
| `external_references` | ExternalReference[] | no       | GitHub issues, CVEs, etc.                            |
| `metadata`            | FindingMetadata     | yes      | Detection context                                    |

### Finding ID Format

IDs are **deterministic and stable** across audits:

```
{dimension_abbr}-{seq:03d}-{short-slug}
```

Examples:

- `api-001-flutter-path-mismatch`
- `lifecycle-023-stale-epic-130`
- `security-005-hardcoded-api-key`

**Dimension abbreviations**:

| Dimension      | Abbreviation |
| -------------- | ------------ |
| api_alignment  | `api`        |
| lifecycle      | `lifecycle`  |
| documentation  | `docs`       |
| feature_parity | `parity`     |
| test_coverage  | `tests`      |
| security       | `security`   |
| dependencies   | `deps`       |
| ci_cd          | `ci`         |

### FileLocation Object

```json
{
  "path": "lib/core/api/auth_service.dart",
  "line": 42,
  "line_end": 45,
  "lines_of_context": 3,
  "code_snippet": "// snippet text here"
}
```

### Finding Categories by Dimension

#### Dimension 1: API Alignment

| Category                  | Severity Range | Auto-fixable |
| ------------------------- | -------------- | ------------ |
| `PATH_MISMATCH`           | high-critical  | No           |
| `METHOD_MISMATCH`         | high           | No           |
| `AUTH_MISMATCH`           | high           | No           |
| `RESPONSE_SHAPE_MISMATCH` | medium-high    | No           |

#### Dimension 2: Lifecycle

| Category             | Severity Range | Auto-fixable |
| -------------------- | -------------- | ------------ |
| `STALE_EPIC`         | medium-high    | **Yes**      |
| `BOARD_STATUS_DRIFT` | low-medium     | **Yes**      |
| `ORPHANED_ISSUE`     | low            | No           |
| `STALE_BLOCKER`      | medium         | **Yes**      |

#### Dimension 3: Documentation

| Category                 | Severity Range | Auto-fixable |
| ------------------------ | -------------- | ------------ |
| `DOC_ENDPOINT_DRIFT`     | medium-high    | No           |
| `STALE_OPENAPI_SPEC`     | high           | No           |
| `MISSING_API_DOCS`       | medium         | No           |
| `STALE_ARCHITECTURE_DOC` | low-medium     | No           |

#### Dimension 4: Feature Parity

| Category          | Severity Range | Auto-fixable |
| ----------------- | -------------- | ------------ |
| `FEATURE_MISSING` | high-critical  | No           |
| `FEATURE_PARTIAL` | medium         | No           |
| `FEATURE_STUB`    | low-medium     | No           |

#### Dimension 5: Test Coverage

| Category                  | Severity Range | Auto-fixable |
| ------------------------- | -------------- | ------------ |
| `MISSING_COVERAGE`        | medium-high    | No           |
| `CRITICAL_PATH_UNCOVERED` | high-critical  | No           |
| `STUB_TEST_FILE`          | low-medium     | No           |

#### Dimension 6: Security

| Category                 | Severity Range | Auto-fixable |
| ------------------------ | -------------- | ------------ |
| `HARDCODED_SECRET`       | critical       | No           |
| `UNVALIDATED_INPUT`      | high           | No           |
| `UNENCRYPTED_DATA`       | high           | No           |
| `SQL_INJECTION`          | critical       | No           |
| `XSS_RISK`               | high           | No           |
| `INSECURE_DEPENDENCY`    | high           | No           |
| `EXPOSED_INTERNAL_ERROR` | medium         | No           |
| `SENSITIVE_DATA_LOG`     | medium-high    | No           |

#### Dimension 7: Dependencies

| Category                | Severity Range | Auto-fixable |
| ----------------------- | -------------- | ------------ |
| `VULNERABLE_DEPENDENCY` | high-critical  | No           |
| `OUTDATED_DEPENDENCY`   | low-medium     | No           |

#### Dimension 8: CI/CD

| Category                   | Severity Range | Auto-fixable |
| -------------------------- | -------------- | ------------ |
| `DISABLED_WORKFLOW`        | high           | No           |
| `CONTINUE_ON_ERROR`        | medium         | No           |
| `MISSING_COVERAGE_ENFORCE` | medium         | No           |
| `MISSING_REQUIRED_CHECK`   | high           | No           |
| `OUTDATED_ACTION`          | low            | No           |
| `NO_BRANCH_PROTECTION`     | high           | No           |
| `SECRETS_IN_WORKFLOW`      | critical       | No           |

---

## Dimension-Specific Finding Extensions

### Dimension 1: API Alignment Extension

```json
{
  "...": "base fields",
  "client": "flutter",
  "endpoint": "POST /v1/pipelines/{issue}/run",
  "expected": "POST /v1/pipelines/trigger",
  "actual_or_called": "POST /v1/pipelines/{issue}/run",
  "mismatch_type": "PATH_MISMATCH"
}
```

### Dimension 2: Lifecycle Extension

```json
{
  "...": "base fields",
  "issue_number": 130,
  "issue_title": "Epic: Auth Client Alignment",
  "issue_state": "open",
  "issue_type": "epic",
  "lifecycle_issue": "STALE_EPIC",
  "board_status_current": "Ready",
  "board_status_expected": "Done",
  "related_issues": [45, 67, 89]
}
```

### Dimension 3: Documentation Extension

```json
{
  "...": "base fields",
  "doc_type": "ECOSYSTEM",
  "doc_file": "docs/ECOSYSTEM.md",
  "content_mismatch": "Endpoint table lists POST /v1/auth/web/github but platform has POST /v1/auth/github",
  "truth_source": "platform_routes"
}
```

### Dimension 4: Feature Parity Extension

```json
{
  "...": "base fields",
  "feature": "pipelines.async",
  "feature_category": "pipelines",
  "coverage": {
    "vscode": "FULL",
    "angular": "PARTIAL",
    "flutter": "MISSING",
    "platform": "FULL"
  },
  "parity_score": 57,
  "priority_rationale": "Async execution is critical for all clients to support non-blocking workflows"
}
```

**Coverage levels**:

| Level     | Score | Meaning                       |
| --------- | ----- | ----------------------------- |
| `FULL`    | 100   | Service + UI + Tests          |
| `PARTIAL` | 70    | Service + UI, no tests        |
| `STUB`    | 30    | Exists but incomplete         |
| `MISSING` | 0     | Not implemented               |
| `N/A`     | 100   | Not applicable to this client |

### Dimension 5: Test Coverage Extension

```json
{
  "...": "base fields",
  "file_or_module": "src/app/core/api/pipeline.service.ts",
  "coverage_percent": 45,
  "coverage_threshold": 80,
  "untested_paths": ["error_handling", "retry_logic", "stream_close"],
  "critical_path_coverage": "NO",
  "test_file_exists": true
}
```

### Dimension 6: Security Extension

```json
{
  "...": "base fields",
  "security_category": "HARDCODED_SECRET",
  "cve_reference": null,
  "affected_code": "const API_KEY = '[REDACTED]'"
}
```

### Dimension 7: Dependencies Extension

```json
{
  "...": "base fields",
  "package_or_dependency": "lodash",
  "current_version": "4.17.15",
  "latest_version": "4.17.21",
  "is_vulnerability": false,
  "cve_ids": [],
  "ecosystem": "npm"
}
```

### Dimension 8: CI/CD Extension

```json
{
  "...": "base fields",
  "workflow_file": ".github/workflows/test.yml",
  "workflow_name": "Test Suite",
  "ci_issue": "CONTINUE_ON_ERROR"
}
```

---

## Sample Full Report

```json
{
  "$schema": "https://nightgauge.dev/schemas/product-audit-report-v1.json",
  "schema_version": "1.0",
  "timestamp": "2026-03-23T14:30:00Z",
  "run_id": "audit-20260323-143000",
  "workspace_root": "/Users/you/repos/acme",
  "overall_score": 78,
  "overall_score_trend": {
    "previous_score": 72,
    "delta": 6,
    "direction": "improving",
    "days_since_last_audit": 7
  },
  "dimensions": [
    {
      "name": "api_alignment",
      "index": 1,
      "score": 85,
      "weight": 0.2,
      "finding_count": 3,
      "critical_count": 0,
      "high_count": 1,
      "medium_count": 2,
      "low_count": 0,
      "status": "completed",
      "duration_seconds": 47,
      "findings": [
        {
          "id": "api-001-flutter-path-mismatch",
          "severity": "high",
          "category": "PATH_MISMATCH",
          "confidence": 95,
          "repo": "acme-mobile",
          "dimension": "api_alignment",
          "client": "flutter",
          "endpoint": "POST /v1/pipelines/{issue}/run",
          "expected": "POST /v1/pipelines/trigger",
          "actual_or_called": "POST /v1/pipelines/{issue}/run",
          "mismatch_type": "PATH_MISMATCH",
          "detail": "Flutter calls non-existent endpoint. Platform expects POST /v1/pipelines/trigger.",
          "auto_fixable": false,
          "suggested_action": "Update Flutter API client path from '/v1/pipelines/{issue}/run' to '/v1/pipelines/trigger'",
          "files": [
            {
              "path": "lib/core/network/api/pipeline_api.dart",
              "line": 42,
              "lines_of_context": 3,
              "code_snippet": "final response = await dio.post(\n  '/v1/pipelines/{issue}/run',\n  data: body\n);"
            }
          ],
          "related_findings": [],
          "external_references": [],
          "metadata": {
            "detected_at": "2026-03-23T14:32:00Z",
            "detection_method": "static_analysis",
            "manual_review_required": false
          }
        }
      ]
    }
  ],
  "critical_findings": [],
  "trend_analysis": {
    "audits_in_history": 3,
    "score_history": [68, 72, 78],
    "improving_dimensions": ["api_alignment", "documentation"],
    "degrading_dimensions": ["test_coverage"],
    "stable_dimensions": ["lifecycle", "feature_parity", "security", "dependencies", "ci_cd"],
    "recommendations": [
      "Test coverage declining — prioritize writing tests in next sprint",
      "API alignment improved 3 points — continue focused cross-repo reviews"
    ]
  },
  "execution_metadata": {
    "mode": "full",
    "dimensions_run": [
      "api_alignment",
      "lifecycle",
      "documentation",
      "feature_parity",
      "test_coverage",
      "security",
      "dependencies",
      "ci_cd"
    ],
    "dimensions_skipped": [],
    "duration_seconds": 287,
    "repos_scanned": 4,
    "repos_available": ["nightgauge", "acme-platform", "acme-dashboard", "acme-mobile"],
    "repos_missing": [],
    "issues_created": 0,
    "issues_auto_fixed": 0,
    "skip_reasons": {},
    "cli_flags": {
      "create_issues": false,
      "fix": false,
      "quick": false,
      "threshold": 75
    }
  }
}
```

---

## Adding New Dimensions

The schema is designed for extensibility. To add a new dimension (index 9+):

1. Add a new entry to the `dimensions` array in the report with a unique `name` and `index`
2. Create dimension-specific finding fields following the pattern in this document
3. Add the new dimension name to `DimensionResult.name` enum in `schemas/product-audit-report-v1.json`
4. Add dimension-specific finding extension to `schemas/product-audit-finding-v1.json`
5. Update `product_audit.dimension_weights` in `.nightgauge/config.yaml` (must sum to 1.0)
6. Add a subagent task to Phase 2 of `SKILL.md`

Old reports without the new dimension remain valid — the schema uses
`additionalProperties: true` at the dimension level for forward compatibility.

---

## Author

nightgauge
