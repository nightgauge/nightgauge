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
 *   npx tsx scripts/evaluate-skills.ts --render-profile compact --skills pr-merge
 *   NIGHTGAUGE_SKILL_EVAL_LIVE=1 npx tsx scripts/evaluate-skills.ts --mode live --skills pr-merge
 *
 * Defaults to mock mode (deterministic, zero API cost). Live mode requires
 * NIGHTGAUGE_SKILL_EVAL_LIVE=1. Exits non-zero when any cell regresses
 * versus the supplied baseline. When --baseline is given but the file is
 * missing/unparseable/empty, the run fails CLOSED (exit 1) so the CI gate can
 * never silently pass against a non-existent baseline (#4092).
 *
 * --render-profile <full|compact> is the #1654 measurement lane: it prepends
 * `nightgauge skill render --stage <skill> --profile <profile>`'s output to
 * every scenario prompt before the runner sees it, so a live run actually
 * exercises the profile a scenario's skill would carry in the real pipeline
 * — the harness otherwise sends only the bare scenario prompt (the skill
 * text is never part of it), which cannot distinguish a full render from a
 * compact one. Mock mode ignores prompt content by construction
 * (MockModelRunner is fixture-keyed), so the flag is inert there except for
 * tagging the recorded JSONL with `render_profile` on every cell.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 * @see Issue #4092 - Wire the harness into CI as a required regression gate
 * @see Issue #1654 - compact render profile mechanism + pr-merge compact skill
 */

import { execFile } from "node:child_process";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import * as fs from "fs/promises";
import * as path from "path";

const execFileAsync = promisify(execFile);
import {
  EvalRecorder,
  LiveClaudeModelRunner,
  MockModelRunner,
  MODEL_TIERS,
  EVAL_SKILLS,
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

/**
 * Default eval matrix, derived from the `MODEL_TIERS` band authority (#581)
 * rather than a hand-inlined list (#582). `fable` is EXCLUDED deliberately,
 * not silently: extending the skill-eval harness to the fable band is #383,
 * blockedBy the honest eval lane (#571) — fable coverage is only worth paying
 * for on honest cells. Pass `--models fable` to opt in explicitly.
 */
const ALL_MODELS: ModelTier[] = MODEL_TIERS.filter((tier) => tier !== "fable");
type EvalSkill = (typeof EVAL_SKILLS)[number];

// Repo root is one level up from scripts/. Scenarios/fixtures live at the root.
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");
const FIXTURES_DIR = path.join(REPO_ROOT, "evals/fixtures");

/** The #1654 render profiles `nightgauge skill render --profile` accepts. */
type RenderProfile = "full" | "compact";

interface CliArgs {
  skills: EvalSkill[];
  models: ModelTier[];
  mode: EvalMode;
  baseline?: string;
  renderProfile?: RenderProfile;
}

function parseArgs(argv: string[]): CliArgs {
  let skills: EvalSkill[] = [...EVAL_SKILLS];
  let models: ModelTier[] = [...ALL_MODELS];
  let mode: EvalMode = "mock";
  let baseline: string | undefined;
  let renderProfile: RenderProfile | undefined;

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
    } else if (arg === "--render-profile" && next) {
      if (next !== "full" && next !== "compact") {
        throw new Error(`--render-profile must be "full" or "compact", got "${next}"`);
      }
      renderProfile = next;
      i++;
    }
  }

  return { skills, models, mode, baseline, renderProfile };
}

function validateSkills(values: string[]): EvalSkill[] {
  for (const v of values) {
    if (!(EVAL_SKILLS as readonly string[]).includes(v)) {
      throw new Error(`unknown skill "${v}". Valid: ${EVAL_SKILLS.join(", ")}`);
    }
  }
  return values as EvalSkill[];
}

function validateModels(values: string[]): ModelTier[] {
  // Validate against the full band authority, not the default matrix — the
  // default excludes `fable` for cost, but an explicit `--models fable` is
  // the documented opt-in (#383).
  for (const v of values) {
    if (!(MODEL_TIERS as readonly string[]).includes(v)) {
      throw new Error(`unknown model "${v}". Valid: ${MODEL_TIERS.join(", ")}`);
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

/**
 * Resolve the `nightgauge` Go binary the same fallback chain the skills'
 * own shell procedures use (e.g. `_includes/post-merge.md`'s "Step 8.1"):
 * `NIGHTGAUGE_BIN` env var, then the repo-root `bin/nightgauge` build
 * output, then whatever `nightgauge` resolves to on `PATH`. Kept in this
 * script rather than the SDK because #1654's file ownership scopes the
 * render-profile measurement lane to `scripts/evaluate-skills.ts` alone.
 */
async function resolveNightgaugeBinary(): Promise<string> {
  const envBin = process.env.NIGHTGAUGE_BIN;
  if (envBin) {
    try {
      await fs.access(envBin);
      return envBin;
    } catch {
      // fall through to the other candidates
    }
  }
  const repoRootBin = path.join(REPO_ROOT, "bin", "nightgauge");
  try {
    await fs.access(repoRootBin);
    return repoRootBin;
  } catch {
    // fall through to PATH resolution
  }
  return "nightgauge";
}

/**
 * Render one stage's skill at the given profile via
 * `nightgauge skill render --stage <stage> --profile <profile>`, exactly the
 * command #1654's issue body names. Throws (rather than falling back
 * silently) on a non-zero exit — a broken render is a measurement-lane
 * defect the caller should see, not a scenario prompt quietly missing its
 * skill text.
 */
async function renderSkillProfile(
  bin: string,
  stage: string,
  profile: RenderProfile
): Promise<string> {
  const { stdout } = await execFileAsync(bin, [
    "skill",
    "render",
    "--stage",
    stage,
    "--profile",
    profile,
    "--skills-root",
    path.join(REPO_ROOT, "skills"),
  ]);
  return stdout;
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

  if (args.renderProfile) {
    console.log(
      `Render profile: ${args.renderProfile} — prepending ` +
        `\`nightgauge skill render --profile ${args.renderProfile}\` output to each scenario prompt.`
    );
    const bin = await resolveNightgaugeBinary();
    const renderedByStage = new Map<string, string>();
    for (const scenario of scenarios) {
      if (!renderedByStage.has(scenario.skill)) {
        try {
          renderedByStage.set(
            scenario.skill,
            await renderSkillProfile(bin, scenario.skill, args.renderProfile)
          );
        } catch (err) {
          // A skill this harness evaluates but the Go binary cannot render
          // (e.g. "check-triage", which is not a pipeline stage) is not a
          // measurement-lane failure — skip it and leave that scenario's
          // prompt as the bare scenario text, same as --render-profile unset.
          // Anything else (a stale binary without --profile, a broken render)
          // is: skipping it would measure bare prompts and report them as the
          // profile, which is how a live run silently compared nothing.
          if (!String((err as Error).message).includes("no skill directory for stage")) {
            throw new Error(
              `could not render "${scenario.skill}" at profile "${args.renderProfile}" with ${bin} ` +
                `(set NIGHTGAUGE_BIN to a current build): ${(err as Error).message}`,
              { cause: err }
            );
          }
          console.error(
            `WARNING: could not render "${scenario.skill}" at profile "${args.renderProfile}": ` +
              `${(err as Error).message}`
          );
          renderedByStage.set(scenario.skill, "");
        }
      }
      const rendered = renderedByStage.get(scenario.skill);
      if (rendered) {
        scenario.prompt = `${rendered}\n\n---\n\n${scenario.prompt}`;
      }
    }
  }

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

  if (args.renderProfile) {
    // Tag every recorded cell with the profile this run measured — the
    // schema itself is EvalRecorder's (outside #1654's file ownership), so
    // this stamps the JSONL after the fact rather than growing the SDK's
    // record shape. `render_profile: compact` (or "full") lands on every
    // line, which is what #1660-#1664's cross-profile comparison reads.
    const raw = await fs.readFile(recordPath, "utf-8");
    const tagged = raw
      .split("\n")
      .filter((line) => line.length > 0)
      .map((line) => JSON.stringify({ ...JSON.parse(line), render_profile: args.renderProfile }))
      .join("\n");
    await fs.writeFile(recordPath, tagged + "\n");
  }

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
