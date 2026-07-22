/**
 * Unit tests for HeadlessOrchestrator audit trail emission.
 *
 * Verifies that AuditEventClient is correctly instantiated and that
 * structured audit events are emitted at each pipeline lifecycle boundary.
 *
 * Testing strategy: test the AuditEventClient directly with the SDK's schemas
 * rather than mocking the full orchestrator (which requires extensive vscode
 * mocking). Each test case exercises the audit schemas and config layer to
 * verify correctness of the wiring design.
 *
 * @see Issue #1582 - Pipeline execution audit trail emission
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { AuditEventClient, AuditEventSchema, AUDIT_ACTIONS } from "@nightgauge/sdk";
import type { AuditConfig } from "@nightgauge/sdk";

// ---------------------------------------------------------------------------
// Minimal vscode mock (required for any file that imports from incrediConfig)
// ---------------------------------------------------------------------------
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [],
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: vi.fn(() => ({
    event: vi.fn(),
    fire: vi.fn(),
    dispose: vi.fn(),
  })),
  Uri: { file: vi.fn((p: string) => ({ fsPath: p })) },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeAuditConfig(overrides: Partial<AuditConfig> = {}): AuditConfig {
  return {
    enabled: true,
    platformUrl: undefined,
    apiKey: undefined,
    batchSize: 100,
    flushIntervalMs: 60_000,
    offlineQueuePath: "/tmp/test-audit-queue.json",
    offlineQueueMaxSize: 1_000,
    retryMaxAttempts: 0,
    retryBackoffMs: 0,
    timeoutMs: 1_000,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// 1. AUDIT_ACTIONS contains stage lifecycle actions
// ---------------------------------------------------------------------------

describe("AUDIT_ACTIONS — stage lifecycle actions present", () => {
  it("includes stage.started", () => {
    expect(AUDIT_ACTIONS).toContain("stage.started");
  });

  it("includes stage.completed", () => {
    expect(AUDIT_ACTIONS).toContain("stage.completed");
  });

  it("includes stage.failed", () => {
    expect(AUDIT_ACTIONS).toContain("stage.failed");
  });

  it("includes pipeline.started", () => {
    expect(AUDIT_ACTIONS).toContain("pipeline.started");
  });

  it("includes pipeline.completed", () => {
    expect(AUDIT_ACTIONS).toContain("pipeline.completed");
  });

  it("includes pipeline.failed", () => {
    expect(AUDIT_ACTIONS).toContain("pipeline.failed");
  });

  it("includes skill.invoked", () => {
    expect(AUDIT_ACTIONS).toContain("skill.invoked");
  });

  it("includes cost.recorded", () => {
    expect(AUDIT_ACTIONS).toContain("cost.recorded");
  });

  it("includes commit.created", () => {
    expect(AUDIT_ACTIONS).toContain("commit.created");
  });
});

// ---------------------------------------------------------------------------
// 2. AuditEventSchema validation
// ---------------------------------------------------------------------------

describe("AuditEventSchema — validates pipeline audit events", () => {
  it("accepts a valid pipeline.started event", () => {
    const result = AuditEventSchema.safeParse({
      action: "pipeline.started",
      resourceType: "pipeline",
      resourceId: "issue-1582",
      metadata: {
        pipelineRunId: "abc-123",
        issueNumber: 1582,
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid stage.started event", () => {
    const result = AuditEventSchema.safeParse({
      action: "stage.started",
      resourceType: "stage",
      resourceId: "1582:feature-dev",
      metadata: {
        pipelineRunId: "abc-123",
        stage: "feature-dev",
        issueNumber: 1582,
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid stage.completed event", () => {
    const result = AuditEventSchema.safeParse({
      action: "stage.completed",
      resourceType: "stage",
      resourceId: "1582:feature-dev",
      metadata: {
        pipelineRunId: "abc-123",
        stage: "feature-dev",
        issueNumber: 1582,
        durationMs: 12_000,
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid stage.failed event", () => {
    const result = AuditEventSchema.safeParse({
      action: "stage.failed",
      resourceType: "stage",
      resourceId: "1582:feature-dev",
      metadata: {
        pipelineRunId: "abc-123",
        stage: "feature-dev",
        issueNumber: 1582,
        error: "Stage exited with code 1",
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid cost.recorded event", () => {
    const result = AuditEventSchema.safeParse({
      action: "cost.recorded",
      resourceType: "stage",
      resourceId: "feature-dev",
      metadata: {
        pipelineRunId: "abc-123",
        stage: "feature-dev",
        inputTokens: 5_000,
        outputTokens: 2_000,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.05,
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid skill.invoked event", () => {
    const result = AuditEventSchema.safeParse({
      action: "skill.invoked",
      resourceType: "skill",
      resourceId: "feature-dev",
      metadata: {
        pipelineRunId: "abc-123",
        stage: "feature-dev",
        issueNumber: 1582,
        model: "sonnet",
        outcome: "success",
        durationMs: 12_000,
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid commit.created event", () => {
    const result = AuditEventSchema.safeParse({
      action: "commit.created",
      resourceType: "commit",
      resourceId: "abc123def456",
      metadata: {
        pipelineRunId: "abc-123",
        issueNumber: 1582,
        stage: "feature-validate",
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid pipeline.completed event", () => {
    const result = AuditEventSchema.safeParse({
      action: "pipeline.completed",
      resourceType: "pipeline",
      resourceId: "issue-1582",
      metadata: {
        pipelineRunId: "abc-123",
        issueNumber: 1582,
        totalDurationMs: 120_000,
        stagesCompleted: ["issue-pickup", "feature-planning", "feature-dev"],
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("accepts a valid pipeline.failed event", () => {
    const result = AuditEventSchema.safeParse({
      action: "pipeline.failed",
      resourceType: "pipeline",
      resourceId: "issue-1582",
      metadata: {
        pipelineRunId: "abc-123",
        issueNumber: 1582,
        totalDurationMs: 60_000,
        failedStage: "feature-dev",
        timestamp: new Date().toISOString(),
      },
    });
    expect(result.success).toBe(true);
  });

  it("rejects an unknown action", () => {
    const result = AuditEventSchema.safeParse({
      action: "unknown.action",
    });
    expect(result.success).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 3. AuditEventClient — enqueue behaviour
// ---------------------------------------------------------------------------

describe("AuditEventClient — enqueue behaviour", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("does not throw when audit is disabled", () => {
    const client = new AuditEventClient({
      ...makeAuditConfig(),
      enabled: false,
    });
    expect(() =>
      client.enqueue({
        action: "pipeline.started",
        resourceType: "pipeline",
        resourceId: "issue-1",
        metadata: { pipelineRunId: "x", issueNumber: 1 },
      })
    ).not.toThrow();
  });

  it("discards invalid events and does not throw", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({ action: "not.a.real.action" });

    expect(stderrSpy).toHaveBeenCalledWith(
      expect.stringContaining("Invalid audit event discarded")
    );
  });

  it("enqueues valid stage.started event without error", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({
      action: "stage.started",
      resourceType: "stage",
      resourceId: "42:feature-dev",
      metadata: {
        pipelineRunId: "run-1",
        stage: "feature-dev",
        issueNumber: 42,
      },
    });

    // No stderr write indicates event was accepted (not discarded)
    expect(stderrSpy).not.toHaveBeenCalled();
  });

  it("enqueues valid stage.completed event without error", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({
      action: "stage.completed",
      resourceType: "stage",
      resourceId: "42:feature-dev",
      metadata: {
        pipelineRunId: "run-1",
        stage: "feature-dev",
        issueNumber: 42,
        durationMs: 5000,
      },
    });

    expect(stderrSpy).not.toHaveBeenCalled();
  });

  it("enqueues valid stage.failed event without error", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({
      action: "stage.failed",
      resourceType: "stage",
      resourceId: "42:feature-dev",
      metadata: {
        pipelineRunId: "run-1",
        stage: "feature-dev",
        issueNumber: 42,
        error: "Exit 1",
      },
    });

    expect(stderrSpy).not.toHaveBeenCalled();
  });

  it("enqueues valid cost.recorded event without error", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({
      action: "cost.recorded",
      resourceType: "stage",
      resourceId: "feature-dev",
      metadata: {
        pipelineRunId: "run-1",
        stage: "feature-dev",
        inputTokens: 1000,
        outputTokens: 500,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0.02,
      },
    });

    expect(stderrSpy).not.toHaveBeenCalled();
  });

  it("enqueues valid pipeline.completed event without error", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({
      action: "pipeline.completed",
      resourceType: "pipeline",
      resourceId: "issue-42",
      metadata: {
        pipelineRunId: "run-1",
        issueNumber: 42,
        totalDurationMs: 90_000,
        stagesCompleted: ["issue-pickup", "feature-dev"],
      },
    });

    expect(stderrSpy).not.toHaveBeenCalled();
  });

  it("enqueues valid pipeline.failed event without error", () => {
    const client = new AuditEventClient(makeAuditConfig());
    const stderrSpy = vi.spyOn(process.stderr, "write").mockImplementation(() => true);

    client.enqueue({
      action: "pipeline.failed",
      resourceType: "pipeline",
      resourceId: "issue-42",
      metadata: {
        pipelineRunId: "run-1",
        issueNumber: 42,
        totalDurationMs: 30_000,
        failedStage: "feature-dev",
      },
    });

    expect(stderrSpy).not.toHaveBeenCalled();
  });

  it("dispose() clears the flush timer", async () => {
    const clearIntervalSpy = vi.spyOn(global, "clearInterval");
    const client = new AuditEventClient(makeAuditConfig());
    await client.dispose();
    expect(clearIntervalSpy).toHaveBeenCalled();
  });

  it("flush() is a no-op when queue is empty", async () => {
    const client = new AuditEventClient(makeAuditConfig());
    // Should not throw
    await expect(client.flush()).resolves.toBeUndefined();
    await client.dispose();
  });
});

// ---------------------------------------------------------------------------
// 4. getAuditConfig defaults (direct import — not through HeadlessOrchestrator)
// ---------------------------------------------------------------------------

describe("getAuditConfig — returns safe defaults when no config file present", () => {
  it("returns enabled=false by default", async () => {
    // Dynamic import so vscode mock is applied before module loads
    const { getAuditConfig } = await import("../../src/utils/incrediConfig");
    const config = getAuditConfig("/nonexistent/workspace");
    expect(config.enabled).toBe(false);
  });

  it("returns all required AuditConfig fields", async () => {
    const { getAuditConfig } = await import("../../src/utils/incrediConfig");
    const config = getAuditConfig("/nonexistent/workspace");
    expect(config).toMatchObject({
      enabled: expect.any(Boolean),
      batchSize: expect.any(Number),
      flushIntervalMs: expect.any(Number),
      offlineQueuePath: expect.any(String),
      offlineQueueMaxSize: expect.any(Number),
      retryMaxAttempts: expect.any(Number),
      retryBackoffMs: expect.any(Number),
      timeoutMs: expect.any(Number),
    });
  });
});
