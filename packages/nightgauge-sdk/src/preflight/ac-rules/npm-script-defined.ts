import { readdir, readFile, stat } from "node:fs/promises";
import path from "node:path";
import type { RuleEvaluator } from "../types.js";

const NPM_RUN_RE =
  /(?:`npm\s+run\s+([A-Za-z0-9:_-]+)`|\bnpm\s+run\s+([A-Za-z0-9:_-]+)\b|script\s+`([A-Za-z0-9:_-]+)`)/;

const EXCLUDED_DIRS = new Set([
  "node_modules",
  "dist",
  "build",
  ".git",
  "coverage",
  ".nightgauge",
  ".next",
  ".turbo",
]);

async function findPackageJsonScripts(workdir: string, scriptName: string): Promise<string[]> {
  const evidence: string[] = [];
  async function walk(dir: string): Promise<void> {
    let entries;
    try {
      entries = await readdir(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (entry.isDirectory()) {
        if (EXCLUDED_DIRS.has(entry.name)) continue;
        await walk(path.join(dir, entry.name));
      } else if (entry.isFile() && entry.name === "package.json") {
        const fullPath = path.join(dir, entry.name);
        try {
          const info = await stat(fullPath);
          if (info.size > 5 * 1024 * 1024) continue;
          const raw = await readFile(fullPath, "utf-8");
          const pkg = JSON.parse(raw) as { scripts?: Record<string, string> };
          if (pkg && pkg.scripts && typeof pkg.scripts[scriptName] === "string") {
            evidence.push(path.relative(workdir, fullPath));
          }
        } catch {
          // ignore unreadable / invalid package.json
        }
      }
    }
  }
  await walk(workdir);
  return evidence;
}

const npmScriptDefinedRule: RuleEvaluator = {
  name: "npm-script-defined",

  applies(text: string) {
    const m = NPM_RUN_RE.exec(text);
    if (!m) return null;
    const name = m[1] ?? m[2] ?? m[3];
    if (!name) return null;
    return { script: name };
  },

  async evaluate(ctx) {
    const script = ctx.extracted.script;
    const matches = await findPackageJsonScripts(ctx.workdir, script);
    if (matches.length === 0) {
      return {
        classification: "unsatisfied",
        reason: `npm script \`${script}\` not defined in any package.json`,
        evidence: [],
      };
    }
    return {
      classification: "satisfied",
      reason: `npm script \`${script}\` defined in ${matches.length} package.json file(s)`,
      evidence: matches,
    };
  },
};

export default npmScriptDefinedRule;
