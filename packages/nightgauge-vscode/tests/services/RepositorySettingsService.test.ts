/**
 * RepositorySettingsService.test.ts
 *
 * Unit tests for RepositorySettingsService — auto-merge detection, caching,
 * disable flow, cache invalidation, and graceful error handling.
 *
 * @see Issue #2720 — Detect and disable repo auto-merge
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ── VSCode mock ───────────────────────────────────────────────────────────────

vi.mock("vscode", () => {
  class EventEmitter<T> {
    private handlers: Array<(e: T) => void> = [];
    event = (handler: (e: T) => void) => {
      this.handlers.push(handler);
      return { dispose: () => {} };
    };
    fire(e: T) {
      this.handlers.forEach((h) => h(e));
    }
    dispose() {
      this.handlers = [];
    }
  }

  return {
    EventEmitter,
    workspace: {
      getConfiguration: vi.fn(() => ({ get: vi.fn(() => "") })),
    },
    extensions: {
      getExtension: vi.fn(() => undefined),
    },
  };
});

// ── child_process mock with promisify.custom support ─────────────────────────
// See MEMORY.md: Without kCustom, promisify resolves with stdout string not
// { stdout, stderr }, making destructuring return undefined.
// Using execFile (not exec) to match the no-shell, arg-array pattern used by
// RepositorySettingsService for security compliance.

vi.mock("child_process", () => {
  const execFileMock = vi.fn();
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  (execFileMock as any)[kCustom] = (file: string, args: string[], opts: unknown) =>
    new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
      execFileMock(file, args, opts, (err: Error | null, stdout: string, stderr: string) => {
        if (err) reject(Object.assign(err, { stdout, stderr }));
        else resolve({ stdout, stderr });
      });
    });
  return { execFile: execFileMock };
});

// ── BinaryResolver mock ───────────────────────────────────────────────────────

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: {
    fromVSCode: vi.fn(() => ({
      resolve: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
    })),
  },
}));

// ── fs mock ───────────────────────────────────────────────────────────────────

vi.mock("fs", () => ({
  existsSync: vi.fn(() => true),
}));

// ── imports ───────────────────────────────────────────────────────────────────

import { execFile } from "child_process";
import { RepositorySettingsService } from "../../src/services/RepositorySettingsService";

const execMock = execFile as unknown as ReturnType<typeof vi.fn>;

// ── helper: make a minimal Logger stub ───────────────────────────────────────

function makeLogger() {
  return {
    debug: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  } as any;
}

// ── tests ─────────────────────────────────────────────────────────────────────

describe("RepositorySettingsService", () => {
  let service: RepositorySettingsService;
  let logger: ReturnType<typeof makeLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = makeLogger();
    service = new RepositorySettingsService(logger, "/workspace");
  });

  // ── detectAutoMerge ────────────────────────────────────────────────────────

  describe("detectAutoMerge", () => {
    it("returns true when allow_auto_merge is true", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: true }), "");
        }
      );

      const result = await service.detectAutoMerge("nightgauge", "myrepo");
      expect(result).toBe(true);
    });

    it("returns false when allow_auto_merge is false", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );

      const result = await service.detectAutoMerge("nightgauge", "myrepo");
      expect(result).toBe(false);
    });

    it("caches the result — only calls exec once for repeated queries", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );

      await service.detectAutoMerge("nightgauge", "myrepo");
      await service.detectAutoMerge("nightgauge", "myrepo");
      await service.detectAutoMerge("nightgauge", "myrepo");

      expect(execMock).toHaveBeenCalledTimes(1);
    });

    it("uses separate cache entries for different repos", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );

      await service.detectAutoMerge("nightgauge", "repo-a");
      await service.detectAutoMerge("nightgauge", "repo-b");

      expect(execMock).toHaveBeenCalledTimes(2);
    });

    it("returns false on exec error (fail-safe)", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(new Error("binary not found"), "", "");
        }
      );

      const result = await service.detectAutoMerge("nightgauge", "myrepo");
      expect(result).toBe(false);
    });

    it("returns false on invalid JSON response", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, "not-json", "");
        }
      );

      const result = await service.detectAutoMerge("nightgauge", "myrepo");
      expect(result).toBe(false);
    });

    it("fires onAutoMergeDetected event when auto-merge is true", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: true }), "");
        }
      );

      const fired: Array<{ owner: string; repo: string }> = [];
      service.onAutoMergeDetected((e) => fired.push(e));

      await service.detectAutoMerge("nightgauge", "myrepo");

      expect(fired).toHaveLength(1);
      expect(fired[0]).toEqual({ owner: "nightgauge", repo: "myrepo" });
    });

    it("does NOT fire onAutoMergeDetected when auto-merge is false", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );

      const fired: unknown[] = [];
      service.onAutoMergeDetected((e) => fired.push(e));

      await service.detectAutoMerge("nightgauge", "myrepo");

      expect(fired).toHaveLength(0);
    });
  });

  // ── disableAutoMerge ───────────────────────────────────────────────────────

  describe("disableAutoMerge", () => {
    it("calls the Go binary with --force flag", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, "", "");
        }
      );

      await service.disableAutoMerge("nightgauge", "myrepo");

      expect(execMock).toHaveBeenCalledTimes(1);
      const args = execMock.mock.calls[0][1] as string[];
      expect(args).toContain("repo");
      expect(args).toContain("disable-auto-merge");
      expect(args).toContain("--force");
      expect(args).toContain("nightgauge");
      expect(args).toContain("myrepo");
    });

    it("throws on exec error", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(new Error("API error"), "", "error output");
        }
      );

      await expect(service.disableAutoMerge("nightgauge", "myrepo")).rejects.toThrow();
    });

    it("invalidates cache after successful disable", async () => {
      // First call: cache the result
      execMock.mockImplementationOnce(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: true }), "");
        }
      );
      await service.detectAutoMerge("nightgauge", "myrepo");
      expect(execMock).toHaveBeenCalledTimes(1);

      // Disable: clears cache
      execMock.mockImplementationOnce(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, "", "");
        }
      );
      await service.disableAutoMerge("nightgauge", "myrepo");

      // Next detect: should call exec again (cache was invalidated)
      execMock.mockImplementationOnce(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );
      const result = await service.detectAutoMerge("nightgauge", "myrepo");
      expect(result).toBe(false);
      expect(execMock).toHaveBeenCalledTimes(3);
    });
  });

  // ── invalidateCache / clearCache ───────────────────────────────────────────

  describe("invalidateCache", () => {
    it("forces re-detection after invalidation", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );

      await service.detectAutoMerge("nightgauge", "myrepo");
      service.invalidateCache("nightgauge", "myrepo");
      await service.detectAutoMerge("nightgauge", "myrepo");

      expect(execMock).toHaveBeenCalledTimes(2);
    });

    it("clearCache forces re-detection for all repos", async () => {
      execMock.mockImplementation(
        (_file: string, _args: string[], _opts: unknown, cb: Function) => {
          cb(null, JSON.stringify({ allow_auto_merge: false }), "");
        }
      );

      await service.detectAutoMerge("nightgauge", "repo-a");
      await service.detectAutoMerge("nightgauge", "repo-b");
      service.clearCache();
      await service.detectAutoMerge("nightgauge", "repo-a");
      await service.detectAutoMerge("nightgauge", "repo-b");

      expect(execMock).toHaveBeenCalledTimes(4);
    });
  });
});
