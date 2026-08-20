import { describe, expect, it } from "vitest";
import {
  ContextSchemaError,
  MissingInputFile,
  PipelineStateError,
  RunStateMissing,
  SchemaVersionMismatch,
  StageGraph,
  WorktreeMissing,
} from "@nightgauge/sdk";
import {
  computeAvailableActions,
  computeRecoveryRequired,
  isRecoveryRequiredError,
} from "../../../src/orchestrator/recovery/computeRecoveryRequired";

const graph = StageGraph.fromFallback();

describe("computeAvailableActions", () => {
  it("keeps every recovery state observational", () => {
    const actions = computeAvailableActions();

    expect(actions).toEqual(["open-run-state-directory", "cancel"]);
    expect(actions as string[]).not.toContain("run-producing-stage");
    expect(actions as string[]).not.toContain("resume-from-paused-stage");
    expect(actions as string[]).not.toContain("discard-run");
    expect(actions as string[]).not.toContain("restart-from-beginning");
  });
});

describe("computeRecoveryRequired", () => {
  it("classifies missing input and names its producer without authorizing execution", () => {
    const error = new MissingInputFile(
      ".nightgauge/pipeline/planning-793.json",
      "feature-dev",
      "feature-planning"
    );

    expect(
      computeRecoveryRequired(error, 793, "feature-dev", { lifecycle: "paused" }, graph)
    ).toEqual({
      issueNumber: 793,
      triggeringStage: "feature-dev",
      producingStage: "feature-planning",
      errorKind: "MISSING_INPUT_FILE",
      errorDetail: error.message,
      runState: "paused",
      availableActions: ["open-run-state-directory", "cancel"],
    });
  });

  it.each([
    [new ContextSchemaError("dev-793.json", "invalid"), "CONTEXT_SCHEMA_ERROR"],
    [new WorktreeMissing("/private/worktree", "fix/793"), "WORKTREE_MISSING"],
    [new RunStateMissing(793), "RUN_STATE_MISSING"],
    [new SchemaVersionMismatch("dev-793.json", "2.0", 1), "SCHEMA_VERSION_MISMATCH"],
  ] as const)("classifies %s as %s", (error, expectedKind) => {
    const payload = computeRecoveryRequired(
      error,
      793,
      "feature-dev",
      { lifecycle: "aborted" },
      graph
    );

    expect(payload).toMatchObject({
      issueNumber: 793,
      triggeringStage: "feature-dev",
      errorKind: expectedKind,
      runState: "aborted",
      availableActions: ["open-run-state-directory", "cancel"],
    });
    expect(isRecoveryRequiredError(error)).toBe(true);
  });

  it("leaves generic and unrecognized errors on the flat error path", () => {
    const genericStateError = new PipelineStateError("generic", "GENERIC", true, []);

    expect(
      computeRecoveryRequired(genericStateError, 793, "feature-dev", { lifecycle: "none" }, graph)
    ).toBeNull();
    expect(
      computeRecoveryRequired(new Error("boom"), 793, "feature-dev", { lifecycle: "none" }, graph)
    ).toBeNull();
    expect(isRecoveryRequiredError(genericStateError)).toBe(false);
    expect(isRecoveryRequiredError(new Error("boom"))).toBe(false);
  });
});
