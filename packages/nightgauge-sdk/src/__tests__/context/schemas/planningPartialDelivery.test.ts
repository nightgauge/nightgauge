import { describe, expect, it } from "vitest";
import { PlanningContextSchema } from "../../../context/schemas/index.js";

const minimalPlanning = {
  schema_version: "1.9",
  issue_number: 3160,
  plan_file: ".nightgauge/plans/3160-test.md",
  approach: "Scope the run to the partial-delivery remainder",
  files_to_create: [],
  files_to_modify: [],
  created_at: "2026-11-10T00:00:00.000Z",
};

describe("PlanningContextSchema — partial_delivery (Issue #3160)", () => {
  it("parses an unscoped planning context without the field", () => {
    const result = PlanningContextSchema.safeParse(minimalPlanning);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.partial_delivery).toBeUndefined();
    }
  });

  it("parses the remainder the scheduler splices", () => {
    const result = PlanningContextSchema.safeParse({
      ...minimalPlanning,
      partial_delivery: {
        pr: 3141,
        criteria: ["An integration test `x_integration_test.go` exists"],
        comment_at: "2026-11-10T12:00:00Z",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.partial_delivery?.pr).toBe(3141);
      expect(result.data.partial_delivery?.criteria).toEqual([
        "An integration test `x_integration_test.go` exists",
      ]);
    }
  });

  it("rejects a remainder with no PR number", () => {
    const result = PlanningContextSchema.safeParse({
      ...minimalPlanning,
      partial_delivery: { pr: 0, criteria: ["x"] },
    });
    expect(result.success).toBe(false);
  });
});
