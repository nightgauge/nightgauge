/**
 * #3487 step 4: a booked partial-delivery no-op is not presented as a failure
 * on the extension's live surfaces (slot card, tree item, Output Window badge,
 * notifications), and its kind, outcome and predicate are named once.
 */

import { describe, it, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(),
      onDidCreate: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
    getConfiguration: vi.fn(() => ({ get: vi.fn(() => undefined) })),
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  commands: { executeCommand: vi.fn(), registerCommand: vi.fn() },
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
  Uri: { file: (p: string) => ({ fsPath: p }) },
  ExtensionMode: { Production: 1, Development: 2, Test: 3 },
}));

import {
  endedPartialNoOp,
  PARTIAL_NO_OP_LABEL,
  PARTIAL_NO_OP_OUTCOME,
  PARTIAL_NO_OP_STATE_OUTCOME,
  PARTIAL_NO_OP_TERMINAL_KIND,
} from "../../src/services/partialNoOp";
import { slotTerminalKind } from "../../src/services/ConcurrentPipelineManager";
import { failedSlotSurfaces } from "../../src/bootstrap/slotEnding";
import {
  slotCardStatus,
  type SlotCardData,
  type SlotStageSummary,
} from "../../src/views/dashboard/SlotCardTypes";
import { renderSlotCard } from "../../src/views/dashboard/tabs/OverviewTabHtml";
import { determineAction, outcomeDisplay } from "../../src/services/DiscordService";

const src = (rel: string) => readFileSync(path.resolve(__dirname, "../../src", rel), "utf-8");

describe("partial no-op names (#3487 Part I)", () => {
  it("names the kind and the outcomes once", () => {
    expect(PARTIAL_NO_OP_TERMINAL_KIND).toBe("not_pipeline_actionable");
    expect(PARTIAL_NO_OP_OUTCOME).toBe("no_op");
    expect(PARTIAL_NO_OP_STATE_OUTCOME).toBe("no-op");
  });

  it("decides a no-op on the booking alone", () => {
    expect(endedPartialNoOp({ success: false, partialNoOp: {} })).toBe(true);
    expect(endedPartialNoOp({ success: false, partialNoOp: { remainder: undefined } })).toBe(true);
    expect(endedPartialNoOp({ success: true, partialNoOp: {} })).toBe(false);
    expect(endedPartialNoOp({ success: false })).toBe(false);
  });

  it.each(["services/ConcurrentPipelineManager.ts", "services/HeadlessOrchestrator.ts"])(
    "%s uses the shared predicate and constants, with no inline copy",
    (rel) => {
      const text = src(rel);
      expect(text).toMatch(/import \{[^}]*\bendedPartialNoOp\b[^}]*\} from "\.\/partialNoOp"/);
      expect(text).toMatch(/\bendedPartialNoOp\(/);
      // No inline copy of the predicate.
      expect(text).not.toMatch(/!\s*\w+\.success\s*&&\s*\w+(\?)?\.partialNoOp/);
      expect(text).not.toMatch(/!pipelineSucceeded\s*&&\s*pipelineResult\?\.partialNoOp/);
      // No local definition of the kind.
      expect(text).not.toMatch(/const PARTIAL_NO_OP_TERMINAL_KIND\s*=/);
      // The no-op outcome is never a bare literal.
      expect(text).not.toMatch(/outcome:\s*"no_op"/);
      expect(text).not.toMatch(/classifiedOutcome\s*=\s*"no-op"/);
    }
  );

  it("HeadlessOrchestrator books the no-op's kind through the constant", () => {
    const text = src("services/HeadlessOrchestrator.ts");
    const atRefusal = text.match(/private async partialNoOpAtRefusal\([\s\S]*?\n {2}}/)?.[0] ?? "";
    expect(atRefusal).toContain("PARTIAL_NO_OP_TERMINAL_KIND");
    expect(atRefusal).not.toContain('"not_pipeline_actionable"');
    const booking = text.match(/static partialRerunNoOpBooking\([\s\S]*?\n {2}}/)?.[0] ?? "";
    expect(booking).toContain("PARTIAL_NO_OP_TERMINAL_KIND");
    expect(booking).not.toContain('"not_pipeline_actionable"');
  });
});

describe("partial no-op live surfaces (#3487 step 4)", () => {
  it("presents a booked no-op as a no-op, and every other ending as a failure", () => {
    const kind = slotTerminalKind({ partialNoOp: {} });
    expect(failedSlotSurfaces(kind)).toEqual({
      noOp: true,
      treeStatus: "no-op",
      badge: "complete",
      outputSuccess: true,
    });
    // A first-run gate no-op and a crash carry no structured kind.
    for (const other of [undefined, slotTerminalKind({}), "subagent_crash", "operator_stop"]) {
      expect(failedSlotSurfaces(other)).toEqual({
        noOp: false,
        treeStatus: "failed",
        badge: "error",
        outputSuccess: false,
      });
    }
  });

  it("onSlotFailed applies the surfaces and raises no failure toast", () => {
    const text = src("bootstrap/services.ts");
    const body = text.match(/onSlotFailed: \([\s\S]*?\n {6}onSlotDeferred:/)?.[0] ?? "";
    expect(body).toContain("failedSlotSurfaces(terminalKind)");
    expect(body).toContain("outputWindow.notifySlotCompleted(slotIndex, surfaces.badge, costUsd)");
    expect(body).toContain(
      "updateConcurrentSlotStatus(issueNumber, surfaces.treeStatus, repoSlug)"
    );
    expect(body).toContain("dashboard.markSlotNoOp(issueNumber, repoSlug)");
    expect(body).not.toMatch(/show(Error|Warning|Information)Message/);
  });

  const failedStages: SlotStageSummary[] = [
    { stage: "issue-pickup", status: "complete" },
    { stage: "feature-dev", status: "failed" },
  ];

  it("derives the card status from the no-op mark ahead of the failed stage", () => {
    expect(slotCardStatus({ noOp: true, stages: failedStages })).toBe("no-op");
    expect(slotCardStatus({ stages: failedStages })).toBe("failed");
    expect(slotCardStatus({ paused: true, noOp: true, stages: failedStages })).toBe("paused");
  });

  function card(status: SlotCardData["status"]): string {
    return renderSlotCard({
      slotIndex: 0,
      issueNumber: 3487,
      title: "Partial re-run",
      status,
      stages: failedStages,
      completedStageCount: 1,
      totalStageCount: 2,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
    });
  }

  it("renders a no-op slot card without the failure label or styling", () => {
    const html = card("no-op");
    expect(html).toContain(PARTIAL_NO_OP_LABEL);
    expect(html).toContain("slot-card-no-op");
    expect(html).not.toContain("slot-card-failed");
    expect(html).not.toContain(">Failed<");
    // A real failure still renders as one.
    const failed = card("failed");
    expect(failed).toContain("slot-card-failed");
    expect(failed).toContain("Failed");
  });

  it("labels a no-op run's notification no-op, never failed", () => {
    const display = outcomeDisplay(PARTIAL_NO_OP_STATE_OUTCOME, { failedStageCount: 1 });
    expect(display.label).toMatch(/no-op/i);
    expect(display.label).not.toMatch(/fail/i);
    expect(display.color).toBe(outcomeDisplay("cancelled").color);
    expect(outcomeDisplay("failure").label).toMatch(/fail/i);
    const action = determineAction({
      outcome_type: PARTIAL_NO_OP_STATE_OUTCOME,
      stages: { "feature-dev": { status: "failed", error: "merge_conflict" } },
    } as unknown as Parameters<typeof determineAction>[0]);
    expect(action).toMatch(/no-op/i);
  });
});
