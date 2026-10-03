/**
 * HeadlessOrchestrator.escalationCeiling.test.ts
 *
 * Pins #2386: every model escalation a run the extension orchestrates can make
 * stays inside the stage's routed-tier ceiling, the performance mode's
 * envelope with `model_routing.max_model` applied, as the Go dispatch path's
 * `ClampToCeiling` does. Before the fix all three paths (a stage's
 * MODEL_ESCALATION_NEEDED signal, the proactive pre-stage escalation and the
 * health-gated escalateAllStages policy) moved a `sonnet` stage to `opus`
 * under `efficiency`, whose ceiling is `sonnet`.
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../src/utils/logger";

// Every stage resolves to sonnet unless an override says otherwise.
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "sonnet", source: "auto" }),
}));

interface Orch {
  stageModelOverrides: Map<string, string>;
  eventDispatcher: { onEscalationBlocked: (reason: string, signal: unknown) => void };
  evaluateEscalation: (stage: string, signal: unknown, issueNumber: number) => string | null;
  evaluatePreStageHealth: (stage: string, issueNumber: number) => string | null;
  escalateAllStages: () => void;
  computeHealthTrendSlope: () => number;
  computeStageFailureRate: (stage: string) => number;
}

function makeOrch(): Orch {
  const logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
  const orch = new HeadlessOrchestrator(
    null as never,
    logger as unknown as Logger,
    {
      contextFileWaitMs: 0,
    } as never
  );
  vi.spyOn(
    orch as never as { getWorkingDirectory: () => string },
    "getWorkingDirectory"
  ).mockReturnValue("/nonexistent/escalation-ceiling-ws");
  const o = orch as unknown as Orch;
  // A declining health trend and a stage that fails often: the proactive
  // escalation's own guards pass, so only the ceiling can stop it.
  vi.spyOn(o, "computeHealthTrendSlope").mockReturnValue(-10);
  vi.spyOn(o, "computeStageFailureRate").mockReturnValue(0.5);
  return o;
}

const SIGNAL = { type: "MODEL_ESCALATION_NEEDED", rationale: "stuck" };

describe("HeadlessOrchestrator escalation stays inside the mode's ceiling (#2386)", () => {
  const saved = { ...process.env };

  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
    delete process.env.NIGHTGAUGE_MODEL_ROUTING_MAX_MODEL;
  });

  afterEach(() => {
    process.env = { ...saved };
  });

  describe("under efficiency, whose ceiling is sonnet", () => {
    beforeEach(() => {
      process.env.NIGHTGAUGE_PERFORMANCE_MODE = "efficiency";
    });

    it("blocks a stage's escalation signal and reports the ceiling", () => {
      const orch = makeOrch();
      const blocked = vi.spyOn(orch.eventDispatcher, "onEscalationBlocked");
      expect(orch.evaluateEscalation("feature-dev", SIGNAL, 7)).toBeNull();
      expect(blocked).toHaveBeenCalledWith("escalation_ceiling_reached", SIGNAL);
    });

    it("applies no proactive escalation", () => {
      const orch = makeOrch();
      expect(orch.evaluatePreStageHealth("feature-dev", 7)).toBeNull();
      expect(orch.stageModelOverrides.has("feature-dev")).toBe(false);
    });

    it("escalates no stage under the escalateAllStages policy", () => {
      const orch = makeOrch();
      orch.escalateAllStages();
      expect([...orch.stageModelOverrides.values()]).not.toContain("opus");
    });

    it("still escalates a stage that is below the ceiling", () => {
      const orch = makeOrch();
      orch.stageModelOverrides.set("feature-dev", "haiku");
      expect(orch.evaluateEscalation("feature-dev", SIGNAL, 7)).toBe("sonnet");
    });
  });

  it("model_routing.max_model caps escalation the same way", () => {
    process.env.NIGHTGAUGE_MODEL_ROUTING_MAX_MODEL = "sonnet";
    const orch = makeOrch();
    expect(orch.evaluateEscalation("feature-dev", SIGNAL, 7)).toBeNull();
  });

  describe("under elevated, whose ceiling is opus", () => {
    it("escalates a sonnet stage to opus on every path", () => {
      expect(makeOrch().evaluateEscalation("feature-dev", SIGNAL, 7)).toBe("opus");
      expect(makeOrch().evaluatePreStageHealth("feature-dev", 7)).toBe("opus");
      const orch = makeOrch();
      orch.escalateAllStages();
      expect(orch.stageModelOverrides.get("feature-dev")).toBe("opus");
    });
  });
});
