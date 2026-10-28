#!/usr/bin/env npx tsx
/**
 * Effort report and `effort_default` recommendation over control-sweep
 * records (Issue #2119). Usually invoked through `nightgauge eval report`.
 *
 * Usage:
 *   npx tsx scripts/eval-report.ts --model claude-opus-4-8 [--control-hash <sha>]
 *     [--records-dir .nightgauge/model-evals] [--tolerance 5] [--min-samples 3]
 *     [--json] [--emit-patch effort-default.patch]
 *
 * Exit codes: 0 report printed (and patch written, if asked); 1 no records,
 * or a patch was requested without a `recommended` outcome; 2 invalid arguments.
 *
 * @see docs/MODEL_EVALUATION.md - "Effort report and registry recommendation"
 */

import { fileURLToPath } from "node:url";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import {
  DEFAULT_EFFORT_MIN_SAMPLES,
  DEFAULT_EFFORT_TOLERANCE,
  DEFAULT_MODEL_EVAL_RECORDS_DIR,
  buildEffortDefaultPatch,
  buildEffortReport,
  formatEffortReport,
  getModelDescriptor,
  parseSweepRecords,
  type ModelEvalRecord,
} from "../packages/nightgauge-sdk/src/eval/index.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/** Both registry copies; the Go copy is byte-identical to the SDK one. */
const REGISTRY_PATHS = [
  "packages/nightgauge-sdk/src/eval/model-registry.json",
  "internal/models/model-registry.json",
];

interface ReportArgs {
  model: string;
  controlHash?: string;
  recordsDir: string;
  tolerance: number;
  minSamples: number;
  json: boolean;
  emitPatch?: string;
}

class UsageError extends Error {}

function parseArgs(argv: string[]): ReportArgs {
  const get = (flag: string): string | undefined => {
    const i = argv.indexOf(flag);
    return i >= 0 ? argv[i + 1] : undefined;
  };
  const num = (flag: string, fallback: number): number => {
    const v = get(flag);
    if (v === undefined) return fallback;
    const n = Number(v);
    if (!Number.isFinite(n) || n < 0) throw new UsageError(`${flag} must be a non-negative number`);
    return n;
  };
  const model = get("--model");
  if (!model) throw new UsageError("--model is required");
  const minSamples = num("--min-samples", DEFAULT_EFFORT_MIN_SAMPLES);
  if (!Number.isInteger(minSamples) || minSamples < 1) {
    throw new UsageError("--min-samples must be an integer >= 1");
  }
  return {
    model,
    controlHash: get("--control-hash"),
    recordsDir: get("--records-dir") ?? DEFAULT_MODEL_EVAL_RECORDS_DIR,
    tolerance: num("--tolerance", DEFAULT_EFFORT_TOLERANCE),
    minSamples,
    json: argv.includes("--json"),
    emitPatch: get("--emit-patch"),
  };
}

async function readRecords(dir: string): Promise<{ records: ModelEvalRecord[]; invalid: number }> {
  let names: string[];
  try {
    names = await fs.readdir(dir);
  } catch {
    return { records: [], invalid: 0 };
  }
  const records: ModelEvalRecord[] = [];
  let invalid = 0;
  for (const name of names.filter((n) => n.endsWith(".jsonl")).sort()) {
    const parsed = parseSweepRecords(await fs.readFile(path.join(dir, name), "utf-8"));
    records.push(...parsed.records);
    invalid += parsed.invalid;
  }
  return { records, invalid };
}

async function main(): Promise<number> {
  let args: ReportArgs;
  try {
    args = parseArgs(process.argv.slice(2));
  } catch (err) {
    console.error(`eval-report: ${(err as Error).message}`);
    return 2;
  }
  const descriptor = getModelDescriptor(args.model);
  if (!descriptor || descriptor.id !== args.model) {
    console.error(`eval-report: model '${args.model}' is not in the registry`);
    return 2;
  }

  const recordsDir = path.resolve(REPO_ROOT, args.recordsDir);
  const { records, invalid } = await readRecords(recordsDir);
  const report = buildEffortReport(records, {
    modelId: args.model,
    controlHash: args.controlHash,
    tolerance: args.tolerance,
    minSamples: args.minSamples,
  });
  const currentDefault = descriptor.behavior?.effort_default;

  if (args.json) {
    console.log(
      JSON.stringify(
        { ...report, current_effort_default: currentDefault ?? null, invalid_lines: invalid },
        null,
        2
      )
    );
  } else {
    console.log(formatEffortReport(report, currentDefault));
    if (invalid > 0) console.log(`invalid record lines skipped: ${invalid}`);
    if (descriptor.provider === "anthropic") {
      console.log(
        "note: effort is unverifiable on the claude adapter (docs/MODEL_EVALUATION.md § Safe interpretation)"
      );
    }
  }
  if (report.cells.length === 0) {
    console.error(`eval-report: no sweep records for ${args.model} under ${args.recordsDir}`);
    return 1;
  }

  if (args.emitPatch === undefined) return 0;
  const rec = report.recommendation;
  if (rec.status !== "recommended" || rec.effort === null) {
    console.error(`eval-report: no patch written: recommendation is ${rec.status} (${rec.reason})`);
    return 1;
  }
  if (report.modes.includes("mock")) {
    console.error("eval-report: no patch written: the input includes stub (mock) records");
    return 1;
  }
  const diffs: string[] = [];
  for (const rel of REGISTRY_PATHS) {
    const text = await fs.readFile(path.join(REPO_ROOT, rel), "utf-8");
    diffs.push(buildEffortDefaultPatch(text, rel, args.model, rec.effort));
  }
  if (diffs.every((d) => d === "")) {
    console.log(`effort_default is already ${rec.effort}; no patch written`);
    return 0;
  }
  const out = path.resolve(process.cwd(), args.emitPatch);
  await fs.writeFile(out, diffs.join(""), "utf-8");
  console.log(`patch → ${out} (apply it in a pull request: git apply ${args.emitPatch})`);
  return 0;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error(err instanceof Error ? (err.stack ?? err.message) : String(err));
    process.exit(1);
  });
