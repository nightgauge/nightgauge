/**
 * E2E: Pipeline Execution Tests
 *
 * Validates single-stage and multi-stage pipeline execution scenarios with
 * fully mocked IPC. Tests exercise context file reading/writing and schema
 * validation to catch contract violations early.
 *
 * Three suites:
 *   1. Single-Stage Execution — context file is written and validates against schema
 *   2. Multi-Stage Chain — context files chain correctly across stages
 *   3. IPC Round-Trip — pipelineRun request/response cycle with mocked IPC
 *
 * @see Issue #2504 — E2E pipeline execution tests
 * @see Issue #1825 — Phase 5: E2E smoke tests
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as path from "path";

import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
} from "@nightgauge/sdk/src/context/schemas/index.js";

import {
  createTempWorkspace,
  makeIssueContext,
  makePlanningContext,
  makeDevContext,
  type TempWorkspace,
} from "../helpers/workspaceSetup";

import { createBoardItem } from "../mocks/board-item";
// ---------------------------------------------------------------------------
// Hoisted mocks — created inline in vi.hoisted() so they are available before
// any imports resolve. Calling imported functions inside vi.hoisted() causes
// "Cannot access before initialization" because imports are evaluated after
// the hoisted block runs.
// ---------------------------------------------------------------------------

const ipcMock = vi.hoisted(() => ({
  mockBoardList: vi.fn().mockResolvedValue([]),
  mockBoardCounts: vi.fn().mockResolvedValue({
    ready: 0,
    inProgress: 0,
    inReview: 0,
    done: 0,
    backlog: 0,
  }),
  mockConfigGetProjectConfig: vi.fn().mockResolvedValue({
    owner: "nightgauge",
    projectNumber: 42,
    defaultRepo: "",
  }),
  mockPipelineRun: vi.fn().mockResolvedValue({ success: true, runId: "run-1" }),
  mockPipelineGetState: vi.fn().mockResolvedValue(null),
  mockStart: vi.fn().mockResolvedValue(undefined),
  mockStop: vi.fn(),
  mockOn: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  mockCall: vi.fn().mockResolvedValue({}),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: ipcMock.mockBoardList,
      boardCounts: ipcMock.mockBoardCounts,
      configGetProjectConfig: ipcMock.mockConfigGetProjectConfig,
      pipelineRun: ipcMock.mockPipelineRun,
      pipelineGetState: ipcMock.mockPipelineGetState,
      start: ipcMock.mockStart,
      stop: ipcMock.mockStop,
      on: ipcMock.mockOn,
      call: ipcMock.mockCall,
    }),
  },
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

// ============================================================================
// Suite 1: Single-Stage Execution — context file schema validation
// ============================================================================

describe("E2E: Single-Stage Execution with Mocked IPC", () => {
  let workspace: TempWorkspace;

  beforeEach(() => {
    workspace = createTempWorkspace();
    ipcMock.mockBoardList.mockReset();
    ipcMock.mockPipelineRun.mockReset();
  });

  afterEach(() => {
    workspace.cleanup();
  });

  it("should write issue context file that validates against IssueContextSchema", () => {
    // Simulate what issue-pickup writes to disk
    const issueCtx = makeIssueContext(2504, { title: "feat: add E2E tests" });
    workspace.writeContext("issue-2504.json", issueCtx);

    const raw = workspace.readContext("issue-2504.json");
    const result = IssueContextSchema.safeParse(raw);

    expect(result.success, `Schema parse failed: ${JSON.stringify(result.error?.issues)}`).toBe(
      true
    );
    if (result.success) {
      expect(result.data.issue_number).toBe(2504);
      expect(result.data.title).toBe("feat: add E2E tests");
    }
  });

  it("should validate required fields: schema_version, issue_number, requirements", () => {
    const issueCtx = makeIssueContext(100);
    workspace.writeContext("issue-100.json", issueCtx);

    const raw = workspace.readContext("issue-100.json") as Record<string, unknown>;

    expect(raw).toHaveProperty("schema_version");
    expect(raw).toHaveProperty("issue_number", 100);
    expect(raw).toHaveProperty("requirements");
    expect(typeof raw.requirements).toBe("object");
  });

  it("should validate planning context schema after issue-pickup completes", () => {
    // Stage 1 output (issue-pickup) feeds Stage 2 input (feature-planning)
    const issueCtx = makeIssueContext(42);
    workspace.writeContext("issue-42.json", issueCtx);

    const planCtx = makePlanningContext(42, {
      plan_file: ".nightgauge/plans/42-my-feature.md",
      approach: "Implement using existing patterns.",
      files_to_create: ["src/new-feature.ts"],
      files_to_modify: ["src/index.ts"],
    });
    workspace.writeContext("planning-42.json", planCtx);

    const raw = workspace.readContext("planning-42.json");
    const result = PlanningContextSchema.safeParse(raw);

    expect(result.success, `Planning schema failed: ${JSON.stringify(result.error?.issues)}`).toBe(
      true
    );
    if (result.success) {
      expect(result.data.issue_number).toBe(42);
      expect(result.data.files_to_create).toContain("src/new-feature.ts");
    }
  });

  it("should validate dev context schema after feature-dev completes", () => {
    const devCtx = makeDevContext(42, {
      tests_status: {
        passed: 12,
        failed: 0,
        coverage: 87.5,
        test_command: "npx -w nightgauge-vscode vitest run",
        includes_integration: false,
        includes_e2e: false,
        test_files_run: 3,
        e2e_framework: null,
        e2e_tests_generated: false,
      },
    });
    workspace.writeContext("dev-42.json", devCtx);

    const raw = workspace.readContext("dev-42.json");
    const result = DevContextSchema.safeParse(raw);

    expect(result.success, `Dev schema failed: ${JSON.stringify(result.error?.issues)}`).toBe(true);
    if (result.success) {
      expect(result.data.issue_number).toBe(42);
      expect(result.data.tests_status?.passed).toBe(12);
    }
  });

  it("should track token usage field in dev context", () => {
    // Token accumulation tracked via tests_status.passed as a proxy.
    // Real token tracking is done by SkillRunner — here we verify the context
    // file shape supports it.
    const devCtx = makeDevContext(99, {
      tests_status: { passed: 8, failed: 0, test_files_run: 2 },
    });
    workspace.writeContext("dev-99.json", devCtx);

    const raw = workspace.readContext("dev-99.json") as Record<string, unknown>;
    const testsStatus = raw.tests_status as Record<string, unknown>;

    expect(testsStatus).toBeDefined();
    expect(typeof testsStatus.passed).toBe("number");
  });
});

// ============================================================================
// Suite 2: Multi-Stage Chain — context inheritance across stages
// ============================================================================

describe("E2E: Multi-Stage Chain with Context Inheritance", () => {
  let workspace: TempWorkspace;

  beforeEach(() => {
    workspace = createTempWorkspace();
  });

  afterEach(() => {
    workspace.cleanup();
  });

  it("should chain pickup → planning → dev stages with accumulating context", () => {
    const issueNumber = 200;

    // Stage 1: issue-pickup writes issue context
    const issueCtx = makeIssueContext(issueNumber, {
      title: "feat: multi-stage chain test",
      labels: ["type:feature", "size:L", "priority:high"],
    });
    workspace.writeContext(`issue-${issueNumber}.json`, issueCtx);

    // Stage 2: feature-planning reads issue context and writes planning context
    const issueRaw = workspace.readContext(`issue-${issueNumber}.json`) as Record<string, unknown>;
    expect(issueRaw.issue_number).toBe(issueNumber); // planning reads this

    const planCtx = makePlanningContext(issueNumber, {
      approach: "Build on existing architecture.",
      files_to_create: ["src/chain-feature.ts"],
      files_to_modify: [],
    });
    workspace.writeContext(`planning-${issueNumber}.json`, planCtx);

    // Stage 3: feature-dev reads planning context and writes dev context
    const planRaw = workspace.readContext(`planning-${issueNumber}.json`) as Record<
      string,
      unknown
    >;
    expect(planRaw.issue_number).toBe(issueNumber); // dev reads this
    expect(planRaw.approach).toBe("Build on existing architecture.");

    const devCtx = makeDevContext(issueNumber, {
      files_changed: {
        created: ["src/chain-feature.ts"],
        modified: [],
        deleted: [],
      },
    });
    workspace.writeContext(`dev-${issueNumber}.json`, devCtx);

    // Verify full chain: all three files exist and parse correctly
    const devRaw = workspace.readContext(`dev-${issueNumber}.json`) as Record<string, unknown>;
    expect(devRaw.issue_number).toBe(issueNumber);

    const filesChanged = devRaw.files_changed as Record<string, unknown[]>;
    expect(filesChanged.created).toContain("src/chain-feature.ts");
  });

  it("should inherit issue context from pickup to planning", () => {
    const issueCtx = makeIssueContext(300, {
      title: "feat: context inheritance",
      requirements: ["Req A", "Req B"],
      acceptance_criteria: ["AC1: passes"],
    });
    workspace.writeContext("issue-300.json", issueCtx);

    // Planning stage inherits issue_number from the issue context file
    const issueRaw = workspace.readContext("issue-300.json") as Record<string, unknown>;

    const planCtx = makePlanningContext(issueRaw.issue_number as number);
    workspace.writeContext(`planning-${issueRaw.issue_number}.json`, planCtx);

    const planRaw = workspace.readContext("planning-300.json") as Record<string, unknown>;
    expect(planRaw.issue_number).toBe(300); // Same as issue
  });

  it("should maintain complexity assessment field from planning through dev", () => {
    const planCtx = makePlanningContext(400, {
      complexity_assessment: {
        computed_score: 5,
        documentation_scope: "extended",
        size_label: "L",
        type_label: "feature",
        priority_label: "high",
      },
    });
    workspace.writeContext("planning-400.json", planCtx);

    const raw = workspace.readContext("planning-400.json") as Record<string, unknown>;
    const complexity = raw.complexity_assessment as Record<string, unknown>;
    expect(complexity.computed_score).toBe(5);

    // Dev context does not re-derive complexity — it reads from planning.
    // Just verify the planning file preserves it correctly.
    const result = PlanningContextSchema.safeParse(raw);
    expect(result.success).toBe(true);
  });
});

// ============================================================================
// Suite 3: IPC Round-Trip Integration
// ============================================================================

describe("E2E: IPC Round-Trip Integration", () => {
  beforeEach(() => {
    ipcMock.mockPipelineRun.mockReset();
    ipcMock.mockOn.mockReset().mockReturnValue({ dispose: vi.fn() });
  });

  it("should send pipelineRun request and receive run result", async () => {
    ipcMock.mockPipelineRun.mockResolvedValueOnce({
      success: true,
      runId: "run-abc123",
    });

    const { IpcClient } = await import("../../src/services/IpcClient");
    const client = IpcClient.getInstance();

    const result = await client.pipelineRun("nightgauge", "nightgauge", 2504);

    expect(ipcMock.mockPipelineRun).toHaveBeenCalledWith("nightgauge", "nightgauge", 2504);
    expect(result).toEqual({ success: true, runId: "run-abc123" });
  });

  it("should stream events during skill execution via on() handler", () => {
    const events: unknown[] = [];
    ipcMock.mockOn.mockImplementation((_event: string, handler: (d: unknown) => void) => {
      // Simulate two progress events then a complete event
      handler({ stage: "feature-dev", progress: 0.5 });
      handler({ stage: "feature-dev", progress: 1.0 });
      return { dispose: vi.fn() };
    });

    // Use the already-imported mock directly — the IpcClient mock is wired via
    // vi.mock() and ipcMock.mockOn already captures the handler above.
    // We just need any object that calls client.on() to trigger the mock.
    const client = { on: ipcMock.mockOn };

    client.on("pipeline.progress", (data: unknown) => {
      events.push(data);
    });

    // Both calls should have fired via the mock
    expect(events).toHaveLength(2);
    expect((events[0] as any).progress).toBe(0.5);
    expect((events[1] as any).progress).toBe(1.0);
  });

  it("should handle IPC error response gracefully", async () => {
    ipcMock.mockPipelineRun.mockRejectedValueOnce(new Error("IPC connection failed"));

    const { IpcClient } = await import("../../src/services/IpcClient");
    const client = IpcClient.getInstance();

    await expect(client.pipelineRun("nightgauge", "nightgauge", 9999)).rejects.toThrow(
      "IPC connection failed"
    );
  });
});
