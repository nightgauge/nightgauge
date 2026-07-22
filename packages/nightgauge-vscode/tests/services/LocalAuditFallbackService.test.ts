/**
 * Unit tests for LocalAuditFallbackService (Issue #3324)
 *
 * Tests local history → AuditLogEntry mapping, date filtering, pagination,
 * and graceful error handling when TelemetryStore throws.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { LocalAuditFallbackService } from "../../src/services/LocalAuditFallbackService";
import type { AuditFilterState } from "../../src/views/dashboard/DashboardState";
import type { HistoryIndex } from "../../src/services/TelemetryStore";

function makeFilters(overrides: Partial<AuditFilterState> = {}): AuditFilterState {
  return {
    dateFrom: "2025-01-01T00:00:00.000Z",
    dateTo: "2030-12-31T23:59:59.999Z",
    actionFilter: "",
    userFilter: "",
    ...overrides,
  };
}

function makeIndex(entries: HistoryIndex["entries"]): HistoryIndex {
  return {
    schema_version: "1",
    updated_at: new Date().toISOString(),
    total_runs: entries.length,
    entries,
  };
}

async function writeIndex(dir: string, index: HistoryIndex): Promise<void> {
  const historyDir = path.join(dir, ".nightgauge", "pipeline", "history");
  await fs.mkdir(historyDir, { recursive: true });
  await fs.writeFile(path.join(historyDir, "index.json"), JSON.stringify(index), "utf-8");
}

describe("LocalAuditFallbackService", () => {
  let tmpDir: string;

  beforeEach(async () => {
    tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "laf-test-"));
  });

  afterEach(async () => {
    await fs.rm(tmpDir, { recursive: true, force: true });
  });

  it("maps 5 history entries to AuditLogEntry objects with correct field mapping", async () => {
    const now = new Date("2026-03-01T10:00:00.000Z");
    const entries = Array.from({ length: 5 }, (_, i) => ({
      issue_number: 100 + i,
      title: `Issue ${100 + i}`,
      outcome: "complete" as const,
      cost_usd: 0.01 * (i + 1),
      total_input_tokens: 1000,
      total_output_tokens: 500,
      total_cache_read_tokens: 0,
      total_cache_creation_tokens: 0,
      duration_ms: 60000,
      stage_count: 6,
      started_at: now.toISOString(),
      recorded_at: new Date(now.getTime() + i * 1000).toISOString(),
      branch: "feat/test",
    }));

    await writeIndex(tmpDir, makeIndex(entries));
    const svc = new LocalAuditFallbackService(tmpDir);
    const result = await svc.buildLocalAuditData(makeFilters());

    expect(result.isLocalFallback).toBe(true);
    expect(result.hasAccess).toBe(true);
    expect(result.entries).toHaveLength(5);
    expect(result.localDataLabel).toBe("Showing local telemetry — platform unreachable");

    const first = result.entries[0];
    expect(first.action).toBe("pipeline_run_completed");
    expect(first.resourceType).toBe("pipeline_run");
    expect(first.status).toBe("success");
    expect(first.userId).toBe("local");
    expect(first.metadata?.branch).toBe("feat/test");
  });

  it("maps failed outcome to pipeline_run_failed action and failure status", async () => {
    await writeIndex(
      tmpDir,
      makeIndex([
        {
          issue_number: 200,
          title: "Failed issue",
          outcome: "failed",
          cost_usd: 0.05,
          total_input_tokens: 500,
          total_output_tokens: 200,
          total_cache_read_tokens: 0,
          total_cache_creation_tokens: 0,
          duration_ms: 30000,
          stage_count: 3,
          started_at: "2026-03-01T00:00:00.000Z",
          recorded_at: "2026-03-01T00:01:00.000Z",
          branch: "feat/fail",
        },
      ])
    );

    const svc = new LocalAuditFallbackService(tmpDir);
    const result = await svc.buildLocalAuditData(makeFilters());

    expect(result.entries[0].action).toBe("pipeline_run_failed");
    expect(result.entries[0].status).toBe("failure");
  });

  it("maps cancelled outcome to pipeline_run_cancelled action and pending status", async () => {
    await writeIndex(
      tmpDir,
      makeIndex([
        {
          issue_number: 201,
          title: "Cancelled issue",
          outcome: "cancelled",
          cost_usd: 0.001, // non-zero to avoid isGhostEntry filter
          total_input_tokens: 10,
          total_output_tokens: 0,
          total_cache_read_tokens: 0,
          total_cache_creation_tokens: 0,
          duration_ms: 500,
          stage_count: 1,
          started_at: "2026-03-01T00:00:00.000Z",
          recorded_at: "2026-03-01T00:00:30.000Z",
          branch: "feat/cancel",
        },
      ])
    );

    const svc = new LocalAuditFallbackService(tmpDir);
    const result = await svc.buildLocalAuditData(makeFilters());

    expect(result.entries[0].action).toBe("pipeline_run_cancelled");
    expect(result.entries[0].status).toBe("pending");
  });

  it("applies date filter — only entries in range are returned", async () => {
    await writeIndex(
      tmpDir,
      makeIndex([
        {
          issue_number: 300,
          title: "In range",
          outcome: "complete",
          cost_usd: 0.01,
          total_input_tokens: 100,
          total_output_tokens: 50,
          total_cache_read_tokens: 0,
          total_cache_creation_tokens: 0,
          duration_ms: 1000,
          stage_count: 6,
          started_at: "2026-02-01T00:00:00.000Z",
          recorded_at: "2026-02-15T00:00:00.000Z",
          branch: "feat/in",
        },
        {
          issue_number: 301,
          title: "Out of range",
          outcome: "complete",
          cost_usd: 0.01,
          total_input_tokens: 100,
          total_output_tokens: 50,
          total_cache_read_tokens: 0,
          total_cache_creation_tokens: 0,
          duration_ms: 1000,
          stage_count: 6,
          started_at: "2025-01-01T00:00:00.000Z",
          recorded_at: "2025-01-01T00:00:00.000Z",
          branch: "feat/out",
        },
      ])
    );

    const svc = new LocalAuditFallbackService(tmpDir);
    const result = await svc.buildLocalAuditData(
      makeFilters({ dateFrom: "2026-01-01T00:00:00.000Z", dateTo: "2026-12-31T23:59:59.999Z" })
    );

    expect(result.entries).toHaveLength(1);
    expect(result.entries[0].resourceId).toBe("300");
  });

  it("returns empty AuditLogData with isLocalFallback:true when history is empty", async () => {
    await writeIndex(tmpDir, makeIndex([]));
    const svc = new LocalAuditFallbackService(tmpDir);
    const result = await svc.buildLocalAuditData(makeFilters());

    expect(result.isLocalFallback).toBe(true);
    expect(result.hasAccess).toBe(true);
    expect(result.entries).toHaveLength(0);
    expect(result.pagination.totalCount).toBe(0);
  });

  it("returns empty AuditLogData with isLocalFallback:true when index does not exist", async () => {
    const svc = new LocalAuditFallbackService(tmpDir);
    const result = await svc.buildLocalAuditData(makeFilters());

    expect(result.isLocalFallback).toBe(true);
    expect(result.hasAccess).toBe(true);
    expect(result.entries).toHaveLength(0);
  });

  it("paginates correctly — page 0 returns first PAGE_SIZE entries", async () => {
    const baseDate = new Date("2026-03-01T00:00:00.000Z");
    const entries = Array.from({ length: 60 }, (_, i) => ({
      issue_number: 400 + i,
      title: `Issue ${400 + i}`,
      outcome: "complete" as const,
      cost_usd: 0.01,
      total_input_tokens: 100,
      total_output_tokens: 50,
      total_cache_read_tokens: 0,
      total_cache_creation_tokens: 0,
      duration_ms: 1000,
      stage_count: 6,
      started_at: new Date(baseDate.getTime() + i * 3600_000).toISOString(),
      recorded_at: new Date(baseDate.getTime() + i * 3600_000 + 60_000).toISOString(),
      branch: "feat/page",
    }));

    await writeIndex(tmpDir, makeIndex(entries));
    const svc = new LocalAuditFallbackService(tmpDir);

    const page0 = await svc.buildLocalAuditData(makeFilters(), 0);
    expect(page0.entries).toHaveLength(50);
    expect(page0.pagination.totalCount).toBe(60);
    expect(page0.pagination.hasNextPage).toBe(true);
    expect(page0.pagination.hasPrevPage).toBe(false);

    const page1 = await svc.buildLocalAuditData(makeFilters(), 1);
    expect(page1.entries).toHaveLength(10);
    expect(page1.pagination.hasNextPage).toBe(false);
    expect(page1.pagination.hasPrevPage).toBe(true);
  });
});
