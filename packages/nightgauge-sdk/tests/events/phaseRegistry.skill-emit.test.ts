/**
 * Cross-validation: PHASE_REGISTRY ↔ skill SKILL.md emits.
 *
 * Mirrors `scripts/validate-phase-markers.ts` on the test side so registry
 * drift is caught both by `npm run test` and by `bash scripts/ci-local.sh`.
 *
 * The companion test in `phaseRegistry.test.ts` covers the same six pipeline
 * stages with a slightly different shape (per-stage describe blocks). This
 * file enumerates every registry entry as its own assertion so a single drift
 * shows up as a single named test failure pointing at the missing or extra
 * phase.
 *
 * @see Issue #3263
 */
import { describe, it, expect } from "vitest";
import * as fs from "fs";
import * as path from "path";
import { PHASE_REGISTRY, type ExecutionStage } from "../../src/events/phaseRegistry.js";

const SKILLS_DIR = path.resolve(__dirname, "../../../../skills");

const STAGE_TO_SKILL_DIR: Record<ExecutionStage, string> = {
  "issue-pickup": "nightgauge-issue-pickup",
  "feature-planning": "nightgauge-feature-planning",
  "feature-dev": "nightgauge-feature-dev",
  "feature-validate": "nightgauge-feature-validate",
  "pr-create": "nightgauge-pr-create",
  "pr-merge": "nightgauge-pr-merge",
};

const MARKER_RE =
  /phase:start name="([a-z][a-z0-9-]*)" index=(\d+) total=(\d+) stage="([a-z][a-z0-9-]*)"/g;

interface ParsedMarker {
  name: string;
  index: number;
  total: number;
  stage: string;
}

function readSkillMarkers(stage: ExecutionStage): Map<string, ParsedMarker> {
  const skillFile = path.join(SKILLS_DIR, STAGE_TO_SKILL_DIR[stage], "SKILL.md");
  const content = fs.readFileSync(skillFile, "utf8");
  const out = new Map<string, ParsedMarker>();
  MARKER_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = MARKER_RE.exec(content)) !== null) {
    const [, name, indexStr, totalStr, markerStage] = m;
    if (name === "{phase-name}") continue;
    if (markerStage !== stage) continue;
    if (!out.has(name)) {
      out.set(name, {
        name,
        index: parseInt(indexStr, 10),
        total: parseInt(totalStr, 10),
        stage: markerStage,
      });
    }
  }
  return out;
}

const EXECUTION_STAGES = Object.keys(PHASE_REGISTRY) as ExecutionStage[];

describe("Phase Registry — skill emit cross-validation (Issue #3263)", () => {
  for (const stage of EXECUTION_STAGES) {
    describe(stage, () => {
      const registryPhases = PHASE_REGISTRY[stage];
      const skillMarkers = readSkillMarkers(stage);

      it(`emits exactly ${registryPhases.length} pipeline-stage phases`, () => {
        expect(skillMarkers.size).toBe(registryPhases.length);
      });

      for (const phase of registryPhases) {
        it(`emits "${phase.name}" at index ${phase.index} with total=${registryPhases.length}`, () => {
          const marker = skillMarkers.get(phase.name);
          expect(
            marker,
            `PHASE_REGISTRY.${stage}["${phase.name}"] has no matching emit in ` +
              `skills/${STAGE_TO_SKILL_DIR[stage]}/SKILL.md. Add the marker or remove the registry entry.`
          ).toBeDefined();
          if (!marker) return;
          expect(marker.index).toBe(phase.index);
          expect(marker.total).toBe(registryPhases.length);
        });
      }

      it("does not emit any phase missing from the registry", () => {
        const registryNames = new Set(registryPhases.map((p) => p.name));
        const orphaned = [...skillMarkers.keys()].filter((n) => !registryNames.has(n));
        expect(
          orphaned,
          `Skill emits phases not present in PHASE_REGISTRY[${stage}]: ${orphaned.join(", ")}. ` +
            `Add them to phaseRegistry.ts or remove the markers.`
        ).toEqual([]);
      });
    });
  }
});
