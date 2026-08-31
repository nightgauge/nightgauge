/**
 * The human-only issue, as a first-class outcome (#1241).
 *
 * Some issues on the board are not pipeline work at all: their deliverable is a
 * counsel sign-off, a credential only the operator holds, a physical act, or a
 * decision reserved to a human. Nothing an agent does produces the artifact.
 *
 * Before this, a stage that noticed had no way to SAY so. The `blocked` fork
 * (#1142) existed and was exactly right for the case, but was unreachable from
 * here for two independent reasons, and both had to be fixed:
 *
 *   1. `readFeedbackSignals` required `backtrack_target_stage != null`. That is
 *      correct for every signal that ASKS for a rewind and exactly wrong for one
 *      whose content is "there is nowhere to rewind to" — such a signal had to
 *      invent a target it did not mean in order to be read at all.
 *   2. The fork was consulted only from the post-validate gate. feature-dev's
 *      and feature-planning's gate failures went straight to the failure path,
 *      so a correct refusal — which leaves an empty workspace, indistinguishable
 *      at the gate from a stage that promised work and produced none — was
 *      booked `dev_produced_no_changes`: an agent-class terminal that increments
 *      the lifetime failure cap and halts the repository.
 *
 * (2) is what was reported: the specimen run — a privacy-policy legal
 * review, halted autonomous dispatch for the whole repository after feature-dev
 * correctly declined to fabricate legal text.
 *
 * These tests drive the real methods on a real orchestrator with a real
 * deliverable on disk. Nothing in the fork is stubbed.
 *
 * RED-PROOFS (each leaves the code compiling):
 *   A. Drop the `TERMINAL_BLOCKING_SIGNAL_TYPES.has(...)` clause from
 *      `readFeedbackSignals`'s filter.
 *      → "admits a declaration that names no rewind target" and both
 *        disposition tests go red: zero signals, disposition `halt`. OBSERVED.
 *   B. Remove the `TERMINAL_BLOCKING_SIGNAL_TYPES` branch from
 *      `notRewindableReason` (the type still falls outside
 *      REWINDABLE_SIGNAL_TYPES, so the fork still fires) → the reason-text
 *      assertion goes red while the `kind` assertion stays green, which is why
 *      both are asserted. OBSERVED.
 *   C. Delete `"feature-planning"` from `FEEDBACK_EMITTING_STAGES`.
 *      → "planning can declare it too" goes red. OBSERVED.
 *
 * @see Issue #1241
 * @see Issue #1142 — the blocked fork this makes reachable
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "sonnet", source: "default" }),
  findSkillFile: vi.fn().mockReturnValue(null),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      diagnosticsRecordStageExit: vi.fn().mockResolvedValue({ recorded: true }),
      attentionRaise: vi.fn().mockResolvedValue({ outcome: "created", id: "dr_test" }),
      call: vi.fn().mockResolvedValue({}),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { PipelineFeedbackSignal, PipelineStage } from "@nightgauge/sdk";

const ISSUE = 1241;

/** The declaration a stage writes when the issue is not pipeline work. */
function notActionableSignal(stage: PipelineStage): Record<string, unknown> {
  return {
    signal_type: "NOT_PIPELINE_ACTIONABLE",
    emitted_by_stage: stage,
    // Null BY DEFINITION — there is no stage to return to. This is the field
    // that made the declaration unreadable before #1241.
    backtrack_target_stage: null,
    severity: "blocking",
    rationale:
      "The deliverable is counsel sign-off on the published privacy policy and terms. " +
      "No code change satisfies it.",
    evidence: ["issue body: 'Items requiring counsel sign-off'"],
  };
}

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

let root: string;
let orch: HeadlessOrchestrator;

type Internals = {
  readFeedbackSignals(stage: PipelineStage, issueNumber: number): PipelineFeedbackSignal[];
  evaluateFailedStageFeedback(
    stage: PipelineStage,
    issueNumber: number
  ): Promise<{ kind: string; reason?: string; signal?: PipelineFeedbackSignal }>;
};

/** Write `<type>-{N}.json` carrying `feedback`, as the stage would have. */
function stageDeliverable(type: string, feedback: unknown[]): void {
  const dir = path.join(root, ".nightgauge", "pipeline");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(
    path.join(dir, `${type}-${ISSUE}.json`),
    JSON.stringify({ schema_version: "1.0", issue_number: ISSUE, feedback }, null, 2),
    "utf-8"
  );
}

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "ng-1241-orch-"));
  orch = new HeadlessOrchestrator(
    { getState: vi.fn() } as unknown as PipelineStateService,
    makeLogger(),
    { contextFileWaitMs: 0 } as never
  );
  // Deliverable paths resolve through the ContextAssembler, which roots itself
  // at the VSCode workspace folder — unavailable here. Point the ONE resolver
  // at the temp tree rather than mocking `fs`, so the tests read a real file
  // through the real code path.
  (orch as unknown as { getContextPath(type: string, n: number): string }).getContextPath = (
    type: string,
    n: number
  ) => path.join(root, ".nightgauge", "pipeline", `${type}-${n}.json`);
});

afterEach(() => {
  fs.rmSync(root, { recursive: true, force: true });
  vi.restoreAllMocks();
});

describe("NOT_PIPELINE_ACTIONABLE (#1241)", () => {
  it("admits a declaration that names no rewind target", () => {
    stageDeliverable("dev", [notActionableSignal("feature-dev")]);

    const signals = (orch as unknown as Internals).readFeedbackSignals("feature-dev", ISSUE);

    expect(signals).toHaveLength(1);
    expect(signals[0].signal_type).toBe("NOT_PIPELINE_ACTIONABLE");
  });

  it("still requires a rewind target of every signal that asks for a rewind", () => {
    // The exemption is scoped to the terminal types, not a blanket relaxation:
    // a PLAN_REVISION_NEEDED naming no target is still malformed and dropped.
    stageDeliverable("dev", [
      {
        signal_type: "PLAN_REVISION_NEEDED",
        emitted_by_stage: "feature-dev",
        backtrack_target_stage: null,
        severity: "blocking",
        rationale: "the plan misread the module layout",
        evidence: [],
      },
    ]);

    expect((orch as unknown as Internals).readFeedbackSignals("feature-dev", ISSUE)).toHaveLength(
      0
    );
  });

  it("routes feature-dev's declaration to `blocked`, never to a rewind or a halt", async () => {
    stageDeliverable("dev", [notActionableSignal("feature-dev")]);

    const disposition = await (orch as unknown as Internals).evaluateFailedStageFeedback(
      "feature-dev",
      ISSUE
    );

    expect(disposition.kind).toBe("blocked");
    // The REASON is asserted alongside the kind because the fork also fires for
    // the ordinary "not in REWINDABLE_SIGNAL_TYPES" fall-through, which reaches
    // the same kind by a different route and reports it as an unfixable plan
    // rather than as human-only work. Only the text separates them, and only
    // the text reaches the operator's card.
    expect(disposition.reason).toContain("NOT_PIPELINE_ACTIONABLE");
    expect(disposition.reason).toContain("needs a human");
  });

  it("lets feature-planning declare it too, before feature-dev spends anything", async () => {
    stageDeliverable("planning", [notActionableSignal("feature-planning")]);

    const disposition = await (orch as unknown as Internals).evaluateFailedStageFeedback(
      "feature-planning",
      ISSUE
    );

    expect(disposition.kind).toBe("blocked");
    expect(disposition.signal?.emitted_by_stage).toBe("feature-planning");
  });
});
