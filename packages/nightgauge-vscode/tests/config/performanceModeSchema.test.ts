/**
 * The performance mode is not configured in config.yaml: it is
 * NIGHTGAUGE_PERFORMANCE_MODE, else the checkout's performance-mode.yaml, else
 * `elevated` (#2343). `pipeline.performance_mode` only ever held keys nothing
 * read — `default` (#2343) and `overrides.maximum` (#2378) — so the schema
 * no longer has it, and the legacy `supercharge` block keeps only the two keys
 * the extension reads.
 *
 * @see docs/PERFORMANCE_MODES.md
 */

import { describe, it, expect } from "vitest";
import { PipelineConfigSchema } from "../../src/config/schema";

describe("pipeline schema: no unread performance-mode keys (#2343, #2378)", () => {
  it("has no pipeline.performance_mode", () => {
    expect(Object.keys(PipelineConfigSchema.shape)).not.toContain("performance_mode");
  });

  it("keeps only the read keys of the legacy supercharge block", () => {
    const supercharge = PipelineConfigSchema.shape.supercharge.unwrap();
    expect(Object.keys(supercharge.shape).sort()).toEqual(["codex_model", "model"]);
  });
});
