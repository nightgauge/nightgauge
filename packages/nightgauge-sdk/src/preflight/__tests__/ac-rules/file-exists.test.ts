import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import fileExistsRule from "../../ac-rules/file-exists.js";

describe("file-exists rule", () => {
  let workdir: string;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(os.tmpdir(), "ac-fileexists-"));
  });

  afterEach(async () => {
    await rm(workdir, { recursive: true, force: true });
  });

  describe("applies()", () => {
    it("matches when AC mentions a file path with a presence verb", () => {
      expect(fileExistsRule.applies("File `docs/README.md` exists")).toEqual({
        path: "docs/README.md",
      });
      expect(fileExistsRule.applies("New file packages/sdk/src/foo.ts created")).toEqual({
        path: "packages/sdk/src/foo.ts",
      });
    });

    it("returns null when no path is present", () => {
      expect(fileExistsRule.applies("the feature is added")).toBeNull();
    });

    it("returns null when no presence verb is present", () => {
      expect(fileExistsRule.applies("docs/README.md is fine")).toBeNull();
    });
  });

  describe("evaluate()", () => {
    it("classifies satisfied when file exists", async () => {
      const target = path.join(workdir, "docs", "README.md");
      await mkdir(path.dirname(target), { recursive: true });
      await writeFile(target, "hello", "utf-8");

      const r = await fileExistsRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { path: "docs/README.md" },
      });
      expect(r.classification).toBe("satisfied");
      expect(r.evidence).toContain("docs/README.md");
    });

    it("classifies unsatisfied when file is missing", async () => {
      const r = await fileExistsRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { path: "docs/MISSING.md" },
      });
      expect(r.classification).toBe("unsatisfied");
      expect(r.evidence).toEqual([]);
    });
  });
});
