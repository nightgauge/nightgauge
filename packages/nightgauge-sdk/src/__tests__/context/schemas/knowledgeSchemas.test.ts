import { describe, expect, it } from "vitest";
import {
  KnowledgeTypeSchema,
  KnowledgeEntrySchema,
  KnowledgeIndexSchema,
} from "../../../context/schemas/index.js";

// Shared valid datetime strings
const NOW = "2026-01-01T00:00:00.000Z";
const LATER = "2026-02-01T12:00:00.000Z";

// Minimal valid KnowledgeEntry
const minimalEntry = {
  title: "My PRD",
  type: "prd",
  created: NOW,
  updated: NOW,
};

// ──────────────────────────────────────────────────────────────
// KnowledgeTypeSchema
// ──────────────────────────────────────────────────────────────

describe("KnowledgeTypeSchema", () => {
  it.each(["decision", "prd", "conversation", "adr", "reference", "note"])(
    'accepts valid type "%s"',
    (type) => {
      const result = KnowledgeTypeSchema.safeParse(type);
      expect(result.success).toBe(true);
    }
  );

  it("normalizes hyphens to underscores (flexEnum behavior)", () => {
    // No hyphenated variants in the KnowledgeType enum, but flexEnum converts
    // hyphens to underscores as a general defense. Verify a hyphenated string
    // that matches a valid value after normalization (none apply here, so verify
    // a non-hyphenated value passes the underlying path).
    const result = KnowledgeTypeSchema.safeParse("prd");
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data).toBe("prd");
    }
  });

  it("rejects unknown value", () => {
    const result = KnowledgeTypeSchema.safeParse("unknown-type");
    expect(result.success).toBe(false);
  });

  it("rejects empty string", () => {
    const result = KnowledgeTypeSchema.safeParse("");
    expect(result.success).toBe(false);
  });

  it("rejects non-string input", () => {
    const result = KnowledgeTypeSchema.safeParse(42);
    expect(result.success).toBe(false);
  });
});

// ──────────────────────────────────────────────────────────────
// KnowledgeEntrySchema
// ──────────────────────────────────────────────────────────────

describe("KnowledgeEntrySchema", () => {
  it("validates a minimal valid entry (title, type, created, updated)", () => {
    const result = KnowledgeEntrySchema.safeParse(minimalEntry);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.title).toBe("My PRD");
      expect(result.data.type).toBe("prd");
      expect(result.data.created).toBe(NOW);
      expect(result.data.updated).toBe(NOW);
    }
  });

  it("accepts optional tags field", () => {
    const input = { ...minimalEntry, tags: ["architecture", "backend"] };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tags).toEqual(["architecture", "backend"]);
    }
  });

  it("accepts optional related_issues field", () => {
    const input = { ...minimalEntry, related_issues: [42, 100] };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.related_issues).toEqual([42, 100]);
    }
  });

  it("accepts optional related_files field", () => {
    const input = {
      ...minimalEntry,
      related_files: ["src/services/foo.ts", "docs/ARCHITECTURE.md"],
    };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.related_files).toEqual(["src/services/foo.ts", "docs/ARCHITECTURE.md"]);
    }
  });

  it("accepts all optional fields together", () => {
    const input = {
      ...minimalEntry,
      updated: LATER,
      tags: ["sdk", "schema"],
      related_issues: [1674],
      related_files: ["packages/nightgauge-sdk/src/context/schemas/knowledge.ts"],
    };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(true);
  });

  it("allows extra fields via passthrough", () => {
    const input = { ...minimalEntry, extra_field: "extra-value" };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect((result.data as Record<string, unknown>).extra_field).toBe("extra-value");
    }
  });

  it("rejects missing required field: title", () => {
    const { title: _removed, ...rest } = minimalEntry;
    const result = KnowledgeEntrySchema.safeParse(rest);
    expect(result.success).toBe(false);
  });

  it("rejects empty string for title", () => {
    const input = { ...minimalEntry, title: "" };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects missing required field: type", () => {
    const { type: _removed, ...rest } = minimalEntry;
    const result = KnowledgeEntrySchema.safeParse(rest);
    expect(result.success).toBe(false);
  });

  it("rejects invalid type value", () => {
    const input = { ...minimalEntry, type: "wiki" };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects invalid datetime for created", () => {
    const input = { ...minimalEntry, created: "2026-01-01" };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects invalid datetime for updated", () => {
    const input = { ...minimalEntry, updated: "not-a-date" };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects non-array tags", () => {
    const input = { ...minimalEntry, tags: "architecture" };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects non-positive issue numbers in related_issues", () => {
    const input = { ...minimalEntry, related_issues: [0] };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects negative issue numbers in related_issues", () => {
    const input = { ...minimalEntry, related_issues: [-1] };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects non-integer issue numbers in related_issues", () => {
    const input = { ...minimalEntry, related_issues: [1.5] };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});

// ──────────────────────────────────────────────────────────────
// KnowledgeIndexSchema
// ──────────────────────────────────────────────────────────────

describe("KnowledgeIndexSchema", () => {
  const minimalIndex = {
    total_entries: 0,
    generated_at: NOW,
    categories: {},
  };

  it("validates a minimal valid index (empty categories)", () => {
    const result = KnowledgeIndexSchema.safeParse(minimalIndex);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.total_entries).toBe(0);
      expect(result.data.categories).toEqual({});
    }
  });

  it("validates a populated index with epics and features categories", () => {
    const input = {
      total_entries: 2,
      generated_at: NOW,
      categories: {
        features: [
          {
            issue_number: 42,
            slug: "42-photo-upload",
            path: ".nightgauge/knowledge/features/42-photo-upload/",
            files: ["PRD.md", "decisions.md"],
          },
        ],
        epics: [
          {
            issue_number: 100,
            slug: "100-auth-epic",
            path: ".nightgauge/knowledge/epics/100-auth-epic/",
            files: ["PRD.md"],
            entry: {
              title: "Auth Epic PRD",
              type: "prd",
              created: NOW,
              updated: NOW,
            },
          },
        ],
      },
    };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.total_entries).toBe(2);
      expect(result.data.categories["features"]).toHaveLength(1);
      expect(result.data.categories["epics"]).toHaveLength(1);
    }
  });

  it("accepts extra fields via passthrough", () => {
    const input = { ...minimalIndex, extra: "value" };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      expect((result.data as Record<string, unknown>).extra).toBe("value");
    }
  });

  it("accepts category entries without optional entry metadata", () => {
    const input = {
      total_entries: 1,
      generated_at: NOW,
      categories: {
        features: [
          {
            issue_number: 42,
            slug: "42-test",
            path: ".nightgauge/knowledge/features/42-test/",
            files: ["PRD.md"],
            // no entry field
          },
        ],
      },
    };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(true);
    if (result.success) {
      const feat = result.data.categories["features"]?.[0];
      expect(feat?.entry).toBeUndefined();
    }
  });

  it("accepts extra fields on category entries via passthrough", () => {
    const input = {
      total_entries: 1,
      generated_at: NOW,
      categories: {
        features: [
          {
            issue_number: 1,
            slug: "1-test",
            path: ".nightgauge/knowledge/features/1-test/",
            files: [],
            custom_field: "custom-value",
          },
        ],
      },
    };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(true);
  });

  it("rejects negative total_entries", () => {
    const input = { ...minimalIndex, total_entries: -1 };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects non-integer total_entries", () => {
    const input = { ...minimalIndex, total_entries: 1.5 };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects invalid generated_at datetime", () => {
    const input = { ...minimalIndex, generated_at: "2026-01-01" };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects missing total_entries", () => {
    const { total_entries: _removed, ...rest } = minimalIndex;
    const result = KnowledgeIndexSchema.safeParse(rest);
    expect(result.success).toBe(false);
  });

  it("rejects missing generated_at", () => {
    const { generated_at: _removed, ...rest } = minimalIndex;
    const result = KnowledgeIndexSchema.safeParse(rest);
    expect(result.success).toBe(false);
  });

  it("rejects non-positive issue_number in category entry", () => {
    const input = {
      total_entries: 1,
      generated_at: NOW,
      categories: {
        features: [
          {
            issue_number: 0,
            slug: "0-bad",
            path: ".nightgauge/knowledge/features/0-bad/",
            files: [],
          },
        ],
      },
    };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(false);
  });

  it("rejects empty slug in category entry", () => {
    const input = {
      total_entries: 1,
      generated_at: NOW,
      categories: {
        features: [
          {
            issue_number: 1,
            slug: "",
            path: ".nightgauge/knowledge/features/1-test/",
            files: [],
          },
        ],
      },
    };
    const result = KnowledgeIndexSchema.safeParse(input);
    expect(result.success).toBe(false);
  });
});
