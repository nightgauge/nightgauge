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

/**
 * The OTHER unreachable declaration (#1504).
 *
 * #1241 widened `readFeedbackSignals` for a TYPE. The second declared way to
 * say "no lap of this pipeline helps" is the `EXTERNAL_BLOCKER_EVIDENCE` marker
 * that `notRewindableReason` has honoured since #1142 — and the shape the
 * feature-planning skill MANDATES for an open prerequisite is
 * `PLAN_REVISION_NEEDED` + `backtrack_target_stage: null` + a `blocked-on:`
 * evidence entry + no plan file. That is a rewindable type naming no target, so
 * the filter dropped it before the branch written for it was ever reached: the
 * marker branch was dead code in production for its whole first life.
 *
 * The producer contract and the reader therefore disagreed, and the run was
 * booked `premature_turn_end` — a repo halt, a public failure comment, and the
 * issue reverted to Ready to be convicted again on the next tick.
 *
 * The signal below is the specimen, trimmed — the reported run
 * 01a0772b-9f16-7684-ac5d-d1ce9988a505, 2026-09-06.
 *
 * RED-PROOFS (each leaves the code compiling):
 *   D. Drop the `declaresExternalBlocker(signal)` clause from
 *      `readFeedbackSignals`'s filter → both "keeps" and "routes" tests go red
 *      (zero signals, disposition `halt`), which is the reported bug exactly.
 *   E. Widen the clause to `signal.evidence.length > 0` → "a rewindable signal
 *      with no marker is still dropped" goes red, proving the admission is
 *      keyed to the marker and not merely to having evidence.
 *
 * @see Issue #1504
 */
describe("external-blocker marker (#1504)", () => {
  /** The shape skills/nightgauge-feature-planning/SKILL.md § open prerequisite mandates. */
  function openPrerequisiteSignal(): Record<string, unknown> {
    return {
      signal_type: "PLAN_REVISION_NEEDED",
      emitted_by_stage: "feature-planning",
      // Null by mandate: the skill tells the stage to write no target, because
      // no stage of THIS issue is where the work is.
      backtrack_target_stage: null,
      severity: "blocking",
      rationale:
        "The issue body declares this blocked by two OPEN upstream issues; the " +
        "question-generation endpoint is a stub until they merge, so no plan can " +
        "satisfy the acceptance criteria.",
      evidence: [
        "blocked-on: acme-org/upstream-service#1253 (OPEN) — declared in the issue body",
        "blocked-on: acme-org/upstream-service#1252 (OPEN) — Wave-1-first rule",
        "issue-692.json: dependencies.blockedBy == [] — the declaration is prose only",
      ],
    };
  }

  it("keeps a rewindable signal that declares an external blocker and names no target", () => {
    stageDeliverable("planning", [openPrerequisiteSignal()]);

    const signals = (orch as unknown as Internals).readFeedbackSignals("feature-planning", ISSUE);

    expect(signals).toHaveLength(1);
    expect(signals[0].signal_type).toBe("PLAN_REVISION_NEEDED");
    expect(signals[0].backtrack_target_stage).toBeNull();
  });

  it("routes the planning skill's open-prerequisite signal to `blocked`, not a halt", async () => {
    stageDeliverable("planning", [openPrerequisiteSignal()]);

    const disposition = await (orch as unknown as Internals).evaluateFailedStageFeedback(
      "feature-planning",
      ISSUE
    );

    // `blocked` and not `rewind`: a re-plan cannot close someone else's issue.
    // `blocked` and not `halt`: halting is what booked this run
    // premature_turn_end and stopped the whole repository.
    expect(disposition.kind).toBe("blocked");
    // Asserted alongside the kind for the same reason as the #1241 tests: three
    // routes reach `blocked` and only the text tells the operator which wall
    // they hit. This one must quote the blocker, because the blocker closing is
    // what makes the issue workable again.
    expect(disposition.reason).toContain("out-of-scope blocker");
    expect(disposition.reason).toContain("acme-org/upstream-service#1253");
  });

  it("still drops a rewindable signal with a null target and no marker", () => {
    // The admission is keyed to the MARKER, not relaxed for every signal that
    // happens to carry evidence. A PLAN_REVISION_NEEDED naming no target and
    // declaring no external blocker is still malformed.
    stageDeliverable("planning", [
      {
        ...openPrerequisiteSignal(),
        evidence: [
          "issue-692.json: dependencies.blockedBy == []",
          "the plan misread the module layout",
        ],
      },
    ]);

    expect(
      (orch as unknown as Internals).readFeedbackSignals("feature-planning", ISSUE)
    ).toHaveLength(0);
  });

  it("still excludes MODEL_ESCALATION_NEEDED even when it carries the marker", () => {
    // Escalation retries the SAME stage on a stronger model; it is not a
    // backtrack and never was one, so the marker must not smuggle it into the
    // blocked fork and turn a recoverable retry into a terminal outcome.
    stageDeliverable("planning", [
      {
        ...openPrerequisiteSignal(),
        signal_type: "MODEL_ESCALATION_NEEDED",
      },
    ]);

    expect(
      (orch as unknown as Internals).readFeedbackSignals("feature-planning", ISSUE)
    ).toHaveLength(0);
  });
});
