/**
 * Tests for platformResult.ts (Issue #743)
 *
 * Covers classifyPlatformError()'s mapping from the Go layer's error text
 * (and the local IPC transport's own error text) into a PlatformFailureKind,
 * with status extracted where the message carries one — the piece all six
 * Platform*Service classes share.
 */

import { describe, it, expect, vi } from "vitest";
import {
  classifyPlatformError,
  reportPlatformFailure,
  platformOk,
} from "../../src/services/platformResult";

describe("classifyPlatformError", () => {
  it("maps 'server returned 401' to unauthorized with status 401", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: get analytics health: server returned 401"),
      "platform.getAnalyticsHealth"
    );
    expect(failure.ok).toBe(false);
    expect(failure.kind).toBe("unauthorized");
    expect(failure.status).toBe(401);
    expect(failure.endpoint).toBe("platform.getAnalyticsHealth");
    expect(failure.message).toContain("server returned 401");
  });

  it("maps 'server returned 403' to forbidden with status 403", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: get cost analytics: server returned 403"),
      "platform.getCostAnalytics"
    );
    expect(failure.kind).toBe("forbidden");
    expect(failure.status).toBe(403);
  });

  it("maps 'server returned 500' to server_error with status 500", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: get analytics runs: server returned 500"),
      "platform.getAnalyticsRuns"
    );
    expect(failure.kind).toBe("server_error");
    expect(failure.status).toBe(500);
  });

  // #800: a 422 from /v1/analytics/cost was bucketed as server_error and the
  // Cost tab told the user "This may be transient — retry". It was not
  // transient — the extension was sending a bare calendar date into a
  // `format: date-time` query parameter — so the retry button could never
  // succeed. A 4xx is our bug, and must be named as one.
  it("maps a 422 to bad_request, not server_error", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32603: get cost analytics: server returned 422"),
      "platform.getCostAnalytics"
    );
    expect(failure.kind).toBe("bad_request");
    expect(failure.status).toBe(422);
  });

  it("maps other 4xx client errors to bad_request", () => {
    for (const status of [400, 404, 409, 422]) {
      const failure = classifyPlatformError(
        new Error(`IPC error -32000: get cost analytics: server returned ${status}`),
        "platform.getCostAnalytics"
      );
      expect(failure.kind).toBe("bad_request");
      expect(failure.status).toBe(status);
    }
  });

  it("keeps 401 and 403 on their own kinds rather than folding them into bad_request", () => {
    expect(
      classifyPlatformError(new Error("server returned 401"), "platform.getCostAnalytics").kind
    ).toBe("unauthorized");
    expect(
      classifyPlatformError(new Error("server returned 403"), "platform.getCostAnalytics").kind
    ).toBe("forbidden");
  });

  it("keeps 5xx on server_error, which is genuinely worth retrying", () => {
    for (const status of [500, 502, 503]) {
      expect(
        classifyPlatformError(new Error(`server returned ${status}`), "platform.getCostAnalytics")
          .kind
      ).toBe("server_error");
    }
  });

  it("maps 'unexpected response 401' (usage summary's own phrasing) to unauthorized", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: get analytics dashboard: unexpected response 401"),
      "platform.getUsageSummary"
    );
    expect(failure.kind).toBe("unauthorized");
    expect(failure.status).toBe(401);
  });

  it("maps 'analytics service unavailable' to not_configured with no status", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: analytics service unavailable"),
      "platform.getAnalyticsHealth"
    );
    expect(failure.kind).toBe("not_configured");
    expect(failure.status).toBeUndefined();
  });

  it("maps 'compliance service unavailable' to not_configured", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: compliance service unavailable"),
      "platform.auditListReports"
    );
    expect(failure.kind).toBe("not_configured");
  });

  it("maps 'platform client not configured' to not_configured", () => {
    const failure = classifyPlatformError(
      new Error("IPC error -32000: platform client not configured"),
      "platform.getUsageSummary"
    );
    expect(failure.kind).toBe("not_configured");
  });

  it("maps a dead Go backend to offline with no status", () => {
    const failure = classifyPlatformError(
      new Error("Go backend exited with code 1"),
      "platform.getAnalyticsTrends"
    );
    expect(failure.kind).toBe("offline");
    expect(failure.status).toBeUndefined();
  });

  it("maps an IPC timeout to offline", () => {
    const failure = classifyPlatformError(
      new Error("IPC request platform.getCostAnalytics timed out after 30000ms"),
      "platform.getCostAnalytics"
    );
    expect(failure.kind).toBe("offline");
  });

  it("maps a disposed IPC client to offline", () => {
    const failure = classifyPlatformError(
      new Error("IPC client disposed"),
      "platform.getAnalyticsRuns"
    );
    expect(failure.kind).toBe("offline");
  });

  it("maps a network-level dial failure to offline", () => {
    const failure = classifyPlatformError(
      new Error("get analytics health: dial tcp 10.0.0.1:443: connect: connection refused"),
      "platform.getAnalyticsHealth"
    );
    expect(failure.kind).toBe("offline");
  });

  it("falls back to server_error for an unrecognized message, never throwing", () => {
    const failure = classifyPlatformError(new Error("something bizarre happened"), "platform.getX");
    expect(failure.kind).toBe("server_error");
    expect(failure.status).toBeUndefined();
  });

  it("handles a non-Error rejection without throwing", () => {
    const failure = classifyPlatformError("a plain string rejection", "platform.getX");
    expect(failure.ok).toBe(false);
    expect(failure.message).toBe("a plain string rejection");
    expect(failure.kind).toBe("server_error");
  });
});

describe("reportPlatformFailure", () => {
  it("logs once via logger.warn and returns the classified failure", () => {
    const logger = { warn: vi.fn() };
    const failure = reportPlatformFailure(
      logger,
      new Error("get analytics health: server returned 401"),
      "platform.getAnalyticsHealth"
    );

    expect(logger.warn).toHaveBeenCalledTimes(1);
    const [message, data] = logger.warn.mock.calls[0];
    expect(message).toContain("platform.getAnalyticsHealth");
    expect(data).toMatchObject({ kind: "unauthorized", status: 401 });
    expect(failure).toEqual({
      ok: false,
      kind: "unauthorized",
      status: 401,
      endpoint: "platform.getAnalyticsHealth",
      message: "get analytics health: server returned 401",
    });
  });
});

describe("platformOk", () => {
  it("wraps a value as a success result", () => {
    expect(platformOk({ a: 1 })).toEqual({ ok: true, value: { a: 1 } });
  });
});
