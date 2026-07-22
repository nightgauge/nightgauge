import { readFile } from "node:fs/promises";
import path from "node:path";
import type { RuleEvaluator } from "../types.js";

const DOC_RE = /(docs\/[A-Za-z0-9_./-]+\.md|[A-Za-z0-9_/-]+\.md)/;
const SECTION_RE = /(?:section|heading|chapter)\s+`([^`]+)`|##+\s+([A-Za-z0-9 _-]{2,})/;

const docSectionPresentRule: RuleEvaluator = {
  name: "doc-section-present",

  applies(text: string) {
    if (!/\b(documented|documentation)\b/i.test(text)) return null;
    const doc = DOC_RE.exec(text);
    if (!doc) return null;
    const sec = SECTION_RE.exec(text);
    if (!sec) return null;
    const section = (sec[1] ?? sec[2] ?? "").trim();
    if (!section) return null;
    return { doc: doc[1], section };
  },

  async evaluate(ctx) {
    const docPath = path.resolve(ctx.workdir, ctx.extracted.doc);
    let content: string;
    try {
      content = await readFile(docPath, "utf-8");
    } catch {
      return {
        classification: "unsatisfied",
        reason: `Doc file not found: ${ctx.extracted.doc}`,
        evidence: [],
      };
    }
    const sectionRe = new RegExp(
      `^#+\\s+${ctx.extracted.section.replace(/[.*+?^${}()|[\\]\\\\]/g, "\\$&")}\\s*$`,
      "im"
    );
    if (sectionRe.test(content)) {
      return {
        classification: "satisfied",
        reason: `Section \`${ctx.extracted.section}\` present in ${ctx.extracted.doc}`,
        evidence: [ctx.extracted.doc],
      };
    }
    return {
      classification: "unsatisfied",
      reason: `Section \`${ctx.extracted.section}\` not found in ${ctx.extracted.doc}`,
      evidence: [],
    };
  },
};

export default docSectionPresentRule;
