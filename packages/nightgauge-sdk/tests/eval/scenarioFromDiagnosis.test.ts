/**
 * Regression scenarios generated from a skill-caused diagnosis (#1287).
 *
 * Each case drives the full generate → verify → write path against a temp
 * root, and the happy path re-loads the written files through the real loader
 * and harness to prove they are runnable, green on the fixture, and red on the
 * non-answer sentinel.
 */

import * as fs from "fs/promises";
import * as os from "os";
import * as path from "path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { evaluateAssertions } from "../../src/eval/assertions.js";
import { loadFixtures, loadScenarios } from "../../src/eval/loader.js";
import { MockModelRunner } from "../../src/eval/modelRunner.js";
import {
  generateScenarioFromDiagnosis,
  generateScenarioFromInput,
  verifyGeneratedScenario,
  NON_ANSWER_SENTINEL,
  skillIdPrefix,
  type SkillRegression,
} from "../../src/eval/scenarioFromDiagnosis.js";
import { SkillEvalHarness } from "../../src/eval/SkillEvalHarness.js";

const regression = (over: Partial<SkillRegression> = {}): SkillRegression => ({
  skill: "check-triage",
  slug: "unreproduced-files-spike",
  description: "Check triage files a spike when nothing reproduced.",
  failure_mode: "Proposed a timeout increase for a failure it never reproduced.",
  condition:
    'You are triaging the failing check "E2E". Twenty local runs passed and the failure never recurred. What do you do?',
  decision: {
    action: ["file_spike", "propose_fix", "increase_timeout"],
    proposes_code_change: [true, false],
  },
  corrected: { action: "file_spike", proposes_code_change: false },
  corrected_rationale: "Nothing reproduced, so I file a type:spike and stop.",
  pre_fix: { action: "increase_timeout", proposes_code_change: true },
  pre_fix_evidence: 'transcript: "I\'ll bump the timeout to 120s"',
  ...over,
});

const diagnosis = (over: Partial<SkillRegression> = {}, kind = "skill_instruction") => ({
  cause: { kind },
  skill_regression: regression(over),
});

let root: string;
beforeEach(async () => {
  root = await fs.mkdtemp(path.join(os.tmpdir(), "scenario-gen-"));
});
afterEach(async () => {
  await fs.rm(root, { recursive: true, force: true });
});

const listAll = async (): Promise<string[]> => {
  const out: string[] = [];
  const walk = async (d: string): Promise<void> => {
    for (const e of await fs.readdir(d, { withFileTypes: true }).catch(() => [])) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) await walk(p);
      else out.push(path.relative(root, p));
    }
  };
  await walk(root);
  return out;
};

describe("skillIdPrefix", () => {
  it("matches the hand-authored id prefixes", () => {
    expect(skillIdPrefix("check-triage")).toBe("ct");
    expect(skillIdPrefix("feature-dev")).toBe("fd");
    expect(skillIdPrefix("pr-merge")).toBe("pm");
  });
});

describe("generateScenarioFromDiagnosis", () => {
  it("derives positive json_path_equals assertions over the last json block", () => {
    const g = generateScenarioFromDiagnosis(regression());
    expect(g.scenario.id).toBe("ct-unreproduced-files-spike");
    expect(g.scenario.assertions.every((a) => a.type === "json_path_equals")).toBe(true);
    expect(g.scenario.prompt).toContain(
      '"action": "file_spike" | "propose_fix" | "increase_timeout"'
    );
    expect(g.scenarioPath).toBe("evals/scenarios/check-triage/unreproduced-files-spike.json");
    expect(g.fixturePath).toBe("evals/fixtures/check-triage/ct-unreproduced-files-spike.json");
  });
});

describe("generateScenarioFromInput", () => {
  it("skips a product-code cause and writes nothing", async () => {
    const report = await generateScenarioFromInput({ cause: { kind: "product_code" } }, { root });
    expect(report.status).toBe("skipped");
    expect(await listAll()).toEqual([]);
  });

  it("rejects a skill cause with no skill_regression block as invalid input", async () => {
    await expect(
      generateScenarioFromInput({ cause: { kind: "agent_config" } }, { root })
    ).rejects.toThrow(/skill_regression/);
  });

  it("writes a scenario that loads, passes in mock mode, and fails the sentinel", async () => {
    const report = await generateScenarioFromInput(diagnosis(), { root });
    expect(report.status, report.reason).toBe("written");
    expect(report.checks.every((c) => c.passed)).toBe(true);

    const scenarios = await loadScenarios({
      skills: ["check-triage"],
      scenariosDir: path.join(root, "evals/scenarios"),
    });
    const fixtures = await loadFixtures({
      skills: ["check-triage"],
      fixturesDir: path.join(root, "evals/fixtures"),
    });
    expect(scenarios.map((s) => s.id)).toEqual(["ct-unreproduced-files-spike"]);

    const green = await new SkillEvalHarness(new MockModelRunner(fixtures)).run({
      scenarios,
      models: ["haiku", "sonnet", "opus"],
      timestamp: "2026-10-25T00:00:00Z",
    });
    expect(green.cells.every((c) => c.verdict === "pass")).toBe(true);

    const red = evaluateAssertions({ text: NON_ANSWER_SENTINEL }, scenarios[0].assertions);
    expect(red.passed).toBe(false);
  });

  it("dry-run verifies but writes nothing", async () => {
    const report = await generateScenarioFromInput(diagnosis(), { root, dryRun: true });
    expect(report.status).toBe("verified");
    expect(report.dry_run).toBe(true);
    expect(await listAll()).toEqual([]);
  });

  it("never overwrites an existing scenario id", async () => {
    await generateScenarioFromInput(diagnosis(), { root });
    const before = await listAll();
    const again = await generateScenarioFromInput(diagnosis({ corrected_rationale: "different" }), {
      root,
    });
    expect(again.status).toBe("rejected");
    expect(again.reason).toMatch(/id_is_new/);
    expect(await listAll()).toEqual(before);
  });

  const rejections: Array<[string, Partial<SkillRegression>, RegExp]> = [
    [
      "pre_fix equal to corrected",
      { pre_fix: { action: "file_spike", proposes_code_change: false } },
      /pre_fix_differs_from_corrected.*red_on_pre_fix|red_on_pre_fix/,
    ],
    [
      "a corrected value outside the options",
      { corrected: { action: "rerun", proposes_code_change: false } },
      /decisions_within_options/,
    ],
    ["a skill outside EVAL_SKILLS", { skill: "retro" }, /skill_is_evaluated/],
  ];

  for (const [name, over, reason] of rejections) {
    it(`rejects ${name} and writes nothing`, async () => {
      const report = await generateScenarioFromInput(diagnosis(over), { root });
      expect(report.status).toBe("rejected");
      expect(report.reason).toMatch(reason);
      expect(await listAll()).toEqual([]);
    });
  }
});

describe("verifyGeneratedScenario", () => {
  it("fails red_on_pre_fix when the assertions cannot see the pre-fix difference", () => {
    const r = regression();
    const g = generateScenarioFromDiagnosis(r);
    // Keep only the field the pre-fix decision gets right: the scenario would
    // have been green before the fix, so it guards nothing.
    const preFixRight = { ...r, pre_fix: { action: "file_spike", proposes_code_change: true } };
    g.scenario.assertions = g.scenario.assertions.filter(
      (a) => a.type === "json_path_equals" && a.path === "action"
    );
    const checks = verifyGeneratedScenario(g, preFixRight);
    expect(checks.find((c) => c.name === "red_on_pre_fix")?.passed).toBe(false);
    expect(checks.find((c) => c.name === "fixture_passes")?.passed).toBe(true);
  });

  it("passes every gate for a well-formed diagnosis", () => {
    const r = regression();
    const checks = verifyGeneratedScenario(generateScenarioFromDiagnosis(r), r);
    expect(checks.filter((c) => !c.passed)).toEqual([]);
  });
});
