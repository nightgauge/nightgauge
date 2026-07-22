import { spawn } from "node:child_process";
import type { RuleEvaluator } from "../types.js";

const PROTECTION_RE = /\bbranch\s+protection\b.*?\bmain\b/i;
const REQUIRED_CHECK_RE = /required\s+(?:check|status\s+check)\s+`([^`]+)`/i;

interface RunResult {
  code: number;
  stdout: string;
  stderr: string;
}

async function runCommand(command: string, args: string[], cwd: string): Promise<RunResult> {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(command, args, { cwd, stdio: "pipe" });
    } catch (err) {
      resolve({ code: 127, stdout: "", stderr: String(err) });
      return;
    }
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (c) => (stdout += String(c)));
    child.stderr.on("data", (c) => (stderr += String(c)));
    child.on("error", (err) => resolve({ code: 127, stdout, stderr: String(err) }));
    child.on("close", (code) => resolve({ code: code ?? 1, stdout, stderr }));
  });
}

async function getRepoSlug(workdir: string): Promise<string | null> {
  const r = await runCommand(
    "gh",
    ["repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner"],
    workdir
  );
  if (r.code !== 0) return null;
  const slug = r.stdout.trim();
  return slug.length > 0 ? slug : null;
}

async function getBranchProtection(workdir: string, slug: string): Promise<unknown | null> {
  const r = await runCommand(
    "gh",
    ["api", `repos/${slug}/branches/main/protection`, "--silent"],
    workdir
  );
  if (r.code !== 0) {
    const r2 = await runCommand("gh", ["api", `repos/${slug}/branches/main/protection`], workdir);
    if (r2.code !== 0) return null;
    try {
      return JSON.parse(r2.stdout);
    } catch {
      return null;
    }
  }
  // --silent suppresses output; re-fetch without it
  const r2 = await runCommand("gh", ["api", `repos/${slug}/branches/main/protection`], workdir);
  if (r2.code !== 0) return null;
  try {
    return JSON.parse(r2.stdout);
  } catch {
    return null;
  }
}

const branchProtectionRule: RuleEvaluator = {
  name: "branch-protection-rule-present",

  applies(text: string) {
    if (!PROTECTION_RE.test(text)) return null;
    const m = REQUIRED_CHECK_RE.exec(text);
    return { requiredCheck: m ? m[1] : "" };
  },

  async evaluate(ctx) {
    const slug = await getRepoSlug(ctx.workdir);
    if (!slug) {
      return {
        classification: "undetectable",
        reason: "gh not authenticated or no repo detected",
        evidence: [],
      };
    }
    const protection = await getBranchProtection(ctx.workdir, slug);
    if (!protection) {
      return {
        classification: "undetectable",
        reason: `Could not query branch protection for ${slug} (gh not authenticated or no permission)`,
        evidence: [],
      };
    }
    if (!ctx.extracted.requiredCheck) {
      return {
        classification: "satisfied",
        reason: `Branch protection enabled on ${slug}/main`,
        evidence: [`gh api repos/${slug}/branches/main/protection`],
      };
    }
    const required =
      (
        protection as {
          required_status_checks?: { contexts?: string[] };
        }
      ).required_status_checks?.contexts ?? [];
    if (required.includes(ctx.extracted.requiredCheck)) {
      return {
        classification: "satisfied",
        reason: `Required check \`${ctx.extracted.requiredCheck}\` enforced on ${slug}/main`,
        evidence: [`gh api repos/${slug}/branches/main/protection`],
      };
    }
    return {
      classification: "unsatisfied",
      reason: `Required check \`${ctx.extracted.requiredCheck}\` not enforced on ${slug}/main`,
      evidence: [],
    };
  },
};

export default branchProtectionRule;
