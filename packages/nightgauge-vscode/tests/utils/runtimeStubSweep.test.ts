/**
 * runtimeStubSweep.test.ts
 *
 * #307 — startup restore must ignore AND delete cross-contaminated runtime
 * stubs: files with empty repo/stage (the "initialized" snapshot a concurrent
 * multi-repo run stranded in the launch repo), or whose repo field points at a
 * different repo than the one containing them.
 */

import { describe, it, expect } from "vitest";
import { classifyRuntimeStub, repoSlugsMatch } from "../../src/utils/runtimeStubSweep";

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
