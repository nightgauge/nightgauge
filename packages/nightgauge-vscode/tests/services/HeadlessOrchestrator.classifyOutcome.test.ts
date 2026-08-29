/**
 * Issue #1109 — a run whose own stage list contains a failure must never be
 * classified with a success outcome type.
 *
 * A watched dogfood run failed its `feature-dev` post-condition gate: no
 * handoff, no PR, nothing merged, real money spent. It was classified
 * `productive`, and that raw value — not the corrected label the notifiers
 * render — is what the calibration table, the health snapshot and
 * `determineAction` consume. The contradiction was detected in `outcomeDisplay`
 * and then discarded.
 *
 * These tests pin the correction at the place `outcome_type` is COMPUTED and
 * PERSISTED, so what reaches the durable record and the corpus is the corrected
 * outcome.
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

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineOutcomeType } from "../../src/services/PipelineStateService";
import { SUCCESS_OUTCOMES } from "../../src/utils/telemetryEventBuilder";
import { countFailedStages } from "../../src/utils/failedStages";
import {
  countFailedStages as countFailedStagesFromNotifier,
  outcomeDisplay,
} from "../../src/services/DiscordService";

/**
 * The two private seams under test. Reached through an `unknown` cast rather
 * than an intersection, which TypeScript reduces to `never` for privates.
 */
interface ClassifierInternals {
  classifyPipelineOutcomeFromArtifacts: (n: number, s: string[]) => PipelineOutcomeType;
  classifyAndRecordOutcome: (
    n: number,
    s: string[],
    f?: string
  ) => Promise<PipelineOutcomeType | undefined>;
}

interface Recorder {
  orchestrator: ClassifierInternals;
  persisted: PipelineOutcomeType[];
  warnings: Array<{ msg: string; meta?: Record<string, unknown> }>;
}

/**
 * Build an orchestrator whose classification artifacts say "productive" —
 * `feature-dev` ran and its dev context lists modified files — over a state
 * snapshot the caller supplies.
 */
function makeOrchestrator(stages: Record<string, { status: string }>): Recorder {
  const persisted: PipelineOutcomeType[] = [];
  const warnings: Array<{ msg: string; meta?: Record<string, unknown> }> = [];

  const stateService = {
    getState: async () => ({ stages }),
    setOutcomeType: async (o: PipelineOutcomeType) => {
      persisted.push(o);
    },
    getRunId: () => "run-1109",
  };

  const logger = {
    info: vi.fn(),
    debug: vi.fn(),
    error: vi.fn(),
    warn: (msg: string, meta?: Record<string, unknown>) => {
      warnings.push({ msg, meta });
    },
  };

  const orchestrator = new HeadlessOrchestrator(
    stateService as never,
    logger as never
  ) as unknown as ClassifierInternals;

  // The artifact half of the classifier is not what #1109 is about — pin it to
  // the value the reported run produced ("productive": feature-dev ran, the
  // dev context listed modified files, no gate reported a no_op) so the test
  // exercises the failed-stage cross-check and nothing else.
  orchestrator.classifyPipelineOutcomeFromArtifacts = () => "productive";

  return { orchestrator, persisted, warnings };
}

function classify(rec: Recorder, failedStage?: string): Promise<PipelineOutcomeType | undefined> {
  return rec.orchestrator.classifyAndRecordOutcome(
    338,
    ["issue-pickup", "feature-dev"],
    failedStage
  );
}

describe("HeadlessOrchestrator outcome classification vs the run's failed stages (#1109)", () => {
  let rec: Recorder;

  describe("a run with a failed stage and productive artifacts", () => {
    beforeEach(async () => {
      rec = makeOrchestrator({
        "issue-pickup": { status: "complete" },
        "feature-dev": { status: "failed" },
      });
      await classify(rec);
    });

    it("PERSISTS an outcome_type that is not a success outcome", () => {
      expect(rec.persisted).toHaveLength(1);
      const persisted = rec.persisted[0];
      expect(persisted).toBeDefined();
      expect(SUCCESS_OUTCOMES).not.toContain(persisted);
      expect(persisted).not.toBe("productive");
    });

    it("books the run as `partial`", () => {
      expect(rec.persisted[0]).toBe("partial");
    });

    it("logs the contradiction at the classifier, not only at the notifier", () => {
      const warned = rec.warnings.find((w) => w.msg.includes("contradicted by the run's own"));
      expect(warned).toBeDefined();
      expect(warned?.meta).toMatchObject({
        proposedOutcomeType: "productive",
        outcome_type: "partial",
        failedStageCount: 1,
      });
    });

    it("hands the notifiers an outcome they no longer have to correct", () => {
      // outcomeDisplay's cosmetic correction is now defence in depth: over the
      // corrected outcome it neither warns nor rewrites the label.
      const warnLogger = { warn: vi.fn() };
      const display = outcomeDisplay(rec.persisted[0], {
        failedStageCount: 1,
        logger: warnLogger,
      });
      expect(warnLogger.warn).not.toHaveBeenCalled();
      expect(display.label).toBe("Failed ✗");
    });
  });

  it("classifies honestly when the loop halted on a stage the snapshot has not recorded yet", async () => {
    // The state snapshot still shows feature-dev running; the pipeline loop
    // knows it failed. The run must not be booked productive on that race.
    rec = makeOrchestrator({
      "issue-pickup": { status: "complete" },
      "feature-dev": { status: "running" },
    });
    await classify(rec, "feature-dev");

    expect(SUCCESS_OUTCOMES).not.toContain(rec.persisted[0]);
    expect(rec.persisted[0]).toBe("partial");
  });

  it("leaves the productive heuristic untouched when no stage failed", async () => {
    rec = makeOrchestrator({
      "issue-pickup": { status: "complete" },
      "feature-dev": { status: "complete" },
      "pr-merge": { status: "skipped" },
    });
    const outcome = await classify(rec);

    expect(outcome).toBe("productive");
    expect(rec.persisted).toEqual(["productive"]);
    expect(rec.warnings.filter((w) => w.msg.includes("contradicted"))).toHaveLength(0);
  });
});

describe("HeadlessOrchestrator.reconcileOutcomeWithFailedStages (#1109)", () => {
  it("rewrites EVERY success outcome, not a hand-maintained subset", () => {
    for (const success of SUCCESS_OUTCOMES) {
      expect(HeadlessOrchestrator.reconcileOutcomeWithFailedStages(success, 1)).toBe("partial");
    }
  });

  it("never rewrites an outcome that is already non-success", () => {
    // These carry MORE information than `partial` — a blocked run names its
    // blocker class, a no-op names the gate. Downgrading them would lose that.
    for (const outcome of [
      "skill-no-op",
      "blocked",
      "budget-ceiling",
      "deferred",
      "cancelled",
      "failure",
      "partial",
    ] as PipelineOutcomeType[]) {
      expect(HeadlessOrchestrator.reconcileOutcomeWithFailedStages(outcome, 3)).toBe(outcome);
    }
  });

  it("is a no-op when no stage failed", () => {
    expect(HeadlessOrchestrator.reconcileOutcomeWithFailedStages("productive", 0)).toBe(
      "productive"
    );
    expect(HeadlessOrchestrator.reconcileOutcomeWithFailedStages("productive", -1)).toBe(
      "productive"
    );
  });

  it("books a non-success outcome for `partial`", () => {
    // Guards the choice of replacement: it must be outside the authoritative
    // success set analytics maps to outcome="success".
    expect(SUCCESS_OUTCOMES).not.toContain(HeadlessOrchestrator.FAILED_STAGE_OUTCOME);
  });
});

describe("the failed-stage predicate is shared, not per-consumer (#1109)", () => {
  it("the notifiers and the classifier import the SAME function", () => {
    expect(countFailedStagesFromNotifier).toBe(countFailedStages);
  });

  it("counts only stages in `failed` status", () => {
    expect(
      countFailedStages({
        stages: {
          a: { status: "complete" },
          b: { status: "failed" },
          c: { status: "running" },
          d: { status: "failed" },
        },
      })
    ).toBe(2);
    expect(countFailedStages({ stages: {} })).toBe(0);
    expect(countFailedStages(null)).toBe(0);
    expect(countFailedStages(undefined)).toBe(0);
  });
});
