/**
 * Workspace setup helpers for E2E tests.
 *
 * Creates isolated temporary directories with pre-populated pipeline context
 * files so tests can exercise context-file reading/writing without touching
 * the real repository.
 */

import * as fs from "fs";
import * as os from "os";
import * as path from "path";

/** Describes a pre-created pipeline context file. */
export interface ContextFileSpec {
  /** Relative path inside the temp workspace (e.g. ".nightgauge/pipeline/issue-42.json"). */
  relativePath: string;
  /** JSON-serializable content to write. */
  content: unknown;
}

/** Handle returned by createTempWorkspace. Call cleanup() in afterEach. */
export interface TempWorkspace {
  /** Absolute path to the temporary workspace root. */
  root: string;
  /** Absolute path to the .nightgauge/pipeline directory. */
  pipelineDir: string;
  /** Read a pipeline context file and parse it as JSON. */
  readContext(filename: string): unknown;
  /** Write a JSON object to a pipeline context file. */
  writeContext(filename: string, content: unknown): void;
  /** Remove the temporary workspace directory. */
  cleanup(): void;
}

/**
 * Create a temporary workspace directory with optional pre-seeded context files.
 * Use cleanup() in afterEach to remove it.
 */
export function createTempWorkspace(seed: ContextFileSpec[] = []): TempWorkspace {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "nightgauge-test-"));
  const pipelineDir = path.join(root, ".nightgauge", "pipeline");
  fs.mkdirSync(pipelineDir, { recursive: true });

  for (const spec of seed) {
    const abs = path.join(root, spec.relativePath);
    fs.mkdirSync(path.dirname(abs), { recursive: true });
    fs.writeFileSync(abs, JSON.stringify(spec.content, null, 2), "utf-8");
  }

  return {
    root,
    pipelineDir,
    readContext(filename: string): unknown {
      const abs = path.join(pipelineDir, filename);
      return JSON.parse(fs.readFileSync(abs, "utf-8"));
    },
    writeContext(filename: string, content: unknown): void {
      const abs = path.join(pipelineDir, filename);
      fs.writeFileSync(abs, JSON.stringify(content, null, 2), "utf-8");
    },
    cleanup(): void {
      fs.rmSync(root, { recursive: true, force: true });
    },
  };
}

/**
 * Build a minimal issue-{N}.json fixture that passes IssueContextSchema validation.
 */
export function makeIssueContext(
  issueNumber: number,
  overrides: Record<string, unknown> = {}
): Record<string, unknown> {
  return {
    schema_version: "1.5",
    issue_number: issueNumber,
    type: "feature",
    title: `feat: test issue #${issueNumber}`,
    body: "Test issue body.",
    labels: ["type:feature", "size:M", "priority:medium"],
    requirements: {
      summary: "Test requirement summary.",
      user_story: null,
      acceptance_criteria: ["AC1: passes", "AC2: passes"],
      technical_notes: null,
    },
    routing: {
      change_type: "code",
      complexity_score: 3,
      suggested_route: "standard",
      skip_stages: [],
      rationale: "Test routing",
      estimated_time_minutes: 30,
    },
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

/**
 * Build a minimal planning-{N}.json fixture.
 */
export function makePlanningContext(
  issueNumber: number,
  overrides: Record<string, unknown> = {}
): Record<string, unknown> {
  return {
    schema_version: "1.3",
    issue_number: issueNumber,
    plan_file: `.nightgauge/plans/${issueNumber}-test-plan.md`,
    approach: "Implement feature following the plan.",
    files_to_create: [],
    files_to_modify: [],
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

/**
 * Build a minimal dev-{N}.json fixture.
 */
export function makeDevContext(
  issueNumber: number,
  overrides: Record<string, unknown> = {}
): Record<string, unknown> {
  return {
    schema_version: "1.7",
    issue_number: issueNumber,
    commit_sha: null,
    files_changed: { created: [], modified: [], deleted: [] },
    build_verification: {
      ran: true,
      status: "passed",
      commands_run: [],
      timestamp: null,
    },
    tests_status: {
      passed: 5,
      failed: 0,
      coverage: null,
      test_command: "npx vitest run",
      includes_integration: false,
      includes_e2e: false,
      test_files_run: 1,
      e2e_framework: null,
      e2e_tests_generated: false,
    },
    quality_checks: {
      code_standards: "passed",
      security_review: "passed",
      type_check: "passed",
      dead_code_scan: "not_run",
    },
    feedback: [],
    retry_count: 0,
    retry_reasons: [],
    created_at: new Date().toISOString(),
    ...overrides,
  };
}
