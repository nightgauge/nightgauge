import { readdir, readFile, stat } from "node:fs/promises";
import path from "node:path";
import type { RuleEvaluator, RuleContext } from "../types.js";

const DECL_RE = /\b(?:function|class|const|interface|type|method)\s+([A-Za-z_][A-Za-z0-9_]*)/i;
const BACKTICK_RE = /`([A-Za-z_][A-Za-z0-9_]{2,})`/;
const VERB_RE = /\b(added|implemented|exported|defined|introduced)\b/i;

const EXCLUDED_DIRS = new Set([
  "node_modules",
  "dist",
  "build",
  "out",
  ".git",
  "coverage",
  ".nightgauge",
  ".next",
  ".turbo",
]);
const SOURCE_EXTS = new Set([".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".go", ".py", ".rs"]);

async function* walk(dir: string): AsyncGenerator<string> {
  let entries;
  try {
    entries = await readdir(dir, { withFileTypes: true });
  } catch {
    return;
  }
  for (const entry of entries) {
    if (entry.isDirectory()) {
      if (EXCLUDED_DIRS.has(entry.name)) continue;
      yield* walk(path.join(dir, entry.name));
    } else if (entry.isFile()) {
      const ext = path.extname(entry.name);
      if (SOURCE_EXTS.has(ext)) {
        yield path.join(dir, entry.name);
      }
    }
  }
}

async function findSymbolReferences(
  workdir: string,
  symbol: string,
  limit: number
): Promise<string[]> {
  const symbolRe = new RegExp(`\\b${symbol.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\b`);
  const results: string[] = [];
  for await (const file of walk(workdir)) {
    let info;
    try {
      info = await stat(file);
    } catch {
      continue;
    }
    // Skip very large files (>1MB) — these are unlikely sources.
    if (info.size > 1024 * 1024) continue;
    let content: string;
    try {
      content = await readFile(file, "utf-8");
    } catch {
      continue;
    }
    if (symbolRe.test(content)) {
      results.push(path.relative(workdir, file));
      if (results.length >= limit) break;
    }
  }
  return results;
}

const grepForSymbolRule: RuleEvaluator = {
  name: "grep-for-symbol",

  applies(text: string) {
    if (!VERB_RE.test(text)) return null;
    const decl = DECL_RE.exec(text);
    if (decl) return { symbol: decl[1] };
    const tick = BACKTICK_RE.exec(text);
    if (tick) return { symbol: tick[1] };
    return null;
  },

  async evaluate(ctx: RuleContext) {
    const symbol = ctx.extracted.symbol;
    const refs = await findSymbolReferences(ctx.workdir, symbol, 5);
    if (refs.length === 0) {
      return {
        classification: "unsatisfied",
        reason: `Symbol \`${symbol}\` not found in workspace`,
        evidence: [],
      };
    }
    return {
      classification: "satisfied",
      reason: `Symbol \`${symbol}\` found in ${refs.length} file(s)`,
      evidence: refs,
    };
  },
};

export default grepForSymbolRule;
