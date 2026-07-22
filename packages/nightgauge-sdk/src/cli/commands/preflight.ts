/**
 * Preflight Command — deterministic AC reconciliation (Issue #3003).
 *
 * Usage:
 *   nightgauge-sdk preflight ac-reconcile <issue> [--workdir .]
 *                                                     [--out PATH]
 *                                                     [--body-file PATH]
 *
 * Reads the issue body (from --body-file or `gh issue view --json body`),
 * resolves the current `main` SHA via git, runs the rule library, and
 * writes `.nightgauge/pipeline/ac-reconcile-{N}.json`.
 *
 * Always exits 0 when reconciliation completes (the routing decision is up
 * to the caller). Exits non-zero only on hard failures (missing body,
 * invalid issue number, schema validation failure).
 */

import type { CAC } from "cac";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import { reconcileAcceptanceCriteria } from "../../preflight/reconcile.js";

interface PreflightOptions {
  workdir?: string;
  out?: string;
  bodyFile?: string;
}

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

async function fetchIssueBody(issueNumber: number, cwd: string): Promise<string> {
  const r = await runCommand(
    "gh",
    ["issue", "view", String(issueNumber), "--json", "body", "-q", ".body"],
    cwd
  );
  if (r.code !== 0) {
    throw new Error(
      `Failed to fetch issue body via gh (exit ${r.code}): ${r.stderr.trim() || "no error output"}`
    );
  }
  return r.stdout;
}

async function resolveMainSha(cwd: string): Promise<string> {
  const r = await runCommand("git", ["rev-parse", "main"], cwd);
  if (r.code === 0) return r.stdout.trim();
  const r2 = await runCommand("git", ["rev-parse", "HEAD"], cwd);
  if (r2.code === 0) return r2.stdout.trim();
  return "unknown";
}

export function registerPreflightCommand(cli: CAC): void {
  cli
    .command("preflight ac-reconcile <issue>", "Deterministic AC reconciliation (Issue #3003)")
    .option("--workdir <dir>", "Working directory (default: cwd)")
    .option("--out <path>", "Output path (default: .nightgauge/pipeline/ac-reconcile-{N}.json)")
    .option("--body-file <path>", "Read issue body from file instead of gh")
    .action(async (issueArg: string, options: PreflightOptions) => {
      const issueNumber = parseInt(issueArg, 10);
      if (isNaN(issueNumber) || issueNumber <= 0) {
        console.error(`Error: Invalid issue number: ${issueArg}`);
        process.exit(2);
      }

      const workdir = path.resolve(options.workdir ?? process.cwd());
      const outPath =
        options.out ??
        path.join(workdir, ".nightgauge", "pipeline", `ac-reconcile-${issueNumber}.json`);

      let body: string;
      try {
        if (options.bodyFile) {
          body = await readFile(path.resolve(options.bodyFile), "utf-8");
        } else {
          body = await fetchIssueBody(issueNumber, workdir);
        }
      } catch (err) {
        console.error(
          `Error fetching issue body: ${err instanceof Error ? err.message : String(err)}`
        );
        process.exit(1);
      }

      const mainSha = await resolveMainSha(workdir);

      let report;
      try {
        report = await reconcileAcceptanceCriteria({
          workdir,
          issueNumber,
          issueBody: body,
          mainSha,
        });
      } catch (err) {
        console.error(
          `Error running reconciler: ${err instanceof Error ? err.message : String(err)}`
        );
        process.exit(1);
      }

      await mkdir(path.dirname(outPath), { recursive: true });
      await writeFile(outPath, JSON.stringify(report, null, 2), "utf-8");

      const satisfied = report.acceptance_criteria.filter(
        (c) => c.classification === "satisfied"
      ).length;
      const total = report.acceptance_criteria.length;
      console.log(`${satisfied}/${total} satisfied; suggested: ${report.suggested_route.approach}`);
      console.log(`Wrote ${outPath}`);
      process.exit(0);
    });
}
