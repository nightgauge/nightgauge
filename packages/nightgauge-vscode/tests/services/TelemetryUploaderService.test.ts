/**
 * Tests for TelemetryUploaderService (#3315, #3316).
 *
 * Covers mandatory acceptance criteria:
 * 1. Watermark advances after successful upload
 * 2. Retry on HTTP 429 (3 fetch calls for 2×429 then 200)
 * 3. No upload when consent is disabled
 * 4. Batch boundary — 250 lines → 3 batches (100, 100, 50)
 * 5. Size guard — files > 10 MB are skipped
 * 6. No upload when license key is null (not configured)
 * 7. Health stream: 600 records → 2 batches (500, 100), correct endpoint
 * 8. Recommendation stream: filter — only metric_after-populated records uploaded
 * 9. Per-stream consent gate — disabling health leaves pipeline-run unaffected
 * 10. Integration: all three streams in one cycle
 * 11. Watermark isolation — health/recommendation keys don't collide with pipeline-run
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  TelemetryUploaderService,
  MAX_FILE_BYTES,
  MIN_UPLOAD_GAP_MS,
  ACTIVE_RUN_UPLOAD_INTERVAL_MS,
  ACTIVE_RUN_FLUSH_EVENT_COUNT,
} from "../../src/services/TelemetryUploaderService";
import type { TelemetryStream } from "../../src/services/telemetry/types";
import * as vscode from "vscode";

// ─── vscode mock ──────────────────────────────────────────────────────────────

vi.mock("vscode", () => ({
  FileType: { File: 1, Directory: 2, SymbolicLink: 64, Unknown: 0 },
  workspace: {
    fs: {
      readDirectory: vi.fn(),
      stat: vi.fn(),
      readFile: vi.fn(),
      writeFile: vi.fn(),
      rename: vi.fn(),
    },
  },
  Uri: {
    file: vi.fn((p: string) => ({ fsPath: p, toString: () => p })),
  },
}));

vi.stubGlobal("fetch", vi.fn());

// ─── Helpers ─────────────────────────────────────────────────────────────────

const FILE_TYPE_FILE = 1;

function makeLicenseKey(key: string | null = "test-license-key"): () => string | null {
  return () => key;
}

function makeConsentService(
  enabled = true,
  streamOverrides: Partial<Record<TelemetryStream, boolean>> = {}
) {
  return {
    isEnabled: vi.fn().mockReturnValue(enabled),
    isStreamEnabled: vi.fn().mockImplementation((stream: TelemetryStream) => {
      if (stream in streamOverrides) return streamOverrides[stream];
      return enabled;
    }),
  };
}

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

/**
 * Produces mappable V2 pipeline-run records — each carries the fields the
 * V2→V4 mapper requires (repo, started_at, outcome). Records lacking these are
 * exercised separately in the "unmappable" tests below.
 */
function makeJsonlContent(lines: number): string {
  return Array.from({ length: lines }, (_, i) =>
    JSON.stringify({
      schema_version: "2",
      record_type: "run",
      issue_number: i + 1,
      repo: "nightgauge/nightgauge",
      started_at: "2026-05-10T10:00:00.000Z",
      completed_at: "2026-05-10T10:05:00.000Z",
      outcome: "complete",
      total_duration_ms: 300000,
      tokens: { estimated_cost_usd: 0.1, per_stage: {} },
      routing: { complexity_score: 3, path: "standard", skip_stages: [] },
      stages: {},
    })
  ).join("\n");
}

/**
 * A 202 Accepted response with a parseable {accepted, rejected} body — the
 * pipeline-run stream now inspects this body (a bare 202 is not proof of
 * success). `rejected` defaults to none (whole batch accepted).
 */
function okResponse(rejected: { index: number; reason?: string }[] = []): Response {
  return {
    ok: true,
    status: 202,
    json: async () => ({ rejected }),
  } as unknown as Response;
}

function makeRecommendationContent(total: number, withMetricAfter: number): string {
  return Array.from({ length: total }, (_, i) =>
    JSON.stringify({
      schema_version: "1",
      finding_id: `finding-${i}`,
      created_at: new Date().toISOString(),
      metric_after: i < withMetricAfter ? 75 : null,
    })
  ).join("\n");
}

function encodeContent(str: string): Uint8Array {
  return Buffer.from(str, "utf8");
}

/**
 * Set up vscode.workspace.fs mocks for daily files, watermarks, and optional
 * consolidated (single fixed-path) files.
 */
function setupFs(
  files: Array<{ name: string; sizeBytes?: number; content?: string }>,
  existingWatermarks: Record<string, number> = {},
  consolidatedFiles: Array<{ filename: string; sizeBytes?: number; content?: string }> = [],
  traceFiles: Array<{ name: string; sizeBytes?: number; content?: string }> = []
): void {
  const fsMock = vi.mocked(vscode.workspace.fs);

  // Mutable watermark state — updated on each tmp write so subsequent
  // loadWatermarks() calls within the same cycle see accumulated keys from
  // all previously-processed streams (mirrors real file behaviour).
  const watermarkState: Record<string, number> = { ...existingWatermarks };

  // Directory scans are path-routed: the pipeline-run stream scans history/,
  // the trace stream (ADR 013 / #180) scans trace/.
  fsMock.readDirectory.mockImplementation(async (uri) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    const source = fsPath.includes(`${"/"}trace`) ? traceFiles : files;
    return source.map((f) => [f.name, FILE_TYPE_FILE] as [string, vscode.FileType]);
  });

  const findByPath = (fsPath: string) => {
    const filename = fsPath.split("/").pop()!;
    if (fsPath.includes("/trace/")) {
      return traceFiles.find((f) => f.name === filename);
    }
    const dailyFile = files.find((f) => f.name === filename);
    if (dailyFile) return dailyFile;
    const consolidated = consolidatedFiles.find((f) => f.filename === filename);
    if (consolidated) {
      return {
        name: consolidated.filename,
        sizeBytes: consolidated.sizeBytes,
        content: consolidated.content,
      };
    }
    return undefined;
  };

  fsMock.stat.mockImplementation(async (uri) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    const entry = findByPath(fsPath);
    if (entry) {
      return { size: entry.sizeBytes ?? 100, type: FILE_TYPE_FILE, ctime: 0, mtime: 0 };
    }
    // No file registered — simulate not-found so the uploader skips it
    throw Object.assign(new Error("not found"), { code: "FileNotFound" });
  });

  fsMock.readFile.mockImplementation(async (uri) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    if (fsPath.endsWith(".upload-watermarks.json")) {
      return encodeContent(JSON.stringify(watermarkState));
    }
    const entry = findByPath(fsPath);
    if (entry) return encodeContent(entry.content ?? "");
    return encodeContent("");
  });

  fsMock.writeFile.mockImplementation(async (uri, data) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    // Merge new watermark keys into live state so subsequent reads see them
    if (fsPath.endsWith(".upload-watermarks.json.tmp")) {
      const parsed = JSON.parse(Buffer.from(data as Uint8Array).toString("utf8")) as Record<
        string,
        number
      >;
      Object.assign(watermarkState, parsed);
    }
  });
  fsMock.rename.mockResolvedValue(undefined);
}

/**
 * Multi-root variant of {@link setupFs} for the #247 workspace-roots coverage
 * below — routes directory scans / file reads / watermark files by which
 * configured root a path falls under, so history and trace files in
 * different repo roots (e.g. the primary workspace root vs. a target repo)
 * can carry independent content and independent watermark stores, mirroring
 * how each repo root has its own `.nightgauge/pipeline/` directory on disk.
 */
function setupMultiRootFs(
  roots: Array<{
    root: string;
    historyFiles?: Array<{ name: string; content?: string; sizeBytes?: number }>;
    traceFiles?: Array<{ name: string; content?: string; sizeBytes?: number }>;
  }>
): void {
  const fsMock = vi.mocked(vscode.workspace.fs);
  const watermarkState: Record<string, Record<string, number>> = {};
  for (const r of roots) {
    watermarkState[r.root] = {};
  }

  const findRoot = (fsPath: string) => roots.find((r) => fsPath.startsWith(`${r.root}/`));

  const filesFor = (fsPath: string, r: (typeof roots)[number]) =>
    fsPath.includes(`${"/"}trace${"/"}`) || fsPath.endsWith(`${"/"}trace`)
      ? (r.traceFiles ?? [])
      : (r.historyFiles ?? []);

  fsMock.readDirectory.mockImplementation(async (uri) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    const r = findRoot(fsPath);
    const files = r ? filesFor(fsPath, r) : [];
    if (!r || files.length === 0) {
      throw Object.assign(new Error("not found"), { code: "FileNotFound" });
    }
    return files.map((f) => [f.name, FILE_TYPE_FILE] as [string, vscode.FileType]);
  });

  const findFile = (fsPath: string) => {
    const r = findRoot(fsPath);
    if (!r) return undefined;
    const filename = fsPath.split("/").pop()!;
    return filesFor(fsPath, r).find((f) => f.name === filename);
  };

  fsMock.stat.mockImplementation(async (uri) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    const entry = findFile(fsPath);
    if (entry) {
      return { size: entry.sizeBytes ?? 100, type: FILE_TYPE_FILE, ctime: 0, mtime: 0 };
    }
    throw Object.assign(new Error("not found"), { code: "FileNotFound" });
  });

  fsMock.readFile.mockImplementation(async (uri) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    if (fsPath.endsWith(".upload-watermarks.json")) {
      const r = findRoot(fsPath);
      return encodeContent(JSON.stringify(r ? watermarkState[r.root] : {}));
    }
    const entry = findFile(fsPath);
    if (entry) return encodeContent(entry.content ?? "");
    return encodeContent("");
  });

  fsMock.writeFile.mockImplementation(async (uri, data) => {
    const fsPath = (uri as { fsPath: string }).fsPath;
    if (fsPath.endsWith(".upload-watermarks.json.tmp")) {
      const r = findRoot(fsPath);
      if (r) {
        const parsed = JSON.parse(Buffer.from(data as Uint8Array).toString("utf8")) as Record<
          string,
          number
        >;
        Object.assign(watermarkState[r.root], parsed);
      }
    }
  });
  fsMock.rename.mockResolvedValue(undefined);
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("TelemetryUploaderService", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    fetchMock = vi.mocked(fetch);
  });

  // ── Test 1: Watermark advance ────────────────────────────────────────────

  it("uploads only new lines beyond watermark and advances watermark", async () => {
    const content = makeJsonlContent(5);
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 500 }], {
      "2026-05-10.jsonl": 2,
    });

    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).toHaveBeenCalledTimes(1);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.com/v1/telemetry/pipeline-run");
    expect(init.method).toBe("POST");

    const body = JSON.parse(init.body as string) as unknown[];
    expect(body).toHaveLength(3);

    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCall = fsMock.writeFile.mock.calls.find(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    expect(writeCall).toBeTruthy();
    const savedWatermarks = JSON.parse(
      Buffer.from(writeCall![1] as Uint8Array).toString("utf8")
    ) as Record<string, number>;
    expect(savedWatermarks["2026-05-10.jsonl"]).toBe(5);
  });

  // ── Test 2: Retry on 429 ──────────────────────────────────────────────────

  it("retries on 429 and advances watermark after eventual success", async () => {
    const content = makeJsonlContent(3);
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);

    fetchMock
      .mockResolvedValueOnce({ ok: false, status: 429 } as Response)
      .mockResolvedValueOnce({ ok: false, status: 429 } as Response)
      .mockResolvedValueOnce(okResponse());

    vi.useFakeTimers();

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    const cyclePromise = service.runUploadCycle();
    await vi.runAllTimersAsync();
    await cyclePromise;

    vi.useRealTimers();

    expect(fetchMock).toHaveBeenCalledTimes(3);

    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCall = fsMock.writeFile.mock.calls.find(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    expect(writeCall).toBeTruthy();
    const savedWatermarks = JSON.parse(
      Buffer.from(writeCall![1] as Uint8Array).toString("utf8")
    ) as Record<string, number>;
    expect(savedWatermarks["2026-05-10.jsonl"]).toBe(3);
  });

  // ── Test 2b: 404 logs at info; other non-2xx logs at error ──────────────────

  it("logs 404 response at info level and other non-2xx at error level", async () => {
    const content = makeJsonlContent(3);

    // First run: 404 → should use info (endpoint not deployed — low noise)
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);
    fetchMock.mockResolvedValueOnce({ ok: false, status: 404 } as Response);

    const logger404 = makeLogger();
    const service404 = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger404 as never
    );
    await service404.runUploadCycle();

    expect(logger404.info).toHaveBeenCalledWith(
      expect.stringContaining("batch upload got non-2xx"),
      expect.objectContaining({ status: 404 })
    );
    expect(logger404.error).not.toHaveBeenCalledWith(
      expect.stringContaining("batch upload got non-2xx"),
      expect.anything()
    );

    // Second run: 500 → should use error (a real server failure, surfaced loudly)
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);
    fetchMock.mockResolvedValueOnce({ ok: false, status: 500 } as Response);

    const logger500 = makeLogger();
    const service500 = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger500 as never
    );
    await service500.runUploadCycle();

    expect(logger500.error).toHaveBeenCalledWith(
      expect.stringContaining("batch upload got non-2xx"),
      expect.objectContaining({ status: 500 })
    );
    expect(logger500.info).not.toHaveBeenCalledWith(
      expect.stringContaining("batch upload got non-2xx"),
      expect.anything()
    );
  });

  // ── Test 2c: Abort on first file failure — no flood ─────────────────────

  it("stops processing remaining files when the first file upload fails", async () => {
    setupFs([
      { name: "2026-02-13.jsonl", content: makeJsonlContent(3), sizeBytes: 300 },
      { name: "2026-02-14.jsonl", content: makeJsonlContent(3), sizeBytes: 300 },
      { name: "2026-02-15.jsonl", content: makeJsonlContent(3), sizeBytes: 300 },
    ]);

    fetchMock.mockResolvedValue({ ok: false, status: 404 } as Response);

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    // Only one fetch — the first file fails and the rest are not attempted.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  // ── Test 3: No upload when consent disabled ───────────────────────────────

  it("skips upload entirely when consent is not granted", async () => {
    setupFs([{ name: "2026-05-10.jsonl", content: makeJsonlContent(5), sizeBytes: 500 }]);

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService(false) as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).not.toHaveBeenCalled();
    expect(vi.mocked(vscode.workspace.fs).readDirectory).not.toHaveBeenCalled();
  });

  // ── Test 4: Batch boundary — 250 lines → 3 batches ───────────────────────

  it("splits 250 records into 3 batches (100, 100, 50)", async () => {
    setupFs([{ name: "2026-05-10.jsonl", content: makeJsonlContent(250), sizeBytes: 25000 }]);

    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).toHaveBeenCalledTimes(3);

    const batchSizes = fetchMock.mock.calls.map(([, init]: [string, RequestInit]) => {
      const body = JSON.parse(init.body as string) as unknown[];
      return body.length;
    });
    expect(batchSizes).toEqual([100, 100, 50]);
  });

  // ── Test 5: Size guard ────────────────────────────────────────────────────

  it("skips files larger than 10 MB and logs a warning", async () => {
    setupFs([{ name: "2026-05-10.jsonl", sizeBytes: MAX_FILE_BYTES + 1 }]);

    const logger = makeLogger();
    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger as never
    );

    await service.runUploadCycle();

    expect(fetchMock).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("skipping oversized file"),
      expect.objectContaining({ filename: "2026-05-10.jsonl" })
    );
  });

  // ── Test 6: No token ──────────────────────────────────────────────────────

  it("skips upload when license key is null", async () => {
    setupFs([{ name: "2026-05-10.jsonl", content: makeJsonlContent(5), sizeBytes: 500 }]);

    const service = new TelemetryUploaderService(
      makeLicenseKey(null),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).not.toHaveBeenCalled();
    expect(vi.mocked(vscode.workspace.fs).readDirectory).not.toHaveBeenCalled();
  });

  // ── JWT fallback: upload with access token when no license key ────────────

  it("uploads using the access token (JWT) when no license key is configured", async () => {
    const content = makeJsonlContent(2);
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 200 }]);
    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(null),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never,
      () => Promise.resolve("header.payload.signature")
    );

    await service.runUploadCycle();

    expect(fetchMock).toHaveBeenCalled();
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect((init.headers as Record<string, string>)["Authorization"]).toBe(
      "Bearer header.payload.signature"
    );
  });

  it("skips upload when neither license key nor access token is available", async () => {
    setupFs([{ name: "2026-05-10.jsonl", content: makeJsonlContent(5), sizeBytes: 500 }]);

    const service = new TelemetryUploaderService(
      makeLicenseKey(null),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never,
      () => Promise.resolve(null)
    );

    await service.runUploadCycle();

    expect(fetchMock).not.toHaveBeenCalled();
  });

  // ── Additional: Concurrency guard ─────────────────────────────────────────

  it("does not run concurrent upload cycles", async () => {
    setupFs([{ name: "2026-05-10.jsonl", content: makeJsonlContent(5), sizeBytes: 500 }]);
    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    // Start first cycle and immediately start second — guard is synchronous so
    // second should return early.
    const first = service.runUploadCycle();
    const second = service.runUploadCycle();

    await Promise.all([first, second]);

    // Only one batch posted despite two cycle triggers
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  // ── Additional: dispose clears interval ───────────────────────────────────

  it("clears the periodic timer on dispose", () => {
    vi.useFakeTimers();
    const clearIntervalSpy = vi.spyOn(globalThis, "clearInterval");

    const fsMock = vi.mocked(vscode.workspace.fs);
    fsMock.readDirectory.mockRejectedValue(new Error("no dir"));
    fsMock.readFile.mockRejectedValue(new Error("no file"));

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      null
    );

    service.initialize();
    service.dispose();

    expect(clearIntervalSpy).toHaveBeenCalled();
    vi.useRealTimers();
  });

  // ── Test 7: Health stream — 600 records → 2 batches ──────────────────────

  it("uploads health stream in batches of 500 to the correct endpoint", async () => {
    const healthContent = makeJsonlContent(600);
    setupFs(
      [], // no daily files
      {},
      [{ filename: "health-history.jsonl", content: healthContent, sizeBytes: 60000 }]
    );

    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    // pipeline-run: 0 files → 0 fetches; health: 2 batches (500, 100)
    expect(fetchMock).toHaveBeenCalledTimes(2);

    const calls = fetchMock.mock.calls as [string, RequestInit][];
    expect(calls[0][0]).toBe("https://api.example.com/v1/telemetry/health-snapshot");
    expect(calls[1][0]).toBe("https://api.example.com/v1/telemetry/health-snapshot");

    const batchSizes = calls.map(([, init]) => {
      const body = JSON.parse(init.body as string) as unknown[];
      return body.length;
    });
    expect(batchSizes).toEqual([500, 100]);
  });

  // ── Test 8: Health watermark advances correctly after 600-record upload ───

  it("advances health watermark to 600 after uploading 600 records", async () => {
    const healthContent = makeJsonlContent(600);
    setupFs([], {}, [
      { filename: "health-history.jsonl", content: healthContent, sizeBytes: 60000 },
    ]);

    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCall = fsMock.writeFile.mock.calls.find(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    expect(writeCall).toBeTruthy();
    const saved = JSON.parse(Buffer.from(writeCall![1] as Uint8Array).toString("utf8")) as Record<
      string,
      number
    >;
    expect(saved["health-history.jsonl"]).toBe(600);
  });

  // ── Test 9: Recommendation filter — only metric_after records uploaded ────

  it("uploads only recommendation records with metric_after populated", async () => {
    // 10 records, 7 have metric_after, 3 have null
    const recContent = makeRecommendationContent(10, 7);
    setupFs([], {}, [
      { filename: "recommendation-history.jsonl", content: recContent, sizeBytes: 2000 },
    ]);

    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).toHaveBeenCalledTimes(1);

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.com/v1/telemetry/recommendation-outcome");

    const body = JSON.parse(init.body as string) as unknown[];
    expect(body).toHaveLength(7);
  });

  // ── Test 10: Recommendation — all-filtered records don't advance watermark ─

  it("does not advance recommendation watermark when all records are filtered out", async () => {
    // All 5 records have metric_after=null → all filtered
    const recContent = Array.from({ length: 5 }, (_, i) =>
      JSON.stringify({
        schema_version: "1",
        finding_id: `f${i}`,
        created_at: new Date().toISOString(),
        metric_after: null,
      })
    ).join("\n");

    setupFs([], {}, [
      { filename: "recommendation-history.jsonl", content: recContent, sizeBytes: 1000 },
    ]);

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).not.toHaveBeenCalled();

    // Watermark file should NOT have been written (no upload happened)
    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCall = fsMock.writeFile.mock.calls.find(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    expect(writeCall).toBeUndefined();
  });

  // ── Test 11: Per-stream consent gate ─────────────────────────────────────

  it("skips health stream when health consent disabled but uploads pipeline-run", async () => {
    const dailyContent = makeJsonlContent(3);
    const healthContent = makeJsonlContent(5);

    setupFs([{ name: "2026-05-10.jsonl", content: dailyContent, sizeBytes: 300 }], {}, [
      { filename: "health-history.jsonl", content: healthContent, sizeBytes: 500 },
    ]);

    fetchMock.mockResolvedValue(okResponse());

    const consent = makeConsentService(true, { health: false });
    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      consent as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    // Only pipeline-run should have been called (1 fetch)
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://api.example.com/v1/telemetry/pipeline-run");
  });

  // ── Test 12: Integration — all three streams in one cycle ─────────────────

  it("uploads all three streams to correct endpoints in a single cycle", async () => {
    const dailyContent = makeJsonlContent(5);
    const healthContent = makeJsonlContent(10);
    const recContent = makeRecommendationContent(6, 4); // 4 with metric_after

    setupFs([{ name: "2026-05-10.jsonl", content: dailyContent, sizeBytes: 500 }], {}, [
      { filename: "health-history.jsonl", content: healthContent, sizeBytes: 1000 },
      { filename: "recommendation-history.jsonl", content: recContent, sizeBytes: 600 },
    ]);

    fetchMock.mockResolvedValue(okResponse());

    const logger = makeLogger();
    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger as never
    );

    await service.runUploadCycle();

    // 1 pipeline-run batch + 1 health batch + 1 recommendation batch = 3 fetches
    expect(fetchMock).toHaveBeenCalledTimes(3);

    const urls = (fetchMock.mock.calls as [string, RequestInit][]).map(([url]) => url);
    expect(urls).toContain("https://api.example.com/v1/telemetry/pipeline-run");
    expect(urls).toContain("https://api.example.com/v1/telemetry/health-snapshot");
    expect(urls).toContain("https://api.example.com/v1/telemetry/recommendation-outcome");

    // Summary log line should be emitted
    expect(logger.info).toHaveBeenCalledWith(
      expect.stringContaining("cycle complete"),
      expect.objectContaining({ total: expect.any(Number) })
    );
  });

  // ── Test 13: Watermark isolation — keys don't collide ────────────────────

  it("uses separate watermark keys for health and recommendation vs pipeline-run", async () => {
    const dailyContent = makeJsonlContent(3);
    const healthContent = makeJsonlContent(4);
    const recContent = makeRecommendationContent(3, 3);

    setupFs([{ name: "2026-05-10.jsonl", content: dailyContent, sizeBytes: 300 }], {}, [
      { filename: "health-history.jsonl", content: healthContent, sizeBytes: 400 },
      { filename: "recommendation-history.jsonl", content: recContent, sizeBytes: 300 },
    ]);

    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCalls = fsMock.writeFile.mock.calls.filter(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    expect(writeCalls.length).toBeGreaterThan(0);

    // Collect all watermark saves and check the last one has all three keys
    const lastWrite = writeCalls[writeCalls.length - 1];
    const saved = JSON.parse(Buffer.from(lastWrite[1] as Uint8Array).toString("utf8")) as Record<
      string,
      number
    >;

    // Each stream uses its own basename key
    expect(saved).toHaveProperty("2026-05-10.jsonl");
    expect(saved).toHaveProperty("health-history.jsonl");
    expect(saved).toHaveProperty("recommendation-history.jsonl");

    // Watermark values are independent
    expect(saved["2026-05-10.jsonl"]).toBe(3);
    expect(saved["health-history.jsonl"]).toBe(4);
    expect(saved["recommendation-history.jsonl"]).toBe(3);
  });

  // ── Test 14: Unmappable records are skipped, not uploaded, watermark advances ─

  it("skips unmappable records (missing repo) without uploading but advances the watermark", async () => {
    // Pre-`repo` V2 records — valid JSON, but the V4 mapper cannot derive a
    // repo, so they are permanently skippable rather than uploaded.
    const content = Array.from({ length: 3 }, (_, i) =>
      JSON.stringify({
        schema_version: "2",
        record_type: "run",
        issue_number: i + 1,
        started_at: "2026-05-10T10:00:00.000Z",
        outcome: "complete",
      })
    ).join("\n");

    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);
    fetchMock.mockResolvedValue(okResponse());

    const logger = makeLogger();
    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger as never
    );

    await service.runUploadCycle();

    // Nothing POSTed (all unmappable) …
    expect(fetchMock).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("skipping unmappable run record"),
      expect.objectContaining({ reason: expect.stringContaining("repo") })
    );

    // … but the watermark advances past them so they aren't reprocessed forever.
    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCall = fsMock.writeFile.mock.calls.find(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    expect(writeCall).toBeTruthy();
    const saved = JSON.parse(Buffer.from(writeCall![1] as Uint8Array).toString("utf8")) as Record<
      string,
      number
    >;
    expect(saved["2026-05-10.jsonl"]).toBe(3);
  });

  // ── Test 15: A 202 with rejected records does NOT advance past them ──────────

  it("does not advance the watermark past server-rejected records and logs the rejection", async () => {
    const content = makeJsonlContent(3); // 3 mappable records
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);

    // Server accepts records 0 and 2 but REJECTS record at batch index 1.
    fetchMock.mockResolvedValue(okResponse([{ index: 1, reason: "schemaVersion: invalid" }]));

    const logger = makeLogger();
    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger as never
    );

    await service.runUploadCycle();

    // Rejection is surfaced loudly (the silent-data-loss guard).
    expect(logger.error).toHaveBeenCalledWith(
      expect.stringContaining("platform REJECTED pipeline-run records"),
      expect.objectContaining({ rejected: 1 })
    );

    // Watermark stops at the first rejected line (index 1) so it retries; it is
    // NOT silently advanced past the dropped record (the original bug).
    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCall = fsMock.writeFile.mock.calls.find(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    const saved = writeCall
      ? (JSON.parse(Buffer.from(writeCall[1] as Uint8Array).toString("utf8")) as Record<
          string,
          number
        >)
      : {};
    expect(saved["2026-05-10.jsonl"] ?? 0).toBe(1);
  });

  // ── Test 16: Poison-message safety valve — give up after MAX cycles ──────────

  it("advances past a permanently-rejected record after MAX_REJECTION_RETRY_CYCLES to avoid wedging the file", async () => {
    const content = makeJsonlContent(3);
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);

    // Reject batch index 0 on every cycle → the file can never advance past
    // line 0 on its own, so without the safety valve it would wedge forever.
    fetchMock.mockResolvedValue(okResponse([{ index: 0, reason: "permanently bad" }]));

    const logger = makeLogger();
    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      logger as never
    );

    // 5 cycles = MAX_REJECTION_RETRY_CYCLES.
    for (let i = 0; i < 5; i++) {
      await service.runUploadCycle();
    }

    expect(logger.error).toHaveBeenCalledWith(
      expect.stringContaining("giving up on rejected records"),
      expect.objectContaining({ filename: "2026-05-10.jsonl" })
    );

    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCalls = fsMock.writeFile.mock.calls.filter(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    const lastWrite = writeCalls[writeCalls.length - 1];
    const saved = JSON.parse(Buffer.from(lastWrite![1] as Uint8Array).toString("utf8")) as Record<
      string,
      number
    >;
    // After giving up, the whole new range is consumed so the stream unblocks.
    expect(saved["2026-05-10.jsonl"]).toBe(3);
  });

  // ── Test 17: pipelineRunId threading ──────────────────────────────────────
  //
  // The V3 JSONL record's `run_id` must land on the wire record as
  // `pipelineRunId` so the uploader's best-effort batch upsert converges on
  // the SAME pipeline_runs row the authoritative Go notify-path push
  // created, instead of the platform minting a derived-id duplicate.

  function makeJsonlLineWithRunId(runId: string | undefined): string {
    return JSON.stringify({
      schema_version: "2",
      record_type: "run",
      issue_number: 1,
      repo: "nightgauge/nightgauge",
      started_at: "2026-05-10T10:00:00.000Z",
      completed_at: "2026-05-10T10:05:00.000Z",
      outcome: "complete",
      total_duration_ms: 300000,
      tokens: { estimated_cost_usd: 0.1, per_stage: {} },
      routing: { complexity_score: 3, path: "standard", skip_stages: [] },
      stages: {},
      ...(runId !== undefined ? { run_id: runId } : {}),
    });
  }

  it("threads a valid UUID run_id onto the uploaded record as pipelineRunId", async () => {
    const runId = "01890a5d-ac96-774b-bcce-b302099a8057";
    const content = makeJsonlLineWithRunId(runId);
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);
    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as Array<Record<string, unknown>>;
    expect(body).toHaveLength(1);
    expect(body[0]!["pipelineRunId"]).toBe(runId);
  });

  it("omits pipelineRunId from the uploaded record when run_id is missing or not a well-formed UUID", async () => {
    const content = [makeJsonlLineWithRunId(undefined), makeJsonlLineWithRunId("not-a-uuid")].join(
      "\n"
    );
    setupFs([{ name: "2026-05-10.jsonl", content, sizeBytes: 300 }]);
    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string) as Array<Record<string, unknown>>;
    expect(body).toHaveLength(2);
    for (const record of body) {
      expect(record).not.toHaveProperty("pipelineRunId");
    }
  });

  // ── Trace stream (ADR 013 / Issue #180) ───────────────────────────────────

  function makeTraceContent(runId: string, lines: number): string {
    return Array.from({ length: lines }, (_, i) =>
      JSON.stringify({
        schema_version: 1,
        run_id: runId,
        issue: 180,
        seq: i + 1,
        ts: `2026-07-17T10:00:0${i}.000Z`,
        stage: "feature-dev",
        kind: i === 0 ? "stage_start" : "phase_transition",
        producer: "sdk",
      })
    ).join("\n");
  }

  it("uploads trace events verbatim to the pipeline-trace endpoint with prefixed watermarks", async () => {
    const runId = "01890a5d-ac96-774b-bcce-b302099a8057";
    setupFs(
      [],
      {},
      [],
      [{ name: `${runId}.jsonl`, content: makeTraceContent(runId, 3), sizeBytes: 600 }]
    );
    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService() as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    const traceCalls = fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/v1/telemetry/pipeline-trace")
    );
    expect(traceCalls).toHaveLength(1);
    const body = JSON.parse((traceCalls[0]![1] as RequestInit).body as string) as Array<
      Record<string, unknown>
    >;
    expect(body).toHaveLength(3);
    // Events upload verbatim.
    expect(body[0]).toMatchObject({
      schema_version: 1,
      run_id: runId,
      seq: 1,
      kind: "stage_start",
      producer: "sdk",
    });

    const fsMock = vi.mocked(vscode.workspace.fs);
    const writeCalls = fsMock.writeFile.mock.calls.filter(([uri]) =>
      (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
    );
    const lastWrite = writeCalls[writeCalls.length - 1];
    const saved = JSON.parse(Buffer.from(lastWrite![1] as Uint8Array).toString("utf8")) as Record<
      string,
      number
    >;
    // Watermark key is trace/-prefixed so run-id filenames can never collide
    // with history or consolidated keys.
    expect(saved[`trace/${runId}.jsonl`]).toBe(3);
  });

  it("skips the trace stream when its consent stream is disabled", async () => {
    const runId = "01890a5d-ac96-774b-bcce-b302099a8057";
    setupFs(
      [],
      {},
      [],
      [{ name: `${runId}.jsonl`, content: makeTraceContent(runId, 2), sizeBytes: 400 }]
    );
    fetchMock.mockResolvedValue(okResponse());

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService(true, { trace: false }) as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    const traceCalls = fetchMock.mock.calls.filter(([url]) =>
      String(url).endsWith("/v1/telemetry/pipeline-trace")
    );
    expect(traceCalls).toHaveLength(0);
  });

  it("bails out of the trace file loop after the first failed file", async () => {
    const runA = "01890a5d-ac96-774b-bcce-b30209900001";
    const runB = "01890a5d-ac96-774b-bcce-b30209900002";
    setupFs(
      [],
      {},
      [],
      [
        { name: `${runA}.jsonl`, content: makeTraceContent(runA, 2), sizeBytes: 400 },
        { name: `${runB}.jsonl`, content: makeTraceContent(runB, 2), sizeBytes: 400 },
      ]
    );
    // Endpoint not deployed: 404 for every POST.
    fetchMock.mockResolvedValue({ ok: false, status: 404 } as Response);

    const service = new TelemetryUploaderService(
      makeLicenseKey(),
      makeConsentService(true, {
        "pipeline-run": false,
        health: false,
        recommendation: false,
      }) as never,
      () => "https://api.example.com",
      "/workspace",
      makeLogger() as never
    );

    await service.runUploadCycle();

    // One attempt for the first file, then bail — not one per trace file.
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  // ── Continuous / active-run upload (#234 / ADR 014) ───────────────────────

  describe("continuous / active-run upload", () => {
    const IDLE_INTERVAL_MS = 15 * 60 * 1000; // mirror UPLOAD_INTERVAL_MS (idle cadence)

    const traceCallsFor = () =>
      fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/v1/telemetry/pipeline-trace"));

    function makeActiveService(consent = makeConsentService()) {
      return new TelemetryUploaderService(
        makeLicenseKey(),
        consent as never,
        () => "https://api.example.com",
        "/workspace",
        makeLogger() as never
      );
    }

    // (1) A started run flushes on the short active cadence, never waiting out
    //     the 15-minute idle timer.
    it("flushes trace uploads on the short active cadence without the 15-min timer", async () => {
      vi.useFakeTimers();
      const runId = "01890a5d-ac96-774b-bcce-b30209900101";
      setupFs(
        [],
        {},
        [],
        [{ name: `${runId}.jsonl`, content: makeTraceContent(runId, 3), sizeBytes: 600 }]
      );
      fetchMock.mockResolvedValue(okResponse());

      const service = makeActiveService();
      service.onRunStarted();
      service.onRunProgress();
      service.onRunProgress();

      // Let the coalesced flush fire — far under the 15-minute idle interval.
      await vi.advanceTimersByTimeAsync(MIN_UPLOAD_GAP_MS);

      const traceCalls = traceCallsFor();
      expect(traceCalls).toHaveLength(1);
      const body = JSON.parse((traceCalls[0]![1] as RequestInit).body as string) as unknown[];
      expect(body).toHaveLength(3);

      service.dispose();
      vi.useRealTimers();
    });

    // (2) Backpressure: a burst of progress events within one gap window
    //     coalesces into ONE upload; the next flush only runs after the gap.
    it("coalesces a burst into one cycle and rate-bounds the next flush", async () => {
      vi.useFakeTimers();
      const runId = "01890a5d-ac96-774b-bcce-b30209900102";
      const traceFile = {
        name: `${runId}.jsonl`,
        content: makeTraceContent(runId, 3),
        sizeBytes: 600,
      };
      setupFs([], {}, [], [traceFile]);
      fetchMock.mockResolvedValue(okResponse());

      const service = makeActiveService();

      // Burst: start + many progress events, all within one gap window.
      service.onRunStarted();
      for (let i = 0; i < 40; i++) service.onRunProgress();

      await vi.advanceTimersByTimeAsync(1); // fire the single coalesced flush
      expect(traceCallsFor()).toHaveLength(1);

      // New lines + another progress burst — the next flush must wait the gap.
      traceFile.content = makeTraceContent(runId, 6);
      for (let i = 0; i < ACTIVE_RUN_FLUSH_EVENT_COUNT; i++) service.onRunProgress();

      await vi.advanceTimersByTimeAsync(1); // well inside the gap
      expect(traceCallsFor()).toHaveLength(1); // still rate-bounded

      await vi.advanceTimersByTimeAsync(MIN_UPLOAD_GAP_MS); // now the gap has elapsed
      expect(traceCallsFor()).toHaveLength(2);

      service.dispose();
      vi.useRealTimers();
    });

    // (3) Consent gate: a disabled trace stream uploads zero trace batches even
    //     while a run is active and producing progress.
    it("uploads zero trace batches while active when trace consent is off", async () => {
      vi.useFakeTimers();
      const runId = "01890a5d-ac96-774b-bcce-b30209900103";
      setupFs(
        [],
        {},
        [],
        [{ name: `${runId}.jsonl`, content: makeTraceContent(runId, 5), sizeBytes: 1000 }]
      );
      fetchMock.mockResolvedValue(okResponse());

      const service = makeActiveService(makeConsentService(true, { trace: false }));
      service.onRunStarted();
      for (let i = 0; i < ACTIVE_RUN_FLUSH_EVENT_COUNT; i++) service.onRunProgress();

      await vi.advanceTimersByTimeAsync(MIN_UPLOAD_GAP_MS);

      expect(traceCallsFor()).toHaveLength(0);

      service.dispose();
      vi.useRealTimers();
    });

    // (4) Watermark: two active flushes over a growing trace file upload only
    //     the NEW lines the second time.
    it("uploads only new lines on a second active flush over a growing file", async () => {
      vi.useFakeTimers();
      const runId = "01890a5d-ac96-774b-bcce-b30209900104";
      const traceFile = {
        name: `${runId}.jsonl`,
        content: makeTraceContent(runId, 3),
        sizeBytes: 600,
      };
      setupFs([], {}, [], [traceFile]);
      fetchMock.mockResolvedValue(okResponse());

      const service = makeActiveService();

      service.onRunStarted();
      await vi.advanceTimersByTimeAsync(1);
      let traceCalls = traceCallsFor();
      expect(traceCalls).toHaveLength(1);
      const firstBody = JSON.parse((traceCalls[0]![1] as RequestInit).body as string) as Array<
        Record<string, unknown>
      >;
      expect(firstBody).toHaveLength(3);

      // File grows 3 -> 5; the second flush uploads ONLY the 2 new lines.
      traceFile.content = makeTraceContent(runId, 5);
      for (let i = 0; i < ACTIVE_RUN_FLUSH_EVENT_COUNT; i++) service.onRunProgress();
      await vi.advanceTimersByTimeAsync(MIN_UPLOAD_GAP_MS);

      traceCalls = traceCallsFor();
      expect(traceCalls).toHaveLength(2);
      const secondBody = JSON.parse((traceCalls[1]![1] as RequestInit).body as string) as Array<
        Record<string, unknown>
      >;
      expect(secondBody).toHaveLength(2);
      expect(secondBody[0]).toMatchObject({ seq: 4 });
      expect(secondBody[1]).toMatchObject({ seq: 5 });

      service.dispose();
      vi.useRealTimers();
    });

    // (5) Cadence restore: once the last run completes, the short-interval timer
    //     is cleared — no further uploads until the 15-minute idle timer.
    it("restores the idle cadence after the last run completes", async () => {
      vi.useFakeTimers();
      const runId = "01890a5d-ac96-774b-bcce-b30209900105";
      const traceFile = {
        name: `${runId}.jsonl`,
        content: makeTraceContent(runId, 3),
        sizeBytes: 600,
      };
      setupFs([], {}, [], [traceFile]);
      fetchMock.mockResolvedValue(okResponse());

      const service = makeActiveService();

      service.onRunStarted();
      await vi.advanceTimersByTimeAsync(1);
      expect(traceCallsFor()).toHaveLength(1);

      // Completing the only active run drops activeRunCount to 0 → idle cadence.
      // The final flush has no new lines, so it uploads nothing.
      service.onRunCompleted();
      await vi.advanceTimersByTimeAsync(1);
      const countAfterComplete = traceCallsFor().length;
      expect(countAfterComplete).toBe(1);

      // Grow the file and advance PAST several short intervals — because the
      // short cadence was cleared, nothing uploads.
      traceFile.content = makeTraceContent(runId, 6);
      await vi.advanceTimersByTimeAsync(ACTIVE_RUN_UPLOAD_INTERVAL_MS * 3);
      expect(traceCallsFor()).toHaveLength(countAfterComplete);

      // The restored 15-minute idle timer eventually flushes the new lines.
      await vi.advanceTimersByTimeAsync(IDLE_INTERVAL_MS);
      expect(traceCallsFor().length).toBeGreaterThan(countAfterComplete);

      service.dispose();
      vi.useRealTimers();
    });
  });

  // ── Multi-repo workspace roots (#247) ────────────────────────────────────
  //
  // The Go binary's interactive-run writer lands history/trace JSONL under
  // the run's TARGET repo root (`repoRoot(p.Repo)`, internal/ipc/server.go),
  // not necessarily the primary workspace root. Before #247 the uploader only
  // ever scanned `incrediRoot`, so target-repo runs in a multi-repo workspace
  // silently never uploaded. `getWorkspaceRoots` (7th constructor arg) is how
  // the uploader learns about the other roots to scan — wired in
  // bootstrap/services.ts from `WorkspaceManager.getAllRepositories()`.
  describe("multi-repo workspace roots (#247)", () => {
    const PRIMARY_ROOT = "/workspace";
    const TARGET_ROOT = "/target-repo";

    function makeMultiRootService(
      getWorkspaceRoots: () => string[],
      opts: {
        consent?: ReturnType<typeof makeConsentService>;
        logger?: ReturnType<typeof makeLogger>;
      } = {}
    ) {
      return new TelemetryUploaderService(
        makeLicenseKey(),
        (opts.consent ?? makeConsentService()) as never,
        () => "https://api.example.com",
        PRIMARY_ROOT,
        (opts.logger ?? makeLogger()) as never,
        undefined,
        getWorkspaceRoots
      );
    }

    it("scans pipeline-run history across every workspace repo root, not just incrediRoot", async () => {
      setupMultiRootFs([
        {
          root: PRIMARY_ROOT,
          historyFiles: [
            { name: "2026-05-10.jsonl", content: makeJsonlContent(2), sizeBytes: 200 },
          ],
        },
        {
          root: TARGET_ROOT,
          historyFiles: [
            { name: "2026-05-11.jsonl", content: makeJsonlContent(3), sizeBytes: 300 },
          ],
        },
      ]);
      fetchMock.mockResolvedValue(okResponse());

      const service = makeMultiRootService(() => [TARGET_ROOT]);
      await service.runUploadCycle();

      const pipelineRunCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).endsWith("/v1/telemetry/pipeline-run")
      );
      // One batch per root — the target repo's run history is no longer
      // silently skipped.
      expect(pipelineRunCalls).toHaveLength(2);

      const totalRecords = pipelineRunCalls.reduce((n, call) => {
        const [, init] = call as [string, RequestInit];
        const body = JSON.parse(init.body as string) as unknown[];
        return n + body.length;
      }, 0);
      expect(totalRecords).toBe(5);

      // Each root persists its own watermark file.
      const fsMock = vi.mocked(vscode.workspace.fs);
      const writeCalls = fsMock.writeFile.mock.calls.filter(([uri]) =>
        (uri as { fsPath: string }).fsPath.includes("upload-watermarks.json.tmp")
      );
      expect(
        writeCalls.some(([uri]) => (uri as { fsPath: string }).fsPath.startsWith(PRIMARY_ROOT))
      ).toBe(true);
      expect(
        writeCalls.some(([uri]) => (uri as { fsPath: string }).fsPath.startsWith(TARGET_ROOT))
      ).toBe(true);
    });

    it("scans the trace stream across every workspace repo root", async () => {
      const runIdPrimary = "01890a5d-ac96-774b-bcce-b30209900201";
      const runIdTarget = "01890a5d-ac96-774b-bcce-b30209900202";
      setupMultiRootFs([
        {
          root: PRIMARY_ROOT,
          traceFiles: [
            {
              name: `${runIdPrimary}.jsonl`,
              content: makeTraceContent(runIdPrimary, 2),
              sizeBytes: 400,
            },
          ],
        },
        {
          root: TARGET_ROOT,
          traceFiles: [
            {
              name: `${runIdTarget}.jsonl`,
              content: makeTraceContent(runIdTarget, 3),
              sizeBytes: 600,
            },
          ],
        },
      ]);
      fetchMock.mockResolvedValue(okResponse());

      const service = makeMultiRootService(() => [TARGET_ROOT]);
      await service.runUploadCycle();

      const traceCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).endsWith("/v1/telemetry/pipeline-trace")
      );
      expect(traceCalls).toHaveLength(2);

      const totalEvents = traceCalls.reduce((n, call) => {
        const [, init] = call as [string, RequestInit];
        const body = JSON.parse(init.body as string) as unknown[];
        return n + body.length;
      }, 0);
      expect(totalEvents).toBe(5);
    });

    it("still scans incrediRoot when getWorkspaceRoots omits it", async () => {
      // incrediRoot is always unioned in — a getWorkspaceRoots callback that
      // reports only target repos must not cause the primary root to be
      // dropped from the scan.
      setupMultiRootFs([
        {
          root: PRIMARY_ROOT,
          historyFiles: [
            { name: "2026-05-10.jsonl", content: makeJsonlContent(1), sizeBytes: 100 },
          ],
        },
      ]);
      fetchMock.mockResolvedValue(okResponse());

      const service = makeMultiRootService(() => []); // reports no extra roots
      await service.runUploadCycle();

      const pipelineRunCalls = fetchMock.mock.calls.filter(([url]) =>
        String(url).endsWith("/v1/telemetry/pipeline-run")
      );
      expect(pipelineRunCalls).toHaveLength(1);
    });

    it("does not re-scan other roots once one root's upload aborts (endpoint down)", async () => {
      setupMultiRootFs([
        {
          root: PRIMARY_ROOT,
          historyFiles: [
            { name: "2026-05-10.jsonl", content: makeJsonlContent(2), sizeBytes: 200 },
          ],
        },
        {
          root: TARGET_ROOT,
          historyFiles: [
            { name: "2026-05-11.jsonl", content: makeJsonlContent(2), sizeBytes: 200 },
          ],
        },
      ]);
      fetchMock.mockResolvedValue({ ok: false, status: 404 } as Response);

      const service = makeMultiRootService(() => [TARGET_ROOT]);
      await service.runUploadCycle();

      // Only the first root's file is attempted; the second root is never
      // reached this cycle (same unreachable endpoint would fail identically).
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it("keeps the poison-message retry counter isolated per repo root for identically-named history files", async () => {
      // Both roots have a file with the SAME name, and both always have their
      // first record rejected. Before the #247 fix the retry-cycle counter
      // was keyed only by filename, so two roots sharing a filename (history
      // files are date-stamped, e.g. "2026-05-10.jsonl" — very likely to
      // collide across repos) would increment the SAME shared counter twice
      // per cycle, hitting MAX_REJECTION_RETRY_CYCLES (5) after 3 cycles
      // instead of 5 and giving up on records prematurely in BOTH repos.
      const filename = "2026-05-10.jsonl";
      setupMultiRootFs([
        {
          root: PRIMARY_ROOT,
          historyFiles: [{ name: filename, content: makeJsonlContent(3), sizeBytes: 300 }],
        },
        {
          root: TARGET_ROOT,
          historyFiles: [{ name: filename, content: makeJsonlContent(3), sizeBytes: 300 }],
        },
      ]);
      // Every batch has its first record rejected, in both roots.
      fetchMock.mockResolvedValue(okResponse([{ index: 0, reason: "permanently bad" }]));

      const logger = makeLogger();
      const service = makeMultiRootService(() => [TARGET_ROOT], { logger });

      for (let i = 0; i < 3; i++) {
        await service.runUploadCycle();
      }

      // 3 cycles is below MAX_REJECTION_RETRY_CYCLES (5) for each
      // independently-tracked root — neither should have given up yet.
      expect(logger.error).not.toHaveBeenCalledWith(
        expect.stringContaining("giving up on rejected records"),
        expect.anything()
      );

      // 2 more cycles (5 total) — now each root's OWN counter reaches 5 and
      // both give up independently (not one giving up on behalf of the other).
      for (let i = 0; i < 2; i++) {
        await service.runUploadCycle();
      }

      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("giving up on rejected records"),
        expect.objectContaining({ filename, root: PRIMARY_ROOT })
      );
      expect(logger.error).toHaveBeenCalledWith(
        expect.stringContaining("giving up on rejected records"),
        expect.objectContaining({ filename, root: TARGET_ROOT })
      );
    });
  });
});
