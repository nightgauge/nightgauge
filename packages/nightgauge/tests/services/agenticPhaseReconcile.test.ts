/**
 * #1887: the extension's legacy route replays `nightgauge phase reconcile`
 * after an agentic stage passes its gate. These pin the argv, the
 * gate-and-exit precondition, the fail-soft paths, and that only the record
 * the state service agrees to change is counted.
 */
import { describe, it, expect, vi } from "vitest";
import {
  buildPhaseReconcileArgs,
  isAgenticReconcileStage,
  parsePhaseReconcileOutput,
  reconcileAgenticStagePhases,
  type AgenticReconcileRequest,
} from "../../src/services/agenticPhaseReconcile";

const since = new Date("2026-10-28T12:00:00.000Z");

function req(over: Partial<AgenticReconcileRequest> = {}): AgenticReconcileRequest {
  return {
    stage: "feature-dev",
    issueNumber: 1887,
    since,
    exitCode: 0,
    gatePassed: true,
    workdir: "/tmp/wt",
    ...over,
  };
}

const verdicts = JSON.stringify([
  { stage: "feature-dev", name: "testing", index: 8, total: 17, status: "complete" },
  { stage: "feature-dev", name: "e2e-testing", index: 9, total: 17, status: "skipped" },
  { stage: "feature-dev", name: "write-dev-context", index: 13, total: 17, status: "complete" },
]);

function deps(stdout = verdicts) {
  return {
    resolveBinary: vi.fn(async () => "/bin/nightgauge" as string | null),
    run: vi.fn(async (_binary: string, _args: string[], _cwd: string) => stdout),
    observe: vi.fn(async (_t: { name: string }) => true),
    warn: vi.fn(),
  };
}

describe("buildPhaseReconcileArgs", () => {
  it("passes the stage, issue, start time, exit code, gate verdict and workspace", () => {
    expect(buildPhaseReconcileArgs(req())).toEqual([
      "phase",
      "reconcile",
      "--stage",
      "feature-dev",
      "--issue",
      "1887",
      "--since",
      "2026-10-28T12:00:00.000Z",
      "--exit-code",
      "0",
      "--gate-passed=true",
      "--workdir",
      "/tmp/wt",
    ]);
  });

  it("spells a failed gate as --gate-passed=false, not an absent flag", () => {
    expect(buildPhaseReconcileArgs(req({ gatePassed: false }))).toContain("--gate-passed=false");
  });
});

describe("isAgenticReconcileStage", () => {
  it("covers exactly the three agentic stages", () => {
    expect(
      ["feature-planning", "feature-dev", "feature-validate"].every(isAgenticReconcileStage)
    ).toBe(true);
    expect(isAgenticReconcileStage("issue-pickup")).toBe(false);
    expect(isAgenticReconcileStage("pr-merge")).toBe(false);
  });
});

describe("parsePhaseReconcileOutput", () => {
  it("accepts the PhaseTransition[] wire shape, including an empty array", () => {
    expect(parsePhaseReconcileOutput(verdicts)).toHaveLength(3);
    expect(parsePhaseReconcileOutput("[]\n")).toEqual([]);
  });

  it("rejects non-JSON, a non-array, and an element missing a field", () => {
    expect(parsePhaseReconcileOutput("not json")).toBeUndefined();
    expect(parsePhaseReconcileOutput('{"name":"x"}')).toBeUndefined();
    expect(
      parsePhaseReconcileOutput(
        '[{"stage":"feature-dev","name":"x","index":1,"status":"complete"}]'
      )
    ).toBeUndefined();
  });
});

describe("reconcileAgenticStagePhases", () => {
  it("records every verdict and runs in the stage workspace", async () => {
    const d = deps();
    const changed = await reconcileAgenticStagePhases(req(), d);

    expect(changed).toBe(3);
    expect(d.run).toHaveBeenCalledWith(
      "/bin/nightgauge",
      buildPhaseReconcileArgs(req()),
      "/tmp/wt"
    );
    expect(d.observe).toHaveBeenCalledWith(
      expect.objectContaining({ name: "e2e-testing", index: 9, total: 17, status: "skipped" })
    );
  });

  it("counts only the phases the record accepted — a marker-recorded phase is not rewritten", async () => {
    const d = deps();
    d.observe.mockImplementation(async (t: { name: string }) => t.name !== "testing");

    expect(await reconcileAgenticStagePhases(req(), d)).toBe(2);
    expect(d.observe).toHaveBeenCalledTimes(3);
  });

  it.each([
    ["a non-zero exit", { exitCode: 1 }],
    ["an unverified gate", { gatePassed: false }],
    ["a deterministic stage", { stage: "pr-create" }],
  ])("does not spawn on %s", async (_label, over) => {
    const d = deps();
    expect(await reconcileAgenticStagePhases(req(over), d)).toBe(0);
    expect(d.resolveBinary).not.toHaveBeenCalled();
    expect(d.run).not.toHaveBeenCalled();
    expect(d.observe).not.toHaveBeenCalled();
  });

  it("warns and records nothing when the binary does not resolve", async () => {
    const d = deps();
    d.resolveBinary.mockResolvedValue(null);
    expect(await reconcileAgenticStagePhases(req(), d)).toBe(0);
    expect(d.run).not.toHaveBeenCalled();
    expect(d.warn).toHaveBeenCalledWith(
      expect.stringContaining("binary unresolved"),
      expect.anything()
    );
  });

  it("warns and does not throw when the verb fails", async () => {
    const d = deps();
    d.run.mockRejectedValue(new Error("exit status 1"));
    await expect(reconcileAgenticStagePhases(req(), d)).resolves.toBe(0);
    expect(d.observe).not.toHaveBeenCalled();
    expect(d.warn).toHaveBeenCalledWith(expect.stringContaining("verb failed"), expect.anything());
  });

  it("warns and records nothing on malformed output", async () => {
    const d = deps("Error: something\n");
    expect(await reconcileAgenticStagePhases(req(), d)).toBe(0);
    expect(d.observe).not.toHaveBeenCalled();
    expect(d.warn).toHaveBeenCalledWith(
      expect.stringContaining("not a PhaseTransition"),
      expect.anything()
    );
  });

  it("ignores a transition for another stage or with a non-terminal status", async () => {
    const d = deps(
      JSON.stringify([
        { stage: "feature-planning", name: "x", index: 0, total: 9, status: "complete" },
        { stage: "feature-dev", name: "testing", index: 8, total: 17, status: "running" },
      ])
    );
    expect(await reconcileAgenticStagePhases(req(), d)).toBe(0);
    expect(d.observe).not.toHaveBeenCalled();
  });

  it("keeps going when recording one phase throws", async () => {
    const d = deps();
    d.observe.mockRejectedValueOnce(new Error("state write failed"));
    expect(await reconcileAgenticStagePhases(req(), d)).toBe(2);
  });
});
