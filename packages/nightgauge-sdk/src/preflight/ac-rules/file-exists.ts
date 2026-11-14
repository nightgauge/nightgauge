import { access, readdir } from "node:fs/promises";
import path from "node:path";
import type { RuleEvaluator } from "../types.js";

const PATH_RE = /([A-Za-z0-9_./-]+\.(?:md|ts|tsx|js|jsx|json|sh|ya?ml|toml|go|py|rs))/;
const PRESENCE_VERB_RE = /\b(exists|created|present|added|new\s+file)\b/i;
// Clause boundaries: clause punctuation followed by whitespace or the end
// (never a bare `.`, so `scripts/x.sh` survives) and the subordinators that
// open a new clause. The presence verb must refer to the path, so both have
// to sit in one clause (#3269). Mirrors acClauseSplitRE in
// internal/preflight/ac_reconcile.go.
const CLAUSE_SPLIT_RE = /[.,;:!?](?:\s+|$)|\b(?:when|if|unless|while|whereas|but)\b/i;

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

/** The first path in the first clause holding both a presence verb and a path. */
function fileExistsPath(text: string): string | null {
  for (const clause of text.split(new RegExp(CLAUSE_SPLIT_RE, "gi"))) {
    if (!PRESENCE_VERB_RE.test(clause)) continue;
    const m = PATH_RE.exec(clause);
    if (m) return m[1];
  }
  return null;
}

/** Regular files named `base` under workdir, skipping EXCLUDED_DIRS, up to limit. */
async function findFilesByBasename(
  workdir: string,
  base: string,
  limit: number
): Promise<string[]> {
  const found: string[] = [];
  async function walk(dir: string): Promise<void> {
    let entries;
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (found.length >= limit) return;
      if (entry.isDirectory()) {
        if (EXCLUDED_DIRS.has(entry.name)) continue;
        await walk(path.join(dir, entry.name));
      } else if (entry.isFile() && entry.name === base) {
        found.push(path.relative(workdir, path.join(dir, entry.name)).split(path.sep).join("/"));
      }
    }
  }
  await walk(workdir);
  return found;
}

/**
 * Checks a file a criterion says exists. Only an exact path with a directory
 * component that is absent is unsatisfied; a bare basename is resolved by
 * searching the tree, and none or several matches leave the criterion
 * undetectable for the model's diff check (#3269).
 */
const fileExistsRule: RuleEvaluator = {
  name: "file-exists",

  applies(text: string) {
    const p = fileExistsPath(text);
    return p ? { path: p } : null;
  },

  async evaluate(ctx) {
    const target = ctx.extracted.path;
    if (target.includes("/")) {
      try {
        await access(path.resolve(ctx.workdir, target));
        return {
          classification: "satisfied",
          reason: `File present: ${target}`,
          evidence: [target],
        };
      } catch {
        return {
          classification: "unsatisfied",
          reason: `File not found: ${target}`,
          evidence: [],
        };
      }
    }
    const found = await findFilesByBasename(ctx.workdir, target, 2);
    if (found.length === 1) {
      return {
        classification: "satisfied",
        reason: `File present: ${found[0]}`,
        evidence: found,
      };
    }
    return {
      classification: "undetectable",
      reason:
        found.length === 0
          ? `No file named ${target} in the working tree`
          : `Several files named ${target} in the working tree`,
      evidence: [],
    };
  },
};

export default fileExistsRule;
