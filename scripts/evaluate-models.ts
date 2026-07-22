#!/usr/bin/env tsx
/**
 * Model Evaluation & Benchmarking — standalone suite runner (Issue #4174).
 *
 * Runs the realistic-task corpus (S3) through the {model × effort × reasoning}
 * matrix (S4), scores each cell (S5), writes a JSONL run record, and prints a
 * per-model comparison matrix (cost / quality / latency / attempts). Mirrors the
 * scripts/evaluate-skills.ts pattern.
 *
 * Usage:
 *   npx tsx scripts/evaluate-models.ts
 *   npx tsx scripts/evaluate-models.ts --models claude-sonnet-5,claude-opus-4-8
 *   npx tsx scripts/evaluate-models.ts --effort low,high --reasoning none,high
 *   npx tsx scripts/evaluate-models.ts --tasks evals/tasks --out .nightgauge/model-evals/run.jsonl
 *   # Live (real models, real API cost, needs `claude` auth):
 *   npx tsx scripts/evaluate-models.ts --mode live --models claude-haiku-4-5-20251001 --tasks evals/tasks
 *   npx tsx scripts/evaluate-models.ts --mode live --max-attempts 3   # bounded RALPH loop per cell
 *   npx tsx scripts/evaluate-models.ts --mode live --no-install       # skip per-cell dependency install
 *   # Live + subjective LLM judge (grades UI/UX/docs quality; extra grading cost):
 *   npx tsx scripts/evaluate-models.ts --mode live --judge
 *   npx tsx scripts/evaluate-models.ts --mode live --judge --judge-model claude-opus-4-8 --judge-samples 3
 *   # Store the run on the platform for the dashboard (historical trends):
 *   #   auth: NIGHTGAUGE_LICENSE_KEY (or ~/.nightgauge/config.yaml platform.license_key)
 *   npx tsx scripts/evaluate-models.ts --mode live --judge --emit
 *   # Prompt-variant axis (#72): A/B named prompt overlays against the baseline.
 *   # Definitions live in evals/variants/<name>.json; baseline always runs too,
 *   # and the run ends with a per-(variant, model) composite-score delta table.
 *   npx tsx scripts/evaluate-models.ts --mode live --judge --variants concise-preamble
 *   npx tsx scripts/evaluate-models.ts --variants a,b --variants-dir evals/variants
 *
 * Defaults to MOCK execution (deterministic, zero API cost) so the wiring is
 * exercised without live models. Live execution (`--mode live`) uses the
 * `LiveCellExecutor`: it spawns each model against each task inside the cell's
 * isolated worktree, runs the task's build/test checks there, and grades from
 * the real result — so quality genuinely differentiates across models. Live
 * runs cost real API money and need ambient `claude` auth; never run in CI.
 *
 * @see docs/decisions/011-model-eval-system.md
 * @see docs/MODEL_EVALUATION.md — live-mode operation
 */

import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import * as yaml from "js-yaml";
import {
  BASELINE_PROMPT_VARIANT,
  DEFAULT_JUDGE_MODEL,
  DEFAULT_MODEL_EVAL_RECORDS_DIR,
  EvalEmitError,
  LiveCellExecutor,
  LiveClaudeJudge,
  WorktreeWorkspaceProvider,
  activeModels,
  buildMatrix,
  computeVariantDeltas,
  emitEvalRun,
  formatComparisonMatrix,
  formatVariantDeltas,
  getModelDescriptor,
  loadEvalTasks,
  loadPromptVariants,
  resolveEvalEmitConfig,
  runEvalSuite,
  serializeEvalRun,
  type CellExecution,
  type EvalCellExecutor,
  type EvalMatrixCell,
  type ModelDescriptor,
} from "../packages/nightgauge-sdk/src/eval/index.js";

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

interface CliArgs {
  tasksDir: string;
  models: string[];
  efforts: EvalMatrixCell["effort"][];
  reasonings: EvalMatrixCell["reasoning"][];
  mode: "mock" | "live";
  out: string;
  /** Live-mode: max model invocations per cell (bounded RALPH loop). */
  maxAttempts: number;
  /** Live-mode: skip the per-cell dependency install. */
  noInstall: boolean;
  /** Live-mode: grade subjective quality with an LLM judge. */
  judge: boolean;
  /** Judge grader model (default {@link DEFAULT_JUDGE_MODEL}). */
  judgeModel: string;
  /** Judge samples per cell for the reliability guard. */
  judgeSamples: number;
  /** Push the run to the platform (`POST /v1/analytics/evals`) for the dashboard. */
  emit: boolean;
  /** Platform base URL override (else env / config.yaml / default). */
  platformUrl?: string;
  /** Named prompt variants to A/B against baseline (#72). Baseline always runs. */
  variants: string[];
  /** Directory holding `<name>.json` variant definitions. */
  variantsDir: string;
}

function parseArgs(argv: string[]): CliArgs {
  const get = (flag: string): string | undefined => {
    const i = argv.indexOf(flag);
    return i >= 0 ? argv[i + 1] : undefined;
  };
  const csv = (v: string | undefined): string[] | undefined =>
    v
      ? v
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
      : undefined;

  const defaultModels = activeModels()
    .filter((m) => m.provider === "anthropic")
    .map((m) => m.id);

  return {
    tasksDir: get("--tasks") ?? "evals/tasks",
    models: csv(get("--models")) ?? defaultModels,
    efforts: (csv(get("--effort")) as CliArgs["efforts"]) ?? ["high"],
    reasonings: (csv(get("--reasoning")) as CliArgs["reasonings"]) ?? ["none"],
    mode: (get("--mode") as "mock" | "live") ?? "mock",
    maxAttempts: Math.max(1, Number(get("--max-attempts") ?? "1") || 1),
    noInstall: argv.includes("--no-install"),
    judge: argv.includes("--judge"),
    judgeModel: get("--judge-model") ?? DEFAULT_JUDGE_MODEL,
    judgeSamples: Math.max(1, Number(get("--judge-samples") ?? "3") || 3),
    emit: argv.includes("--emit"),
    platformUrl: get("--platform-url"),
    variants: csv(get("--variants")) ?? [],
    variantsDir: get("--variants-dir") ?? "evals/variants",
    out:
      get("--out") ??
      path.join(
        DEFAULT_MODEL_EVAL_RECORDS_DIR,
        `run-${new Date().toISOString().replace(/[:.]/g, "-")}.jsonl`
      ),
  };
}

/**
 * Read the `platform:` section of `~/.nightgauge/config.yaml` (the standard
 * machine config the Go binary also uses) as a fallback credential source for
 * `--emit`. Returns null on any absence/parse error — env vars still apply.
 */
async function readMachineConfig(): Promise<{ api_url?: string; license_key?: string } | null> {
  try {
    const raw = await fs.readFile(path.join(os.homedir(), ".nightgauge", "config.yaml"), "utf-8");
    const parsed = yaml.load(raw) as {
      platform?: { api_url?: string; license_key?: string };
    } | null;
    return parsed?.platform ?? null;
  } catch {
    return null;
  }
}

/**
 * Push the run to the platform ingest endpoint so it lands in the dashboard's
 * historical store. Non-fatal: a failure logs and returns without affecting the
 * exit code or the local JSONL.
 */
async function emitToPlatform(
  run: Parameters<typeof emitEvalRun>[0],
  args: CliArgs
): Promise<void> {
  // Never push fabricated mock telemetry to the shared historical store — it
  // would corrupt the dashboard's cost/quality trends.
  if (args.mode !== "live") {
    console.error(
      "\n⚠  --emit skipped: only live runs are stored (mock data would pollute trends)."
    );
    return;
  }
  const resolved = resolveEvalEmitConfig({
    env: process.env,
    fileConfig: await readMachineConfig(),
    overrides: args.platformUrl ? { baseUrl: args.platformUrl } : undefined,
  });
  if ("error" in resolved) {
    console.error(`\n⚠  --emit skipped: ${resolved.error}`);
    return;
  }
  try {
    const result = await emitEvalRun(run, resolved);
    if (result.rejected.length > 0) {
      console.error(
        `\n⚠  Platform accepted ${result.accepted}, rejected ${result.rejected.length}: ` +
          result.rejected.map((r) => `#${r.index} ${r.reason}`).join("; ")
      );
    } else {
      console.log(`\n✓ Emitted to platform (${resolved.baseUrl}) — accepted ${result.accepted}.`);
      console.log("  View at /admin/evals on the dashboard.");
    }
  } catch (err) {
    const msg =
      err instanceof EvalEmitError ? err.message : err instanceof Error ? err.message : String(err);
    console.error(`\n⚠  --emit failed (run kept locally): ${msg}`);
  }
}

/**
 * Live mode needs a working `claude` CLI (ambient auth). Fail fast with a clear
 * remediation instead of erroring cell-by-cell deep inside the run.
 */
function preflightClaudeCli(command: string): string | null {
  const r = spawnSync(command, ["--version"], { encoding: "utf-8" });
  if (r.error || (r.status ?? 1) !== 0) {
    return (
      `live mode requires a working \`${command}\` CLI on PATH with valid auth.\n` +
      `Install: npm install -g @anthropic-ai/claude-code  (then \`claude auth login\`).\n` +
      `Override the command with NIGHTGAUGE_CLAUDE_CLI_COMMAND.`
    );
  }
  return null;
}

/**
 * Deterministic mock executor: fabricates plausible telemetry that varies by
 * model tier and effort, so the pipeline/scoring wiring can be exercised end to
 * end without live model calls. NOT a real run — for wiring/CI only.
 */
function mockExecutor(): EvalCellExecutor {
  const outputMultiplier: Record<string, number> = { low: 1, medium: 1.5, high: 2.2 };
  return {
    async execute(_task, cell): Promise<CellExecution> {
      const d = getModelDescriptor(cell.model_id);
      const tierFactor = d?.tier === "opus" ? 1.3 : d?.tier === "haiku" ? 0.6 : 1;
      const out = Math.round(4000 * (outputMultiplier[cell.effort] ?? 1) * tierFactor);
      return {
        verdict: "pass",
        tokens: { input: 12000, output: out, cache_read: 2000, cache_creation: 500 },
        latency_ms: Math.round(45000 * (outputMultiplier[cell.effort] ?? 1)),
        attempts_to_green: 1,
        gate_results: [
          { name: "build", passed: true },
          { name: "test", passed: true },
        ],
        model_version_label: d?.display_name ?? cell.model_id,
        stage: "feature-dev",
      };
    },
  };
}

async function main(): Promise<number> {
  const args = parseArgs(process.argv.slice(2));

  const claudeCommand = process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND ?? "claude";
  if (args.mode === "live") {
    const preflightError = preflightClaudeCli(claudeCommand);
    if (preflightError) {
      console.error(preflightError);
      return 2;
    }
  }

  const tasks = await loadEvalTasks(path.resolve(REPO_ROOT, args.tasksDir));
  if (tasks.length === 0) {
    console.error(`no tasks found under ${args.tasksDir}`);
    return 1;
  }

  const models: ModelDescriptor[] = args.models
    .map((id) => getModelDescriptor(id))
    .filter((m): m is ModelDescriptor => Boolean(m));
  if (models.length === 0) {
    console.error(`no known models among: ${args.models.join(", ")}`);
    return 1;
  }

  // Prompt-variant axis (#72): baseline always runs so every variant has its
  // in-run comparison anchor. Definitions are validated up front — a broken
  // variant file must fail the run before any API money is spent.
  const variantNames = [
    BASELINE_PROMPT_VARIANT,
    ...args.variants.filter((v) => v !== BASELINE_PROMPT_VARIANT),
  ];
  const variantDefs = await loadPromptVariants(
    path.resolve(REPO_ROOT, args.variantsDir),
    variantNames
  );

  const matrix = buildMatrix(
    models.map((m) => m.id),
    args.efforts,
    args.reasonings,
    variantNames
  );

  const cellCount = tasks.length * matrix.length;
  const variantNote = variantNames.length > 1 ? ` × ${variantNames.length} prompt variant(s)` : "";
  console.log(
    `Running ${tasks.length} task(s) × ${matrix.length} cell(s) [${args.mode}]${variantNote} ` +
      `across ${models.map((m) => m.display_name).join(", ")}`
  );

  const executor: EvalCellExecutor =
    args.mode === "live"
      ? new LiveCellExecutor({
          command: claudeCommand,
          maxAttempts: args.maxAttempts,
          skipInstall: args.noInstall,
          onLog: (msg) => console.log(`  ${msg}`),
          // Subjective grading (opt-in): a read-only judge model inspects each
          // cell's produced work. Never the model under test — the CLI passes a
          // fixed grader — to avoid self-grading bias.
          judgeSamples: args.judgeSamples,
          // Prompt-variant overlays (#72), applied to the task instruction
          // before each cell's prompt is assembled.
          variants: variantDefs,
          judgeFactory: args.judge
            ? (task, _cell, workspace) =>
                new LiveClaudeJudge({
                  workspaceDir: workspace.dir,
                  task,
                  command: claudeCommand,
                  model: args.judgeModel,
                })
            : undefined,
        })
      : mockExecutor();

  if (args.mode === "live") {
    const attemptsNote = args.maxAttempts > 1 ? ` × up to ${args.maxAttempts} attempts` : "";
    const judgeNote = args.judge
      ? ` + LLM judge (${args.judgeModel}, ${args.judgeSamples}× per cell)`
      : "";
    console.log(
      `\n⚠  LIVE MODE — spawns real models against ${cellCount} cell(s)${attemptsNote}${judgeNote}.\n` +
        `   This costs real API money and runs the pipeline toolchain in each isolated ` +
        `worktree. Ctrl-C now to abort.\n`
    );
  }

  const run = await runEvalSuite({
    suite: path.basename(args.tasksDir),
    runId: `run-${new Date().toISOString().replace(/[:.]/g, "-")}`,
    timestamp: new Date().toISOString(),
    mode: args.mode,
    tasks,
    matrix,
    models,
    executor,
    workspaces: new WorktreeWorkspaceProvider({ repoRoot: REPO_ROOT }),
    // Live cells are slower + serial-ish (real toolchain); mock can fan out wide.
    concurrency: args.mode === "live" ? 2 : 4,
    scoringBaseline: { attempts: 1, latencyMs: 60_000, costUsd: 0.5 },
  });

  const outPath = path.resolve(REPO_ROOT, args.out);
  await fs.mkdir(path.dirname(outPath), { recursive: true });
  await fs.writeFile(outPath, serializeEvalRun(run) + "\n", "utf-8");

  console.log("\n" + formatComparisonMatrix(run));
  if (variantNames.length > 1) {
    // Per-(variant, model) quality delta vs baseline (#72). Negative Δ = the
    // variant REGRESSED against the unmodified prompt.
    console.log("\nPrompt-variant deltas vs baseline:");
    console.log(formatVariantDeltas(computeVariantDeltas(run.cells)));
  }
  console.log(
    `\nTotal cost: $${run.summary.total_cost_usd.toFixed(4)} | ` +
      `${run.summary.passed}/${run.summary.total} passed | records → ${args.out}`
  );

  if (args.emit) await emitToPlatform(run, args);
  return 0;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error(err);
    process.exit(1);
  });
