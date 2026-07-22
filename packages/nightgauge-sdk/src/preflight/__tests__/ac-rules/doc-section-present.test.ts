import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { mkdtemp, rm, writeFile, mkdir } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import docSectionPresentRule from "../../ac-rules/doc-section-present.js";

describe("doc-section-present rule", () => {
  let workdir: string;

  beforeEach(async () => {
    workdir = await mkdtemp(path.join(os.tmpdir(), "ac-docsec-"));
  });

  afterEach(async () => {
    await rm(workdir, { recursive: true, force: true });
  });

  describe("applies()", () => {
    it("matches when AC mentions a doc and a section", () => {
      expect(
        docSectionPresentRule.applies(
          "Documented in `docs/CONTEXT_ARCHITECTURE.md` section `Schema Versioning`"
        )
      ).toEqual({ doc: "docs/CONTEXT_ARCHITECTURE.md", section: "Schema Versioning" });
    });

    it("returns null when no documentation keyword is present", () => {
      expect(docSectionPresentRule.applies("docs/whatever.md section `Foo`")).toBeNull();
    });

    it("returns null when no doc reference is present", () => {
      expect(docSectionPresentRule.applies("Documented in section `Foo`")).toBeNull();
    });
  });

  describe("evaluate()", () => {
    it("classifies satisfied when section heading exists", async () => {
      const docPath = path.join(workdir, "docs", "FAILURE_TAXONOMY.md");
      await mkdir(path.dirname(docPath), { recursive: true });
      await writeFile(
        docPath,
        "# Failure Taxonomy\n\n## Categories\n\n### infrastructure\n\nText.\n",
        "utf-8"
      );
      const r = await docSectionPresentRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { doc: "docs/FAILURE_TAXONOMY.md", section: "infrastructure" },
      });
      expect(r.classification).toBe("satisfied");
      expect(r.evidence).toContain("docs/FAILURE_TAXONOMY.md");
    });

    it("classifies unsatisfied when section is missing", async () => {
      const docPath = path.join(workdir, "docs", "FOO.md");
      await mkdir(path.dirname(docPath), { recursive: true });
      await writeFile(docPath, "# Foo\n\nNo Categories here.", "utf-8");
      const r = await docSectionPresentRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { doc: "docs/FOO.md", section: "Categories" },
      });
      expect(r.classification).toBe("unsatisfied");
    });

    it("classifies unsatisfied when doc is missing", async () => {
      const r = await docSectionPresentRule.evaluate({
        workdir,
        ac: { index: 0, text: "", checkbox_state: "unchecked" },
        extracted: { doc: "docs/MISSING.md", section: "Whatever" },
      });
      expect(r.classification).toBe("unsatisfied");
    });
  });
});
