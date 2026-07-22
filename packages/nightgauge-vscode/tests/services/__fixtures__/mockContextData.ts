/**
 * Schema-valid mock data for pipeline context files.
 *
 * Used across all HeadlessOrchestrator test files to ensure readFileSync
 * returns data that passes Zod schema validation.
 *
 * @see Issue #1180 - Deterministic skill output validation
 */

/**
 * Default issue number used across most tests.
 * Tests that use different issue numbers should override MOCK_ISSUE_CONTEXT.issue_number.
 */
export const DEFAULT_ISSUE_NUMBER = 42;

export const MOCK_ISSUE_CONTEXT = {
  schema_version: "1.3",
  issue_number: DEFAULT_ISSUE_NUMBER,
  title: "Test issue",
  type: "feature",
  branch: "main",
  base_branch: "main",
  requirements: { summary: "Test summary" },
  labels: ["type:feature", "priority:medium"],
  routing: {
    change_type: "code",
    complexity_score: 3,
    suggested_route: "standard",
    skip_stages: [],
    rationale: "M code change",
    estimated_time_minutes: 30,
  },
  created_at: "2026-01-01T00:00:00Z",
};

export const MOCK_PLANNING_CONTEXT = {
  schema_version: "1.1",
  issue_number: DEFAULT_ISSUE_NUMBER,
  plan_file: ".nightgauge/plans/42-test.md",
  approach: "standard",
  files_to_create: [],
  files_to_modify: ["src/test.ts"],
  created_at: "2026-01-01T00:00:00Z",
};

export const MOCK_DEV_CONTEXT = {
  schema_version: "1.4",
  issue_number: DEFAULT_ISSUE_NUMBER,
  // Issue #1608: commit_sha is null — commit deferred to feature-validate
  commit_sha: null,
  files_changed: { created: [], modified: ["src/test.ts"], deleted: [] },
  tests_status: { passed: 5, failed: 0 },
  quality_checks: { code_standards: "passed", security_review: "passed" },
  created_at: "2026-01-01T00:00:00Z",
};

export const MOCK_VALIDATE_CONTEXT = {
  schema_version: "1.9",
  issue_number: DEFAULT_ISSUE_NUMBER,
  validation_status: "passed",
  // Issue #1608: commit_sha now lives in validate context
  commit_sha: "abc123",
  manual_checklist: [{ item: "Tests pass", verified: true }],
  project_type: "typescript",
  created_at: "2026-01-01T00:00:00Z",
};

export const MOCK_PR_CONTEXT = {
  schema_version: "1.0",
  issue_number: DEFAULT_ISSUE_NUMBER,
  pr_number: 100,
  pr_url: "https://github.com/test/test/pull/100",
  title: "feat(#42): test",
  base_branch: "main",
  status: "open",
  reviewers: [],
  preflight_results: {
    json_validation: "passed",
    yaml_validation: "passed",
    version_consistency: "passed",
    security_scan: "passed",
    coverage_check: "passed",
  },
  created_at: "2026-01-01T00:00:00Z",
};

/**
 * Returns schema-valid JSON for context files based on file path.
 * Falls back to issue context for unmatched paths (backward-compatible).
 *
 * Usage in vi.mock factory: Not recommended due to hoisting issues.
 * Usage in beforeEach: `vi.mocked(fs.readFileSync).mockImplementation(mockReadFileSync)`
 */
export function mockReadFileSync(filePath: string): string {
  if (typeof filePath === "string") {
    if (filePath.includes("planning-")) return JSON.stringify(MOCK_PLANNING_CONTEXT);
    if (filePath.includes("validate-")) return JSON.stringify(MOCK_VALIDATE_CONTEXT);
    if (filePath.includes("dev-")) return JSON.stringify(MOCK_DEV_CONTEXT);
    if (filePath.includes("pr-")) return JSON.stringify(MOCK_PR_CONTEXT);
  }
  // Default: issue context (also used for non-context reads like routing)
  return JSON.stringify(MOCK_ISSUE_CONTEXT);
}

/**
 * Create a mockReadFileSync with a custom issue number.
 * Useful for tests that use issue numbers other than 42.
 */
export function createMockReadFileSync(issueNumber: number): (filePath: string) => string {
  const issueCtx = { ...MOCK_ISSUE_CONTEXT, issue_number: issueNumber };
  const planCtx = { ...MOCK_PLANNING_CONTEXT, issue_number: issueNumber };
  const devCtx = { ...MOCK_DEV_CONTEXT, issue_number: issueNumber };
  const valCtx = { ...MOCK_VALIDATE_CONTEXT, issue_number: issueNumber };
  const prCtx = { ...MOCK_PR_CONTEXT, issue_number: issueNumber };

  return (filePath: string): string => {
    if (typeof filePath === "string") {
      if (filePath.includes("planning-")) return JSON.stringify(planCtx);
      if (filePath.includes("validate-")) return JSON.stringify(valCtx);
      if (filePath.includes("dev-")) return JSON.stringify(devCtx);
      if (filePath.includes("pr-")) return JSON.stringify(prCtx);
    }
    return JSON.stringify(issueCtx);
  };
}
