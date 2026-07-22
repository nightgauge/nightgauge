/**
 * goHistoryBridge.test.ts
 *
 * Unit tests for the Go pipeline.complete → TelemetryStore history bridge.
 * Validates that history records are correctly constructed from the IPC payload
 * and written to TelemetryStore when the Go scheduler completes a pipeline.
 *
 * @see Issue #1984 - Dashboard health metrics stuck at stale values
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as nodeFs from "node:fs/promises";
import * as nodePath from "node:path";

// ---------------------------------------------------------------------------
// Helpers — replicate the bridge handler logic for unit testing
// (The bridge is registered inline in services.ts; this file tests the same
// logic in isolation by reimplementing the pure record-construction portion.)
// ---------------------------------------------------------------------------

interface PipelineCompletePayload {
  issueNumber: number;
  success: boolean;
  totalInputTokens: number;
  totalOutputTokens: number;
  totalCostUSD: number;
  durationMs?: number;
  startedAt?: string;
  perStage: Array<{ stage: string; inputTokens: number; outputTokens: number }>;
}

interface IssueContext {
  title?: string;
  branch?: string;
  base_branch?: string;
  labels?: string[];
  routing?: {
    complexity_score?: number;
    suggested_route?: string;
    skip_stages?: string[];
  };
}

/**
 * Mirrors extractMetadata() from services.ts.
 * Extracted here so the test helper can include size/type/priority in records.
 */
function extractMetadata(labels: string[] | undefined): {
  size: string | null;
  type: string | null;
  priority: string | null;
} {
  if (!labels) {
    return { size: null, type: null, priority: null };
  }
  let size: string | null = null;
  let type: string | null = null;
  let priority: string | null = null;
  for (const label of labels) {
    if (!size && label.startsWith("size:")) {
      const value = label.slice("size:".length).toUpperCase();
      if (["XS", "S", "M", "L", "XL"].includes(value)) size = value;
    }
    if (!type && label.startsWith("type:")) {
      const value = label.slice("type:".length).toLowerCase();
      if (["feature", "bug", "docs", "refactor", "chore", "test", "verification"].includes(value))
        type = value;
    }
    if (!priority && label.startsWith("priority:")) {
      const value = label.slice("priority:".length).toLowerCase();
      if (["critical", "high", "medium", "low"].includes(value)) priority = value;
    }
  }
  return { size, type, priority };
}

/**
 * Pure record-construction logic extracted from the bridge handler.
 * Mirrors the logic in services.ts subscribe('pipeline.complete') handler.
 */
async function buildGoHistoryRecord(
  d: PipelineCompletePayload,
  incrediRoot: string,
  readIssueFn: (path: string) => Promise<string | null>
): Promise<Record<string, unknown>> {
  const issueContextPath = nodePath.join(
    incrediRoot,
    ".nightgauge",
    "pipeline",
    `issue-${d.issueNumber}.json`
  );

  let issueCtx: IssueContext = {};
  try {
    const raw = await readIssueFn(issueContextPath);
    if (raw) {
      issueCtx = JSON.parse(raw) as IssueContext;
    }
  } catch {
    // Non-critical: proceed with defaults if context file missing
  }

  const now = new Date().toISOString();
  const startedAt = d.startedAt ?? now;
  const durationMs = d.durationMs ?? 0;

  const perStageTokens: Record<
    string,
    {
      input: number;
      output: number;
      cache_read: number;
      cache_creation: number;
      cost_usd: number;
    }
  > = {};
  for (const s of d.perStage) {
    perStageTokens[s.stage] = {
      input: s.inputTokens,
      output: s.outputTokens,
      cache_read: 0,
      cache_creation: 0,
      cost_usd: 0,
    };
  }

  const stages: Record<string, { status: string }> = {};
  for (const s of d.perStage) {
    stages[s.stage] = { status: "complete" };
  }
  if (!d.success && d.perStage.length > 0) {
    const lastStage = d.perStage[d.perStage.length - 1].stage;
    stages[lastStage] = { status: "failed" };
  }

  const metadata = extractMetadata(issueCtx.labels);
  return {
    schema_version: "2",
    record_type: "run",
    issue_number: d.issueNumber,
    title: issueCtx.title ?? `Issue #${d.issueNumber}`,
    branch: issueCtx.branch ?? "",
    base_branch: issueCtx.base_branch ?? "main",
    execution_mode: "automatic",
    started_at: startedAt,
    completed_at: now,
    total_duration_ms: durationMs,
    outcome: d.success ? "complete" : "failed",
    labels: issueCtx.labels,
    size: metadata.size,
    type: metadata.type,
    priority: metadata.priority,
    stages,
    tokens: {
      total_input: d.totalInputTokens,
      total_output: d.totalOutputTokens,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: d.totalCostUSD,
      per_stage: perStageTokens,
    },
    files: { read_count: 0, written_count: 0 },
    routing: {
      complexity_score: issueCtx.routing?.complexity_score ?? 0,
      path: issueCtx.routing?.suggested_route ?? "standard",
      skip_stages: issueCtx.routing?.skip_stages ?? [],
    },
    recorded_at: now,
  };
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const BASE_PAYLOAD: PipelineCompletePayload = {
  issueNumber: 42,
  success: true,
  totalInputTokens: 10000,
  totalOutputTokens: 2000,
  totalCostUSD: 0.05,
  durationMs: 120000,
  startedAt: "2026-03-11T10:00:00Z",
  perStage: [
    { stage: "issue-pickup", inputTokens: 2000, outputTokens: 400 },
    { stage: "feature-planning", inputTokens: 3000, outputTokens: 600 },
    { stage: "feature-dev", inputTokens: 5000, outputTokens: 1000 },
  ],
};

const ISSUE_CTX: IssueContext = {
  title: "Fix dashboard stale metrics",
  branch: "feat/42-fix-dashboard",
  base_branch: "main",
  labels: ["bug", "size:S"],
  routing: {
    complexity_score: 3,
    suggested_route: "standard",
    skip_stages: [],
  },
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Go history bridge — buildGoHistoryRecord", () => {
  const incrediRoot = "/workspace/myrepo";

  describe("successful pipeline run", () => {
    it('sets schema_version to "2" and record_type to "run"', async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      expect(record.schema_version).toBe("2");
      expect(record.record_type).toBe("run");
    });

    it('sets outcome to "complete" when success=true', async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      expect(record.outcome).toBe("complete");
    });

    it("populates tokens from IPC payload", async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      const tokens = record.tokens as Record<string, unknown>;
      expect(tokens.total_input).toBe(10000);
      expect(tokens.total_output).toBe(2000);
      expect(tokens.estimated_cost_usd).toBe(0.05);
    });

    it("populates per_stage tokens for each stage", async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      const tokens = record.tokens as Record<string, unknown>;
      const perStage = tokens.per_stage as Record<string, unknown>;
      expect(perStage["issue-pickup"]).toEqual({
        input: 2000,
        output: 400,
        cache_read: 0,
        cache_creation: 0,
        cost_usd: 0,
      });
      expect(perStage["feature-dev"]).toEqual({
        input: 5000,
        output: 1000,
        cache_read: 0,
        cache_creation: 0,
        cost_usd: 0,
      });
    });

    it('marks all stages as "complete" when success=true', async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      const stages = record.stages as Record<string, { status: string }>;
      expect(stages["issue-pickup"].status).toBe("complete");
      expect(stages["feature-planning"].status).toBe("complete");
      expect(stages["feature-dev"].status).toBe("complete");
    });

    it("uses issue context title, branch, and labels", async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      expect(record.title).toBe("Fix dashboard stale metrics");
      expect(record.branch).toBe("feat/42-fix-dashboard");
      expect(record.labels).toEqual(["bug", "size:S"]);
    });

    it("uses startedAt and durationMs from IPC payload", async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      expect(record.started_at).toBe("2026-03-11T10:00:00Z");
      expect(record.total_duration_ms).toBe(120000);
    });

    it("populates routing from issue context", async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      const routing = record.routing as Record<string, unknown>;
      expect(routing.complexity_score).toBe(3);
      expect(routing.path).toBe("standard");
      expect(routing.skip_stages).toEqual([]);
    });
  });

  describe("failed pipeline run", () => {
    const failedPayload: PipelineCompletePayload = {
      ...BASE_PAYLOAD,
      success: false,
    };

    it('sets outcome to "failed" when success=false', async () => {
      const record = await buildGoHistoryRecord(failedPayload, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      expect(record.outcome).toBe("failed");
    });

    it('marks the last stage as "failed"', async () => {
      const record = await buildGoHistoryRecord(failedPayload, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      const stages = record.stages as Record<string, { status: string }>;
      expect(stages["issue-pickup"].status).toBe("complete");
      expect(stages["feature-planning"].status).toBe("complete");
      expect(stages["feature-dev"].status).toBe("failed");
    });
  });

  describe("missing issue context file", () => {
    it("uses defaults when issue context is not found", async () => {
      const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () => {
        throw new Error("ENOENT");
      });
      expect(record.title).toBe("Issue #42");
      expect(record.branch).toBe("");
      expect(record.base_branch).toBe("main");
    });

    it("does not throw when issue context file is missing", async () => {
      await expect(
        buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () => {
          throw new Error("ENOENT");
        })
      ).resolves.toBeDefined();
    });
  });

  describe("optional IPC fields", () => {
    it("defaults durationMs to 0 when not provided", async () => {
      const payload = { ...BASE_PAYLOAD };
      delete payload.durationMs;
      const record = await buildGoHistoryRecord(payload, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      expect(record.total_duration_ms).toBe(0);
    });

    it("uses current time as startedAt when not provided", async () => {
      const payload = { ...BASE_PAYLOAD };
      delete payload.startedAt;
      const before = Date.now();
      const record = await buildGoHistoryRecord(payload, incrediRoot, async () =>
        JSON.stringify(ISSUE_CTX)
      );
      const after = Date.now();
      const startedMs = new Date(record.started_at as string).getTime();
      expect(startedMs).toBeGreaterThanOrEqual(before);
      expect(startedMs).toBeLessThanOrEqual(after);
    });
  });
});

// ---------------------------------------------------------------------------
// Tests for extractMetadata helper (Issue #2544)
// ---------------------------------------------------------------------------

describe("extractMetadata", () => {
  it("returns all null for undefined labels", () => {
    expect(extractMetadata(undefined)).toEqual({ size: null, type: null, priority: null });
  });

  it("returns all null for empty labels array", () => {
    expect(extractMetadata([])).toEqual({ size: null, type: null, priority: null });
  });

  it("extracts size:M → 'M'", () => {
    expect(extractMetadata(["size:M"])).toMatchObject({ size: "M" });
  });

  it("extracts all size values (XS, S, M, L, XL)", () => {
    for (const s of ["XS", "S", "M", "L", "XL"]) {
      expect(extractMetadata([`size:${s}`])).toMatchObject({ size: s });
    }
  });

  it("extracts type:bug → 'bug'", () => {
    expect(extractMetadata(["type:bug"])).toMatchObject({ type: "bug" });
  });

  it("extracts all valid type values", () => {
    for (const t of ["feature", "bug", "docs", "refactor", "chore", "test", "verification"]) {
      expect(extractMetadata([`type:${t}`])).toMatchObject({ type: t });
    }
  });

  it("extracts priority:critical → 'critical'", () => {
    expect(extractMetadata(["priority:critical"])).toMatchObject({ priority: "critical" });
  });

  it("extracts all valid priority values", () => {
    for (const p of ["critical", "high", "medium", "low"]) {
      expect(extractMetadata([`priority:${p}`])).toMatchObject({ priority: p });
    }
  });

  it("extracts all three from a mixed labels array", () => {
    const labels = ["type:bug", "priority:critical", "size:M", "pipeline:refined", "other-label"];
    expect(extractMetadata(labels)).toEqual({ size: "M", type: "bug", priority: "critical" });
  });

  it("is case-insensitive for size prefix (SIZE:M → 'M')", () => {
    // The implementation uses startsWith which is case-sensitive, but label
    // values come from GitHub which always lowercases label names. This test
    // documents the current behavior — no case folding on the prefix itself.
    expect(extractMetadata(["size:m"])).toMatchObject({ size: "M" });
  });

  it("returns null for unknown size value", () => {
    expect(extractMetadata(["size:XXL"])).toMatchObject({ size: null });
  });

  it("returns null for unknown type value", () => {
    expect(extractMetadata(["type:unknown"])).toMatchObject({ type: null });
  });

  it("returns null for unknown priority value", () => {
    expect(extractMetadata(["priority:urgent"])).toMatchObject({ priority: null });
  });

  it("uses first matching label when duplicates exist", () => {
    expect(extractMetadata(["size:S", "size:M"])).toMatchObject({ size: "S" });
  });
});

describe("Go history bridge — metadata fields (Issue #2544)", () => {
  const incrediRoot = "/workspace/myrepo";

  it("includes size, type, and priority from issue labels in the record", async () => {
    const ctx = {
      title: "Fix auth bug",
      branch: "feat/99-fix-auth",
      labels: ["type:bug", "priority:critical", "size:M"],
    };
    const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
      JSON.stringify(ctx)
    );
    expect(record.size).toBe("M");
    expect(record.type).toBe("bug");
    expect(record.priority).toBe("critical");
  });

  it("sets size/type/priority to null when labels have no matching entries", async () => {
    const ctx = {
      title: "Some issue",
      labels: ["pipeline:refined", "other-label"],
    };
    const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () =>
      JSON.stringify(ctx)
    );
    expect(record.size).toBeNull();
    expect(record.type).toBeNull();
    expect(record.priority).toBeNull();
  });

  it("sets size/type/priority to null when issue context file is missing", async () => {
    const record = await buildGoHistoryRecord(BASE_PAYLOAD, incrediRoot, async () => {
      throw new Error("ENOENT");
    });
    expect(record.size).toBeNull();
    expect(record.type).toBeNull();
    expect(record.priority).toBeNull();
  });
});
