/**
 * Tests for platform emit (Issue #4167). The HTTP boundary is injected — no
 * network — and config resolution is tested with fake env/file/overrides.
 */

import { describe, it, expect } from "vitest";
import {
  emitEvalRun,
  resolveEvalEmitConfig,
  EvalEmitError,
  DEFAULT_PLATFORM_URL,
  EVAL_INGEST_PATH,
  type EvalEmitConfig,
  type FetchLike,
} from "../../src/eval/platformEmit.js";
import { MODEL_EVAL_SCHEMA_VERSION, type EvalRun } from "../../src/eval/modelEvalSchemas.js";

const RUN: EvalRun = {
  schema_version: MODEL_EVAL_SCHEMA_VERSION,
  run_id: "run-1",
  timestamp: "2026-07-01T00:00:00.000Z",
  mode: "live",
  suite: "compare",
  tasks: ["t"],
  matrix: [{ model_id: "claude-sonnet-5", effort: "high", reasoning: "none" }],
  models: [],
  cells: [],
  summary: { total: 0, passed: 0, failed: 0, errored: 0, total_cost_usd: 0 },
};

const CONFIG: EvalEmitConfig = { baseUrl: "https://api.example.com", token: "lk_secret" };

/** A FetchLike that records the request and returns a scripted response. */
function stubFetch(
  status: number,
  bodyObj: unknown
): {
  fetchImpl: FetchLike;
  calls: Array<{ url: string; headers: Record<string, string>; body: string }>;
} {
  const calls: Array<{ url: string; headers: Record<string, string>; body: string }> = [];
  const fetchImpl: FetchLike = async (url, init) => {
    calls.push({ url, headers: init.headers, body: init.body });
    return {
      status,
      ok: status >= 200 && status < 300,
      text: async () => JSON.stringify(bodyObj),
    };
  };
  return { fetchImpl, calls };
}

describe("resolveEvalEmitConfig", () => {
  it("prefers overrides, then env API key, then license key, then file", () => {
    const r = resolveEvalEmitConfig({
      env: { NIGHTGAUGE_API_KEY: "api", NIGHTGAUGE_LICENSE_KEY: "lic" },
      fileConfig: { license_key: "file" },
      overrides: { baseUrl: "https://ov.example.com/" },
    });
    expect(r).toEqual({ baseUrl: "https://ov.example.com", token: "api" }); // trailing slash trimmed
  });

  it("falls back to license key env then file, and default URL", () => {
    expect(resolveEvalEmitConfig({ env: { NIGHTGAUGE_LICENSE_KEY: "lic" } })).toEqual({
      baseUrl: DEFAULT_PLATFORM_URL,
      token: "lic",
    });
    expect(
      resolveEvalEmitConfig({
        env: {},
        fileConfig: { api_url: "https://f.example.com", license_key: "fk" },
      })
    ).toEqual({ baseUrl: "https://f.example.com", token: "fk" });
  });

  it("returns an error (not a throw) when no token is found", () => {
    const r = resolveEvalEmitConfig({ env: {}, fileConfig: null });
    expect("error" in r).toBe(true);
    expect((r as { error: string }).error).toMatch(/no platform credential/);
  });

  it("refuses to send credentials to a non-HTTPS, non-loopback URL", () => {
    const r = resolveEvalEmitConfig({
      env: { NIGHTGAUGE_LICENSE_KEY: "lic" },
      overrides: { baseUrl: "http://attacker.example.com" },
    });
    expect("error" in r).toBe(true);
    expect((r as { error: string }).error).toMatch(/must be https/i);
  });

  it("allows http:// only for localhost/loopback (local dev)", () => {
    expect(
      resolveEvalEmitConfig({
        env: { NIGHTGAUGE_LICENSE_KEY: "lic" },
        overrides: { baseUrl: "http://localhost:8787" },
      })
    ).toEqual({ baseUrl: "http://localhost:8787", token: "lic" });
    expect(
      resolveEvalEmitConfig({
        env: { NIGHTGAUGE_LICENSE_KEY: "lic" },
        overrides: { baseUrl: "http://127.0.0.1:8787/" },
      })
    ).toEqual({ baseUrl: "http://127.0.0.1:8787", token: "lic" });
  });

  it("rejects an unparseable platform URL", () => {
    const r = resolveEvalEmitConfig({
      env: { NIGHTGAUGE_LICENSE_KEY: "lic" },
      overrides: { baseUrl: "not a url" },
    });
    expect((r as { error: string }).error).toMatch(/invalid platform URL/);
  });
});

describe("emitEvalRun", () => {
  it("POSTs the run to the ingest path with bearer auth and returns the envelope", async () => {
    const { fetchImpl, calls } = stubFetch(202, { accepted: 1, rejected: [] });
    const result = await emitEvalRun(RUN, CONFIG, { fetchImpl });

    expect(result).toEqual({ accepted: 1, rejected: [] });
    expect(calls[0].url).toBe("https://api.example.com" + EVAL_INGEST_PATH);
    expect(calls[0].headers.Authorization).toBe("Bearer lk_secret");
    expect(calls[0].headers["Content-Type"]).toBe("application/json");
    // The run is sent as-is (no mapping): body round-trips to the same object.
    expect(JSON.parse(calls[0].body).run_id).toBe("run-1");
    expect(JSON.parse(calls[0].body).cells).toEqual([]);
  });

  it("surfaces per-run rejections from a 202 (accepted:0)", async () => {
    const { fetchImpl } = stubFetch(202, {
      accepted: 0,
      rejected: [{ index: 0, reason: "cells.0.verdict: Invalid enum value" }],
    });
    const result = await emitEvalRun(RUN, CONFIG, { fetchImpl });
    expect(result.accepted).toBe(0);
    expect(result.rejected[0].reason).toMatch(/verdict/);
  });

  it("throws EvalEmitError on non-2xx, surfacing the server error code/message", async () => {
    const { fetchImpl } = stubFetch(403, {
      error: {
        code: "LICENSE_TIER_EXCEEDED",
        message: "Eval ingest is not available on the community plan.",
      },
    });
    await expect(emitEvalRun(RUN, CONFIG, { fetchImpl })).rejects.toMatchObject({
      name: "EvalEmitError",
      status: 403,
      code: "LICENSE_TIER_EXCEEDED",
    });
  });

  it("times out a stalled response BODY (not just headers) instead of hanging", async () => {
    // fetch resolves on headers; text() (the body) stalls past the timeout.
    const slowBodyFetch: FetchLike = async () => ({
      status: 202,
      ok: true,
      text: () => new Promise<string>((resolve) => setTimeout(() => resolve("{}"), 50)),
    });
    await expect(
      emitEvalRun(RUN, CONFIG, { fetchImpl: slowBodyFetch, timeoutMs: 5 })
    ).rejects.toMatchObject({
      name: "EvalEmitError",
    });
    await expect(
      emitEvalRun(RUN, CONFIG, { fetchImpl: slowBodyFetch, timeoutMs: 5 })
    ).rejects.toThrow(/timed out/);
  });

  it("never leaks the token in error messages", async () => {
    const leaky: FetchLike = async () => {
      throw new Error("connect ECONNREFUSED with token lk_secret in the message");
    };
    const err = await emitEvalRun(RUN, CONFIG, { fetchImpl: leaky }).catch(
      (e) => e as EvalEmitError
    );
    expect(err).toBeInstanceOf(EvalEmitError);
    expect(err.message).not.toContain("lk_secret");
    expect(err.message).toContain("<redacted>");
  });
});
