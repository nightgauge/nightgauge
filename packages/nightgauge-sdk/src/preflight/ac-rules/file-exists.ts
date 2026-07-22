import { access } from "node:fs/promises";
import path from "node:path";
import type { RuleEvaluator } from "../types.js";

const PATH_RE = /([A-Za-z0-9_./-]+\.(?:md|ts|tsx|js|jsx|json|sh|ya?ml|toml|go|py|rs))/;
const PRESENCE_VERB_RE = /\b(exists|created|present|added|new\s+file)\b/i;

const fileExistsRule: RuleEvaluator = {
  name: "file-exists",

  applies(text: string) {
    if (!PRESENCE_VERB_RE.test(text)) return null;
    const m = PATH_RE.exec(text);
    if (!m) return null;
    return { path: m[1] };
  },

  async evaluate(ctx) {
    const target = path.resolve(ctx.workdir, ctx.extracted.path);
    try {
      await access(target);
      return {
        classification: "satisfied",
        reason: `File present: ${ctx.extracted.path}`,
        evidence: [ctx.extracted.path],
      };
    } catch {
      return {
        classification: "unsatisfied",
        reason: `File not found: ${ctx.extracted.path}`,
        evidence: [],
      };
    }
  },
};

export default fileExistsRule;
