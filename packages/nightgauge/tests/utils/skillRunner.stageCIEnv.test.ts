/**
 * skillRunner.stageCIEnv.test.ts
 *
 * Stages force CI=true for their tools and carry the operator's own CI value
 * in NIGHTGAUGE_HOST_CI, so config.CIHost does not take an operator machine
 * for a shared CI runner (#2514).
 */

import { describe, it, expect } from "vitest";

import { stageCIEnv } from "../../src/utils/skillRunner";

describe("stageCIEnv", () => {
  it("forces CI=true and records an operator machine as not CI", () => {
    expect(stageCIEnv({})).toEqual({ CI: "true", NIGHTGAUGE_HOST_CI: "false" });
  });

  it("records a real CI host", () => {
    expect(stageCIEnv({ CI: "true" }).NIGHTGAUGE_HOST_CI).toBe("true");
    expect(stageCIEnv({ CI: "1" }).NIGHTGAUGE_HOST_CI).toBe("true");
    expect(stageCIEnv({ CI: "TRUE" }).NIGHTGAUGE_HOST_CI).toBe("true");
  });

  it("keeps the outermost host value in a nested spawn", () => {
    expect(stageCIEnv({ CI: "true", NIGHTGAUGE_HOST_CI: "false" }).NIGHTGAUGE_HOST_CI).toBe(
      "false"
    );
  });
});
