import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";
import { EventBus, PipelineRunEmitter } from "../../src/events/EventBus.js";
import { TokenTracker } from "../../src/tracking/TokenTracker.js";
import { ContextManager } from "../../src/context/ContextManager.js";
import { StageExecutor, type SDKMessage } from "../../src/orchestrator/StageExecutor.js";
import {
  IssuePickupStage,
  FeaturePlanningStage,
  FeatureDevStage,
  PRCreateStage,
} from "../../src/stages/index.js";
import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
  ValidateContextSchema,
  PRContextSchema,
} from "../../src/context/schemas/index.js";

function createPromptCapturingQuery(captured: { prompt?: string }) {
  return async function* query(options: { prompt: string }): AsyncGenerator<SDKMessage> {
    captured.prompt = options.prompt;
    yield { type: "assistant", subtype: "text", text: "ok" };
    yield {
      type: "result",
      usage: { input_tokens: 10, output_tokens: 5 },
      total_cost_usd: 0.001,
    };
  };
}

async function createSkillsFixture(root: string): Promise<string> {
  const skillsBasePath = path.join(root, "skills");
  const skillDirs = [
    "nightgauge-issue-pickup",
    "nightgauge-feature-planning",
    "nightgauge-feature-dev",
    "nightgauge-pr-create",
  ];

  for (const dir of skillDirs) {
    const fullDir = path.join(skillsBasePath, dir);
    await fs.mkdir(fullDir, { recursive: true });
    await fs.writeFile(
      path.join(fullDir, "SKILL.md"),
      `# ${dir}\n\nFollow instructions.\n`,
      "utf-8"
    );
  }

  return skillsBasePath;
}

describe("Codex stage validation coverage", () => {
  let tempRoot: string;
  let contextPath: string;
  let contextManager: ContextManager;
  let skillsBasePath: string;
  let executor: StageExecutor;
  const issueNumber = 553;

  beforeEach(async () => {
    tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "codex-stage-"));
    contextPath = path.join(tempRoot, ".nightgauge/pipeline");
    await fs.mkdir(contextPath, { recursive: true });
    skillsBasePath = await createSkillsFixture(tempRoot);

    const captured: { prompt?: string } = {};
    executor = new StageExecutor(
      new TokenTracker(),
      new PipelineRunEmitter(new EventBus(), 1),
      createPromptCapturingQuery(captured)
    );
    contextManager = new ContextManager(contextPath);
  });

  afterEach(async () => {
    await fs.rm(tempRoot, { recursive: true, force: true });
  });

  it("should execute issue-pickup happy path", async () => {
    await contextManager.write(IssueContextSchema, `issue-${issueNumber}.json`, {
      schema_version: "1.4",
      issue_number: issueNumber,
      title: "Codex validation checks",
      type: "feature",
      branch: "feat/553-codex-validation",
      base_branch: "main",
      requirements: {
        summary: "Add Codex validation coverage and preflight checks",
        acceptance_criteria: ["Add tests", "Add preflight checks"],
      },
      labels: ["type:feature"],
      created_at: "2026-02-10T00:00:00.000Z",
    });

    const stage = new IssuePickupStage();
    const result = await stage.execute(executor, contextManager, {
      issueNumber,
      skillsBasePath,
    });

    expect(result.success).toBe(true);
    expect(result.output?.issue_number).toBe(issueNumber);
  });

  it("should execute feature-planning happy path and preserve acceptance criteria in prompt", async () => {
    await contextManager.write(IssueContextSchema, `issue-${issueNumber}.json`, {
      schema_version: "1.4",
      issue_number: issueNumber,
      title: "Codex validation checks",
      type: "feature",
      branch: "feat/553-codex-validation",
      base_branch: "main",
      requirements: {
        summary: "Add Codex validation coverage and preflight checks",
        acceptance_criteria: [
          "Codex stage happy-path tests are added for MVP stages",
          "Regression checks verify plan creation and criteria verification still work",
        ],
      },
      labels: ["type:feature"],
      created_at: "2026-02-10T00:00:00.000Z",
    });
    await contextManager.write(PlanningContextSchema, `planning-${issueNumber}.json`, {
      schema_version: "1.1",
      issue_number: issueNumber,
      plan_file: `.nightgauge/plans/${issueNumber}-codex-validation.md`,
      approach: "Add preflight checks and validation tests",
      files_to_create: [
        "packages/nightgauge-sdk/src/cli/codexPreflight.ts",
        "packages/nightgauge-sdk/tests/cli/codexPreflight.test.ts",
      ],
      files_to_modify: [
        "packages/nightgauge-sdk/src/cli/commands/run.ts",
        "packages/nightgauge-sdk/src/cli/commands/stage.ts",
      ],
      created_at: "2026-02-10T00:05:00.000Z",
    });

    const captured: { prompt?: string } = {};
    executor = new StageExecutor(
      new TokenTracker(),
      new PipelineRunEmitter(new EventBus(), 1),
      createPromptCapturingQuery(captured)
    );
    const stage = new FeaturePlanningStage();
    const result = await stage.execute(executor, contextManager, {
      issueNumber,
      skillsBasePath,
    });

    expect(result.success).toBe(true);
    expect(captured.prompt).toContain(
      "- [ ] Codex stage happy-path tests are added for MVP stages"
    );
    expect(captured.prompt).toContain(".nightgauge/plans/553-*.md");
    expect(captured.prompt).toContain(".nightgauge/pipeline/planning-553.json");
  });

  it("should execute feature-dev happy path", async () => {
    await contextManager.write(PlanningContextSchema, `planning-${issueNumber}.json`, {
      schema_version: "1.1",
      issue_number: issueNumber,
      plan_file: `.nightgauge/plans/${issueNumber}-codex-validation.md`,
      approach: "Implement tests and preflight checks",
      files_to_create: [],
      files_to_modify: [],
      created_at: "2026-02-10T00:10:00.000Z",
    });
    await contextManager.write(DevContextSchema, `dev-${issueNumber}.json`, {
      schema_version: "1.0",
      issue_number: issueNumber,
      commit_sha: "abc123",
      files_changed: {
        created: ["packages/nightgauge-sdk/tests/stages/codexValidation.test.ts"],
        modified: ["packages/nightgauge-sdk/src/cli/commands/run.ts"],
        deleted: [],
      },
      tests_status: {
        passed: 12,
        failed: 0,
        coverage: 85,
      },
      quality_checks: {
        code_standards: "passed",
        security_review: "passed",
      },
      created_at: "2026-02-10T00:20:00.000Z",
    });

    const stage = new FeatureDevStage();
    const result = await stage.execute(executor, contextManager, {
      issueNumber,
      skillsBasePath,
    });

    expect(result.success).toBe(true);
    expect(result.output?.commit_sha).toBe("abc123");
  });

  it("should execute pr-create happy path", async () => {
    await contextManager.write(DevContextSchema, `dev-${issueNumber}.json`, {
      schema_version: "1.0",
      issue_number: issueNumber,
      commit_sha: "def456",
      files_changed: {
        created: [],
        modified: ["packages/nightgauge-sdk/src/cli/codexPreflight.ts"],
        deleted: [],
      },
      tests_status: {
        passed: 15,
        failed: 0,
        coverage: 90,
      },
      quality_checks: {
        code_standards: "passed",
        security_review: "passed",
      },
      created_at: "2026-02-10T00:30:00.000Z",
    });
    await contextManager.write(PRContextSchema, `pr-${issueNumber}.json`, {
      schema_version: "1.0",
      issue_number: issueNumber,
      pr_number: 999,
      pr_url: "https://github.com/nightgauge/nightgauge/pull/999",
      title: "fix: add codex validation coverage and preflight checks",
      base_branch: "main",
      status: "open",
      reviewers: ["maintainer"],
      preflight_results: {
        json_validation: "passed",
        yaml_validation: "passed",
        version_consistency: "passed",
        security_scan: "passed",
        coverage_check: "passed",
      },
      created_at: "2026-02-10T00:35:00.000Z",
    });

    const stage = new PRCreateStage();
    const result = await stage.execute(executor, contextManager, {
      issueNumber,
      skillsBasePath,
    });

    expect(result.success).toBe(true);
    expect(result.output?.pr_number).toBe(999);
  });
});

/**
 * Schema contract smoke tests for feature-validate and pr-merge.
 *
 * These two stages have Codex wrapper scripts but no SDK stage class
 * implementations. These tests verify that the context file schemas
 * used by those stages parse correctly — confirming the contract between
 * the wrapper scripts and downstream pipeline consumers.
 *
 * See: docs/strategy/codex/CODEX_CLI_PARITY_MATRIX.md — Post-GA Adoption Notes
 */
describe("Codex schema contract — feature-validate and pr-merge", () => {
  let tempRoot: string;
  let contextPath: string;
  let contextManager: ContextManager;
  const issueNumber = 553;

  beforeEach(async () => {
    tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "codex-schema-"));
    contextPath = path.join(tempRoot, ".nightgauge/pipeline");
    await fs.mkdir(contextPath, { recursive: true });
    contextManager = new ContextManager(contextPath);
  });

  afterEach(async () => {
    await fs.rm(tempRoot, { recursive: true, force: true });
  });

  it("should accept a valid feature-validate output context (validate-{N}.json)", async () => {
    // feature-validate writes validate-{N}.json; pr-create and pr-merge read it.
    // This test confirms the ValidateContextSchema accepts the expected output
    // structure produced by the Codex wrapper for this stage.
    const validatePayload = {
      schema_version: "1.9",
      issue_number: issueNumber,
      validation_status: "passed",
      build: {
        ran: true,
        passed: true,
        command: "npm run -w nightgauge-vscode build",
      },
      unit_tests: {
        ran: true,
        passed: true,
        framework: "vitest",
        tests_run: 15,
        tests_passed: 15,
      },
      dead_code_warnings: [],
      commit_sha: "abc456def",
      created_at: "2026-03-13T00:00:00.000Z",
    };

    await contextManager.write(
      ValidateContextSchema,
      `validate-${issueNumber}.json`,
      validatePayload
    );

    const loaded = await contextManager.read(ValidateContextSchema, `validate-${issueNumber}.json`);
    expect(loaded.issue_number).toBe(issueNumber);
    expect(loaded.validation_status).toBe("passed");
    expect(loaded.build?.ran).toBe(true);
    expect(loaded.unit_tests?.tests_run).toBe(15);
  });

  it("should accept a valid feature-validate output with ac_completion_check for docs issues", async () => {
    // Docs-type issues go through an AC completion gate in feature-validate.
    // This confirms the schema supports the ac_completion_check field written
    // by the Codex wrapper for docs-type issues.
    const validatePayload = {
      schema_version: "1.9",
      issue_number: issueNumber,
      validation_status: "passed",
      ac_completion_check: {
        status: "passed",
        checked_count: 4,
        unchecked_count: 0,
        applicable: true,
      },
      commit_sha: "abc456def",
      created_at: "2026-03-13T00:00:00.000Z",
    };

    await contextManager.write(
      ValidateContextSchema,
      `validate-${issueNumber}.json`,
      validatePayload
    );

    const loaded = await contextManager.read(ValidateContextSchema, `validate-${issueNumber}.json`);
    expect(loaded.ac_completion_check?.status).toBe("passed");
    expect(loaded.ac_completion_check?.checked_count).toBe(4);
  });

  it("should accept a valid pr-merge input context (pr-{N}.json with status open)", async () => {
    // pr-merge reads pr-{N}.json produced by pr-create and updates status to merged.
    // This test confirms the PRContextSchema accepts the input structure that
    // the Codex wrapper for pr-merge receives.
    const prPayload = {
      schema_version: "1.0" as const,
      issue_number: issueNumber,
      pr_number: 1001,
      pr_url: "https://github.com/nightgauge/nightgauge/pull/1001",
      title: "feat(#553): add codex validation coverage",
      base_branch: "main",
      status: "open" as const,
      reviewers: ["maintainer"],
      preflight_results: {
        json_validation: "passed",
        yaml_validation: "passed",
        version_consistency: "passed",
        security_scan: "passed",
        coverage_check: "passed",
      },
      created_at: "2026-03-13T00:05:00.000Z",
    };

    await contextManager.write(PRContextSchema, `pr-${issueNumber}.json`, prPayload);

    const loaded = await contextManager.read(PRContextSchema, `pr-${issueNumber}.json`);
    expect(loaded.pr_number).toBe(1001);
    expect(loaded.status).toBe("open");
  });

  it("should accept pr-merge completion status (status: merged)", async () => {
    // After pr-merge completes, status transitions from open to merged.
    // This confirms the PRContextSchema accepts the post-merge status value.
    const mergedPayload = {
      schema_version: "1.0" as const,
      issue_number: issueNumber,
      pr_number: 1001,
      pr_url: "https://github.com/nightgauge/nightgauge/pull/1001",
      title: "feat(#553): add codex validation coverage",
      base_branch: "main",
      status: "merged" as const,
      reviewers: ["maintainer"],
      created_at: "2026-03-13T00:10:00.000Z",
    };

    await contextManager.write(PRContextSchema, `pr-${issueNumber}.json`, mergedPayload);

    const loaded = await contextManager.read(PRContextSchema, `pr-${issueNumber}.json`);
    expect(loaded.status).toBe("merged");
  });
});
