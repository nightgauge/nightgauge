/**
 * Tests for the queue type module — schema version, paused-reason variants.
 *
 * @see Issue #3001 — `upstream_failure` paused-reason variant.
 * @see Issue #3004 — `baseline_ci_red` paused-reason variant + schema 2.2 bump.
 * @see Issue #231 — `blocked_dependency` paused-reason variant + schema 2.3 bump.
 */

import { describe, it, expect } from "vitest";
import {
  QUEUE_SCHEMA_VERSION,
  type QueueItem,
  type QueueItemPausedReason,
} from "../../src/types/queue";

describe("QUEUE_SCHEMA_VERSION", () => {
  it("is bumped to 2.3 for blocked_dependency support", () => {
    expect(QUEUE_SCHEMA_VERSION).toBe("2.3");
  });
});

describe("QueueItemPausedReason", () => {
  it("upstream_failure variant compiles with required fields", () => {
    const r: QueueItemPausedReason = {
      kind: "upstream_failure",
      failed_run_id: "42-2026-04-25T00:00:00Z",
      summary: "stage feature-dev: stall_kill",
    };
    expect(r.kind).toBe("upstream_failure");
  });

  it("baseline_ci_red variant compiles with workflow + optional fields", () => {
    const r: QueueItemPausedReason = {
      kind: "baseline_ci_red",
      workflow: "ci.yml",
      job: "Integration & E2E Tests",
      failed_runs: 3,
      lookback_runs: 5,
      summary: "baseline-ci red: ci.yml failed 3/5",
    };
    expect(r.kind).toBe("baseline_ci_red");
    if (r.kind === "baseline_ci_red") {
      expect(r.workflow).toBe("ci.yml");
      expect(r.failed_runs).toBe(3);
    }
  });

  it("blocked_dependency variant compiles with blockingIssues", () => {
    const r: QueueItemPausedReason = {
      kind: "blocked_dependency",
      summary: "blocked by open dependency #123",
      blockingIssues: [{ number: 123, title: "PlatformApiClient", repo: "nightgauge/nightgauge" }],
    };
    expect(r.kind).toBe("blocked_dependency");
    if (r.kind === "blocked_dependency") {
      expect(r.blockingIssues[0].number).toBe(123);
    }
  });

  it("legacy 2.1 fixture (upstream_failure only) parses cleanly into a 2.2 QueueItem", () => {
    // Simulate a record produced by a 2.1 writer being read by a 2.2 reader.
    const legacyJSON = `{
      "issueNumber": 7,
      "title": "Legacy paused item",
      "position": 1,
      "status": "paused",
      "addedAt": "2026-04-25T00:00:00Z",
      "pausedReason": { "kind": "upstream_failure", "failed_run_id": "7-2026-04-25T00:00:00Z" }
    }`;
    const item: QueueItem = JSON.parse(legacyJSON);
    expect(item.status).toBe("paused");
    expect(item.pausedReason?.kind).toBe("upstream_failure");
  });
});
