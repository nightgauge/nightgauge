import { describe, it, expect } from "vitest";
import {
  isDependabotIssue,
  getDependabotType,
  getDependencyPackageInfo,
} from "../../src/utils/dependabotUtils";

describe("isDependabotIssue", () => {
  it('returns true for issues with "dependencies" label', () => {
    expect(isDependabotIssue(["dependencies"])).toBe(true);
  });

  it('returns true for issues with "security" label', () => {
    expect(isDependabotIssue(["security"])).toBe(true);
  });

  it("returns true for issues with language labels", () => {
    expect(isDependabotIssue(["go"])).toBe(true);
    expect(isDependabotIssue(["javascript"])).toBe(true);
    expect(isDependabotIssue(["python"])).toBe(true);
    expect(isDependabotIssue(["rust"])).toBe(true);
  });

  it("returns true when Dependabot label mixed with other labels", () => {
    expect(isDependabotIssue(["type:chore", "dependencies", "priority:medium"])).toBe(true);
  });

  it("returns false for regular issue labels", () => {
    expect(isDependabotIssue(["type:feature", "priority:high", "size:M"])).toBe(false);
  });

  it("returns false for empty label array", () => {
    expect(isDependabotIssue([])).toBe(false);
  });
});

describe("getDependabotType", () => {
  it('returns "security" for issues with "security" label', () => {
    expect(getDependabotType(["security"])).toBe("security");
  });

  it('returns "security" even when "dependencies" is also present', () => {
    expect(getDependabotType(["dependencies", "security"])).toBe("security");
  });

  it('returns "dependency" for go label without security', () => {
    expect(getDependabotType(["go", "dependencies"])).toBe("dependency");
  });

  it('returns "dependency" for dependencies-only label', () => {
    expect(getDependabotType(["dependencies"])).toBe("dependency");
  });

  it("returns null for non-Dependabot issues", () => {
    expect(getDependabotType(["type:feature", "priority:high"])).toBeNull();
  });

  it("returns null for empty array", () => {
    expect(getDependabotType([])).toBeNull();
  });
});

describe("getDependencyPackageInfo", () => {
  it('parses standard Dependabot "Bump X from Y to Z" format', () => {
    const result = getDependencyPackageInfo("Bump lodash from 4.17.20 to 4.17.21");
    expect(result).toEqual({ name: "lodash", from: "4.17.20", to: "4.17.21" });
  });

  it("parses case-insensitive bump format", () => {
    const result = getDependencyPackageInfo("bump @types/node from 18.0.0 to 20.0.0");
    expect(result).toEqual({
      name: "@types/node",
      from: "18.0.0",
      to: "20.0.0",
    });
  });

  it("parses conventional commit prefix format", () => {
    const result = getDependencyPackageInfo("build(deps): bump lodash from 4.17.20 to 4.17.21");
    expect(result).toEqual({ name: "lodash", from: "4.17.20", to: "4.17.21" });
  });

  it('parses "update X from Y to Z" format', () => {
    const result = getDependencyPackageInfo("Update express from 4.18.0 to 4.19.0");
    expect(result).toEqual({ name: "express", from: "4.18.0", to: "4.19.0" });
  });

  it("supports pre-release version suffixes", () => {
    const result = getDependencyPackageInfo("Bump react from 18.0.0-rc.1 to 18.0.0");
    expect(result).toEqual({
      name: "react",
      from: "18.0.0-rc.1",
      to: "18.0.0",
    });
  });

  it("returns null for non-matching titles", () => {
    expect(getDependencyPackageInfo("Add new feature for auth")).toBeNull();
  });

  it("returns null for empty title", () => {
    expect(getDependencyPackageInfo("")).toBeNull();
  });
});
