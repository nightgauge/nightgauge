/**
 * Unit tests for Dimension 8: CI/CD Integrity
 *
 * Tests YAML workflow parsing, disabled workflow detection,
 * continue-on-error flag detection, and missing required step detection.
 */

import { describe, it, expect } from "vitest";
import {
  parseWorkflowContent,
  findContinueOnErrorSteps,
  hasStepMatching,
  getAllStepNames,
} from "../../../../src/analysis/product-audit/utils/workflow-parser.js";

// ── parseWorkflowContent ──────────────────────────────────────────────────────

describe("parseWorkflowContent", () => {
  it("parses a valid workflow YAML", () => {
    const yaml = `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Build
        run: npm run build
      - name: Test
        run: npm test
`.trim();

    const { parsed, error } = parseWorkflowContent(yaml, "ci.yml");
    expect(error).toBeUndefined();
    expect(parsed).not.toBeNull();
    expect(parsed!.name).toBe("CI");
    expect(parsed!.isDisabled).toBe(false);
  });

  it("detects a workflow where all jobs have if: false", () => {
    const yaml = `
name: Disabled Workflow
on: [push]
jobs:
  build:
    if: false
    runs-on: ubuntu-latest
    steps:
      - name: Build
        run: npm run build
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "disabled.yml");
    expect(parsed!.isDisabled).toBe(true);
  });

  it("returns error for malformed YAML", () => {
    const badYaml = "{ invalid yaml: [unclosed";
    const { parsed, error } = parseWorkflowContent(badYaml, "bad.yml");
    expect(parsed).toBeNull();
    expect(error).toContain("bad.yml");
  });

  it("returns error for non-object YAML", () => {
    const { parsed, error } = parseWorkflowContent("- just a list", "list.yml");
    expect(parsed).toBeNull();
    expect(error).toBeDefined();
  });

  it("handles workflow without jobs gracefully", () => {
    const yaml = `
name: No Jobs
on: [push]
`.trim();

    const { parsed, error } = parseWorkflowContent(yaml, "no-jobs.yml");
    expect(error).toBeUndefined();
    expect(parsed!.isDisabled).toBe(false);
    expect(parsed!.jobs).toBeUndefined();
  });

  it("does not flag workflow as disabled when only one of multiple jobs is disabled", () => {
    const yaml = `
name: Partially disabled
on: [push]
jobs:
  disabled-job:
    if: false
    steps: []
  active-job:
    runs-on: ubuntu-latest
    steps:
      - name: Test
        run: npm test
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "partial.yml");
    // isAllJobsDisabled requires ALL jobs to be disabled
    expect(parsed!.isDisabled).toBe(false);
  });
});

// ── findContinueOnErrorSteps ──────────────────────────────────────────────────

describe("findContinueOnErrorSteps", () => {
  it("returns empty array when no continue-on-error steps", () => {
    const yaml = `
name: CI
jobs:
  build:
    steps:
      - name: Build
        run: npm run build
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    expect(findContinueOnErrorSteps(parsed!)).toHaveLength(0);
  });

  it("detects step-level continue-on-error: true", () => {
    const yaml = `
name: CI
jobs:
  build:
    steps:
      - name: Build
        run: npm run build
      - name: Optional Step
        run: npm run optional
        continue-on-error: true
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    const results = findContinueOnErrorSteps(parsed!);
    expect(results).toHaveLength(1);
    expect(results[0].stepName).toBe("Optional Step");
    expect(results[0].jobId).toBe("build");
    expect(results[0].stepIndex).toBe(1);
  });

  it("detects job-level continue-on-error: true", () => {
    const yaml = `
name: CI
jobs:
  flaky-job:
    continue-on-error: true
    steps:
      - name: Test
        run: npm test
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    const results = findContinueOnErrorSteps(parsed!);
    expect(results).toHaveLength(1);
    expect(results[0].jobId).toBe("flaky-job");
    expect(results[0].stepIndex).toBe(-1);
  });

  it("handles workflow with no jobs", () => {
    const { parsed } = parseWorkflowContent("name: Empty\non: [push]", "empty.yml");
    expect(findContinueOnErrorSteps(parsed!)).toHaveLength(0);
  });

  it("collects multiple continue-on-error instances", () => {
    const yaml = `
name: CI
jobs:
  build:
    steps:
      - name: Step1
        run: cmd1
        continue-on-error: true
      - name: Step2
        run: cmd2
        continue-on-error: true
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    expect(findContinueOnErrorSteps(parsed!)).toHaveLength(2);
  });
});

// ── hasStepMatching ───────────────────────────────────────────────────────────

describe("hasStepMatching", () => {
  it("returns true when workflow has a step matching keyword", () => {
    const yaml = `
name: CI
jobs:
  ci:
    steps:
      - name: Build Application
        run: npm run build
      - name: Run Tests
        run: npm test
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    expect(hasStepMatching(parsed!, ["build"])).toBe(true);
    expect(hasStepMatching(parsed!, ["test"])).toBe(true);
  });

  it("returns false when no step matches keyword", () => {
    const yaml = `
name: CI
jobs:
  ci:
    steps:
      - name: Deploy
        run: ./deploy.sh
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    expect(hasStepMatching(parsed!, ["lint", "eslint"])).toBe(false);
  });

  it("matches against step run commands, not just names", () => {
    const yaml = `
name: CI
jobs:
  ci:
    steps:
      - run: npx vitest run
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    expect(hasStepMatching(parsed!, ["vitest"])).toBe(true);
  });

  it("matches against uses field (action references)", () => {
    const yaml = `
name: CI
jobs:
  ci:
    steps:
      - uses: actions/checkout@v4
      - uses: codecov/codecov-action@v3
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    expect(hasStepMatching(parsed!, ["codecov"])).toBe(true);
  });

  it("returns false when workflow has no jobs", () => {
    const { parsed } = parseWorkflowContent("name: Empty\non: [push]", "empty.yml");
    expect(hasStepMatching(parsed!, ["build"])).toBe(false);
  });
});

// ── getAllStepNames ───────────────────────────────────────────────────────────

describe("getAllStepNames", () => {
  it("collects all step names and run commands", () => {
    const yaml = `
name: CI
jobs:
  ci:
    steps:
      - name: Build
        run: npm run build
      - name: Lint
        run: npm run lint
`.trim();

    const { parsed } = parseWorkflowContent(yaml, "ci.yml");
    const names = getAllStepNames(parsed!);
    expect(names.some((n) => n.includes("build"))).toBe(true);
    expect(names.some((n) => n.includes("lint"))).toBe(true);
  });

  it("returns empty array for workflow without jobs", () => {
    const { parsed } = parseWorkflowContent("name: Empty\non: [push]", "empty.yml");
    expect(getAllStepNames(parsed!)).toHaveLength(0);
  });
});

// ── runDimension8 integration tests ──────────────────────────────────────────

import { runDimension8 } from "../../../../src/analysis/product-audit/dimensions/dimension-8-cicd.js";
import * as fs from "fs";
import * as childProcess from "child_process";
import { vi, beforeEach } from "vitest";

vi.mock("fs");
vi.mock("child_process");

const mockFs = vi.mocked(fs);
const mockExecSync = vi.mocked(childProcess.execSync);

describe("runDimension8", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockFs.existsSync.mockReturnValue(false);
  });

  it("marks repo as missing when root does not exist", async () => {
    const result = await runDimension8([{ name: "missing", root: "/no/exist" }]);
    expect(result.repos_missing).toContain("missing");
    expect(result.findings).toHaveLength(0);
  });

  it("warns when no workflow files found", async () => {
    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return s === "/repo";
      // No .github/workflows
    });

    const result = await runDimension8([{ name: "test", root: "/repo" }]);
    expect(result.warnings.some((w) => w.includes("No workflow files"))).toBe(true);
  });

  it("generates DISABLED_WORKFLOW finding for disabled workflow", async () => {
    const workflowContent = `
name: Disabled CI
on: [push]
jobs:
  build:
    if: false
    steps:
      - name: Build
        run: npm run build
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return ["/repo", "/repo/.github/workflows", "/repo/.github/workflows/ci.yml"].includes(s);
    });
    mockFs.readdirSync.mockReturnValue(["ci.yml"] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => workflowContent);

    const result = await runDimension8([{ name: "test", root: "/repo" }]);
    const disabled = result.findings.find((f) => f.ci_issue === "DISABLED_WORKFLOW");
    expect(disabled).toBeDefined();
    expect(disabled!.severity).toBe("high");
    expect(disabled!.workflow_name).toBe("Disabled CI");
  });

  it("generates CONTINUE_ON_ERROR finding", async () => {
    const workflowContent = `
name: CI
on: [push]
jobs:
  build:
    steps:
      - name: Build
        run: npm run build
      - name: Flaky Test
        run: npm test
        continue-on-error: true
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return ["/repo", "/repo/.github/workflows", "/repo/.github/workflows/ci.yml"].includes(s);
    });
    mockFs.readdirSync.mockReturnValue(["ci.yml"] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => workflowContent);

    const result = await runDimension8([{ name: "test", root: "/repo" }]);
    const finding = result.findings.find((f) => f.ci_issue === "CONTINUE_ON_ERROR");
    expect(finding).toBeDefined();
    expect(finding!.detail).toContain("Flaky Test");
  });

  it("generates MISSING_REQUIRED_CHECK for workflow without test step", async () => {
    const workflowContent = `
name: Deploy Only
on: [push]
jobs:
  deploy:
    steps:
      - name: Deploy
        run: ./deploy.sh
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return ["/repo", "/repo/.github/workflows", "/repo/.github/workflows/deploy.yml"].includes(s);
    });
    mockFs.readdirSync.mockReturnValue(["deploy.yml"] as unknown as ReturnType<
      typeof fs.readdirSync
    >);
    mockFs.readFileSync.mockImplementation(() => workflowContent);

    const result = await runDimension8([{ name: "test", root: "/repo" }]);
    const missingTest = result.findings.find(
      (f) => f.ci_issue === "MISSING_REQUIRED_CHECK" && f.detail.includes("'test'")
    );
    expect(missingTest).toBeDefined();
  });

  it("handles malformed workflow YAML gracefully", async () => {
    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return ["/repo", "/repo/.github/workflows", "/repo/.github/workflows/bad.yml"].includes(s);
    });
    mockFs.readdirSync.mockReturnValue(["bad.yml"] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => "{ invalid yaml: [[[");

    const result = await runDimension8([{ name: "test", root: "/repo" }]);
    expect(result.findings).toHaveLength(0);
    expect(
      result.warnings.some((w) => w.includes("YAML parse error") || w.includes("Failed to parse"))
    ).toBe(true);
  });

  it("generates NO_BRANCH_PROTECTION finding when gh api fails", async () => {
    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      return p.toString() === "/repo";
    });
    mockFs.readdirSync.mockReturnValue([] as unknown as ReturnType<typeof fs.readdirSync>);
    mockExecSync.mockImplementation(() => {
      throw new Error("gh api: 404");
    });

    const result = await runDimension8([
      { name: "nightgauge", root: "/repo", orgName: "nightgauge" },
    ]);

    const branchProtFinding = result.findings.find((f) => f.ci_issue === "NO_BRANCH_PROTECTION");
    expect(branchProtFinding).toBeDefined();
    expect(branchProtFinding!.severity).toBe("high");
  });

  it("scans multiple repos and aggregates findings", async () => {
    const workflowWithContinueOnError = `
name: CI
on: [push]
jobs:
  build:
    steps:
      - name: Build
        run: npm run build
        continue-on-error: true
`.trim();

    mockFs.existsSync.mockImplementation((p: fs.PathLike) => {
      const s = p.toString();
      return [
        "/repo-a",
        "/repo-b",
        "/repo-a/.github/workflows",
        "/repo-b/.github/workflows",
        "/repo-a/.github/workflows/ci.yml",
        "/repo-b/.github/workflows/ci.yml",
      ].includes(s);
    });
    mockFs.readdirSync.mockReturnValue(["ci.yml"] as unknown as ReturnType<typeof fs.readdirSync>);
    mockFs.readFileSync.mockImplementation(() => workflowWithContinueOnError);

    const result = await runDimension8([
      { name: "repo-a", root: "/repo-a" },
      { name: "repo-b", root: "/repo-b" },
    ]);

    expect(result.repos_scanned).toHaveLength(2);
    const continueFindings = result.findings.filter((f) => f.ci_issue === "CONTINUE_ON_ERROR");
    expect(continueFindings.length).toBeGreaterThanOrEqual(2);
  });
});
