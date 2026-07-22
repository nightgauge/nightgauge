/**
 * Issue #76 — pool the lean-variant A/B repetitions and print the decision table.
 *
 * Reads every .nightgauge/model-evals/76-lean-r*.jsonl, concatenates the
 * ModelEvalRecords, and runs computeVariantDeltas over the pooled set (the
 * decision comes from pooled records, not any single run — see
 * evals/skill-variants/feature-validate/README.md).
 *
 * Usage: npx tsx scripts/pool-76-deltas.ts
 */
import { fileURLToPath } from "node:url";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import {
  computeVariantDeltas,
  formatVariantDeltas,
} from "../packages/nightgauge-sdk/src/eval/index.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const RECORDS_DIR = path.join(REPO_ROOT, ".nightgauge", "model-evals");

async function main(): Promise<void> {
  const files = (await fs.readdir(RECORDS_DIR))
    .filter((f) => /^76-lean-r\d+\.jsonl$/.test(f))
    .sort();
  if (files.length === 0) {
    console.error(`no 76-lean-r*.jsonl files under ${RECORDS_DIR}`);
    process.exit(1);
  }

  const records: any[] = [];
  for (const f of files) {
    const text = await fs.readFile(path.join(RECORDS_DIR, f), "utf8");
    for (const line of text.split("\n")) {
      if (line.trim()) records.push(JSON.parse(line));
    }
  }

  console.log(
    `Pooled ${records.length} records from ${files.length} run(s): ${files.join(", ")}\n`
  );

  // Per-cell detail: deterministic checks and composite, grouped for eyeballing.
  const rows = records
    .map((r) => ({
      model: r.model_id,
      variant: r.cell?.prompt_variant ?? "?",
      passed: r.gate_results?.filter((c: any) => c.passed).length,
      total: r.gate_results?.length,
      composite: r.score?.composite,
    }))
    .sort((a, b) => a.model.localeCompare(b.model) || a.variant.localeCompare(b.variant));
  console.log("model\tvariant\tchecks\tcomposite");
  for (const r of rows) {
    console.log(`${r.model}\t${r.variant}\t${r.passed}/${r.total}\t${r.composite ?? "unscored"}`);
  }

  console.log("\n== Pooled variant deltas (decision table) ==");
  console.log(formatVariantDeltas(computeVariantDeltas(records)));
}

void main();
