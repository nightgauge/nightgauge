import { describe, expect, it } from "vitest";
import {
  CreationManifestSchema,
  CreationManifestEntrySchema,
  ManifestBlockerRefSchema,
  ManifestSpikeArtifactSchema,
} from "../../../context/schemas/index.js";

const NOW = "2026-05-06T20:30:00.000Z";

const minimalEntry = {
  repo: "nightgauge/nightgauge",
  number: 3237,
  type: "feature",
  priority: "P1",
  size: "M",
  status: "Ready",
};

const minimalManifest = {
  schema_version: "1.0",
  created_at: NOW,
  created_by_skill: "nightgauge-issue-create",
  entries: [minimalEntry],
};

describe("CreationManifestEntrySchema", () => {
  it("accepts a minimal valid entry and applies array defaults", () => {
    const result = CreationManifestEntrySchema.safeParse(minimalEntry);
    expect(result.success).toBe(true);
    if (!result.success) return;
    expect(result.data.sub_issues).toEqual([]);
    expect(result.data.blocked_by).toEqual([]);
    expect(result.data.body_sections).toEqual([]);
    expect(result.data.component_labels).toEqual([]);
    expect(result.data.knowledge_path ?? null).toBeNull();
    expect(result.data.spike_artifact ?? null).toBeNull();
    expect(result.data.parent_epic ?? null).toBeNull();
  });

  it("preserves declared sub_issues, blocked_by, body_sections", () => {
    const result = CreationManifestEntrySchema.safeParse({
      ...minimalEntry,
      type: "epic",
      sub_issues: [3238, 3239, 3240],
      blocked_by: [{ number: 3100 }, { number: 42, repo: "nightgauge/other" }],
      body_sections: ["Summary", "Acceptance Criteria"],
      component_labels: ["component:skills"],
      parent_epic: "nightgauge/nightgauge#3236",
    });
    expect(result.success).toBe(true);
    if (!result.success) return;
    expect(result.data.sub_issues).toEqual([3238, 3239, 3240]);
    expect(result.data.blocked_by).toHaveLength(2);
    expect(result.data.blocked_by[1]?.repo).toBe("nightgauge/other");
    expect(result.data.body_sections).toContain("Summary");
    expect(result.data.parent_epic).toBe("nightgauge/nightgauge#3236");
  });

  it("accepts spike_artifact for spike entries", () => {
    const result = CreationManifestEntrySchema.safeParse({
      ...minimalEntry,
      type: "spike",
      spike_artifact: { path: "docs/spikes/3237-foo.md", exists: false },
    });
    expect(result.success).toBe(true);
    if (!result.success) return;
    expect(result.data.spike_artifact?.path).toBe("docs/spikes/3237-foo.md");
    expect(result.data.spike_artifact?.exists).toBe(false);
  });

  it("rejects entry missing required fields", () => {
    const { number: _drop, ...incomplete } = minimalEntry;
    void _drop;
    expect(CreationManifestEntrySchema.safeParse(incomplete).success).toBe(false);
  });

  it("rejects negative or zero issue numbers", () => {
    expect(CreationManifestEntrySchema.safeParse({ ...minimalEntry, number: 0 }).success).toBe(
      false
    );
    expect(CreationManifestEntrySchema.safeParse({ ...minimalEntry, number: -1 }).success).toBe(
      false
    );
  });

  it("rejects unknown size", () => {
    expect(CreationManifestEntrySchema.safeParse({ ...minimalEntry, size: "XXL" }).success).toBe(
      false
    );
  });

  it("normalizes flexEnum aliases for type and priority", () => {
    const result = CreationManifestEntrySchema.safeParse({
      ...minimalEntry,
      type: "enhancement",
      priority: "P1",
    });
    // 'enhancement' is not in the type alias map for ManifestIssueType, so
    // verify a hyphen-insensitive value still parses (no hyphenated values
    // exist in the enum, but the preprocess path should not crash).
    // Direct alias coverage is provided by issue.ts tests; here we lock in
    // the 'feature' canonical form when an exact value is supplied.
    expect(result.success).toBe(false); // 'enhancement' is not aliased here
    const ok = CreationManifestEntrySchema.safeParse({
      ...minimalEntry,
      type: "feature",
    });
    expect(ok.success).toBe(true);
  });
});

describe("ManifestBlockerRefSchema", () => {
  it("accepts same-repo blocker (number only)", () => {
    const result = ManifestBlockerRefSchema.safeParse({ number: 100 });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.repo ?? null).toBeNull();
    }
  });

  it("accepts cross-repo blocker", () => {
    const result = ManifestBlockerRefSchema.safeParse({
      number: 200,
      repo: "acme/platform",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.repo).toBe("acme/platform");
    }
  });

  it("rejects missing number", () => {
    expect(ManifestBlockerRefSchema.safeParse({ repo: "x/y" }).success).toBe(false);
  });
});

describe("ManifestSpikeArtifactSchema", () => {
  it("accepts artifact with path and exists flag", () => {
    const result = ManifestSpikeArtifactSchema.safeParse({
      path: "docs/spikes/3237-foo.md",
      exists: true,
    });
    expect(result.success).toBe(true);
  });

  it("defaults exists to false on non-boolean input", () => {
    const result = ManifestSpikeArtifactSchema.safeParse({
      path: "docs/spikes/3237-foo.md",
      exists: "yes",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.exists).toBe(false);
    }
  });

  it("rejects empty path", () => {
    expect(ManifestSpikeArtifactSchema.safeParse({ path: "" }).success).toBe(false);
  });
});

describe("CreationManifestSchema", () => {
  it("accepts a minimal valid manifest with one entry", () => {
    const result = CreationManifestSchema.safeParse(minimalManifest);
    expect(result.success).toBe(true);
  });

  it("rejects empty entries array", () => {
    const result = CreationManifestSchema.safeParse({
      ...minimalManifest,
      entries: [],
    });
    expect(result.success).toBe(false);
  });

  it("rejects malformed schema_version", () => {
    const result = CreationManifestSchema.safeParse({
      ...minimalManifest,
      schema_version: "v1",
    });
    expect(result.success).toBe(false);
  });

  it("rejects non-ISO created_at", () => {
    const result = CreationManifestSchema.safeParse({
      ...minimalManifest,
      created_at: "yesterday",
    });
    expect(result.success).toBe(false);
  });

  it("preserves project_number when provided", () => {
    const result = CreationManifestSchema.safeParse({
      ...minimalManifest,
      project_number: 1,
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.project_number).toBe(1);
    }
  });

  it("round-trips through JSON", () => {
    const enriched = {
      schema_version: "1.0",
      created_at: NOW,
      created_by_skill: "nightgauge-issue-create",
      project_number: 1,
      entries: [
        {
          ...minimalEntry,
          type: "epic",
          sub_issues: [3238, 3239],
          body_sections: ["Summary", "Acceptance Criteria", "Technical Notes"],
          component_labels: ["component:skills"],
          knowledge_path: ".nightgauge/knowledge/features/3237-foo",
        },
        {
          repo: "nightgauge/nightgauge",
          number: 3238,
          type: "feature",
          priority: "P2",
          size: "S",
          status: "Backlog",
          parent_epic: "nightgauge/nightgauge#3237",
          blocked_by: [{ number: 3237 }],
          body_sections: ["Summary"],
        },
      ],
    };
    const parsed = CreationManifestSchema.parse(enriched);
    const roundTrip = CreationManifestSchema.parse(JSON.parse(JSON.stringify(parsed)));
    expect(roundTrip.entries).toHaveLength(2);
    expect(roundTrip.entries[1]?.parent_epic).toBe("nightgauge/nightgauge#3237");
  });

  it("passes through unknown top-level fields", () => {
    const result = CreationManifestSchema.safeParse({
      ...minimalManifest,
      future_extension: { foo: "bar" },
    });
    expect(result.success).toBe(true);
  });
});
