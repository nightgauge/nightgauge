import { spawn } from "node:child_process";
import type { RuleEvaluator } from "../types.js";

const PROTECTION_RE = /\bbranch\s+protection\b.*?\bmain\b/i;
const REQUIRED_CHECK_RE = /required\s+(?:check|status\s+check)\s+`([^`]+)`/i;

interface RunResult {
  code: number;
  stdout: string;
  stderr: string;
}

/**
 * Hard ceiling on a single `gh` invocation.
 *
 * These calls reach the network, so their latency is set by the environment,
 * not by us: on a CI runner a `gh` call that ultimately fails still spends
 * ~1.4s on DNS/TLS/auth before giving up. Unbounded, that made this rule's
 * cost unbounded too — and the deterministic AC-reconciliation integration
 * test, which exercises this rule through a fixture, sat right on vitest's
 * 5000ms default and flipped between pass and fail on identical trees.
 *
 * A rule that cannot answer in time must answer "undetectable", never hang.
 */
const GH_COMMAND_TIMEOUT_MS = 2_000;

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
    let settled = false;
    const finish = (r: RunResult) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(r);
    };
    const timer = setTimeout(() => {
      // SIGKILL rather than SIGTERM: `gh` blocked on a socket does not
      // necessarily honour a terminate, and this path must be bounded.
      child.kill("SIGKILL");
      // Killing the child is not enough to free the caller. If `gh` has
      // spawned its own children they inherit these pipes, so the write ends
      // stay open, the streams never emit 'close', and the event loop is held
      // open long after this promise settles — measured at a full 60s against
      // a hanging stub even though evaluate() returned in ~2s. Tear the read
      // ends down explicitly so a timed-out call costs nothing after it times out.
      child.stdout?.destroy();
      child.stderr?.destroy();
      child.unref?.();
      finish({
        code: 124,
        stdout,
        stderr: `${stderr}\n[timed out after ${GH_COMMAND_TIMEOUT_MS}ms]`,
      });
    }, GH_COMMAND_TIMEOUT_MS);
    // Do not keep the event loop alive purely for this timer.
    timer.unref?.();
    child.stdout.on("data", (c) => (stdout += String(c)));
    child.stderr.on("data", (c) => (stderr += String(c)));
    child.on("error", (err) => finish({ code: 127, stdout, stderr: String(err) }));
    child.on("close", (code) => finish({ code: code ?? 1, stdout, stderr }));
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
  // One call, not three. The previous shape probed with `--silent` and then
  // re-fetched without it in BOTH branches — `--silent` suppresses the output
  // this function exists to parse, so the probe could never contribute a
  // result and only ever bought a wasted network round trip. Combined with
  // getRepoSlug that put three unbounded `gh` calls on the failure path.
  const r = await runCommand("gh", ["api", `repos/${slug}/branches/main/protection`], workdir);
  if (r.code !== 0) return null;
  try {
    return JSON.parse(r.stdout);
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
