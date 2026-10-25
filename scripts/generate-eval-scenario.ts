#!/usr/bin/env tsx
/**
 * Generate a skill-eval regression scenario from a diagnosis whose root cause
 * was a skill instruction or agent configuration (#1287).
 *
 * Reads a check-triage record or retro finding carrying `cause.kind` and a
 * `skill_regression` block, derives a decision-form scenario + mock fixture,
 * verifies it passes on the corrected decision, fails on the non-answer
 * sentinel and fails on the recorded pre-fix decision, and only then writes
 * it under evals/. Never overwrites an existing scenario.
 *
 * Usage:
 *   npx tsx scripts/generate-eval-scenario.ts --from <diagnosis.json> [--root .] [--dry-run]
 *
 * Prints a JSON report `{status, reason?, scenario_path, fixture_path, checks, ...}`.
 * Exit codes: 0 written, verified (--dry-run) or skipped (product_code
 * cause); 1 rejected by a verification gate; 2 invalid input or arguments.
 *
 * @see docs/SKILL_EVALUATION.md § Generated scenarios
 */

import * as fs from "fs/promises";
import { generateScenarioFromInput } from "../packages/nightgauge-sdk/src/eval/index.js";

const USAGE =
  "usage: npx tsx scripts/generate-eval-scenario.ts --from <diagnosis.json> [--root <dir>] [--dry-run]";

function parseArgs(argv: string[]): { from: string; root: string; dryRun: boolean } {
  let from = "";
  let root = ".";
  let dryRun = false;
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg === "--from") from = argv[++i] ?? "";
    else if (arg === "--root") root = argv[++i] ?? "";
    else if (arg === "--dry-run") dryRun = true;
    else throw new Error(`unknown argument "${arg}"`);
  }
  if (!from || !root) throw new Error("--from and --root need a value");
  return { from, root, dryRun };
}

async function main(): Promise<void> {
  let input: unknown;
  let opts: ReturnType<typeof parseArgs>;
  try {
    opts = parseArgs(process.argv.slice(2));
    input = JSON.parse(await fs.readFile(opts.from, "utf-8"));
  } catch (err) {
    console.error(`${(err as Error).message}\n${USAGE}`);
    process.exit(2);
  }

  let report;
  try {
    report = await generateScenarioFromInput(input, { root: opts.root, dryRun: opts.dryRun });
  } catch (err) {
    console.error((err as Error).message);
    process.exit(2);
  }
  console.log(JSON.stringify(report, null, 2));
  process.exit(report.status === "rejected" ? 1 : 0);
}

main().catch((err) => {
  console.error("Fatal error:", err);
  process.exit(2);
});
