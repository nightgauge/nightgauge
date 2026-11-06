/**
 * #3013: a feature-validate artifact the deliverable policy rejected (AC
 * verdicts under acceptance_criteria[].verdict) and the re-ask did not repair
 * must fail the stage. Recovery must NOT synthesize a deterministic validate
 * context over it: the synthetic file carries no acceptance_criteria_verdicts,
 * so it re-validates clean and pr-create's fallback writes `Refs` (#2798).
 */
import { describe, it, expect, vi } from "vitest";

vi.mock("../../src/utils/skillRunner", () => ({
  findSkillFile: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import { DeliverableContractError } from "../../src/orchestrator/context/ContextAssembler";

type Recovery = (
  stage: string,
  issueNumber: number,
  err: Error
) => Promise<{ recovered: boolean; error?: Error }>;

function harness() {
  const generateDeterministicContext = vi.fn().mockResolvedValue({ generated: true });
  const validateStageContextOutput = vi.fn().mockResolvedValue(null);
  const self = {
    logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
    contextAssembler: { generateDeterministicContext },
    validateStageContextOutput,
  };
  const recover = (
    HeadlessOrchestrator.prototype as unknown as { attemptContextRecovery: Recovery }
  ).attemptContextRecovery.bind(self);
  return { recover, generateDeterministicContext, validateStageContextOutput };
}

const contractError = (stage = "feature-validate") =>
  new DeliverableContractError(
    "feature-validate deliverable does not match the contract",
    stage as never,
    "/state/validate-3013.json",
    [
      {
        field: "acceptance_criteria",
        disposition: "fatal",
        rule: "validate.acceptance_criteria.verdicts_under_wrong_key",
        detail: "acceptance_criteria carries 5 verdict entries",
      },
    ]
  );

describe("attemptContextRecovery — policy-rejected validate artifact (#3013)", () => {
  it("fails the stage without synthesizing over the artifact", async () => {
    const h = harness();
    const err = contractError();
    const out = await h.recover("feature-validate", 3013, err);
    expect(out.recovered).toBe(false);
    expect(out.error).toBe(err);
    expect(h.generateDeterministicContext).not.toHaveBeenCalled();
    expect(h.validateStageContextOutput).not.toHaveBeenCalled();
  });

  it("still synthesizes when the validate subagent wrote no file", async () => {
    const h = harness();
    const out = await h.recover("feature-validate", 3013, new Error("context file not found"));
    expect(out.recovered).toBe(true);
    expect(h.generateDeterministicContext).toHaveBeenCalledWith("feature-validate", 3013);
  });

  it("leaves other stages' recovery unchanged", async () => {
    const h = harness();
    const out = await h.recover("feature-dev", 3013, contractError("feature-dev"));
    expect(out.recovered).toBe(true);
    expect(h.generateDeterministicContext).toHaveBeenCalledWith("feature-dev", 3013);
  });
});
