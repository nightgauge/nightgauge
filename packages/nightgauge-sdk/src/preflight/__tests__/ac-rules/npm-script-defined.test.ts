import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import npmScriptDefinedRule from "../../ac-rules/npm-script-defined.js";

describe("npm-script-defined rule", () => {
  let workdir: string;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(os.tmpdir(), "ac-npmscript-"));
  });

  afterEach(async () => {
    await rm(workdir, { recursive: true, force: true });
  });

  describe("applies()", () => {
    it("matches `npm run X` in backticks", () => {
      expect(npmScriptDefinedRule.applies("`npm run build` succeeds")).toEqual({
        script: "build",
      });
    });

    it("matches plain `npm run X`", () => {
      expect(npmScriptDefinedRule.applies("Run npm run lint to check")).toEqual({
        script: "lint",
      });
    });

    it("matches script `X` exists", () => {
      expect(npmScriptDefinedRule.applies("script `format:check` exists in package.json")).toEqual({
        script: "format:check",
      });
    });

    it("returns null when no script reference is found", () => {
      expect(npmScriptDefinedRule.applies("just a generic ac")).toBeNull();
    });
  });

  describe("evaluate()", () => {
    it("classifies satisfied when script defined in package.json", async () => {
      await writeFile(
        path.join(workdir, "package.json"),
        JSON.stringify({ scripts: { build: "tsc" } }),
        "utf-8"
      );
      const r = await npmScriptDefinedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { script: "build" },
      });
      expect(r.classification).toBe("satisfied");
      expect(r.evidence).toContain("package.json");
    });

    it("classifies satisfied when script defined in nested workspace", async () => {
      const nested = path.join(workdir, "packages", "foo");
      await mkdir(nested, { recursive: true });
      await writeFile(
        path.join(nested, "package.json"),
        JSON.stringify({ scripts: { test: "vitest" } }),
        "utf-8"
      );
      const r = await npmScriptDefinedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { script: "test" },
      });
      expect(r.classification).toBe("satisfied");
    });

    it("classifies unsatisfied when no package.json defines the script", async () => {
      await writeFile(
        path.join(workdir, "package.json"),
        JSON.stringify({ scripts: { other: "echo" } }),
        "utf-8"
      );
      const r = await npmScriptDefinedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { script: "build" },
      });
      expect(r.classification).toBe("unsatisfied");
    });

    it("tolerates malformed package.json files", async () => {
      await writeFile(path.join(workdir, "package.json"), "{ not json", "utf-8");
      const r = await npmScriptDefinedRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { script: "build" },
      });
      expect(r.classification).toBe("unsatisfied");
    });
  });
});
