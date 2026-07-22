/**
 * Round-trip guard for the per-stage model selector (#4030): a tier the settings
 * UI offers must be accepted by getStageModel, not silently dropped. The `fable`
 * tier was historically missing from the resolver's allow-list + regex.
 *
 * @see Issue #4030 - per-stage model selection
 */

import { describe, it, expect, afterEach } from "vitest";
import { getStageModel } from "../../../src/utils/resolvers/stageResolver";

const ENV_KEY = "NIGHTGAUGE_PIPELINE_STAGE_MODEL_FEATURE_DEV";

describe("getStageModel — tier round-trip (#4030)", () => {
  afterEach(() => {
    delete process.env[ENV_KEY];
  });

  // The env-var path is the highest-priority branch and returns before any
  // config/file read, so it isolates the allow-list/regex acceptance.
  it.each(["haiku", "sonnet", "opus", "fable"])("accepts the %s tier", (tier) => {
    process.env[ENV_KEY] = tier;
    expect(getStageModel("feature-dev")).toBe(tier);
  });

  it("rejects an unknown tier (falls through, not echoed back)", () => {
    process.env[ENV_KEY] = "totally-not-a-tier";
    expect(getStageModel("feature-dev")).not.toBe("totally-not-a-tier");
  });
});
