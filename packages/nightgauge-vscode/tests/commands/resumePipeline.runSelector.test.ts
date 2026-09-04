/**
 * Tests for the Resume Pipeline command's run selector (#423, ADR-017
 * follow-up to #370 step 3 / PR #421).
 *
 * `resumePipeline.ts` used to hard-target the singleton PipelineStateService.
 * When only a concurrent-slot run is live, the singleton holds no run
 * identity — the command must instead target the live slot's own service so
 * the resume actually reaches Go (`pipeline.setPaused`) for that run.
 *
 * @see src/commands/resumePipeline.ts
 * @see src/commands/runSelector.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { registerResumePipelineCommand } from "../../src/commands/resumePipeline";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { ConcurrentPipelineManager } from "../../src/services/ConcurrentPipelineManager";

// Every PIPELINE_STAGES entry must be present — resumePipeline.ts walks the
// fixed stage list and indexes state.stages[stage] directly.
const PENDING_STAGES = {
  "pipeline-start": { status: "complete" },
  "issue-pickup": { status: "complete" },
  "feature-planning": { status: "complete" },
  "feature-dev": { status: "pending" },
  "feature-validate": { status: "pending" },
  "pr-create": { status: "pending" },
  "pr-merge": { status: "pending" },
  "pipeline-finish": { status: "pending" },
};

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

let registeredHandler: (() => Promise<void>) | null = null;
let quickPickImpl: ((items: unknown[]) => unknown) | null = null;

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((_id: string, handler: () => Promise<void>) => {
      registeredHandler = handler;
      return { dispose: vi.fn() };
    }),
    executeCommand: vi.fn(),
  },
  window: {
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showQuickPick: vi.fn((items: unknown[]) =>
      Promise.resolve(quickPickImpl ? quickPickImpl(items) : undefined)
    ),
  },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const createMockOrchestrator = (): HeadlessOrchestrator =>
  ({
    runPipeline: vi.fn(() => Promise.resolve({ success: true, completedStages: [] })),
  }) as unknown as HeadlessOrchestrator;

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  }) as unknown as Logger;

const createMockStatusBar = (): StatusBarManager =>
  ({
    showPaused: vi.fn(),
    showRunning: vi.fn(),
    showIdle: vi.fn(),
  }) as unknown as StatusBarManager;

interface MockRunService {
  runId: string | null;
  issueNumber: number | null;
  paused: boolean;
  resumePipeline: ReturnType<typeof vi.fn>;
}

const createMockService = (opts: {
  runId: string | null;
  issueNumber: number | null;
  paused?: boolean;
}): PipelineStateService & MockRunService =>
  ({
    runId: opts.runId,
    issueNumber: opts.issueNumber,
    paused: opts.paused ?? true,
    getRunId: vi.fn(() => opts.runId),
    getIssueNumber: vi.fn(() => opts.issueNumber),
    getState: vi.fn(() =>
      Promise.resolve(
        opts.runId === null
          ? null
          : {
              issue_number: opts.issueNumber,
              stages: PENDING_STAGES,
              paused: opts.paused ?? true,
            }
      )
    ),
    resumePipeline: vi.fn(() => Promise.resolve(true)),
  }) as unknown as PipelineStateService & MockRunService;

// concurrentPipelineManager with zero active slots reported via
// activeSlotCount, so the legacy (non-Go-driven) resume path in
// resumePipeline.ts runs and calls orchestrator.runPipeline().
const createMockConcurrentPipelineManager = (
  slots: Array<{ slotIndex: number; issueNumber: number; service: PipelineStateService }>,
  activeSlotCount = 0
): ConcurrentPipelineManager =>
  ({
    activeSlotCount,
    getActiveSlots: vi.fn(() =>
      slots.map((s) => ({
        slotIndex: s.slotIndex,
        issueNumber: s.issueNumber,
        worktreePath: `/mock/worktree-${s.slotIndex}`,
        branch: `feat/${s.issueNumber}`,
        startedAt: new Date().toISOString(),
      }))
    ),
    getSlotStateService: vi.fn(
      (slotIndex: number) => slots.find((s) => s.slotIndex === slotIndex)?.service
    ),
  }) as unknown as ConcurrentPipelineManager;

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("resumePipeline run selector", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockOrchestrator: HeadlessOrchestrator;

  const registerAndGetHandler = (
    stateService: PipelineStateService | null,
    concurrentPipelineManager: ConcurrentPipelineManager | null
  ): (() => Promise<void>) => {
    registeredHandler = null;
    registerResumePipelineCommand(
      mockOrchestrator,
      stateService,
      mockLogger,
      mockStatusBar,
      concurrentPipelineManager
    );
    if (!registeredHandler) {
      throw new Error("Command handler was not registered");
    }
    return registeredHandler;
  };

  beforeEach(() => {
    vi.clearAllMocks();
    quickPickImpl = null;
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockOrchestrator = createMockOrchestrator();
  });

  it("(a) targets the live slot's service, not an idle singleton", async () => {
    const idleSingleton = createMockService({ runId: null, issueNumber: null });
    const slotService = createMockService({
      runId: "01911f6e-0000-7000-8000-000000000001",
      issueNumber: 501,
      paused: true,
    });
    // A non-zero activeSlotCount routes the command through the Go-driven
    // branch, which never calls orchestrator.runPipeline() (issue #423
    // scope: the selector, not the Go-driven/legacy branching, which is
    // pre-existing and untouched).
    const cpm = createMockConcurrentPipelineManager(
      [{ slotIndex: 0, issueNumber: 501, service: slotService }],
      1
    );

    const handler = registerAndGetHandler(idleSingleton, cpm);
    await handler();

    expect((slotService as unknown as MockRunService).resumePipeline).toHaveBeenCalledTimes(1);
    expect((idleSingleton as unknown as MockRunService).resumePipeline).not.toHaveBeenCalled();
  });

  it("(b) prompts with QuickPick when two runs are live, and resumes the chosen one", async () => {
    const serviceA = createMockService({
      runId: "01911f6e-0000-7000-8000-0000000000aa",
      issueNumber: 601,
      paused: true,
    });
    const serviceB = createMockService({
      runId: "01911f6e-0000-7000-8000-0000000000bb",
      issueNumber: 602,
      paused: true,
    });
    const cpm = createMockConcurrentPipelineManager(
      [
        { slotIndex: 0, issueNumber: 601, service: serviceA },
        { slotIndex: 1, issueNumber: 602, service: serviceB },
      ],
      2
    );

    let capturedItems: Array<{ label: string; candidate: unknown }> = [];
    quickPickImpl = (items) => {
      capturedItems = items as typeof capturedItems;
      return capturedItems[1]; // choose the second item
    };

    const idleSingleton = createMockService({ runId: null, issueNumber: null });
    const handler = registerAndGetHandler(idleSingleton, cpm);
    await handler();

    expect(capturedItems).toHaveLength(2);
    expect(capturedItems.map((i) => i.label).join("|")).toContain("601");
    expect(capturedItems.map((i) => i.label).join("|")).toContain("602");
    expect((serviceB as unknown as MockRunService).resumePipeline).toHaveBeenCalledTimes(1);
    expect((serviceA as unknown as MockRunService).resumePipeline).not.toHaveBeenCalled();
  });

  it("(c) refuses with no IPC call when zero runs are live", async () => {
    const idleSingleton = createMockService({ runId: null, issueNumber: null });
    const cpm = createMockConcurrentPipelineManager([], 0);

    const handler = registerAndGetHandler(idleSingleton, cpm);
    await handler();

    expect((idleSingleton as unknown as MockRunService).resumePipeline).not.toHaveBeenCalled();
    expect(mockOrchestrator.runPipeline).not.toHaveBeenCalled();
  });
});
