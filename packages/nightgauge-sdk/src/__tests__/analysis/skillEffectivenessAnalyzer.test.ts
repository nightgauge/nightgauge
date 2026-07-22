import { describe, it, expect } from "vitest";
import { SkillEffectivenessAnalyzer } from "../../analysis/SkillEffectivenessAnalyzer.js";
import type { SkillChangeRecord } from "../../analysis/skill-effectiveness-types.js";
import type { ExecutionHistoryRecord } from "../../analysis/types.js";

// ── Test Data Factories ────────────────────────────────────────────

const SKILL_FILE = "skills/nightgauge-feature-planning/SKILL.md";
const STAGE = "feature-planning";

function makeChange(overrides: Partial<SkillChangeRecord> = {}): SkillChangeRecord {
  return {
    skillFile: SKILL_FILE,
    stage: STAGE,
    commitHash: "abc123",
    changedAt: "2026-01-15T12:00:00Z",
    ...overrides,
  };
}

function makeRecord(stage: string, success: boolean, timestamp: string): ExecutionHistoryRecord {
  return {
    issueNumber: 1,
    stage,
    success,
    retries: 0,
    inputTokens: 100,
    outputTokens: 50,
    costUsd: 0.01,
    durationMs: 1000,
    timestamp,
  };
}

/** Create N history records for a stage at a given timestamp, all with the same success */
function makeRecords(
  stage: string,
  count: number,
  success: boolean,
  timestamp: string
): ExecutionHistoryRecord[] {
  return Array.from({ length: count }, () => makeRecord(stage, success, timestamp));
}

// ── Tests ──────────────────────────────────────────────────────────

describe("SkillEffectivenessAnalyzer", () => {
  describe("analyze()", () => {
    it("returns empty entries when skillChanges is empty", () => {
      const result = SkillEffectivenessAnalyzer.analyze([], []);
      expect(result.skillChangesFound).toBe(0);
      expect(result.entries).toHaveLength(0);
    });

    it("returns empty entries when history is empty", () => {
      const result = SkillEffectivenessAnalyzer.analyze([makeChange()], []);
      expect(result.skillChangesFound).toBe(1);
      expect(result.entries).toHaveLength(1);
      expect(result.entries[0].classification).toBe("insufficient_data");
    });

    it("returns insufficient_data when before window has < 5 samples", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = makeRecords(STAGE, 4, true, "2026-01-10T00:00:00Z"); // 4 < 5
      const after = makeRecords(STAGE, 10, true, "2026-01-20T00:00:00Z");
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      expect(result.entries[0].classification).toBe("insufficient_data");
      expect(result.entries[0].confidence).toBe("insufficient_data");
    });

    it("returns insufficient_data when after window has < 5 samples", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = makeRecords(STAGE, 10, true, "2026-01-10T00:00:00Z");
      const after = makeRecords(STAGE, 3, true, "2026-01-20T00:00:00Z"); // 3 < 5
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      expect(result.entries[0].classification).toBe("insufficient_data");
    });

    it("classifies as effective when delta > 0.10", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      // Before: 5/10 = 50% success
      const before = [
        ...makeRecords(STAGE, 5, true, "2026-01-10T00:00:00Z"),
        ...makeRecords(STAGE, 5, false, "2026-01-10T00:00:00Z"),
      ];
      // After: 8/10 = 80% success — delta = +0.30 > 0.10
      const after = [
        ...makeRecords(STAGE, 8, true, "2026-01-20T00:00:00Z"),
        ...makeRecords(STAGE, 2, false, "2026-01-20T00:00:00Z"),
      ];
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      const entry = result.entries[0];
      expect(entry.classification).toBe("effective");
      expect(entry.delta).toBeCloseTo(0.3, 5);
      expect(entry.beforeWindow.successRate).toBeCloseTo(0.5, 5);
      expect(entry.afterWindow.successRate).toBeCloseTo(0.8, 5);
    });

    it("classifies as regression when delta < -0.10", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      // Before: 8/10 = 80% success
      const before = [
        ...makeRecords(STAGE, 8, true, "2026-01-10T00:00:00Z"),
        ...makeRecords(STAGE, 2, false, "2026-01-10T00:00:00Z"),
      ];
      // After: 5/10 = 50% success — delta = -0.30 < -0.10
      const after = [
        ...makeRecords(STAGE, 5, true, "2026-01-20T00:00:00Z"),
        ...makeRecords(STAGE, 5, false, "2026-01-20T00:00:00Z"),
      ];
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      const entry = result.entries[0];
      expect(entry.classification).toBe("regression");
      expect(entry.delta).toBeCloseTo(-0.3, 5);
    });

    it("classifies as neutral when delta is between -0.10 and +0.10", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      // Before: 6/10 = 60%
      const before = [
        ...makeRecords(STAGE, 6, true, "2026-01-10T00:00:00Z"),
        ...makeRecords(STAGE, 4, false, "2026-01-10T00:00:00Z"),
      ];
      // After: 7/10 = 70% — delta = +0.10 (boundary is exclusive: not > 0.10)
      const after = [
        ...makeRecords(STAGE, 7, true, "2026-01-20T00:00:00Z"),
        ...makeRecords(STAGE, 3, false, "2026-01-20T00:00:00Z"),
      ];
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      // delta exactly 0.10 → not > 0.10 → neutral
      expect(result.entries[0].classification).toBe("neutral");
    });

    it("assigns low confidence for min(before, after) between 5 and 15", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = makeRecords(STAGE, 10, true, "2026-01-10T00:00:00Z");
      const after = [
        ...makeRecords(STAGE, 8, true, "2026-01-20T00:00:00Z"),
        ...makeRecords(STAGE, 2, false, "2026-01-20T00:00:00Z"),
      ];
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      expect(result.entries[0].confidence).toBe("low");
    });

    it("assigns moderate confidence for min(before, after) > 15", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = makeRecords(STAGE, 20, true, "2026-01-10T00:00:00Z");
      const after = [
        ...makeRecords(STAGE, 16, true, "2026-01-20T00:00:00Z"),
        ...makeRecords(STAGE, 4, false, "2026-01-20T00:00:00Z"),
      ];
      const history = [...before, ...after];

      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      expect(result.entries[0].confidence).toBe("moderate");
    });

    it("handles multiple skill changes independently", () => {
      const changedAt1 = "2026-01-10T12:00:00Z";
      const changedAt2 = "2026-01-20T12:00:00Z";

      const stage1 = "feature-planning";
      const stage2 = "feature-dev";

      // Stage1 changes: before=5 success/5 total (100%), after=5 success/5 total (100%) → neutral
      const s1Before = makeRecords(stage1, 5, true, "2026-01-05T00:00:00Z");
      const s1After = makeRecords(stage1, 5, true, "2026-01-15T00:00:00Z");

      // Stage2 changes: before=5/10 (50%), after=8/10 (80%) → effective
      const s2Before = [
        ...makeRecords(stage2, 5, true, "2026-01-15T00:00:00Z"),
        ...makeRecords(stage2, 5, false, "2026-01-15T00:00:00Z"),
      ];
      const s2After = [
        ...makeRecords(stage2, 8, true, "2026-01-25T00:00:00Z"),
        ...makeRecords(stage2, 2, false, "2026-01-25T00:00:00Z"),
      ];

      const history = [...s1Before, ...s1After, ...s2Before, ...s2After];

      const changes = [
        makeChange({
          stage: stage1,
          skillFile: `skills/nightgauge-${stage1}/SKILL.md`,
          changedAt: changedAt1,
        }),
        makeChange({
          stage: stage2,
          skillFile: `skills/nightgauge-${stage2}/SKILL.md`,
          changedAt: changedAt2,
        }),
      ];

      const result = SkillEffectivenessAnalyzer.analyze(changes, history);

      expect(result.entries).toHaveLength(2);
      expect(result.skillChangesFound).toBe(2);

      const s1Entry = result.entries.find((e) => e.stage === stage1)!;
      const s2Entry = result.entries.find((e) => e.stage === stage2)!;
      expect(s1Entry).toBeDefined();
      expect(s2Entry).toBeDefined();
      expect(s2Entry.classification).toBe("effective");
    });

    it("only counts history records for the matching stage", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      // Lots of records for a different stage — should not influence result
      const otherStage = makeRecords("pr-create", 50, true, "2026-01-10T00:00:00Z");
      // Minimal records for the target stage
      const before = makeRecords(STAGE, 4, false, "2026-01-10T00:00:00Z");
      const after = makeRecords(STAGE, 4, true, "2026-01-20T00:00:00Z");

      const history = [...otherStage, ...before, ...after];
      const result = SkillEffectivenessAnalyzer.analyze([makeChange({ changedAt })], history);

      // Both windows have 4 samples < 5 → insufficient_data
      expect(result.entries[0].classification).toBe("insufficient_data");
      expect(result.entries[0].beforeWindow.sampleCount).toBe(4);
    });

    it("includes correct sampleCount and successRate in window summaries", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = [
        ...makeRecords(STAGE, 7, true, "2026-01-10T00:00:00Z"),
        ...makeRecords(STAGE, 3, false, "2026-01-10T00:00:00Z"),
      ];
      const after = [
        ...makeRecords(STAGE, 6, true, "2026-01-20T00:00:00Z"),
        ...makeRecords(STAGE, 4, false, "2026-01-20T00:00:00Z"),
      ];

      const result = SkillEffectivenessAnalyzer.analyze(
        [makeChange({ changedAt })],
        [...before, ...after]
      );

      const entry = result.entries[0];
      expect(entry.beforeWindow.sampleCount).toBe(10);
      expect(entry.beforeWindow.successRate).toBeCloseTo(0.7, 5);
      expect(entry.afterWindow.sampleCount).toBe(10);
      expect(entry.afterWindow.successRate).toBeCloseTo(0.6, 5);
    });

    it("sets analyzedAt on each entry", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = makeRecords(STAGE, 5, true, "2026-01-10T00:00:00Z");
      const after = makeRecords(STAGE, 5, true, "2026-01-20T00:00:00Z");

      const result = SkillEffectivenessAnalyzer.analyze(
        [makeChange({ changedAt })],
        [...before, ...after]
      );

      expect(result.entries[0].analyzedAt).toBeTruthy();
      expect(new Date(result.entries[0].analyzedAt).getTime()).toBeGreaterThan(0);
    });

    it("preserves skillFile and commitHash from the change record", () => {
      const changedAt = "2026-01-15T12:00:00Z";
      const before = makeRecords(STAGE, 5, true, "2026-01-10T00:00:00Z");
      const after = makeRecords(STAGE, 5, true, "2026-01-20T00:00:00Z");

      const change = makeChange({
        changedAt,
        commitHash: "deadbeef01234567",
        skillFile: "skills/nightgauge-feature-planning/SKILL.md",
      });

      const result = SkillEffectivenessAnalyzer.analyze([change], [...before, ...after]);

      expect(result.entries[0].commitHash).toBe("deadbeef01234567");
      expect(result.entries[0].skillFile).toBe("skills/nightgauge-feature-planning/SKILL.md");
    });
  });
});
