/**
 * Tests for the SkillEvalHarness orchestrator + mock runner + loaders
 * (Issue #3814). Mock-mode only — no live model calls.
 */

import { fileURLToPath } from "node:url";
import * as path from "node:path";
import { describe, it, expect } from "vitest";
import { SkillEvalHarness } from "../../src/eval/SkillEvalHarness.js";
import { MockModelRunner, type MockFixtureMap } from "../../src/eval/modelRunner.js";
import { loadScenarios, loadFixtures, type DirReader } from "../../src/eval/loader.js";
import type { EvalScenario } from "../../src/eval/schemas.js";

const TS = "2026-05-30T00:00:00.000Z";

// Repo root is four levels up from this test file:
// packages/nightgauge-sdk/tests/eval/ -> repo root.
const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");
const SCENARIOS_DIR = path.join(REPO_ROOT, "evals/scenarios");
const FIXTURES_DIR = path.join(REPO_ROOT, "evals/fixtures");

const scenarioPass: EvalScenario = {
  id: "demo-pass",
  skill: "pr-create",
  description: "demo",
  failure_mode: "demo",
  prompt: "do the thing",
  assertions: [{ type: "contains", value: "ok" }],
};

const scenarioFail: EvalScenario = {
  id: "demo-fail",
  skill: "pr-create",
  description: "demo",
  failure_mode: "demo",
  prompt: "do the thing",
  assertions: [{ type: "contains", value: "never-present" }],
};

function fixtures(text: string): MockFixtureMap {
  return {
    "demo-pass": { haiku: { text }, sonnet: { text }, opus: { text } },
    "demo-fail": { haiku: { text }, sonnet: { text }, opus: { text } },
  };
}

describe("SkillEvalHarness.run — matrix expansion", () => {
  it("expands the full (scenario × model) matrix", async () => {
    const harness = new SkillEvalHarness(new MockModelRunner(fixtures("ok")));
    const report = await harness.run({
      scenarios: [scenarioPass, scenarioFail],
      models: ["haiku", "sonnet", "opus"],
      timestamp: TS,
    });
    expect(report.cells).toHaveLength(6); // 2 scenarios × 3 tiers
    expect(report.mode).toBe("mock");
    expect(report.timestamp).toBe(TS);
  });

  it("aggregates per-cell verdicts into the summary", async () => {
    const harness = new SkillEvalHarness(new MockModelRunner(fixtures("ok")));
    const report = await harness.run({
      scenarios: [scenarioPass, scenarioFail],
      models: ["haiku", "sonnet", "opus"],
      timestamp: TS,
    });
    expect(report.summary.total).toBe(6);
    expect(report.summary.passed).toBe(3); // demo-pass across 3 tiers
    expect(report.summary.failed).toBe(3); // demo-fail across 3 tiers
    expect(report.summary.errored).toBe(0);
  });

  it("records the concrete version label per tier", async () => {
    const harness = new SkillEvalHarness(new MockModelRunner(fixtures("ok")));
    const report = await harness.run({
      scenarios: [scenarioPass],
      models: ["opus"],
      timestamp: TS,
    });
    expect(report.cells[0].model_version_label).toBe("Opus 4.8");
  });

  it("honors a scenario's own models subset", async () => {
    const haikuOnly: EvalScenario = { ...scenarioPass, id: "demo-pass", models: ["haiku"] };
    const harness = new SkillEvalHarness(new MockModelRunner(fixtures("ok")));
    const report = await harness.run({
      scenarios: [haikuOnly],
      models: ["haiku", "sonnet", "opus"],
      timestamp: TS,
    });
    expect(report.cells).toHaveLength(1);
    expect(report.cells[0].model).toBe("haiku");
  });

  it("marks a cell as error when the runner throws (missing fixture)", async () => {
    const harness = new SkillEvalHarness(new MockModelRunner({}));
    const report = await harness.run({
      scenarios: [scenarioPass],
      models: ["haiku"],
      timestamp: TS,
    });
    expect(report.cells[0].verdict).toBe("error");
    expect(report.summary.errored).toBe(1);
    expect(report.cells[0].error).toContain("missing mock fixture");
  });
});

describe("loadScenarios / loadFixtures — injected reader", () => {
  const reader: DirReader = {
    async listJson(dir) {
      if (dir.endsWith("pr-create")) return [`${dir}/a.json`, `${dir}/b.json`];
      return [];
    },
    async readFile(filePath) {
      if (filePath.includes("scenarios") && filePath.endsWith("a.json")) {
        return JSON.stringify({ ...scenarioPass, id: "a-scenario" });
      }
      if (filePath.includes("scenarios") && filePath.endsWith("b.json")) {
        return JSON.stringify({ ...scenarioFail, id: "b-scenario" });
      }
      // fixtures
      return JSON.stringify({ haiku: { text: "ok" } });
    },
  };

  it("loads and validates scenarios for a skill", async () => {
    const scenarios = await loadScenarios({ skills: ["pr-create"], reader });
    expect(scenarios.map((s) => s.id)).toEqual(["a-scenario", "b-scenario"]);
  });

  it("rejects duplicate scenario ids", async () => {
    const dupReader: DirReader = {
      listJson: async (dir) =>
        dir.endsWith("pr-create") ? [`${dir}/a.json`, `${dir}/b.json`] : [],
      readFile: async () => JSON.stringify({ ...scenarioPass, id: "same" }),
    };
    await expect(loadScenarios({ skills: ["pr-create"], reader: dupReader })).rejects.toThrow(
      /duplicate scenario id/
    );
  });

  it("rejects a scenario whose skill mismatches its directory", async () => {
    const badReader: DirReader = {
      listJson: async (dir) => (dir.endsWith("pr-merge") ? [`${dir}/x.json`] : []),
      readFile: async () => JSON.stringify({ ...scenarioPass, id: "x", skill: "pr-create" }),
    };
    await expect(loadScenarios({ skills: ["pr-merge"], reader: badReader })).rejects.toThrow(
      /declares skill/
    );
  });

  it("loads fixtures keyed by scenario id", async () => {
    const map = await loadFixtures({ skills: ["pr-create"], reader });
    expect(Object.keys(map)).toEqual(["a", "b"]);
    expect(map["a"].haiku?.text).toBe("ok");
  });
});

describe("end-to-end with disk scenarios + fixtures (mock mode)", () => {
  it("produces a complete green matrix over the shipped scenarios", async () => {
    const scenarios = await loadScenarios({ scenariosDir: SCENARIOS_DIR });
    const fixtureMap = await loadFixtures({ fixturesDir: FIXTURES_DIR });
    expect(scenarios.length).toBeGreaterThanOrEqual(18);

    const harness = new SkillEvalHarness(new MockModelRunner(fixtureMap));
    const report = await harness.run({
      scenarios,
      models: ["haiku", "sonnet", "opus"],
      timestamp: TS,
    });

    // Every shipped scenario must pass on every tier in mock mode — the mock
    // baseline is all-green so live divergence is what surfaces as a regression.
    expect(report.summary.errored).toBe(0);
    const failing = report.cells.filter((c) => c.verdict !== "pass");
    expect(failing, JSON.stringify(failing, null, 2)).toHaveLength(0);
  });

  it("ships at least 3 scenarios per pipeline skill", async () => {
    const scenarios = await loadScenarios({ scenariosDir: SCENARIOS_DIR });
    const counts = new Map<string, number>();
    for (const s of scenarios) counts.set(s.skill, (counts.get(s.skill) ?? 0) + 1);
    for (const skill of [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ]) {
      expect(counts.get(skill) ?? 0, `skill ${skill}`).toBeGreaterThanOrEqual(3);
    }
  });
});
