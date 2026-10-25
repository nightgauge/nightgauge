/**
 * Cross-Model Skill Evaluation Harness — regression scenarios generated from a
 * diagnosis whose root cause was a skill instruction or agent configuration.
 *
 * A product-code diagnosis leaves a test behind (`fix.test`, enforced by
 * `nightgauge triage record`). A skill-caused one used to leave nothing:
 * `evals/scenarios/` was hand-authored only. This module turns the diagnosis's
 * structured `skill_regression` block into a decision-form scenario plus mock
 * fixture, and writes them only after proving the scenario
 *
 * 1. passes on the corrected decision (the mock matrix stays green),
 * 2. fails on the non-answer sentinel (it can go red at all), and
 * 3. fails on the decision the pre-fix skill was observed making (it WAS red
 *    before the fix).
 *
 * The pre-fix check replays the recorded pre-fix decision rather than running
 * the model against the old SKILL.md: the live runner sends only the scenario
 * prompt and loads no skill, so a live re-run against a pre-fix skill is not
 * something the harness can do (decisions.md ADR-002).
 *
 * @see Issue #1287
 * @see docs/SKILL_EVALUATION.md § Generated scenarios
 */

import * as fs from "fs/promises";
import * as path from "path";
import { z } from "zod";
import { evaluateAssertions } from "./assertions.js";
import { DEFAULT_FIXTURES_DIR, DEFAULT_SCENARIOS_DIR, loadScenarios } from "./loader.js";
import type { MockFixture } from "./modelRunner.js";
import { TIER_BANDS, type TierBand } from "./tierBands.js";
import {
  EVAL_SKILLS,
  EvalScenarioSchema,
  type EvalAssertion,
  type EvalScenario,
} from "./schemas.js";

/** The non-answer every shipped scenario must fail on (SkillEvalHarness.test.ts). */
export const NON_ANSWER_SENTINEL = "I don't know.";

/** Root-cause kinds; mirrors `internal/triage` `Cause*` constants. */
export const CAUSE_KINDS = ["product_code", "skill_instruction", "agent_config"] as const;

const Scalar = z.union([z.string().min(1), z.number(), z.boolean()]);
type DecisionValue = z.infer<typeof Scalar>;

const Kebab = z.string().regex(/^[a-z0-9]+(-[a-z0-9]+)*$/, "must be kebab-case ([a-z0-9-])");

/**
 * The block a skill-caused diagnosis carries. Shape owner for both producers
 * (the check-triage record and retro findings); the Go record stores it raw.
 */
export const SkillRegressionSchema = z.object({
  /** Skill directory name under evals/scenarios/, e.g. "check-triage". */
  skill: z.string().min(1),
  /** Scenario slug; the id becomes `<skill-prefix>-<slug>`. */
  slug: Kebab,
  description: z.string().min(1),
  /** What the pre-fix skill did wrong. */
  failure_mode: z.string().min(1),
  /** The situation the skill faced, written as the scenario prompt body. */
  condition: z.string().min(1),
  /** Enumerated options per decision field; at least two per field. */
  decision: z.record(Kebab.or(z.string().regex(/^[a-z_][a-z0-9_]*$/)), z.array(Scalar).min(2)),
  /** The decision the fixed skill makes. */
  corrected: z.record(z.string(), Scalar),
  /** Prose the fixed skill gives before its decision block (fixture text). */
  corrected_rationale: z.string().min(1),
  /** The decision the pre-fix skill was observed making. */
  pre_fix: z.record(z.string(), Scalar),
  /** Verbatim transcript/log excerpt showing the pre-fix decision. */
  pre_fix_evidence: z.string().min(1),
});
export type SkillRegression = z.infer<typeof SkillRegressionSchema>;

/** A diagnosis as either producer writes it. Other keys are ignored. */
export const ScenarioDiagnosisSchema = z
  .object({
    cause: z.object({ kind: z.enum(CAUSE_KINDS) }),
    skill_regression: SkillRegressionSchema.optional(),
  })
  .superRefine((d, ctx) => {
    if (d.cause.kind !== "product_code" && d.skill_regression === undefined) {
      ctx.addIssue({
        code: "custom",
        path: ["skill_regression"],
        message: `required when cause.kind is "${d.cause.kind}"`,
      });
    }
  });
export type ScenarioDiagnosis = z.infer<typeof ScenarioDiagnosisSchema>;

export interface GeneratedScenario {
  scenario: EvalScenario;
  fixture: Record<TierBand, MockFixture>;
  /** Repo-relative paths the writer uses. */
  scenarioPath: string;
  fixturePath: string;
}

export interface GenerationCheck {
  name: string;
  passed: boolean;
  detail?: string;
}

/** `verified` is a dry run that passed every gate and wrote nothing. */
export type GenerationStatus = "written" | "verified" | "skipped" | "rejected";

export interface GenerationReport {
  status: GenerationStatus;
  reason?: string;
  scenario?: EvalScenario;
  fixture?: GeneratedScenario["fixture"];
  scenario_path?: string;
  fixture_path?: string;
  checks: GenerationCheck[];
  dry_run: boolean;
}

/** `check-triage` → `ct`; matches every hand-authored id prefix. */
export function skillIdPrefix(skill: string): string {
  return skill
    .split("-")
    .filter((p) => p.length > 0)
    .map((p) => p[0])
    .join("");
}

const FENCE = "```";

function renderAnswer(prose: string, decision: Record<string, DecisionValue>): string {
  return `${prose}\n\n${FENCE}json\n${JSON.stringify(decision)}\n${FENCE}`;
}

function renderOptions(decision: SkillRegression["decision"]): string {
  const fields = Object.entries(decision).map(
    ([key, options]) =>
      `${JSON.stringify(key)}: ${options.map((o) => JSON.stringify(o)).join(" | ")}`
  );
  return `{${fields.join(", ")}}`;
}

/** Derive the scenario and fixture. Pure; does not verify. */
export function generateScenarioFromDiagnosis(regression: SkillRegression): GeneratedScenario {
  const id = `${skillIdPrefix(regression.skill)}-${regression.slug}`;
  const assertions: EvalAssertion[] = Object.entries(regression.corrected).map(([key, value]) => ({
    type: "json_path_equals",
    path: key,
    value,
    scope: "last_fenced_block",
    lang: "json",
    description: `decides ${key} = ${JSON.stringify(value)}`,
  }));
  const scenario: EvalScenario = {
    id,
    skill: regression.skill as EvalScenario["skill"],
    description: regression.description,
    failure_mode: regression.failure_mode,
    prompt:
      `${regression.condition.trim()} End your answer with a single ${FENCE}json fenced block ` +
      `containing exactly this object, with one value chosen for each field: ` +
      renderOptions(regression.decision),
    assertions,
  };
  const cell: MockFixture = {
    text: renderAnswer(regression.corrected_rationale, regression.corrected),
    exit_code: 0,
  };
  return {
    scenario,
    fixture: Object.fromEntries(TIER_BANDS.map((band) => [band, { ...cell }])) as Record<
      TierBand,
      MockFixture
    >,
    scenarioPath: path.join(DEFAULT_SCENARIOS_DIR, regression.skill, `${regression.slug}.json`),
    fixturePath: path.join(DEFAULT_FIXTURES_DIR, regression.skill, `${id}.json`),
  };
}

/**
 * Every gate a generated scenario must pass before it is written. A failing
 * check names why; nothing is written unless all pass.
 */
export function verifyGeneratedScenario(
  generated: GeneratedScenario,
  regression: SkillRegression,
  existingIds: ReadonlySet<string> = new Set()
): GenerationCheck[] {
  const checks: GenerationCheck[] = [];
  const check = (name: string, passed: boolean, detail?: string): void => {
    checks.push(detail === undefined ? { name, passed } : { name, passed, detail });
  };
  const { scenario } = generated;

  check(
    "skill_is_evaluated",
    (EVAL_SKILLS as readonly string[]).includes(regression.skill),
    `scenarios load only for ${EVAL_SKILLS.join(", ")}`
  );

  const keys = Object.keys(regression.decision);
  const sameKeys = (o: Record<string, unknown>): boolean =>
    Object.keys(o).length === keys.length && keys.every((k) => k in o);
  const inOptions = (o: Record<string, DecisionValue>): string[] =>
    keys.filter((k) => !regression.decision[k].some((opt) => opt === o[k]));
  const badCorrected = sameKeys(regression.corrected) ? inOptions(regression.corrected) : keys;
  const badPreFix = sameKeys(regression.pre_fix) ? inOptions(regression.pre_fix) : keys;
  check(
    "decisions_within_options",
    badCorrected.length === 0 && badPreFix.length === 0,
    badCorrected.length + badPreFix.length === 0
      ? undefined
      : `corrected/pre_fix must choose one listed option for every decision field; ` +
          `bad corrected: [${badCorrected.join(", ")}], bad pre_fix: [${badPreFix.join(", ")}]`
  );
  check(
    "pre_fix_differs_from_corrected",
    keys.some((k) => regression.pre_fix[k] !== regression.corrected[k]),
    "the pre-fix decision must differ from the corrected one on at least one field"
  );

  const schema = EvalScenarioSchema.safeParse(scenario);
  check("scenario_schema_valid", schema.success, schema.success ? undefined : schema.error.message);

  check(
    "id_is_new",
    !existingIds.has(scenario.id),
    `scenario id "${scenario.id}" already exists — the generator never overwrites`
  );

  const fixtureEval = evaluateAssertions(generated.fixture.sonnet, scenario.assertions);
  check(
    "fixture_passes",
    fixtureEval.passed,
    fixtureEval.passed ? undefined : fixtureEval.failures.map((f) => f.reason).join("; ")
  );

  const sentinel = evaluateAssertions(
    { text: NON_ANSWER_SENTINEL, exit_code: 0 },
    scenario.assertions
  );
  check(
    "non_answer_fails",
    !sentinel.passed,
    `the scenario must fail on ${JSON.stringify(NON_ANSWER_SENTINEL)} or it cannot go red`
  );

  const preFix = evaluateAssertions(
    { text: renderAnswer(regression.pre_fix_evidence, regression.pre_fix), exit_code: 0 },
    scenario.assertions
  );
  check(
    "red_on_pre_fix",
    !preFix.passed,
    "the scenario must fail on the decision the pre-fix skill was observed making"
  );

  return checks;
}

async function exists(p: string): Promise<boolean> {
  try {
    await fs.access(p);
    return true;
  } catch {
    return false;
  }
}

/**
 * Validate a diagnosis, derive and verify its scenario, and (unless `dryRun`)
 * write the scenario and fixture under `root`. Product-code causes are skipped.
 * Throws only on input that does not match `ScenarioDiagnosisSchema`.
 */
export async function generateScenarioFromInput(
  input: unknown,
  options: { root?: string; dryRun?: boolean } = {}
): Promise<GenerationReport> {
  const root = options.root ?? ".";
  const dryRun = options.dryRun ?? false;
  const parsed = ScenarioDiagnosisSchema.safeParse(input);
  if (!parsed.success) {
    throw new Error(`invalid diagnosis: ${parsed.error.message}`);
  }
  const diagnosis = parsed.data;
  if (diagnosis.cause.kind === "product_code" || diagnosis.skill_regression === undefined) {
    return {
      status: "skipped",
      reason: "cause.kind is product_code — the regression guard is fix.test, not a scenario",
      checks: [],
      dry_run: dryRun,
    };
  }
  const regression = diagnosis.skill_regression;
  const generated = generateScenarioFromDiagnosis(regression);
  const scenarioAbs = path.join(root, generated.scenarioPath);
  const fixtureAbs = path.join(root, generated.fixturePath);

  const existingIds = new Set<string>();
  if ((EVAL_SKILLS as readonly string[]).includes(regression.skill)) {
    const loaded = await loadScenarios({ scenariosDir: path.join(root, DEFAULT_SCENARIOS_DIR) });
    for (const s of loaded) existingIds.add(s.id);
  }
  if ((await exists(scenarioAbs)) || (await exists(fixtureAbs))) {
    existingIds.add(generated.scenario.id);
  }

  const checks = verifyGeneratedScenario(generated, regression, existingIds);
  const failed = checks.filter((c) => !c.passed);
  const report: GenerationReport = {
    status: failed.length > 0 ? "rejected" : dryRun ? "verified" : "written",
    scenario: generated.scenario,
    fixture: generated.fixture,
    scenario_path: generated.scenarioPath,
    fixture_path: generated.fixturePath,
    checks,
    dry_run: dryRun,
  };
  if (failed.length > 0) {
    report.reason = failed.map((c) => `${c.name}: ${c.detail ?? "failed"}`).join("; ");
    return report;
  }
  if (!dryRun) {
    await fs.mkdir(path.dirname(scenarioAbs), { recursive: true });
    await fs.mkdir(path.dirname(fixtureAbs), { recursive: true });
    await fs.writeFile(scenarioAbs, JSON.stringify(generated.scenario, null, 2) + "\n", {
      flag: "wx",
    });
    await fs.writeFile(fixtureAbs, JSON.stringify(generated.fixture, null, 2) + "\n", {
      flag: "wx",
    });
  }
  return report;
}
