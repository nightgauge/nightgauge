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
  isAbsoluteRoot,
  resolveCloneSetting,
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

  describe("isAbsoluteRoot", () => {
    it("accepts POSIX absolute roots and refuses relative ones", () => {
      expect(isAbsoluteRoot("/repo", path.posix)).toBe(true);
      expect(isAbsoluteRoot("repo", path.posix)).toBe(false);
      expect(isAbsoluteRoot("", path.posix)).toBe(false);
    });

    it("on win32 accepts drive and UNC roots", () => {
      expect(isAbsoluteRoot("C:\\repo", path.win32)).toBe(true);
      expect(isAbsoluteRoot("c:/repo", path.win32)).toBe(true);
      expect(isAbsoluteRoot("\\\\server\\share\\repo", path.win32)).toBe(true);
      expect(isAbsoluteRoot("//server/share", path.win32)).toBe(true);
    });

    it("on win32 refuses drive-relative and relative roots", () => {
      expect(isAbsoluteRoot("\\repo", path.win32)).toBe(false);
      expect(isAbsoluteRoot("/repo", path.win32)).toBe(false);
      expect(isAbsoluteRoot("C:repo", path.win32)).toBe(false);
      expect(isAbsoluteRoot("repo\\sub", path.win32)).toBe(false);
      expect(isAbsoluteRoot("\\\\server", path.win32)).toBe(false);
    });

    it("uses the host path by default", () => {
      expect(isAbsoluteRoot(root)).toBe(true);
    });
  });

  describe("resolveCloneSetting", () => {
    const resolver = (r: string) => `resolved:${r}`;

    it("resolves an unset or empty value through the resolver", () => {
      expect(resolveCloneSetting(root, undefined, RELATIVE_PIPELINE_STATE_DIR, resolver)).toBe(
        `resolved:${root}`
      );
      expect(resolveCloneSetting(root, null, RELATIVE_PIPELINE_STATE_DIR, resolver)).toBe(
        `resolved:${root}`
      );
      expect(resolveCloneSetting(root, "  ", RELATIVE_PIPELINE_STATE_DIR, resolver)).toBe(
        `resolved:${root}`
      );
    });

    it("resolves the default value, however spelled, through the resolver", () => {
      for (const value of [
        ".nightgauge/pipeline",
        "./.nightgauge/pipeline",
        ".nightgauge/pipeline/",
        ".nightgauge\\pipeline",
      ]) {
        expect(resolveCloneSetting(root, value, RELATIVE_PIPELINE_STATE_DIR, resolver)).toBe(
          `resolved:${root}`
        );
      }
    });

    it("joins a user override onto the root as before", () => {
      expect(
        resolveCloneSetting(root, "custom/ctx", RELATIVE_PIPELINE_STATE_DIR, pipelineStateDir)
      ).toBe(path.join(root, "custom/ctx"));
      expect(
        resolveCloneSetting(
          root,
          "custom/ctx",
          RELATIVE_PIPELINE_STATE_DIR,
          pipelineStateDir,
          (r, v) => `${r}/${v}`
        )
      ).toBe(`${root}/custom/ctx`);
    });

    it("the default resolves to the helper's path", () => {
      expect(
        resolveCloneSetting(root, RELATIVE_CLONE_LOGS_DIR, RELATIVE_CLONE_LOGS_DIR, cloneLogsDir)
      ).toBe(path.join(root, ".nightgauge", "logs"));
    });

    it("rejects an unusable root for defaults and overrides alike", () => {
      expect(() =>
        resolveCloneSetting("", undefined, RELATIVE_PIPELINE_STATE_DIR, pipelineStateDir)
      ).toThrow(/empty/);
      expect(() =>
        resolveCloneSetting("rel", "custom/ctx", RELATIVE_PIPELINE_STATE_DIR, pipelineStateDir)
      ).toThrow(/relative/);
    });
  });
});
