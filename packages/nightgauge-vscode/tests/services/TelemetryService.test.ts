/**
 * Tests for TelemetryService — submission path (#1480, #3326, #3327).
 *
 * Consent UX moved to TelemetryConsentService and is covered there. This
 * suite verifies:
 * - Consent gate (`consentService.isEnabled()` short-circuits)
 * - Per-stream gate runs BEFORE enqueue (ADR-005)
 * - Redaction-and-submit loop in flushQueue (#3326)
 * - lastUploadAt recorded after a successful flush (#3327)
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { TelemetryService } from "../../src/services/TelemetryService";
import type { PipelineExecutionInput } from "../../src/utils/telemetryEventBuilder";
import type { PipelineState } from "../../src/services/PipelineStateService";
import * as vscode from "vscode";

vi.mock("vscode", () => ({
  window: {
    showInformationMessage: vi.fn(),
    showQuickPick: vi.fn(),
    showErrorMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  env: {
    isTelemetryEnabled: true,
  },
  EventEmitter: vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
}));

// ─── Test helpers ──────────────────────────────────────────────────────────

function makeConfigBridge() {
  return { getPlatform: vi.fn(() => undefined) };
}

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

function makeIpcClient() {
  return {
    platformSubmitAnalytics: vi.fn().mockResolvedValue({ status: "ok" }),
  };
}

interface ConsentMock {
  isEnabled: ReturnType<typeof vi.fn>;
  isStreamEnabled: ReturnType<typeof vi.fn>;
  recordUploadAt: ReturnType<typeof vi.fn>;
}

function makeConsent(
  opts: {
    enabled?: boolean;
    streams?: { [k: string]: boolean };
  } = {}
): ConsentMock {
  const enabled = opts.enabled ?? true;
  const streams = opts.streams ?? { "pipeline-run": true, health: true, recommendation: true };
  return {
    isEnabled: vi.fn(() => enabled),
    isStreamEnabled: vi.fn((stream: string) => enabled && streams[stream] === true),
    recordUploadAt: vi.fn().mockResolvedValue(undefined),
  };
}

function makeInput(): PipelineExecutionInput {
  const state: PipelineState = {
    issue_number: 42,
    title: "Test",
    branch: "feat/42",
    stages: { "issue-pickup": { status: "complete" } },
    started_at: "2026-03-11T10:00:00.000Z",
    tokens: {
      input: 0,
      output: 0,
      total_input: 1000,
      total_output: 200,
    },
    outcome_type: "productive",
  };
  return {
    state,
    issueMetadata: { issueNumber: 42, sizeLabel: "M", typeLabel: "feature" },
    startedAt: new Date("2026-03-11T10:00:00.000Z"),
    completedAt: new Date("2026-03-11T10:00:30.000Z"),
  };
}

// ─── Consent gate ──────────────────────────────────────────────────────────

describe("TelemetryService.isTelemetryEnabled", () => {
  beforeEach(() => {
    vi.mocked(vscode.env).isTelemetryEnabled = true;
  });

  it("returns false when VSCode global telemetry is off", () => {
    vi.mocked(vscode.env).isTelemetryEnabled = false;
    const svc = new TelemetryService(
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any
    );
    expect(svc.isTelemetryEnabled()).toBe(false);
  });

  it("returns false when consent service reports disabled", () => {
    const svc = new TelemetryService(
      makeConfigBridge() as any,
      makeConsent({ enabled: false }) as any
    );
    expect(svc.isTelemetryEnabled()).toBe(false);
  });

  it("returns true when both VSCode global and consent are enabled", () => {
    const svc = new TelemetryService(
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any
    );
    expect(svc.isTelemetryEnabled()).toBe(true);
  });

  it("returns false when no consent service is provided", () => {
    const svc = new TelemetryService(makeConfigBridge() as any, null);
    expect(svc.isTelemetryEnabled()).toBe(false);
  });
});

// ─── Submission ─────────────────────────────────────────────────────────────

describe("TelemetryService.recordPipelineExecution", () => {
  beforeEach(() => {
    TelemetryService.resetInstance();
    vi.mocked(vscode.env).isTelemetryEnabled = true;
  });
  afterEach(() => {
    TelemetryService.resetInstance();
  });

  it("returns null from initialize when ipcClient is null", () => {
    const result = TelemetryService.initialize(
      null,
      makeConfigBridge() as any,
      makeConsent() as any
    );
    expect(result).toBeNull();
  });

  it("does not queue or submit when consent is disabled", async () => {
    const client = makeIpcClient();
    const consent = makeConsent({ enabled: false });
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      consent as any
    )!;

    await svc.recordPipelineExecution(makeInput());

    expect(svc.getQueueSize()).toBe(0);
    expect(client.platformSubmitAnalytics).not.toHaveBeenCalled();
  });

  it("does not queue when pipeline-run stream is disabled (per-stream gate runs before enqueue)", async () => {
    const client = makeIpcClient();
    const consent = makeConsent({
      enabled: true,
      streams: { "pipeline-run": false, health: true, recommendation: true },
    });
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      consent as any
    )!;

    await svc.recordPipelineExecution(makeInput());

    expect(svc.getQueueSize()).toBe(0);
    expect(client.platformSubmitAnalytics).not.toHaveBeenCalled();
    expect(consent.isStreamEnabled).toHaveBeenCalledWith("pipeline-run");
  });

  it("queues and flushes when consent + stream are enabled", async () => {
    const client = makeIpcClient();
    const consent = makeConsent({ enabled: true });
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      consent as any
    )!;

    await svc.recordPipelineExecution(makeInput());

    expect(svc.getQueueSize()).toBe(0);
    expect(client.platformSubmitAnalytics).toHaveBeenCalledTimes(1);
    expect(client.platformSubmitAnalytics.mock.calls[0][0]).toBe("pipeline_execution_completed");
  });

  it("records lastUploadAt after a successful flush", async () => {
    const client = makeIpcClient();
    const consent = makeConsent({ enabled: true });
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      consent as any
    )!;

    await svc.recordPipelineExecution(makeInput());

    expect(consent.recordUploadAt).toHaveBeenCalledTimes(1);
    const arg = consent.recordUploadAt.mock.calls[0][0];
    expect(typeof arg).toBe("number");
  });

  it("does NOT record lastUploadAt when no events were submitted", async () => {
    const client = makeIpcClient();
    const consent = makeConsent({ enabled: true });
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      consent as any
    )!;

    await svc.flushQueue();
    expect(consent.recordUploadAt).not.toHaveBeenCalled();
  });

  it("does NOT record lastUploadAt when all submissions throw", async () => {
    const client = makeIpcClient();
    client.platformSubmitAnalytics.mockRejectedValue(new Error("boom"));
    const consent = makeConsent({ enabled: true });
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      consent as any
    )!;

    await svc.recordPipelineExecution(makeInput());
    expect(consent.recordUploadAt).not.toHaveBeenCalled();
  });

  it("drops oldest event when queue reaches 100", () => {
    const client = makeIpcClient();
    client.platformSubmitAnalytics.mockImplementation(() => new Promise(() => {}));
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any
    )!;

    const enqueue = (svc as any).enqueue.bind(svc);
    for (let i = 0; i < 100; i++) {
      enqueue(
        {
          eventType: `event_${i}`,
          payload: { index: i },
          timestamp: new Date().toISOString(),
        },
        "pipeline-run"
      );
    }
    expect(svc.getQueueSize()).toBe(100);

    enqueue(
      { eventType: "event_100", payload: { index: 100 }, timestamp: new Date().toISOString() },
      "pipeline-run"
    );
    expect(svc.getQueueSize()).toBe(100);

    expect((svc as any).queue[0].event.eventType).toBe("event_1");
    expect((svc as any).queue[99].event.eventType).toBe("event_100");
  });

  it("clears queue even when individual submissions fail", async () => {
    const client = makeIpcClient();
    client.platformSubmitAnalytics.mockRejectedValue(new Error("Server error"));
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any
    )!;

    const enqueue = (svc as any).enqueue.bind(svc);
    for (let i = 0; i < 3; i++) {
      enqueue(
        { eventType: `event_${i}`, payload: {}, timestamp: new Date().toISOString() },
        "pipeline-run"
      );
    }
    await svc.flushQueue();
    expect(svc.getQueueSize()).toBe(0);
  });
});

// ─── Redaction gate (#3326) ────────────────────────────────────────────────

describe("TelemetryService.flushQueue — redaction gate (#3326)", () => {
  beforeEach(() => {
    TelemetryService.resetInstance();
    vi.mocked(vscode.env).isTelemetryEnabled = true;
  });
  afterEach(() => {
    TelemetryService.resetInstance();
  });

  function makeRedactorSpy(opts?: { fieldsRemoved?: number; fieldsRedacted?: number }) {
    const calls: { eventType: string; payload: Record<string, unknown> | undefined }[] = [];
    return {
      calls,
      redact: vi.fn((event: any) => {
        calls.push({ eventType: event.eventType, payload: event.payload });
        return {
          event: {
            eventType: event.eventType,
            payload: { redacted_marker: true },
            timestamp: event.timestamp,
          },
          fieldsRemoved: opts?.fieldsRemoved ?? 0,
          fieldsRedacted: opts?.fieldsRedacted ?? 0,
        };
      }),
    };
  }

  it("calls redact() exactly once per event and submits redacted output", async () => {
    const client = makeIpcClient();
    const logger = makeLogger();
    const redactor = makeRedactorSpy({ fieldsRemoved: 0, fieldsRedacted: 0 });

    const svc = new TelemetryService(
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any,
      logger as any,
      client as any,
      redactor as any
    );

    const enqueue = (svc as any).enqueue.bind(svc);
    enqueue({ eventType: "e1", payload: { token: "drop1" }, timestamp: "t1" }, "pipeline-run");
    enqueue({ eventType: "e2", payload: { foo: 1 }, timestamp: "t2" }, "pipeline-run");

    await svc.flushQueue();

    expect(redactor.redact).toHaveBeenCalledTimes(2);
    expect(client.platformSubmitAnalytics).toHaveBeenCalledTimes(2);
    expect(client.platformSubmitAnalytics.mock.calls[0][1]).toEqual({
      redacted_marker: true,
    });
    expect(client.platformSubmitAnalytics.mock.calls[1][1]).toEqual({
      redacted_marker: true,
    });
    expect(redactor.calls[0].payload).toEqual({ token: "drop1" });
    expect(redactor.calls[1].payload).toEqual({ foo: 1 });
  });

  it("logs redaction summary when fields were removed or redacted", async () => {
    const client = makeIpcClient();
    const logger = makeLogger();
    const redactor = makeRedactorSpy({ fieldsRemoved: 2, fieldsRedacted: 1 });

    const svc = new TelemetryService(
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any,
      logger as any,
      client as any,
      redactor as any
    );

    const enqueue = (svc as any).enqueue.bind(svc);
    enqueue({ eventType: "e1", payload: { token: "drop" }, timestamp: "t1" }, "pipeline-run");
    enqueue({ eventType: "e2", payload: { token: "drop" }, timestamp: "t2" }, "pipeline-run");

    await svc.flushQueue();

    expect(logger.info).toHaveBeenCalledWith(
      "Telemetry redaction summary",
      expect.objectContaining({
        records_redacted: 2,
        fields_removed: 4,
        records_in_flush: 2,
      })
    );
  });

  it("does not log a summary when no fields were redacted", async () => {
    const client = makeIpcClient();
    const logger = makeLogger();
    const redactor = makeRedactorSpy({ fieldsRemoved: 0, fieldsRedacted: 0 });

    const svc = new TelemetryService(
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any,
      logger as any,
      client as any,
      redactor as any
    );

    const enqueue = (svc as any).enqueue.bind(svc);
    enqueue({ eventType: "e1", payload: { foo: 1 }, timestamp: "t1" }, "pipeline-run");

    await svc.flushQueue();
    expect(logger.info).not.toHaveBeenCalledWith("Telemetry redaction summary", expect.anything());
  });

  it("uses the default RedactionService end-to-end (drops secret keys)", async () => {
    const client = makeIpcClient();
    const svc = TelemetryService.initialize(
      client as any,
      makeConfigBridge() as any,
      makeConsent({ enabled: true }) as any
    )!;

    const enqueue = (svc as any).enqueue.bind(svc);
    enqueue(
      {
        eventType: "pipeline_execution_completed",
        payload: { duration_ms: 100, token: "sk-leak", _debug_capture: "user code" },
        timestamp: "2026-05-08T12:00:00.000Z",
      },
      "pipeline-run"
    );

    await svc.flushQueue();

    expect(client.platformSubmitAnalytics).toHaveBeenCalledTimes(1);
    const submittedPayload = client.platformSubmitAnalytics.mock.calls[0][1];
    expect(submittedPayload).toEqual({ duration_ms: 100 });
    expect(submittedPayload).not.toHaveProperty("token");
    expect(submittedPayload).not.toHaveProperty("_debug_capture");
  });
});
