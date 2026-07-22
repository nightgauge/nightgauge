import { describe, expect, it } from "vitest";
import {
  EpicContextSchema,
  SubIssueFindingsSchema,
} from "../../../context/schemas/epic-context.js";

describe("SubIssueFindingsSchema", () => {
  it("parses minimal valid findings", () => {
    const result = SubIssueFindingsSchema.safeParse({
      recorded_at: "2026-03-24T00:00:00Z",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.files_touched).toEqual([]);
      expect(result.data.decisions).toEqual([]);
      expect(result.data.discoveries).toEqual([]);
      expect(result.data.patterns).toEqual([]);
    }
  });

  it("parses fully populated findings", () => {
    const result = SubIssueFindingsSchema.safeParse({
      files_touched: ["src/foo.ts", "src/bar.ts"],
      decisions: ["Use Zod for validation"],
      discoveries: ["ContextManager uses atomic writes"],
      patterns: ["Atomic write via temp + rename"],
      recorded_at: "2026-03-24T12:00:00Z",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.files_touched).toHaveLength(2);
      expect(result.data.decisions).toHaveLength(1);
    }
  });

  it("rejects missing recorded_at", () => {
    const result = SubIssueFindingsSchema.safeParse({
      files_touched: [],
    });
    expect(result.success).toBe(false);
  });
});

describe("EpicContextSchema", () => {
  const minimal = {
    schema_version: "1.0" as const,
    epic_number: 100,
    last_updated: "2026-03-24T00:00:00Z",
  };

  it("parses minimal valid epic context", () => {
    const result = EpicContextSchema.safeParse(minimal);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.epic_number).toBe(100);
      expect(result.data.sub_issue_findings).toEqual({});
      expect(result.data.shared_research.codebase_notes).toEqual([]);
      expect(result.data.shared_research.architecture_notes).toEqual([]);
      expect(result.data.shared_research.relevant_files).toEqual([]);
    }
  });

  it("parses fully populated epic context", () => {
    const result = EpicContextSchema.safeParse({
      ...minimal,
      sub_issue_findings: {
        "42": {
          files_touched: ["src/foo.ts"],
          decisions: ["Use atomic writes"],
          discoveries: ["Base path is configurable"],
          patterns: ["Zod validation pattern"],
          recorded_at: "2026-03-24T01:00:00Z",
        },
        "43": {
          files_touched: ["src/bar.ts"],
          decisions: [],
          discoveries: [],
          patterns: [],
          recorded_at: "2026-03-24T02:00:00Z",
        },
      },
      shared_research: {
        codebase_notes: ["Monorepo with npm workspaces"],
        architecture_notes: ["Three-layer architecture"],
        relevant_files: ["src/foo.ts", "src/bar.ts"],
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(Object.keys(result.data.sub_issue_findings)).toHaveLength(2);
      expect(result.data.shared_research.relevant_files).toHaveLength(2);
    }
  });

  it("rejects wrong schema_version", () => {
    const result = EpicContextSchema.safeParse({
      ...minimal,
      schema_version: "2.0",
    });
    expect(result.success).toBe(false);
  });

  it("rejects missing epic_number", () => {
    const result = EpicContextSchema.safeParse({
      schema_version: "1.0",
      last_updated: "2026-03-24T00:00:00Z",
    });
    expect(result.success).toBe(false);
  });

  it("rejects invalid sub_issue_findings entry", () => {
    const result = EpicContextSchema.safeParse({
      ...minimal,
      sub_issue_findings: {
        "42": { files_touched: "not-an-array" }, // invalid
      },
    });
    expect(result.success).toBe(false);
  });
});
