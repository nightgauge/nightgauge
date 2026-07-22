import { describe, expect, it } from "vitest";
import { PlanningContextSchema } from "../../../context/schemas/index.js";

const minimalPlanning = {
  schema_version: "1.7",
  issue_number: 42,
  plan_file: ".nightgauge/plans/42-test.md",
  approach: "Add recalled_decisions field",
  files_to_create: [],
  files_to_modify: [],
  created_at: "2026-01-01T00:00:00.000Z",
};

const minimalRecallHit = {
  rank: 1,
  score: 2.47,
  path: ".nightgauge/knowledge/features/3591-recall-api/decisions.md",
  kind: "issue",
  snippet: "Chose BM25 over cosine similarity for offline, dependency-free scoring.",
};

describe("PlanningContextSchema — recalled_decisions (Issue #3593)", () => {
  it("parses without recalled_decisions (backward compat)", () => {
    const result = PlanningContextSchema.safeParse(minimalPlanning);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.recalled_decisions).toBeUndefined();
    }
  });

  it("accepts null recalled_decisions (recall skipped)", () => {
    const input = { ...minimalPlanning, recalled_decisions: null };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.recalled_decisions).toBeNull();
    }
  });

  it("accepts empty array recalled_decisions (no hits above threshold)", () => {
    const input = { ...minimalPlanning, recalled_decisions: [] };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.recalled_decisions).toEqual([]);
    }
  });

  it("parses a single valid RecallHit", () => {
    const input = { ...minimalPlanning, recalled_decisions: [minimalRecallHit] };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const hits = result.data.recalled_decisions;
      expect(hits).toHaveLength(1);
      expect(hits?.[0].rank).toBe(1);
      expect(hits?.[0].score).toBe(2.47);
      expect(hits?.[0].path).toBe(".nightgauge/knowledge/features/3591-recall-api/decisions.md");
      expect(hits?.[0].kind).toBe("issue");
      expect(hits?.[0].snippet).toContain("BM25");
    }
  });

  it("accepts optional fields: issue_number, tags, graduated", () => {
    const hit = {
      ...minimalRecallHit,
      issue_number: 3591,
      tags: ["bm25", "scoring", "knowledge"],
      graduated: true,
    };
    const input = { ...minimalPlanning, recalled_decisions: [hit] };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const h = result.data.recalled_decisions?.[0];
      expect(h?.issue_number).toBe(3591);
      expect(h?.tags).toEqual(["bm25", "scoring", "knowledge"]);
      expect(h?.graduated).toBe(true);
    }
  });

  it("passthrough preserves extra fields on RecallHit", () => {
    const hit = { ...minimalRecallHit, query_id: "abc-123", extra_field: "preserved" };
    const input = { ...minimalPlanning, recalled_decisions: [hit] };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const h = result.data.recalled_decisions?.[0] as Record<string, unknown>;
      expect(h?.["query_id"]).toBe("abc-123");
      expect(h?.["extra_field"]).toBe("preserved");
    }
  });

  it("rejects a hit with rank <= 0", () => {
    const hit = { ...minimalRecallHit, rank: 0 };
    const input = { ...minimalPlanning, recalled_decisions: [hit] };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects a hit missing required fields", () => {
    const hit = { rank: 1, score: 1.0 }; // missing path, kind, snippet
    const input = { ...minimalPlanning, recalled_decisions: [hit] };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("parses multiple hits with correct ranks", () => {
    const hits = [
      { ...minimalRecallHit, rank: 1, score: 3.1 },
      { ...minimalRecallHit, rank: 2, score: 2.2, path: "path/two/decisions.md" },
      { ...minimalRecallHit, rank: 3, score: 1.5, path: "path/three/decisions.md" },
    ];
    const input = { ...minimalPlanning, recalled_decisions: hits };
    const result = PlanningContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.recalled_decisions).toHaveLength(3);
      expect(result.data.recalled_decisions?.[2].rank).toBe(3);
    }
  });
});
