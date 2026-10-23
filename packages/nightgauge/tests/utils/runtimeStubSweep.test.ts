/**
 * runtimeStubSweep.test.ts
 *
 * #307 — startup restore must ignore AND delete cross-contaminated runtime
 * stubs: files with empty repo/stage (the "initialized" snapshot a concurrent
 * multi-repo run stranded in the launch repo), or whose repo field points at a
 * different repo than the one containing them.
 */

import { describe, it, expect, vi } from "vitest";
import {
  ANY_RUNTIME_FILE,
  LEGACY_RUNTIME_FILE,
  classifyRuntimeStub,
  repoSlugsMatch,
  runtimeSweepVerdict,
} from "../../src/utils/runtimeStubSweep";

describe("classifyRuntimeStub (#307)", () => {
  it("deletes the empty repo/stage 'initialized' stub", () => {
    // Exactly the incident signature: runtime-304.json with repo/stage "".
    expect(classifyRuntimeStub({ repo: "", stage: "", issueNumber: 304 })).toEqual({
      action: "delete",
      reason: "empty-identity",
    });
  });

  it("deletes a stub with an empty stage even if repo is present", () => {
    expect(classifyRuntimeStub({ repo: "acme/platform", stage: "", issueNumber: 209 })).toEqual({
      action: "delete",
      reason: "empty-identity",
    });
  });

  it("deletes a stub with an empty repo even if stage is present", () => {
    expect(classifyRuntimeStub({ repo: "", stage: "feature-dev", issueNumber: 209 })).toEqual({
      action: "delete",
      reason: "empty-identity",
    });
  });

  it("treats null repo/stage as empty identity", () => {
    expect(classifyRuntimeStub({ repo: null, stage: null, issueNumber: 1 })).toEqual({
      action: "delete",
      reason: "empty-identity",
    });
  });

  it("deletes a stub whose repo does not match the containing repo", () => {
    // runtime-304.json for acme/flutter sitting in acme/infra's pipeline dir.
    expect(
      classifyRuntimeStub(
        { repo: "acme/flutter", stage: "feature-dev", issueNumber: 304 },
        "acme/infra"
      )
    ).toEqual({ action: "delete", reason: "repo-mismatch" });
  });

  it("keeps a stub whose repo matches the containing repo", () => {
    expect(
      classifyRuntimeStub(
        { repo: "acme/platform", stage: "feature-dev", issueNumber: 209, paused: true },
        "acme/platform"
      )
    ).toEqual({ action: "keep" });
  });

  it("keeps a well-formed stub when the containing repo cannot be resolved", () => {
    expect(
      classifyRuntimeStub({ repo: "acme/platform", stage: "pr-create", issueNumber: 209 })
    ).toEqual({ action: "keep" });
  });

  it("tolerates owner/repo vs short-name form of the containing repo", () => {
    expect(
      classifyRuntimeStub(
        { repo: "acme/platform", stage: "feature-dev", issueNumber: 209 },
        "platform"
      )
    ).toEqual({ action: "keep" });
  });
});

describe("repoSlugsMatch", () => {
  it("matches identical slugs case-insensitively", () => {
    expect(repoSlugsMatch("Acme/Platform", "acme/platform")).toBe(true);
  });

  it("matches owner/repo against its short name", () => {
    expect(repoSlugsMatch("acme/platform", "platform")).toBe(true);
    expect(repoSlugsMatch("platform", "acme/platform")).toBe(true);
  });

  it("does not match genuinely different repos", () => {
    expect(repoSlugsMatch("acme/flutter", "acme/infra")).toBe(false);
  });
});

/**
 * ADR-017 #370 step 1 — the sweep's NAME gate.
 *
 * `runtimeSweepVerdict` is what stands between `classifyRuntimeStub` and an
 * `fs.unlink` at extension activation. Before this suite existed the gate was
 * an inline ternary in `bootstrap/services.ts` with nothing reaching it: the
 * obvious future cleanup — collapsing the two name patterns back into one —
 * would have run the classifier over run-identity-keyed snapshots, so a LIVE
 * run whose mid-dispatch body still reads `repo: ""` / `stage: ""` would be
 * classified `empty-identity` and DELETED, destroying the crash snapshot and
 * leaving the run unreconcilable, with the whole 364-file suite green.
 */
describe("runtimeSweepVerdict (ADR-017 #370)", () => {
  const liveMidDispatchBody = { repo: "", stage: "", issueNumber: 370 };

  it("KEEPS a new-scheme snapshot regardless of its body, and never classifies it", () => {
    const classify = vi.fn(() => classifyRuntimeStub(liveMidDispatchBody, "acme/platform"));
    const verdict = runtimeSweepVerdict(
      "runtime-370-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json",
      classify
    );

    expect(verdict.action).toBe("keep");
    // Not merely "the verdict was keep": the classifier must not even RUN, so
    // no future change to its rules can reach a run-identity-keyed file.
    expect(classify).not.toHaveBeenCalled();

    // The body proves the case is adversarial: classified directly, it deletes.
    expect(classifyRuntimeStub(liveMidDispatchBody, "acme/platform")).toEqual({
      action: "delete",
      reason: "empty-identity",
    });
  });

  it("applies the classifier's verdict to a LEGACY name — delete", () => {
    expect(
      runtimeSweepVerdict("runtime-304.json", () =>
        classifyRuntimeStub({ repo: "", stage: "", issueNumber: 304 }, "acme/platform")
      )
    ).toEqual({ action: "delete", reason: "empty-identity" });
  });

  it("applies the classifier's verdict to a LEGACY name — keep", () => {
    expect(
      runtimeSweepVerdict("runtime-209.json", () =>
        classifyRuntimeStub(
          { repo: "acme/platform", stage: "feature-dev", issueNumber: 209 },
          "acme/platform"
        )
      )
    ).toEqual({ action: "keep" });
  });

  it("keeps anything that is not a runtime snapshot at all", () => {
    const classify = vi.fn(() => ({
      action: "delete" as const,
      reason: "empty-identity" as const,
    }));
    expect(runtimeSweepVerdict("current-run.json", classify).action).toBe("keep");
    expect(classify).not.toHaveBeenCalled();
  });
});

describe("runtime snapshot name patterns (ADR-017 #370)", () => {
  const newScheme = "runtime-370-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json";
  const legacy = "runtime-370.json";

  it("LEGACY_RUNTIME_FILE — the only name the destructive sweep may act on", () => {
    expect(LEGACY_RUNTIME_FILE.test(legacy)).toBe(true);
    expect(LEGACY_RUNTIME_FILE.test(newScheme)).toBe(false);
  });

  it("ANY_RUNTIME_FILE — what the non-destructive pause-restore READ accepts", () => {
    expect(ANY_RUNTIME_FILE.test(legacy)).toBe(true);
    expect(ANY_RUNTIME_FILE.test(newScheme)).toBe(true);
    // Issue number is capture 1 in both, which is what lets one parse serve both.
    expect(newScheme.match(ANY_RUNTIME_FILE)?.[1]).toBe("370");
    expect(legacy.match(ANY_RUNTIME_FILE)?.[1]).toBe("370");
  });

  it("rejects non-canonical identities and neighbouring artifacts", () => {
    for (const name of [
      "runtime-370-3f2504e0-4f89-41d3-9a0c-0305e82c3301.json", // UUIDv4
      "runtime-370-019FE6F0-14DA-7470-93CF-4DFC9E88E1E8.json", // uppercase
      "runtime-370-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json.tmp", // mid-rename
      "resuming-370-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.019fe6f0-14da-7470-93cf-4dfc9e88e1e9.json",
      "current-run.json",
    ]) {
      expect(ANY_RUNTIME_FILE.test(name), name).toBe(false);
      expect(LEGACY_RUNTIME_FILE.test(name), name).toBe(false);
    }
  });
});
