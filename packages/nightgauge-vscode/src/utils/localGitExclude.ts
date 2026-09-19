/**
 * Per-machine ignore rules, kept out of the repository's tracked files (#1875).
 *
 * The extension used to make its runtime paths ignored by editing tracked
 * files on whatever checkout it ran against — usually the operator's primary
 * clone on `main`: it replaced an older committed `.nightgauge/.gitignore`
 * wholesale and appended `.worktrees` to the root `.gitignore`. Nothing ever
 * committed those edits, so every session opened on a dirty `main`, and the
 * wholesale replace also discarded any rules the repository had added itself.
 *
 * Git already has a per-repository, never-tracked ignore file:
 * `<git-common-dir>/info/exclude`. It is shared by every worktree of the
 * repository and read with the same syntax as `.gitignore`. Rules a checkout
 * needs at runtime go there, in a delimited block this module owns; the
 * committed files change only through a pull request.
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

/** Absolute git common dir for `repoRoot`, or null outside a git work tree. */
export async function gitCommonDir(repoRoot: string): Promise<string | null> {
  try {
    const { stdout } = await execFileAsync(
      "git",
      ["rev-parse", "--path-format=absolute", "--git-common-dir"],
      { cwd: repoRoot }
    );
    const dir = String(stdout).trim();
    return dir === "" ? null : dir;
  } catch {
    return null;
  }
}

/** True when `relPath` (relative to `repoRoot`) is tracked by git. */
export async function isGitTracked(repoRoot: string, relPath: string): Promise<boolean> {
  try {
    await execFileAsync("git", ["ls-files", "--error-unmatch", "--", relPath], {
      cwd: repoRoot,
    });
    return true;
  } catch {
    return false;
  }
}

/**
 * Replace (or append) the block named `id` in the repository's info/exclude
 * with `patterns`. Returns true when the file changed, false when the block
 * was already current or `repoRoot` is not in a git work tree.
 */
export async function writeLocalExcludeBlock(
  repoRoot: string,
  id: string,
  patterns: string[]
): Promise<boolean> {
  const commonDir = await gitCommonDir(repoRoot);
  if (!commonDir) {
    return false;
  }
  const excludePath = path.join(commonDir, "info", "exclude");
  let existing = "";
  try {
    existing = await fs.readFile(excludePath, "utf8");
  } catch {
    // No exclude file yet.
  }

  const begin = `# nightgauge:begin ${id} (managed per machine; never committed, #1875)`;
  const end = `# nightgauge:end ${id}`;
  const block = [begin, ...patterns, end].join("\n") + "\n";

  const lines = existing.split("\n");
  const startIdx = lines.findIndex((l) => l.startsWith(`# nightgauge:begin ${id} `));
  const endIdx = lines.findIndex((l, i) => i > startIdx && l === end);
  let next: string;
  if (startIdx >= 0 && endIdx > startIdx) {
    const before = lines.slice(0, startIdx).join("\n");
    const after = lines.slice(endIdx + 1).join("\n");
    next = (before ? before + "\n" : "") + block + after;
  } else {
    const base = existing === "" || existing.endsWith("\n") ? existing : existing + "\n";
    next = base + block;
  }
  if (next === existing) {
    return false;
  }
  await fs.mkdir(path.dirname(excludePath), { recursive: true });
  await fs.writeFile(excludePath, next, "utf8");
  return true;
}

/**
 * Rewrite the rules of a `.gitignore` that lives in `dirRel` (relative to the
 * repository root, e.g. ".nightgauge") so they mean the same thing from the
 * repository root, where info/exclude is evaluated.
 *
 * gitignore semantics: a pattern containing a slash anywhere but at its end
 * is anchored to the file's directory; one without is matched at any depth
 * below it. Negation and a trailing directory slash carry over unchanged.
 */
export function toRootPatterns(dirRel: string, content: string): string[] {
  const base = dirRel.replace(/^\/+|\/+$/g, "");
  const out: string[] = [];
  for (const raw of content.split("\n")) {
    const line = raw.trimEnd();
    if (line === "" || line.startsWith("#")) {
      continue;
    }
    const negated = line.startsWith("!");
    let pattern = negated ? line.slice(1) : line;
    const dirOnly = pattern.endsWith("/");
    if (dirOnly) {
      pattern = pattern.slice(0, -1);
    }
    const anchored = pattern.includes("/");
    pattern = pattern.replace(/^\/+/, "");
    const rooted = anchored ? `/${base}/${pattern}` : `/${base}/**/${pattern}`;
    out.push(`${negated ? "!" : ""}${rooted}${dirOnly ? "/" : ""}`);
  }
  return out;
}
