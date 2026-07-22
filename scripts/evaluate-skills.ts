#!/usr/bin/env tsx
/**
 * Cross-Model Skill Evaluation Harness — standalone runner.
 *
 * Runs a (scenario × model) matrix for one or more pipeline-stage skills and
 * prints a pass/fail matrix, writing a JSONL run record. Mirrors the
 * `scripts/analyze-model-routing.ts` pattern (SDK-service import, file I/O,
 * `main()` shape).
 *
 * Usage:
 *   npx tsx scripts/evaluate-skills.ts
 *   npx tsx scripts/evaluate-skills.ts --skills feature-planning,pr-create
 *   npx tsx scripts/evaluate-skills.ts --models haiku,sonnet,opus
 *   npx tsx scripts/evaluate-skills.ts --baseline .nightgauge/skill-evals/baseline.jsonl
 *   NIGHTGAUGE_SKILL_EVAL_LIVE=1 npx tsx scripts/evaluate-skills.ts --mode live --skills pr-merge
 *
 * Defaults to mock mode (deterministic, zero API cost). Live mode requires
 * NIGHTGAUGE_SKILL_EVAL_LIVE=1. Exits non-zero when any cell regresses
 * versus the supplied baseline. When --baseline is given but the file is
 * missing/unparseable/empty, the run fails CLOSED (exit 1) so the CI gate can
 * never silently pass against a non-existent baseline (#4092).
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 * @see Issue #4092 - Wire the harness into CI as a required regression gate
 */

import { fileURLToPath } from "node:url";
import * as fs from "fs/promises";
import * as path from "path";
import {
  EvalRecorder,
  LiveClaudeModelRunner,
  MockModelRunner,
  PIPELINE_SKILLS,
  SkillEvalHarness,
  isLiveModeEnabled,
  loadFixtures,
  loadScenarios,
  parseRecords,
  type EvalModelRunner,
  type EvalMode,
  type EvalRunReport,
  type ModelTier,
} from "../packages/nightgauge-sdk/src/eval/index.js";

const ALL_MODELS: ModelTier[] = ["haiku", "sonnet", "opus"];
type PipelineSkill = (typeof PIPELINE_SKILLS)[number];

// Repo root is one level up from scripts/. Scenarios/fixtures live at the root.
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");
const FIXTURES_DIR = path.join(REPO_ROOT, "evals/fixtures");

interface CliArgs {
  skills: PipelineSkill[];
  models: ModelTier[];
  mode: EvalMode;
  baseline?: string;
}

function parseArgs(argv: string[]): CliArgs {
  let skills: PipelineSkill[] = [...PIPELINE_SKILLS];
  let models: ModelTier[] = [...ALL_MODELS];
  let mode: EvalMode = "mock";
  let baseline: string | undefined;

  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = argv[i + 1];
    if (arg === "--skills" && next) {
      skills = validateSkills(next.split(",").map((s) => s.trim()));
      i++;
    } else if (arg === "--models" && next) {
      models = validateModels(next.split(",").map((s) => s.trim()));
      i++;
    } else if (arg === "--mode" && next) {
      if (next !== "mock" && next !== "live") {
        throw new Error(`--mode must be "mock" or "live", got "${next}"`);
      }
      mode = next;
      i++;
    } else if (arg === "--baseline" && next) {
      baseline = next;
      i++;
    }
  }

  return { skills, models, mode, baseline };
}

function validateSkills(values: string[]): PipelineSkill[] {
  for (const v of values) {
    if (!(PIPELINE_SKILLS as readonly string[]).includes(v)) {
      throw new Error(`unknown skill "${v}". Valid: ${PIPELINE_SKILLS.join(", ")}`);
    }
  }
  return values as PipelineSkill[];
}

function validateModels(values: string[]): ModelTier[] {
  for (const v of values) {
    if (!(ALL_MODELS as string[]).includes(v)) {
      throw new Error(`unknown model "${v}". Valid: ${ALL_MODELS.join(", ")}`);
    }
  }
  return values as ModelTier[];
}

/** Render a pass/fail matrix: rows = scenarios, columns = model tiers. */
function renderMatrix(report: EvalRunReport): string {
  const symbol = (verdict: string): string =>
    verdict === "pass" ? "PASS" : verdict === "fail" ? "FAIL" : "ERR ";

  const lines: string[] = [];
  const header = ["scenario".padEnd(36), ...report.models.map((m) => m.padEnd(8))].join(" | ");
  lines.push(header);
  lines.push("-".repeat(header.length));

  const scenarioIds = Array.from(new Set(report.cells.map((c) => c.scenario_id)));
  for (const id of scenarioIds) {
    const row = [id.padEnd(36)];
    for (const model of report.models) {
      const cell = report.cells.find((c) => c.scenario_id === id && c.model === model);
      row.push((cell ? symbol(cell.verdict) : "—").padEnd(8));
    }
    lines.push(row.join(" | "));
  }
  return lines.join("\n");
}

async function loadBaseline(baselinePath: string): Promise<EvalRunReport | null> {
  try {
    const jsonl = await fs.readFile(baselinePath, "utf-8");
    const records = parseRecords(jsonl);
    // Reconstruct a minimal report from records for diffing.
    const models = Array.from(new Set(records.map((r) => r.model)));
    const skills = Array.from(new Set(records.map((r) => r.skill)));
    return {
      schema_version: records[0]?.schema_version ?? "1",
      timestamp: records[0]?.timestamp ?? "",
      mode: records[0]?.mode ?? "mock",
      skills,
      models,
      cells: records.map(({ schema_version: _sv, timestamp: _ts, mode: _m, ...cell }) => cell),
      summary: {
        total: records.length,
        passed: records.filter((r) => r.verdict === "pass").length,
        failed: records.filter((r) => r.verdict === "fail").length,
        errored: records.filter((r) => r.verdict === "error").length,
      },
    };
  } catch (err) {
    console.error(`WARNING: could not load baseline ${baselinePath}: ${(err as Error).message}`);
    return null;
  }
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));

  if (args.mode === "live" && !isLiveModeEnabled()) {
    console.error(
      "ERROR: --mode live requires NIGHTGAUGE_SKILL_EVAL_LIVE=1 (gated to avoid accidental API cost)."
    );
    process.exit(1);
  }

  console.log(`Loading scenarios for: ${args.skills.join(", ")}`);
  const scenarios = await loadScenarios({ skills: args.skills, scenariosDir: SCENARIOS_DIR });
  console.log(`Loaded ${scenarios.length} scenarios.`);

  let runner: EvalModelRunner;
  if (args.mode === "live") {
    console.log("Mode: LIVE — spawning `claude --print --model <tier>` per cell.");
    runner = new LiveClaudeModelRunner();
  } else {
    console.log("Mode: MOCK — resolving fixtures (no API calls).");
    const fixtures = await loadFixtures({ skills: args.skills, fixturesDir: FIXTURES_DIR });
    runner = new MockModelRunner(fixtures);
  }

  const harness = new SkillEvalHarness(runner);
  // Timestamp injected here at the I/O boundary; pure code never reads the clock.
  const timestamp = new Date().toISOString();
  const report = await harness.run({ scenarios, models: args.models, timestamp });

  console.log("\n" + renderMatrix(report) + "\n");
  console.log(
    `Summary: ${report.summary.passed}/${report.summary.total} passed, ` +
      `${report.summary.failed} failed, ${report.summary.errored} errored.`
  );

  const recorder = new EvalRecorder();
  const recordPath = await recorder.record(report);
  console.log(`Run record written to ${recordPath}`);

  let exitCode = 0;

  if (args.baseline) {
    const baseline = await loadBaseline(args.baseline);
    // Fail CLOSED (#4092): a --baseline that is missing, unparseable, or empty
    // must hard-exit 1. Otherwise a regression gate pointed at a non-existent
    // baseline silently passes — testing nothing while reporting success.
    if (!baseline || baseline.cells.length === 0) {
      console.error(
        `ERROR: --baseline ${args.baseline} is missing, unparseable, or empty. ` +
          `Refusing to report success against a non-existent baseline (a gate that tests nothing). ` +
          `Regenerate with: npx tsx scripts/evaluate-skills.ts, then copy the run record to ${args.baseline}.`
      );
      process.exit(1);
    }
    const diff = recorder.diffAgainstBaseline(report, baseline);
    console.log(
      `\nRegression diff vs baseline: ${diff.regressions.length} regression(s), ` +
        `${diff.fixes.length} fix(es), ${diff.added.length} new cell(s).`
    );
    for (const r of diff.regressions) {
      console.error(`  REGRESSION: ${r.skill}/${r.scenario_id} @ ${r.model} (${r.from} → ${r.to})`);
    }
    for (const f of diff.fixes) {
      console.log(`  fixed: ${f.skill}/${f.scenario_id} @ ${f.model} (${f.from} → ${f.to})`);
    }
    if (diff.regressions.length > 0) exitCode = 1;
  } else if (report.summary.failed > 0 || report.summary.errored > 0) {
    // No baseline: any failing/errored cell is a non-zero exit so CI can gate.
    exitCode = 1;
  }

  process.exit(exitCode);
}

main().catch((err) => {
  console.error("Fatal error:", err);
  process.exit(1);
});
