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

    it("does not apply when the verb and the path are in different clauses (#3269)", () => {
      const quoted =
        "When `node_modules` is missing at the repository root, " +
        "`clean-install-e2e.sh` runs `npm ci` as a named step before it packages. " +
        "When it is present, the script does not reinstall. If `npm ci` fails, " +
        "the script stops with a named error and npm's output.";
      expect(fileExistsRule.applies(quoted)).toBeNull();
    });

    it("applies when the verb refers to the path in the same clause", () => {
      expect(fileExistsRule.applies("`scripts/x.sh` exists")).toEqual({ path: "scripts/x.sh" });
      expect(fileExistsRule.applies("adds a new file `docs/y.md`")).toEqual({ path: "docs/y.md" });
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

    const ac = { index: 0, text: "", checkbox_state: "unchecked" as const };

    it("resolves a bare basename found under scripts/ as satisfied", async () => {
      await mkdir(path.join(workdir, "scripts"), { recursive: true });
      await writeFile(path.join(workdir, "scripts", "clean-install-e2e.sh"), "#!/bin/sh", "utf-8");
      await mkdir(path.join(workdir, "node_modules", "x"), { recursive: true });
      await writeFile(path.join(workdir, "node_modules", "x", "clean-install-e2e.sh"), "", "utf-8");
      const r = await fileExistsRule.evaluate({
        workdir,
        ac,
        extracted: { path: "clean-install-e2e.sh" },
      });
      expect(r.classification).toBe("satisfied");
      expect(r.evidence).toEqual(["scripts/clean-install-e2e.sh"]);
    });

    it("leaves a bare basename matching two files undetectable", async () => {
      for (const d of ["scripts", "tools"]) {
        await mkdir(path.join(workdir, d), { recursive: true });
        await writeFile(path.join(workdir, d, "run.sh"), "x", "utf-8");
      }
      const r = await fileExistsRule.evaluate({ workdir, ac, extracted: { path: "run.sh" } });
      expect(r.classification).toBe("undetectable");
    });

    it("leaves a bare basename with no match undetectable", async () => {
      const r = await fileExistsRule.evaluate({ workdir, ac, extracted: { path: "absent.sh" } });
      expect(r.classification).toBe("undetectable");
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
