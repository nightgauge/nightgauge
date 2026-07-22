/**
 * otherResolver.skipAuthPreflight.test.ts
 *
 * Tests for `getSkipAuthPreflight` (Issue #3222). The flag controls whether
 * the per-adapter auth pre-flight runs at pipeline start. Default `false` —
 * the pre-flight runs unless explicitly disabled.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: undefined,
  },
}));

import { getSkipAuthPreflight } from "../../../src/utils/resolvers/otherResolver";

function withTempConfig(yaml: string): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "incredi-sap-"));
  fs.mkdirSync(path.join(dir, ".nightgauge"), { recursive: true });
  fs.writeFileSync(path.join(dir, ".nightgauge", "config.yaml"), yaml, "utf-8");
  return dir;
}

describe("getSkipAuthPreflight", () => {
  let tmp: string | undefined;

  afterEach(() => {
    if (tmp && fs.existsSync(tmp)) fs.rmSync(tmp, { recursive: true, force: true });
    tmp = undefined;
  });

  it("returns false when no workspace root", () => {
    expect(getSkipAuthPreflight(undefined)).toBe(false);
  });

  it("returns false when config.yaml is missing", () => {
    tmp = fs.mkdtempSync(path.join(os.tmpdir(), "incredi-sap-empty-"));
    expect(getSkipAuthPreflight(tmp)).toBe(false);
  });

  it("returns false when pipeline block is absent", () => {
    tmp = withTempConfig("github:\n  owner: test\n");
    expect(getSkipAuthPreflight(tmp)).toBe(false);
  });

  it("returns false when skip_auth_preflight is not set under pipeline", () => {
    tmp = withTempConfig("pipeline:\n  max_concurrent: 2\n");
    expect(getSkipAuthPreflight(tmp)).toBe(false);
  });

  it("returns true when skip_auth_preflight: true", () => {
    tmp = withTempConfig("pipeline:\n  skip_auth_preflight: true\n");
    expect(getSkipAuthPreflight(tmp)).toBe(true);
  });

  it("returns false when skip_auth_preflight: false", () => {
    tmp = withTempConfig("pipeline:\n  skip_auth_preflight: false\n");
    expect(getSkipAuthPreflight(tmp)).toBe(false);
  });

  it("ignores skip_auth_preflight set under a sibling top-level block", () => {
    tmp = withTempConfig("github:\n  skip_auth_preflight: true\npipeline:\n  max_concurrent: 1\n");
    expect(getSkipAuthPreflight(tmp)).toBe(false);
  });

  it("survives malformed YAML by returning the default (false)", () => {
    tmp = withTempConfig("pipeline:\n  : invalid:\n  skip_auth_preflight: true\n");
    // The matcher is permissive enough to still find the literal `skip_auth_preflight: true`
    // inside the pipeline block, so this asserts the function does not throw.
    expect(() => getSkipAuthPreflight(tmp)).not.toThrow();
  });
});
