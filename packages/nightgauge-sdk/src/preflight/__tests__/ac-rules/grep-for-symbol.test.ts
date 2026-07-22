import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import grepForSymbolRule from "../../ac-rules/grep-for-symbol.js";

describe("grep-for-symbol rule", () => {
  let workdir: string;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(os.tmpdir(), "ac-grep-"));
  });

  afterEach(async () => {
    await rm(workdir, { recursive: true, force: true });
  });

  describe("applies()", () => {
    it("matches a function declaration with a verb", () => {
      expect(grepForSymbolRule.applies("Function reconcileAcceptanceCriteria added")).toEqual({
        symbol: "reconcileAcceptanceCriteria",
      });
    });

    it("matches a backtick-quoted symbol with a verb", () => {
      expect(grepForSymbolRule.applies("`MyClass` implemented in SDK")).toEqual({
        symbol: "MyClass",
      });
    });

    it("returns null when no verb is present", () => {
      expect(grepForSymbolRule.applies("`MyClass` lives in SDK")).toBeNull();
    });

    it("returns null when no symbol is present", () => {
      expect(grepForSymbolRule.applies("something was added")).toBeNull();
    });
  });

  describe("evaluate()", () => {
    it("classifies satisfied when symbol appears in source", async () => {
      const file = path.join(workdir, "src", "foo.ts");
      await mkdir(path.dirname(file), { recursive: true });
      await writeFile(file, "export function myThing() { return 1; }", "utf-8");

      const r = await grepForSymbolRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { symbol: "myThing" },
      });
      expect(r.classification).toBe("satisfied");
      expect(r.evidence?.length).toBeGreaterThan(0);
    });

    it("classifies unsatisfied when symbol is absent", async () => {
      await mkdir(path.join(workdir, "src"), { recursive: true });
      await writeFile(path.join(workdir, "src", "other.ts"), "const x = 1;", "utf-8");

      const r = await grepForSymbolRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { symbol: "doesNotExist" },
      });
      expect(r.classification).toBe("unsatisfied");
    });

    it("ignores excluded directories", async () => {
      const nm = path.join(workdir, "node_modules", "pkg", "dist.ts");
      await mkdir(path.dirname(nm), { recursive: true });
      await writeFile(nm, "export function trapped() {}", "utf-8");

      const r = await grepForSymbolRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { symbol: "trapped" },
      });
      expect(r.classification).toBe("unsatisfied");
    });
  });
});
