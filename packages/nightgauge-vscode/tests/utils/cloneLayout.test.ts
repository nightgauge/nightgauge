/**
 * Unit tests for the clone layout helpers (#2036).
 *
 * Today each class lives at `<root>/.nightgauge/<class>`. The flip issue
 * (#2037) that moves the location edits this test.
 */

import * as path from "path";
import { describe, it, expect } from "vitest";
import {
  pipelineStateDir,
  plansDir,
  retrosDir,
  cloneLogsDir,
  isUsableWorkspaceRoot,
  RELATIVE_PIPELINE_STATE_DIR,
  RELATIVE_PLANS_DIR,
  RELATIVE_RETROS_DIR,
  RELATIVE_CLONE_LOGS_DIR,
} from "../../src/utils/cloneLayout";

const root = path.resolve("/tmp/ng-clone-layout-root");

const helpers = [
  { name: "pipelineStateDir", fn: pipelineStateDir, cls: "pipeline" },
  { name: "plansDir", fn: plansDir, cls: "plans" },
  { name: "retrosDir", fn: retrosDir, cls: "retros" },
  { name: "cloneLogsDir", fn: cloneLogsDir, cls: "logs" },
] as const;

describe("cloneLayout", () => {
  for (const { name, fn, cls } of helpers) {
    describe(name, () => {
      it(`returns <root>/.nightgauge/${cls}`, () => {
        expect(fn(root)).toBe(path.join(root, ".nightgauge", cls));
      });

      it("matches the hand-built path it replaces", () => {
        expect(fn("/workspace")).toBe(path.join("/workspace", ".nightgauge", cls));
      });

      it("rejects an empty root", () => {
        expect(() => fn("")).toThrow(/empty/);
        expect(() => fn("   ")).toThrow(/empty/);
      });

      it("rejects a relative root", () => {
        expect(() => fn("relative/repo")).toThrow(/relative/);
        expect(() => fn(".")).toThrow(/relative/);
      });

      it("rejects a non-string root", () => {
        expect(() => fn(undefined as unknown as string)).toThrow(/empty/);
      });
    });
  }

  it("exposes the root-relative spellings used in messages and defaults", () => {
    expect(RELATIVE_PIPELINE_STATE_DIR).toBe(".nightgauge/pipeline");
    expect(RELATIVE_PLANS_DIR).toBe(".nightgauge/plans");
    expect(RELATIVE_RETROS_DIR).toBe(".nightgauge/retros");
    expect(RELATIVE_CLONE_LOGS_DIR).toBe(".nightgauge/logs");
  });

  it("isUsableWorkspaceRoot accepts only a non-empty absolute path", () => {
    expect(isUsableWorkspaceRoot(root)).toBe(true);
    expect(isUsableWorkspaceRoot("")).toBe(false);
    expect(isUsableWorkspaceRoot(undefined)).toBe(false);
    expect(isUsableWorkspaceRoot(null)).toBe(false);
    expect(isUsableWorkspaceRoot("relative/repo")).toBe(false);
  });
});
