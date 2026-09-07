/**
 * #1534 — the deterministic (primary) issue-pickup path must report the
 * registry phases it performs and skip the ones it does not, so the tree
 * shows live progress instead of `0/14` and ends with ZERO `unreported` rows
 * instead of fourteen.
 *
 * The mirror of `internal/orchestrator/deterministic_phases_test.go`, which
 * asserts the same contract for the Go runners (#1247 / PR #1398).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { PHASE_REGISTRY } from "@nightgauge/sdk";
import type { Logger } from "../../../src/utils/logger";
import {
  createDeterministicPhaseReporter,
  DETERMINISTIC_PICKUP_SKIP_REASON,
  type DeterministicPhaseSink,
} from "../../../src/utils/deterministicPhases";

const { execFileResponses, binaryPath } = vi.hoisted(() => ({
  execFileResponses: { checkDeps: "" as string, branch: "fix/1534-pickup-phases" as string },
  binaryPath: { value: "/fake/nightgauge" as string | null },
}));

vi.mock("../../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: () => ({ resolve: async () => binaryPath.value }),
  },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (cmd === "git" && args[0] === "branch") {
      return Promise.resolve({ stdout: `${execFileResponses.branch}\n`, stderr: "" });
    }
    if (cmd === "gh" && args[0] === "issue") {
      return Promise.resolve({
        stdout: JSON.stringify({
          title: "Deterministic pickup reports its phases",
          labels: [{ name: "type:bug" }],
          body: "## Summary\n\nReport phases.\n",
        }),
        stderr: "",
      });
    }
    if (cmd === "gh" && args[0] === "repo") {
      return Promise.resolve({ stdout: "nightgauge/nightgauge\n", stderr: "" });
    }
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args[0] === "hook") {
      return Promise.resolve({ stdout: execFileResponses.checkDeps, stderr: "" });
    }
    return Promise.resolve({ stdout: "", stderr: "" });
  };

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: "", stderr: "" });

  return { ...actual, exec: execMock, execFile: execFileMock };
});

import { ContextAssembler } from "../../../src/orchestrator/context/ContextAssembler";

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

type PhaseEvent = {
  op: "start" | "complete" | "skip";
  stage: string;
  name: string;
  total: number;
  index?: number;
  reason?: string;
};

/** A recording sink — the shape `PipelineStateService` satisfies structurally. */
function makeSink(): { events: PhaseEvent[]; sink: DeterministicPhaseSink } {
  const events: PhaseEvent[] = [];
  const sink: DeterministicPhaseSink = {
    async startPhase(stage, name, total, index) {
      events.push({ op: "start", stage, name, total, index });
    },
    async completePhase(stage, name, total) {
      events.push({ op: "complete", stage, name, total });
    },
    async skipPhase(stage, name, total, index, reason) {
      events.push({ op: "skip", stage, name, total, index, reason });
    },
  };
  return { events, sink };
}

/** The waypoints the deterministic pickup path actually performs, in order. */
const PERFORMED = [
  "validate-environment",
  "issue-selection",
  "issue-analysis",
  "blocked-dependency-gate",
  "write-context",
];

const PICKUP_PHASES = PHASE_REGISTRY["issue-pickup"];

describe("deterministic issue-pickup phase reporting (#1534)", () => {
  let tmpDir: string;
  let assembler: ContextAssembler;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-1534-"));
    assembler = new ContextAssembler(makeLogger(), () => tmpDir, null);
    binaryPath.value = "/fake/nightgauge";
    execFileResponses.branch = "fix/1534-pickup-phases";
    execFileResponses.checkDeps = JSON.stringify({
      issue_number: 1534,
      has_open_dependencies: false,
      open_dependencies: [],
      open_count: 0,
    });
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
    vi.clearAllMocks();
  });

  it("emits start/complete for the waypoints it performs, in execution order", async () => {
    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    const result = await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);
    expect(result.generated).toBe(true);

    const performed = events
      .filter((e) => e.op === "start")
      .map((e) => e.name)
      .filter((n) => PERFORMED.includes(n));
    expect(performed).toEqual(PERFORMED);

    // Every started phase is completed, and completion follows its start.
    for (const name of PERFORMED) {
      const startAt = events.findIndex((e) => e.op === "start" && e.name === name);
      const completeAt = events.findIndex((e) => e.op === "complete" && e.name === name);
      expect(startAt, `${name} started`).toBeGreaterThanOrEqual(0);
      expect(completeAt, `${name} completed`).toBeGreaterThan(startAt);
    }
  });

  it("reports every phase in the registry — nothing is left unreported", async () => {
    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);

    const reported = new Set(events.map((e) => e.name));
    for (const phase of PICKUP_PHASES) {
      expect(reported.has(phase.name), `${phase.name} reported`).toBe(true);
    }
    expect(reporter.reportedPhases().length).toBe(PICKUP_PHASES.length);
  });

  it("skips the registry phases it does not perform, with a reason", async () => {
    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);

    const skipped = events.filter((e) => e.op === "skip");
    const expectedSkips = PICKUP_PHASES.filter((p) => !PERFORMED.includes(p.name)).map(
      (p) => p.name
    );
    // Skips land in registry order, which is the order the tree renders them.
    expect(skipped.map((e) => e.name)).toEqual(expectedSkips);
    for (const event of skipped) {
      expect(event.reason).toBe(DETERMINISTIC_PICKUP_SKIP_REASON);
    }
    // No performed phase is ever skipped.
    for (const name of PERFORMED) {
      expect(skipped.some((e) => e.name === name)).toBe(false);
    }
  });

  it("uses registry index and total on every event", async () => {
    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);

    for (const event of events) {
      expect(event.stage).toBe("issue-pickup");
      expect(event.total).toBe(PICKUP_PHASES.length);
      if (event.index !== undefined) {
        const registryIndex = PICKUP_PHASES.findIndex((p) => p.name === event.name);
        expect(event.index, `${event.name} index`).toBe(registryIndex);
      }
    }
  });

  it("every emitted phase name exists in the registry", async () => {
    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);

    const known = new Set(PICKUP_PHASES.map((p) => p.name));
    for (const event of events) {
      expect(known.has(event.name), `${event.name} in registry`).toBe(true);
    }
  });

  it("settles the stage when pickup DEFERS on open blockedBy dependencies (#189)", async () => {
    execFileResponses.checkDeps = JSON.stringify({
      issue_number: 1534,
      has_open_dependencies: true,
      open_dependencies: [{ number: 47, title: "blocker", state: "OPEN" }],
      open_count: 1,
    });

    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    const result = await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);
    expect(result.generated).toBe(false);
    expect(result.blockedBy?.length).toBe(1);

    // The gate ran and returned a verdict, so it completes...
    expect(events.some((e) => e.op === "complete" && e.name === "blocked-dependency-gate")).toBe(
      true
    );
    // ...write-context never ran, and is a skip rather than a silent gap.
    expect(events.some((e) => e.op === "skip" && e.name === "write-context")).toBe(true);
    expect(reporter.reportedPhases().length).toBe(PICKUP_PHASES.length);
  });

  it("does NOT settle when the path fails and falls through to the LLM", async () => {
    // No current branch → the generator bails before anything else, and the
    // caller falls through to the LLM subagent, which reports its own phases.
    // Pre-skipping them here would make the LLM's markers unrecordable.
    execFileResponses.branch = "";

    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    const result = await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);
    expect(result.generated).toBe(false);
    expect(events.some((e) => e.op === "skip")).toBe(false);
    // The one waypoint it reached is still visible as started — live progress
    // for the seconds it ran — it is simply never settled.
    expect(events.map((e) => `${e.op}:${e.name}`)).toEqual(["start:validate-environment"]);
  });

  it("is a no-op for a phase name the registry does not declare", async () => {
    const { events, sink } = makeSink();
    const reporter = createDeterministicPhaseReporter("issue-pickup", sink);

    await reporter.start("not-a-real-phase");
    await reporter.complete("not-a-real-phase");
    await reporter.skip("not-a-real-phase", "nope");

    expect(events).toEqual([]);
    expect(reporter.reportedPhases()).toEqual([]);
  });

  it("survives a sink that rejects — telemetry never fails the pickup", async () => {
    const reporter = createDeterministicPhaseReporter("issue-pickup", {
      startPhase: () => Promise.reject(new Error("state write failed")),
      completePhase: () => Promise.reject(new Error("state write failed")),
      skipPhase: () => Promise.reject(new Error("state write failed")),
    });

    const result = await assembler.generateDeterministicContext("issue-pickup", 1534, reporter);
    expect(result.generated).toBe(true);
  });
});
