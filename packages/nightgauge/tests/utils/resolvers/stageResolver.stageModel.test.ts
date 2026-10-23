/**
 * Round-trip guard for the per-stage model selector (#4030): a tier the settings
 * UI offers must be accepted by getStageModel, not silently dropped. The `fable`
 * tier was historically missing from the resolver's allow-list + regex.
 *
 * @see Issue #4030 - per-stage model selection
 */

import { describe, it, expect, afterEach } from "vitest";
import { getModelDescriptor } from "@nightgauge/sdk";
import { getStageModel } from "../../../src/utils/resolvers/stageResolver";
import { getRoutedTierEnvelope } from "../../../src/utils/modeProfiles";

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

describe("frontier mode resolves the fable ceiling to the CURRENT band leader (#1274)", () => {
  // The router and this envelope both speak BANDS; the concrete id is the
  // registry's answer for the band. That indirection is what made the gap
  // invisible before 5.1 was registered: `frontier` kept resolving to a model
  // one generation behind and nothing failed, because a stale id is still a
  // valid string. Asserting the band alone would reproduce that blindness.
  it.each(["feature-planning", "feature-dev"] as const)(
    "%s escalates to fable, and fable is claude-fable-5-1",
    (stage) => {
      expect(getRoutedTierEnvelope("frontier", stage).ceiling).toBe("fable");
      expect(getModelDescriptor("fable", "anthropic")?.id).toBe("claude-fable-5-1");
    }
  );

  it("claude-fable-5 is deprecated behind it, not deleted", () => {
    const superseded = getModelDescriptor("claude-fable-5");
    expect(superseded?.deprecated).toBe(true);
    expect(superseded?.replacement).toBe("claude-fable-5-1");
  });

  it("non-reasoning stages still cap at opus — frontier does not pay fable rates for plumbing", () => {
    for (const stage of ["issue-pickup", "feature-validate", "pr-create", "pr-merge"] as const) {
      expect(getRoutedTierEnvelope("frontier", stage).ceiling).toBe("opus");
    }
  });
});
