/**
 * epicBranchParent mirrors the Go git.EpicBranchParent (#2377): only a parent
 * epic in the sub-issue's own repository has an epic branch there.
 */

import { describe, it, expect } from "vitest";
import { epicBranchParent } from "../../src/utils/epicBranchParent";

describe("epicBranchParent (#2377)", () => {
  it.each([
    ["no parent", "acme/app", undefined, undefined, undefined],
    ["a null parent", "acme/app", null, "acme/app", undefined],
    ["a parent in another repository", "acme/app", 20, "acme/platform", undefined],
    ["a parent in this repository", "acme/app", 20, "acme/app", 20],
    ["a parent in this repository, another case", "acme/app", 20, "Acme/App", 20],
    ["a parent whose repository is not recorded", "acme/app", 20, undefined, 20],
    ["a parent whose repository is empty", "acme/app", 20, "", 20],
    [
      "a parent elsewhere of a sub-issue with no repository",
      undefined,
      20,
      "acme/platform",
      undefined,
    ],
  ])("%s", (_name, subRepo, parentNumber, parentRepo, want) => {
    expect(epicBranchParent(subRepo, parentNumber, parentRepo)).toBe(want);
  });
});
