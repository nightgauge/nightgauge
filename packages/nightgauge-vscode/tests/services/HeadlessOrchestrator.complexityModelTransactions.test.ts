import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";

const {
  mockWithComplexityModelService,
  mockRecordUnderestimation,
  mockParseReviewerComments,
  mockProcessReviewerFeedback,
  mockExecFile,
  modelService,
} = vi.hoisted(() => {
  const execFile = vi.fn();
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  (execFile as any)[kCustom] = (...args: unknown[]) => execFile(...args);
  return {
    mockWithComplexityModelService: vi.fn(),
    mockRecordUnderestimation: vi.fn(),
    mockParseReviewerComments: vi.fn(),
    mockProcessReviewerFeedback: vi.fn(),
    mockExecFile: execFile,
    modelService: { marker: "brokered-model-service" },
  };
});

vi.mock("node:child_process", async (importOriginal) => ({
  ...(await importOriginal<typeof import("node:child_process")>()),
  execFile: mockExecFile,
}));

vi.mock("../../src/services/ComplexityModelLock", () => ({
  withComplexityModelService: mockWithComplexityModelService,
}));

vi.mock("@nightgauge/sdk", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@nightgauge/sdk")>()),
  FeedbackLearningService: vi.fn(function (received: unknown) {
    expect(received).toBe(modelService);
    return {
      recordUnderestimation: mockRecordUnderestimation,
      parseReviewerComments: mockParseReviewerComments,
      processReviewerFeedback: mockProcessReviewerFeedback,
    };
  }),
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";

function logger() {
  return { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() };
}

function harness(root: string) {
  const log = logger();
  const orchestrator = new HeadlessOrchestrator(null, log as any) as any;
  orchestrator.getWorkingDirectory = () => root;
  orchestrator.getContextPath = (type: string, issue: number) =>
    path.join(root, `.nightgauge/pipeline/${type}-${issue}.json`);
  orchestrator.getIssueContextPath = (issue: number) =>
    path.join(root, `.nightgauge/pipeline/issue-${issue}.json`);
  orchestrator.extractSizeLabel = () => "S";
  orchestrator.extractTypeLabel = () => "feature";
  return { orchestrator, log };
}

describe("HeadlessOrchestrator complexity-model transactions", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockWithComplexityModelService.mockImplementation(
      async (_root: string, action: (service: unknown) => Promise<unknown>) => action(modelService)
    );
    mockRecordUnderestimation.mockResolvedValue({ skipped: false, patternsAdjusted: 1 });
    mockParseReviewerComments.mockReturnValue([{ signal: "review" }]);
    mockProcessReviewerFeedback.mockResolvedValue({
      skipped: false,
      signalsProcessed: 1,
      patternsAdjusted: 1,
    });
  });

  it("routes underestimation feedback through the workspace-rooted broker service", async () => {
    const root = mkdtempSync(path.join(tmpdir(), "nightgauge-feedback-"));
    const pipeline = path.join(root, ".nightgauge/pipeline");
    mkdirSync(pipeline, { recursive: true });
    writeFileSync(
      path.join(pipeline, "dev-42.json"),
      JSON.stringify({ feedback: [{ signal_type: "COMPLEXITY_UNDERESTIMATED" }] })
    );
    writeFileSync(
      path.join(pipeline, "issue-42.json"),
      JSON.stringify({ labels: ["size:S", "type:feature"], title: "Issue" })
    );
    const { orchestrator } = harness(root);

    await orchestrator.applyFeedbackLearning("feature-dev", 42);

    expect(mockWithComplexityModelService).toHaveBeenCalledWith(root, expect.any(Function));
    expect(mockRecordUnderestimation).toHaveBeenCalledWith(
      42,
      "S",
      "feature",
      "Issue",
      "",
      expect.objectContaining({ signal_type: "COMPLEXITY_UNDERESTIMATED" })
    );
  });

  it("routes reviewer feedback parsing and mutation through the same broker service", async () => {
    const root = mkdtempSync(path.join(tmpdir(), "nightgauge-review-feedback-"));
    const pipeline = path.join(root, ".nightgauge/pipeline");
    mkdirSync(pipeline, { recursive: true });
    writeFileSync(path.join(pipeline, "pr-42.json"), JSON.stringify({ pr_number: 57 }));
    writeFileSync(
      path.join(pipeline, "issue-42.json"),
      JSON.stringify({ labels: ["size:S", "type:feature"], title: "Issue" })
    );
    mockExecFile.mockResolvedValue({
      stdout: JSON.stringify([
        {
          body: "Please add a regression test",
          state: "CHANGES_REQUESTED",
          author: { login: "r" },
        },
      ]),
      stderr: "",
    });
    const { orchestrator } = harness(root);

    await orchestrator.captureReviewerFeedback(42);

    expect(mockWithComplexityModelService).toHaveBeenCalledWith(root, expect.any(Function));
    expect(mockParseReviewerComments).toHaveBeenCalled();
    expect(mockProcessReviewerFeedback).toHaveBeenCalledWith(
      42,
      "S",
      "feature",
      "Issue",
      "",
      [{ signal: "review" }],
      "CHANGES_REQUESTED",
      0.03
    );
  });

  it("keeps broker failures non-fatal and reports them", async () => {
    const root = mkdtempSync(path.join(tmpdir(), "nightgauge-feedback-failure-"));
    const pipeline = path.join(root, ".nightgauge/pipeline");
    mkdirSync(pipeline, { recursive: true });
    writeFileSync(
      path.join(pipeline, "dev-42.json"),
      JSON.stringify({ feedback: [{ signal_type: "COMPLEXITY_UNDERESTIMATED" }] })
    );
    writeFileSync(path.join(pipeline, "issue-42.json"), JSON.stringify({ title: "Issue" }));
    mockWithComplexityModelService.mockRejectedValue(new Error("broker unavailable"));
    const { orchestrator, log } = harness(root);

    await expect(orchestrator.applyFeedbackLearning("feature-dev", 42)).resolves.toBeUndefined();
    expect(log.warn).toHaveBeenCalledWith(
      "FeedbackLearning: failed to record underestimation",
      expect.objectContaining({ error: "broker unavailable" })
    );
  });
});
