import { describe, expect, it } from "vitest";
import {
  KnowledgeTypeSchema,
  KnowledgeEntrySchema,
  KnowledgeIndexSchema,
} from "../../../context/schemas/index.js";

// Shared valid datetime strings
const NOW = "2026-01-01T00:00:00.000Z";
const LATER = "2026-02-01T12:00:00.000Z";

// ──────────────────────────────────────────────────────────────
// KnowledgeTypeSchema
// ──────────────────────────────────────────────────────────────

describe("KnowledgeTypeSchema", () => {
  it.each([
    "prd",
    "decisions",
    "adr",
    "architecture",
    "glossary",
    "runbook",
    "post-mortem",
    "conversation",
    "reference",
    "note",
  ])('accepts valid type "%s"', (type) => {
    const result = KnowledgeTypeSchema.safeParse(type);
    expect(result.success).toBe(true);
  });

  it("keeps the hyphenated `post-mortem` value verbatim", () => {
    // The Go binary writes `type: post-mortem`. A normalizing enum would
    // rewrite it and split the vocabulary across the two layers.
    const result = KnowledgeTypeSchema.safeParse("post-mortem");
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data).toBe("post-mortem");
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
  it("validates a minimal valid entry (type only)", () => {
    const result = KnowledgeEntrySchema.safeParse({ type: "prd" });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.type).toBe("prd");
    }
  });

  it("rejects missing required field: type", () => {
    const result = KnowledgeEntrySchema.safeParse({ title: "My PRD" });
    expect(result.success).toBe(false);
  });

  it("rejects an empty type", () => {
    const result = KnowledgeEntrySchema.safeParse({ type: "" });
    expect(result.success).toBe(false);
  });

  it("accepts an unknown type value (OKF consumer tolerance)", () => {
    // The Go parser accepts any type string; the two layers must agree, or an
    // entry a future producer wrote would parse in one and fail in the other.
    const result = KnowledgeEntrySchema.safeParse({ type: "some-future-kind" });
    expect(result.success).toBe(true);
  });

  it("accepts every field of the contract together", () => {
    const input = {
      type: "decisions",
      title: "Decisions: #7",
      description: "What we decided on #7",
      tags: ["kb", "adr"],
      related: ["#7"],
      repos: ["nightgauge"],
      status: "draft",
      superseded_by: "#9",
      generated: { by: "feature-dev/claude-sonnet-5", at: NOW },
      verified: [
        { by: "process:retro", at: NOW },
        { by: "human:octocat", at: LATER },
      ],
      sources: [
        { resource: "https://github.com/nightgauge/nightgauge/issues/7", title: "The issue" },
        { resource: "/architecture/go-ts-parity.md" },
      ],
      stale_after: LATER,
    };
    const result = KnowledgeEntrySchema.safeParse(input);
    expect(result.success).toBe(true);
  });

  it("rejects the deleted `superseded` status", () => {
    const result = KnowledgeEntrySchema.safeParse({ type: "prd", status: "superseded" });
    expect(result.success).toBe(false);
  });

  it("accepts the three lifecycle statuses", () => {
    for (const status of ["draft", "stable", "deprecated"]) {
      expect(KnowledgeEntrySchema.safeParse({ type: "prd", status }).success).toBe(true);
    }
  });

  it("rejects an actor outside the convention", () => {
    for (const by of ["I wrote this", "feature-dev", "human:", "Feature-Dev/claude"]) {
      const result = KnowledgeEntrySchema.safeParse({ type: "prd", generated: { by } });
      expect(result.success).toBe(false);
    }
  });

  it("accepts each actor form the binary constructs", () => {
    for (const by of ["feature-dev/claude-sonnet-5", "human:octocat", "process:retro"]) {
      const result = KnowledgeEntrySchema.safeParse({ type: "prd", generated: { by } });
      expect(result.success).toBe(true);
    }
  });

  it("rejects a source without a resource", () => {
    const result = KnowledgeEntrySchema.safeParse({
      type: "prd",
      sources: [{ title: "no resource" }],
    });
    expect(result.success).toBe(false);
  });

  it("rejects an invalid datetime for stale_after", () => {
    const result = KnowledgeEntrySchema.safeParse({ type: "prd", stale_after: "2026-01-01" });
    expect(result.success).toBe(false);
  });

  it("rejects non-array tags", () => {
    const result = KnowledgeEntrySchema.safeParse({ type: "prd", tags: "architecture" });
    expect(result.success).toBe(false);
  });

  it("allows extra fields via passthrough", () => {
    const result = KnowledgeEntrySchema.safeParse({ type: "prd", extra_field: "extra-value" });
    expect(result.success).toBe(true);
    if (result.success) {
      expect((result.data as Record<string, unknown>).extra_field).toBe("extra-value");
    }
  });

  it("carries no `created` or `updated` field — generated.at is the write stamp", () => {
    // Parity guard: the Go FrontmatterBlock has no such fields either. Adding
    // one back here reintroduces the dual-timestamp drift this contract removed.
    const shape = Object.keys((KnowledgeEntrySchema as unknown as { shape: object }).shape);
    expect(shape).not.toContain("created");
    expect(shape).not.toContain("updated");
    expect(shape).toEqual(
      expect.arrayContaining([
        "type",
        "title",
        "description",
        "tags",
        "related",
        "repos",
        "status",
        "superseded_by",
        "generated",
        "verified",
        "sources",
        "stale_after",
      ])
    );
    expect(shape).toHaveLength(12);
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
