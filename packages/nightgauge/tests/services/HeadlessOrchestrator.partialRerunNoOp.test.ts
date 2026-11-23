/**
 * #3163 AC 4: a zero-change feature-dev on a partial-delivery re-run is booked
 * `not_pipeline_actionable` — a no-op, never a failure — so it feeds neither
 * the lifetime cap nor the cascade breaker. Mirrors the Go scheduler's
 * decidePartialNoOp booking.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

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

// #3487: `nightgauge issue partial-noop --json` answers with this stdout, so
// readPartialNoOpDecision runs end to end with its real boundary guard.
const binaryStdout = vi.hoisted(() => ({ value: "", args: [] as string[][] }));
vi.mock("child_process", async (importOriginal) => {
  const actual = await importOriginal<typeof import("child_process")>();
  const { promisify } = await import("util");
  const execFile = Object.assign(vi.fn(), {
    [promisify.custom]: vi.fn(async (_bin: string, args: string[]) => {
      binaryStdout.args.push(args);
      return { stdout: binaryStdout.value, stderr: "" };
    }),
  });
  return { ...actual, execFile, default: { ...actual, execFile } };
});
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

import { classifyTerminalKind } from "@nightgauge/sdk";
import {
  HeadlessOrchestrator,
  isPartialNoOpRemainder,
} from "../../src/services/HeadlessOrchestrator";

const noOp = {
  error: new Error(
    "premature turn end: stage exited 0 with no state change (gate no-op): dev context records zero file changes"
  ),
  noOp: true,
};

describe("HeadlessOrchestrator.partialRerunNoOpBooking (#3163)", () => {
  it("books a scoped feature-dev no-op as not_pipeline_actionable", () => {
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(1750, "feature-dev", noOp, true);
    if (booking?.action !== "book") throw new Error("expected a booking");
    expect(booking.terminalKind).toBe("not_pipeline_actionable");
    // The run-record ladder must read the same kind from the error text, or
    // the lifetime cap would still see premature_turn_end.
    expect(classifyTerminalKind(booking!.error.message)).toBe("not_pipeline_actionable");
    expect(booking!.error.message).toContain("#1750");
    // The gate's evidence survives for whoever reads the record.
    expect(booking!.error.message).toContain("dev context records zero file changes");
  });

  it("leaves an unscoped run's no-op a failure", () => {
    expect(HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-dev", noOp, false)).toBeNull();
    expect(classifyTerminalKind(noOp.error.message)).toBe("premature_turn_end");
  });

  it("only applies to a feature-dev gate no-op", () => {
    expect(
      HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-planning", noOp, true)
    ).toBeNull();
    const fail = { error: new Error("stage gate failed: build failed"), noOp: false };
    expect(HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-dev", fail, true)).toBeNull();
  });

  it("relaps once on the binary's relap decision (#1051)", () => {
    const relap = HeadlessOrchestrator.partialRerunNoOpBooking(1051, "feature-dev", noOp, true, {
      action: "relap",
      steer: "The plan is binding: implement a.go",
    });
    expect(relap).toEqual({ action: "relap", steer: "The plan is binding: implement a.go" });
  });

  it("ends a named dispute with the binary's detail", () => {
    const detail =
      "[not-pipeline-actionable] feature-dev: partial-delivery re-run of #1051 (after PR #9) disputed the plan instead of changing it: Task 2 [#3163]";
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(1051, "feature-dev", noOp, true, {
      action: "dispute",
      detail,
    });
    if (booking?.action !== "book") throw new Error("expected a booking");
    expect(booking.error.message).toContain("Task 2");
    expect(classifyTerminalKind(booking.error.message)).toBe("not_pipeline_actionable");
  });

  it("keeps the gate verdict when the binary says the run is not scoped", () => {
    expect(
      HeadlessOrchestrator.partialRerunNoOpBooking(1, "feature-dev", noOp, true, {
        action: "not_scoped",
      })
    ).toBeNull();
  });

  it("ends the run issue_closed when Go applied a close on the merged-diff verdict (#3423)", () => {
    const detail =
      "partial-delivery re-run of #3423 (after PR #3422) wrote no changes; every one of its 1 remaining criteria is met on abcdef12's merged diff, so #3423 is closed as completed [#3423]";
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(3423, "feature-dev", noOp, true, {
      action: "close",
      detail,
      applied: true,
    });
    if (booking?.action !== "close") throw new Error("expected a close");
    expect(booking.terminalKind).toBe("issue_closed");
    expect(booking.error.message).toContain("closed as completed");
    expect(classifyTerminalKind(booking.error.message)).toBe("issue_closed");
  });

  it("never treats an unapplied close as closed", () => {
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(3423, "feature-dev", noOp, true, {
      action: "close",
      detail: "met",
    });
    if (booking?.action !== "book") throw new Error("expected a booking");
    expect(booking.terminalKind).toBe("not_pipeline_actionable");
    expect(classifyTerminalKind(booking.error.message)).toBe("not_pipeline_actionable");
  });
});

describe("HeadlessOrchestrator.partialRerunNoOpBooking remainder (#3487)", () => {
  const remainder = {
    all_met: false,
    criteria: [{ text: "A is handled", met: false, evidence: "undecidable: prose" }],
  };

  it.each(["book", "dispute", "close"] as const)("carries the remainder of a %s", (action) => {
    const booking = HeadlessOrchestrator.partialRerunNoOpBooking(3487, "feature-dev", noOp, true, {
      action,
      remainder,
    });
    if (booking?.action !== "book") throw new Error("expected a booking");
    expect(booking.remainder).toEqual(remainder);
  });
});

describe("HeadlessOrchestrator sends the partial no-op booking to Go (#3487)", () => {
  const error = new Error("[not-pipeline-actionable] nothing left");

  function notified(result: Record<string, unknown>) {
    const notifyPipelineComplete = vi.fn().mockResolvedValue(undefined);
    const orch = new HeadlessOrchestrator(null, {
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    } as never) as unknown as {
      stateService: unknown;
      firePipelineComplete(r: unknown): void;
    };
    orch.stateService = { finalizeRun: vi.fn(), notifyPipelineComplete };
    orch.firePipelineComplete({
      success: false,
      completedStages: [],
      totalDurationMs: 1,
      error,
      terminalKind: "not_pipeline_actionable",
      ...result,
    });
    return notifyPipelineComplete.mock.calls[0][0];
  }

  it("carries the flag on the notify payload", () => {
    expect(notified({ partialNoOp: {} }).partialNoOp).toBe(true);
  });

  it("sends none for a publication verdict or a success", () => {
    expect(notified({}).partialNoOp).toBeUndefined();
    expect(notified({ success: true, partialNoOp: {} }).partialNoOp).toBeUndefined();
  });
});

describe("HeadlessOrchestrator books a pr-create refusal (#3487)", () => {
  const kind = "not_pipeline_actionable";

  function refusing() {
    const orch = new HeadlessOrchestrator(null, {
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    } as never) as any;
    orch.readPartialNoOpDecision = vi.fn().mockResolvedValue({ action: "relap" });
    return orch;
  }

  it("reads the remainder of a scoped stop, and closes nothing", async () => {
    const orch = refusing();
    const scope = { pr: 5, criteria: 1 };
    orch.partialRerunIssues.set(3487, scope);
    expect(await orch.partialNoOpAtRefusal(3487, "pr-create", kind)).toEqual({});
    expect(orch.readPartialNoOpDecision).toHaveBeenCalledWith(3487, scope, false);
  });

  it("books nothing for an unscoped stop, another stage or another kind", async () => {
    const orch = refusing();
    expect(await orch.partialNoOpAtRefusal(3487, "pr-create", kind)).toBeUndefined();
    orch.partialRerunIssues.set(3487, { pr: 5, criteria: 1 });
    expect(await orch.partialNoOpAtRefusal(3487, "pr-merge", kind)).toBeUndefined();
    expect(await orch.partialNoOpAtRefusal(3487, "pr-create", "issue_closed")).toBeUndefined();
    expect(orch.readPartialNoOpDecision).not.toHaveBeenCalled();
  });
});

describe("readPartialNoOpDecision's boundary guard (#3487)", () => {
  const met = { text: "A", met: true, evidence: "diff hunk" };
  const logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
  const decide = async (remainder: unknown) => {
    binaryStdout.value = JSON.stringify({ action: "book", remainder });
    logger.warn.mockClear();
    const orch = new HeadlessOrchestrator(null as never, logger as never) as any;
    orch.resolvePostMergeNwo = vi.fn().mockResolvedValue({ owner: "", repo: "" });
    return orch.readPartialNoOpDecision(3487, { pr: 5, criteria: 1 }, false);
  };

  it("keeps a remainder whose every field has its type", async () => {
    const remainder = { all_met: false, criteria: [met, { ...met, met: false }] };
    expect(await decide(remainder)).toEqual({ action: "book", remainder });
    expect(logger.warn).not.toHaveBeenCalled();
  });

  it.each([
    ["all_met missing", { criteria: [met] }],
    ["all_met not a boolean", { all_met: "no", criteria: [met] }],
    ["a criterion without met", { all_met: false, criteria: [{ text: "A", evidence: "e" }] }],
    ["a criterion whose met is a string", { all_met: false, criteria: [{ ...met, met: "yes" }] }],
    ["a criterion without evidence", { all_met: false, criteria: [{ text: "A", met: false }] }],
    ["no criteria", { all_met: true }],
  ])("drops a remainder with %s, and keeps the decision", async (_label, remainder) => {
    const decision = await decide(remainder);
    expect(decision).toEqual({ action: "book" });
    expect(isPartialNoOpRemainder(remainder)).toBe(false);
    // A malformed remainder is a contract fault at the binary boundary: logged.
    expect(logger.warn).toHaveBeenCalledWith("Partial no-op remainder malformed — dropping it", {
      issueNumber: 3487,
    });
  });

  it("passes --apply-close only when asked to close", async () => {
    binaryStdout.value = JSON.stringify({ action: "book" });
    binaryStdout.args = [];
    const orch = new HeadlessOrchestrator(null as never, null as never) as any;
    orch.resolvePostMergeNwo = vi.fn().mockResolvedValue({ owner: "o", repo: "r" });
    await orch.readPartialNoOpDecision(3487, { pr: 5, criteria: 1 }, false);
    await orch.readPartialNoOpDecision(3487, { pr: 5, criteria: 1 });
    expect(binaryStdout.args[0]).not.toContain("--apply-close");
    expect(orch.resolvePostMergeNwo).toHaveBeenCalledTimes(1);
    expect(binaryStdout.args[1]).toEqual(
      expect.arrayContaining(["--apply-close", "--repo", "o/r"])
    );
  });
});
