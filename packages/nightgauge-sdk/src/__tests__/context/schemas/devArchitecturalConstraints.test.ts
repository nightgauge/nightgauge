import { describe, expect, it } from "vitest";
import { DevContextSchema } from "../../../context/schemas/index.js";

const minimalDev = {
  schema_version: "1.8",
  issue_number: 42,
  created_at: "2026-01-01T00:00:00.000Z",
};

const minimalRecallHit = {
  rank: 1,
  score: 2.1,
  path: ".nightgauge/knowledge/features/3591-recall-api/decisions.md",
  kind: "issue",
  snippet: "Chose BM25 over cosine similarity for offline, dependency-free scoring.",
};

describe("DevContextSchema — architectural_constraints (Issue #3594)", () => {
  it("parses without architectural_constraints (backward compat)", () => {
    const result = DevContextSchema.safeParse(minimalDev);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.architectural_constraints).toBeUndefined();
    }
  });

  it("accepts null architectural_constraints (recall skipped)", () => {
    const input = { ...minimalDev, architectural_constraints: null };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.architectural_constraints).toBeNull();
    }
  });

  it("accepts empty array architectural_constraints (no hits above threshold)", () => {
    const input = { ...minimalDev, architectural_constraints: [] };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.architectural_constraints).toEqual([]);
    }
  });

  it("parses a single valid RecallHit", () => {
    const input = { ...minimalDev, architectural_constraints: [minimalRecallHit] };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const hits = result.data.architectural_constraints;
      expect(hits).toHaveLength(1);
      expect(hits?.[0].rank).toBe(1);
      expect(hits?.[0].score).toBe(2.1);
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
    const input = { ...minimalDev, architectural_constraints: [hit] };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const h = result.data.architectural_constraints?.[0];
      expect(h?.issue_number).toBe(3591);
      expect(h?.tags).toEqual(["bm25", "scoring", "knowledge"]);
      expect(h?.graduated).toBe(true);
    }
  });

  it("passthrough preserves extra fields on RecallHit", () => {
    const hit = { ...minimalRecallHit, query_id: "abc-123", extra_field: "preserved" };
    const input = { ...minimalDev, architectural_constraints: [hit] };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const h = result.data.architectural_constraints?.[0] as Record<string, unknown>;
      expect(h?.["query_id"]).toBe("abc-123");
      expect(h?.["extra_field"]).toBe("preserved");
    }
  });

  it("rejects a hit with rank <= 0", () => {
    const hit = { ...minimalRecallHit, rank: 0 };
    const input = { ...minimalDev, architectural_constraints: [hit] };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects a hit missing required fields", () => {
    const hit = { rank: 1, score: 1.5 }; // missing path, kind, snippet
    const input = { ...minimalDev, architectural_constraints: [hit] };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("parses multiple hits with correct ranks", () => {
    const hits = [
      { ...minimalRecallHit, rank: 1, score: 3.2 },
      { ...minimalRecallHit, rank: 2, score: 2.4, path: "path/two/decisions.md" },
      { ...minimalRecallHit, rank: 3, score: 1.6, path: "path/three/decisions.md" },
    ];
    const input = { ...minimalDev, architectural_constraints: hits };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.architectural_constraints).toHaveLength(3);
      expect(result.data.architectural_constraints?.[2].rank).toBe(3);
    }
  });

  it("parses schema_version 1.8 with new field alongside existing fields", () => {
    const input = {
      ...minimalDev,
      schema_version: "1.8",
      knowledge_path: ".nightgauge/knowledge/features/42-test",
      architectural_constraints: [minimalRecallHit],
    };
    const result = DevContextSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.schema_version).toBe("1.8");
      expect(result.data.architectural_constraints).toHaveLength(1);
    }
  });
});
