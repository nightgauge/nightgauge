import { describe, it, expect } from "vitest";
import {
  PHASE_REGISTRY,
  getPhaseTotal,
  getPhaseIndex,
  formatPhaseMarker,
  parsePhaseMarker,
  parsePhaseMarkers,
  type ExecutionStage,
  type StagePhaseDefinition,
} from "../../src/events/phaseRegistry.js";

/** All execution stages (excludes bookend stages) */
const EXECUTION_STAGES: ExecutionStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

/** Expected phase counts per stage */
const EXPECTED_PHASE_COUNTS: Record<ExecutionStage, number> = {
  "issue-pickup": 14, // added blocked-dependency-gate phase (Issue #231)
  "feature-planning": 14, // added recall-prior-decisions phase (Issue #3593)
  "feature-dev": 18, // added recall-architectural-constraints phase (Issue #3594)
  "feature-validate": 23, // added verify-ui-gate phase (Issue #4193)
  "pr-create": 14, // added scope-drift-gate phase (Issue #3040)
  "pr-merge": 14, // added retrospective-feedback phase (Issue #14)
};

describe("Phase Registry", () => {
  describe("registry completeness", () => {
    it("should contain all 6 execution stages", () => {
      const registeredStages = Object.keys(PHASE_REGISTRY);
      expect(registeredStages).toHaveLength(6);
      for (const stage of EXECUTION_STAGES) {
        expect(PHASE_REGISTRY[stage]).toBeDefined();
      }
    });

    it("should not contain bookend stages", () => {
      const registeredStages = Object.keys(PHASE_REGISTRY);
      expect(registeredStages).not.toContain("pipeline-start");
      expect(registeredStages).not.toContain("pipeline-finish");
    });
  });

  describe("phase counts", () => {
    const cases = Object.entries(EXPECTED_PHASE_COUNTS) as [ExecutionStage, number][];

    cases.forEach(([stage, expectedCount]) => {
      it(`${stage} should have ${expectedCount} phases`, () => {
        expect(PHASE_REGISTRY[stage]).toHaveLength(expectedCount);
      });
    });
  });

  describe("phase name format", () => {
    const kebabCasePattern = /^[a-z][a-z0-9-]*$/;

    for (const stage of EXECUTION_STAGES) {
      it(`${stage} phase names should be kebab-case`, () => {
        for (const phase of PHASE_REGISTRY[stage]) {
          expect(phase.name).toMatch(kebabCasePattern);
        }
      });
    }
  });

  describe("phase index sequencing", () => {
    for (const stage of EXECUTION_STAGES) {
      it(`${stage} indices should be sequential from 0 to N-1`, () => {
        const phases = PHASE_REGISTRY[stage];
        phases.forEach((phase: StagePhaseDefinition, i: number) => {
          expect(phase.index).toBe(i);
        });
      });
    }
  });

  describe("phase name uniqueness", () => {
    for (const stage of EXECUTION_STAGES) {
      it(`${stage} should have unique phase names`, () => {
        const names = PHASE_REGISTRY[stage].map((p: StagePhaseDefinition) => p.name);
        expect(new Set(names).size).toBe(names.length);
      });
    }
  });

  describe("getPhaseTotal", () => {
    const cases = Object.entries(EXPECTED_PHASE_COUNTS) as [ExecutionStage, number][];

    cases.forEach(([stage, expectedCount]) => {
      it(`returns ${expectedCount} for ${stage}`, () => {
        expect(getPhaseTotal(stage)).toBe(expectedCount);
      });
    });
  });

  describe("getPhaseIndex", () => {
    it("should return correct index for known phases", () => {
      expect(getPhaseIndex("feature-dev", "validate-environment")).toBe(0);
      expect(getPhaseIndex("feature-dev", "read-planning-context")).toBe(1);
      expect(getPhaseIndex("feature-dev", "implementation")).toBe(8);
      expect(getPhaseIndex("feature-dev", "output-summary")).toBe(16);
    });

    it("should return -1 for unknown phase name", () => {
      expect(getPhaseIndex("feature-dev", "nonexistent-phase")).toBe(-1);
    });

    it("should return correct index for first and last phase of each stage", () => {
      for (const stage of EXECUTION_STAGES) {
        const phases = PHASE_REGISTRY[stage];
        const first = phases[0];
        const last = phases[phases.length - 1];

        expect(getPhaseIndex(stage, first.name)).toBe(0);
        expect(getPhaseIndex(stage, last.name)).toBe(phases.length - 1);
      }
    });
  });

  describe("formatPhaseMarker", () => {
    it("should produce valid HTML comment format", () => {
      const marker = formatPhaseMarker("feature-dev", "implementation");
      expect(marker).toBe(
        '<!-- phase:start name="implementation" index=8 total=18 stage="feature-dev" -->'
      );
    });

    it("should be parseable by regex", () => {
      const marker = formatPhaseMarker("issue-pickup", "validate-environment");
      const pattern =
        /^<!-- phase:start name="([a-z][a-z0-9-]*)" index=(\d+) total=(\d+) stage="([a-z][a-z0-9-]*)" -->$/;
      const match = marker.match(pattern);

      expect(match).not.toBeNull();
      expect(match![1]).toBe("validate-environment");
      expect(match![2]).toBe("0");
      expect(match![3]).toBe("14"); // issue-pickup now has 14 phases (added blocked-dependency-gate, Issue #231)
      expect(match![4]).toBe("issue-pickup");
    });

    it("should return empty string for unknown phase", () => {
      expect(formatPhaseMarker("feature-dev", "nonexistent")).toBe("");
    });

    it("should generate correct markers for all phases in all stages", () => {
      const pattern =
        /^<!-- phase:start name="[a-z][a-z0-9-]*" index=\d+ total=\d+ stage="[a-z][a-z0-9-]*" -->$/;

      for (const stage of EXECUTION_STAGES) {
        for (const phase of PHASE_REGISTRY[stage]) {
          const marker = formatPhaseMarker(stage, phase.name);
          expect(marker).toMatch(pattern);
        }
      }
    });
  });

  describe("parsePhaseMarkers", () => {
    it("returns array of length 1 for a single marker", () => {
      const text =
        '<!-- phase:start name="implementation" index=8 total=18 stage="feature-dev" -->';
      const result = parsePhaseMarkers(text);
      expect(result).toHaveLength(1);
      expect(result[0]).toEqual({
        name: "implementation",
        index: 8,
        total: 18,
        stage: "feature-dev",
      });
    });

    it("returns all 3 markers in document order for bundled delivery", () => {
      const text = [
        '<!-- phase:start name="plan-verification" index=4 total=18 stage="feature-dev" -->',
        "Some output between markers",
        '<!-- phase:start name="implementation" index=8 total=18 stage="feature-dev" -->',
        '<!-- phase:start name="testing" index=9 total=18 stage="feature-dev" -->',
      ].join("\n");
      const result = parsePhaseMarkers(text);
      expect(result).toHaveLength(3);
      expect(result[0].name).toBe("plan-verification");
      expect(result[1].name).toBe("implementation");
      expect(result[2].name).toBe("testing");
    });

    it("returns empty array for empty string", () => {
      expect(parsePhaseMarkers("")).toEqual([]);
    });

    it("returns empty array for text with no markers", () => {
      expect(parsePhaseMarkers("just some regular output text")).toEqual([]);
    });

    it("regression: parsePhaseMarker on single-marker input still returns the parsed object", () => {
      const text =
        '<!-- phase:start name="validate-environment" index=0 total=18 stage="feature-dev" -->';
      const result = parsePhaseMarker(text);
      expect(result).not.toBeNull();
      expect(result).toEqual({
        name: "validate-environment",
        index: 0,
        total: 18,
        stage: "feature-dev",
      });
    });

    it("regression: parsePhaseMarker returns null when no marker present", () => {
      expect(parsePhaseMarker("no marker here")).toBeNull();
    });
  });

  // ---------------------------------------------------------------------------
  // Cross-validation: registry ↔ skill SKILL.md files
  //
  // This test reads the actual skill markdown files and extracts the phase
  // markers emitted by each skill, then validates that the PHASE_REGISTRY
  // matches exactly. This prevents the recurring bug where a skill adds a
  // new phase but the registry is not updated, causing N+1/N phase display.
  // ---------------------------------------------------------------------------
  describe("registry ↔ skill cross-validation", () => {
    const fs = require("fs");
    const path = require("path");

    // Map registry stage names to skill directory names
    const STAGE_TO_SKILL_DIR: Record<string, string> = {
      "issue-pickup": "nightgauge-issue-pickup",
      "feature-planning": "nightgauge-feature-planning",
      "feature-dev": "nightgauge-feature-dev",
      "feature-validate": "nightgauge-feature-validate",
      "pr-create": "nightgauge-pr-create",
      "pr-merge": "nightgauge-pr-merge",
    };

    // Regex that matches phase markers with integer indices only
    // (decimal indices like 1.5 are conditional sub-phases and are
    // intentionally excluded from the registry)
    const MARKER_RE =
      /phase:start name="([a-z][a-z0-9-]*)" index=(\d+) total=(\d+) stage="([a-z][a-z0-9-]*)"/g;

    for (const stage of EXECUTION_STAGES) {
      it(`${stage}: registry phases match skill markers`, () => {
        const skillDir = STAGE_TO_SKILL_DIR[stage];
        const skillPath = path.resolve(__dirname, "../../../../skills", skillDir, "SKILL.md");

        const content = fs.readFileSync(skillPath, "utf8");

        // Extract all unique phase markers with integer indices
        const skillPhases = new Map<string, { index: number; total: number }>();
        let match;
        while ((match = MARKER_RE.exec(content)) !== null) {
          const [, name, indexStr, totalStr, markerStage] = match;
          // Skip the template placeholder marker
          if (name === "{phase-name}") continue;
          // Only match markers for this stage
          if (markerStage !== stage) continue;
          const index = parseInt(indexStr, 10);
          const total = parseInt(totalStr, 10);
          if (!skillPhases.has(name)) {
            skillPhases.set(name, { index, total });
          }
        }

        const registryPhases = PHASE_REGISTRY[stage];
        const registryNames = registryPhases.map((p) => p.name);
        const skillNames = [...skillPhases.keys()];

        // Registry must contain every phase the skill emits
        const missingFromRegistry = skillNames.filter((n) => !registryNames.includes(n));
        expect(
          missingFromRegistry,
          `Phases in ${stage} skill but missing from PHASE_REGISTRY: ${missingFromRegistry.join(", ")}. ` +
            `Update phaseRegistry.ts to include these phases.`
        ).toEqual([]);

        // Registry must not contain phases the skill doesn't emit
        const extraInRegistry = registryNames.filter((n) => !skillNames.includes(n));
        expect(
          extraInRegistry,
          `Phases in PHASE_REGISTRY for ${stage} but not in skill: ${extraInRegistry.join(", ")}. ` +
            `Remove these from phaseRegistry.ts or add markers to the skill.`
        ).toEqual([]);

        // Total count must match
        expect(registryPhases.length).toBe(skillPhases.size);

        // Every skill marker's total= value must equal the registry length
        for (const [name, { total }] of skillPhases) {
          expect(
            total,
            `Phase "${name}" in ${stage} skill claims total=${total} but registry has ${registryPhases.length} phases. ` +
              `Update the total= values in skills/${skillDir}/SKILL.md.`
          ).toBe(registryPhases.length);
        }

        // Index ordering must match
        for (const [name, { index }] of skillPhases) {
          const registryPhase = registryPhases.find((p) => p.name === name);
          expect(
            registryPhase?.index,
            `Phase "${name}" in ${stage}: skill has index=${index} but registry has index=${registryPhase?.index}`
          ).toBe(index);
        }
      });
    }
  });
});
