/**
 * StageGraph tests — verify manifest parsing, producer lookup, and
 * fallback parity. Issue #3239.
 */

import { describe, expect, it } from "vitest";
import {
  DEV_FALLBACK_PRODUCERS,
  StageGraph,
  loadStageGraphFromManifests,
  normalizePathToPattern,
  parseStageManifest,
} from "../../src/orchestrator/StageGraph.js";

describe("normalizePathToPattern", () => {
  it("collapses single-digit issue numbers", () => {
    expect(normalizePathToPattern(".nightgauge/pipeline/planning-7.json")).toBe(
      ".nightgauge/pipeline/planning-{N}.json"
    );
  });

  it("collapses multi-digit issue numbers", () => {
    expect(normalizePathToPattern(".nightgauge/pipeline/planning-12345.json")).toBe(
      ".nightgauge/pipeline/planning-{N}.json"
    );
  });

  it("preserves glob wildcards in paths", () => {
    expect(normalizePathToPattern(".nightgauge/plans/42-thing.md")).toBe(
      ".nightgauge/plans/{N}-thing.md"
    );
  });
});

describe("parseStageManifest", () => {
  it("returns null when no inputs/outputs declared", () => {
    expect(parseStageManifest("feature-dev", "name: feature-dev\nallowed-tools: Read")).toBeNull();
  });

  it("parses indented list-of-scalars frontmatter", () => {
    const fm = `name: feature-planning
inputs:
  - .nightgauge/pipeline/issue-{N}.json
outputs:
  - .nightgauge/pipeline/planning-{N}.json
  - .nightgauge/plans/{N}-*.md`;

    const parsed = parseStageManifest("feature-planning", fm);
    expect(parsed).not.toBeNull();
    expect(parsed?.inputs).toEqual([".nightgauge/pipeline/issue-{N}.json"]);
    expect(parsed?.outputs).toEqual([
      ".nightgauge/pipeline/planning-{N}.json",
      ".nightgauge/plans/{N}-*.md",
    ]);
    expect(parsed?.normalizedOutputs).toEqual(parsed?.outputs);
  });

  it("stops at sibling keys at the same indent", () => {
    const fm = `inputs:
  - a.json
outputs:
  - b.json
allowed-tools: Read`;
    const parsed = parseStageManifest("feature-dev", fm);
    expect(parsed?.outputs).toEqual(["b.json"]);
  });
});

describe("StageGraph.getProducingStage", () => {
  it("resolves exact-match patterns to the producer", () => {
    const g = StageGraph.fromFallback();
    const producer = g.getProducingStage(".nightgauge/pipeline/planning-42.json");
    expect(producer?.stage).toBe("feature-planning");
    expect(producer?.name).toBe("Feature Planning");
  });

  it("resolves glob patterns (plans/*.md)", () => {
    const g = StageGraph.fromFallback();
    const producer = g.getProducingStage(".nightgauge/plans/42-photo-upload.md");
    expect(producer?.stage).toBe("feature-planning");
  });

  it("returns null for unknown files", () => {
    const g = StageGraph.fromFallback();
    expect(g.getProducingStage(".nightgauge/something/random.json")).toBeNull();
  });

  it("returns null when called with an empty path", () => {
    const g = StageGraph.fromFallback();
    expect(g.getProducingStage("")).toBeNull();
  });
});

describe("StageGraph manifest/fallback parity", () => {
  it("every fallback pattern resolves to a producer", () => {
    const g = StageGraph.fromFallback();
    for (const { pattern, stage } of DEV_FALLBACK_PRODUCERS) {
      // Substitute a digit for {N} so the lookup goes through the same
      // normalization path real callers use.
      const concretePath = pattern.replace("{N}", "42");
      const producer = g.getProducingStage(concretePath);
      expect(producer?.stage).toBe(stage);
    }
  });

  it("loadStageGraphFromManifests falls back when no manifests present", () => {
    const fakeFs = {
      existsSync: () => false,
      readFileSync: () => "",
    };
    const g = loadStageGraphFromManifests("/nonexistent", fakeFs);
    expect(g.source).toBe("fallback");
  });

  it("loadStageGraphFromManifests parses manifests when present", () => {
    const manifests: Record<string, string> = {
      "/skills/nightgauge-issue-pickup/SKILL.md": `---
name: nightgauge-issue-pickup
inputs: []
outputs:
  - .nightgauge/pipeline/issue-{N}.json
---
body`,
      "/skills/nightgauge-feature-planning/SKILL.md": `---
name: nightgauge-feature-planning
inputs:
  - .nightgauge/pipeline/issue-{N}.json
outputs:
  - .nightgauge/pipeline/planning-{N}.json
  - .nightgauge/plans/{N}-*.md
---
body`,
    };

    const fakeFs = {
      existsSync: (p: string) => Object.prototype.hasOwnProperty.call(manifests, p),
      readFileSync: (p: string) => manifests[p] ?? "",
    };

    const g = loadStageGraphFromManifests("/skills", fakeFs);
    expect(g.source).toBe("manifests");
    expect(g.getProducingStage(".nightgauge/pipeline/planning-7.json")?.stage).toBe(
      "feature-planning"
    );
    expect(g.getProducingStage(".nightgauge/pipeline/issue-7.json")?.stage).toBe("issue-pickup");
  });
});
