/**
 * log-file-writer.test.ts
 *
 * Unit tests for LogFileWriter utility class.
 *
 * @see Issue #190 - Pipeline logs persistence
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { LogFileWriter } from "../../src/utils/log-file-writer";

// Mock node:fs/promises
vi.mock("node:fs/promises");

describe("LogFileWriter", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    // Default mock implementations
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
    vi.mocked(fs.readdir).mockResolvedValue([]);
    vi.mocked(fs.readFile).mockResolvedValue("" as any);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("generateFilename()", () => {
    it("should generate filename with issue number", () => {
      const date = new Date("2026-02-04T14:30:00Z");
      const filename = LogFileWriter.generateFilename(42, date);
      expect(filename).toBe("2026-02-04_42_session.log");
    });

    it("should generate filename without issue number", () => {
      const date = new Date("2026-02-04T14:30:00Z");
      const filename = LogFileWriter.generateFilename(null, date);
      expect(filename).toBe("2026-02-04_session.log");
    });

    it("should use current date when no date provided", () => {
      const filename = LogFileWriter.generateFilename(99);
      // Should contain today's date
      const today = new Date().toISOString().split("T")[0];
      expect(filename).toBe(`${today}_99_session.log`);
    });

    it("should handle different date boundaries", () => {
      // Test at end of day
      const endOfDay = new Date("2026-12-31T23:59:59Z");
      expect(LogFileWriter.generateFilename(1, endOfDay)).toBe("2026-12-31_1_session.log");

      // Test at start of day
      const startOfDay = new Date("2026-01-01T00:00:00Z");
      expect(LogFileWriter.generateFilename(2, startOfDay)).toBe("2026-01-01_2_session.log");
    });
  });

  describe("appendToLog()", () => {
    it("should write formatted entry to log file", async () => {
      await LogFileWriter.appendToLog(
        workspaceRoot,
        42,
        "INFO",
        "feature-dev",
        "Starting implementation..."
      );

      expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining(".nightgauge/logs"), {
        recursive: true,
      });

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.stringContaining("_42_session.log"),
        expect.stringMatching(
          /\[\d{4}-\d{2}-\d{2}T.+\] \[INFO\] \[feature-dev\] Starting implementation\.\.\.\n/
        ),
        "utf-8"
      );
    });

    it("should write entry without stage tag when stage is null", async () => {
      await LogFileWriter.appendToLog(workspaceRoot, 42, "DEBUG", null, "General message");

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.any(String),
        expect.stringMatching(/\[DEBUG\] General message\n$/),
        "utf-8"
      );
    });

    it("should uppercase the log level", async () => {
      await LogFileWriter.appendToLog(workspaceRoot, 42, "info", null, "Test message");

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.any(String),
        expect.stringContaining("[INFO]"),
        "utf-8"
      );
    });

    it("redacts secrets from the message before writing to disk (#170)", async () => {
      // Token built by concatenation so the fixture is not a contiguous glpat-
      // literal that would trip the credential scanner (real shape at runtime).
      const gitlabPat = "glpat-" + "N3FwABCDEFGHIJKLMNOP";
      await LogFileWriter.appendToLog(
        workspaceRoot,
        42,
        "INFO",
        "feature-validate",
        `stage echoed export JWT_PRIVATE_KEY='-----BEGIN PRIVATE KEY-----\\nMIIEvQ\\n-----END PRIVATE KEY-----' then ${gitlabPat}`
      );

      const written = vi.mocked(fs.appendFile).mock.calls[0]?.[1] as string;
      expect(written).not.toContain("BEGIN PRIVATE KEY");
      expect(written).not.toContain(gitlabPat);
      expect(written).toContain("[REDACTED");
      // Non-secret framing (timestamp/level/stage tag) is preserved.
      expect(written).toMatch(/\[INFO\] \[feature-validate\] /);
    });

    it("should respect retain: false config", async () => {
      await LogFileWriter.appendToLog(
        workspaceRoot,
        42,
        "INFO",
        "feature-dev",
        "Should not write",
        { retain: false }
      );

      expect(fs.mkdir).not.toHaveBeenCalled();
      expect(fs.appendFile).not.toHaveBeenCalled();
    });

    it("should use custom log directory from config", async () => {
      await LogFileWriter.appendToLog(workspaceRoot, 42, "INFO", null, "Test message", {
        retain: true,
        dir: "custom/logs",
      });

      expect(fs.mkdir).toHaveBeenCalledWith("/test/workspace/custom/logs", {
        recursive: true,
      });

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.stringContaining("custom/logs"),
        expect.any(String),
        "utf-8"
      );
    });

    it("should handle mkdir failure gracefully", async () => {
      const error = new Error("EACCES: permission denied");
      vi.mocked(fs.mkdir).mockRejectedValue(error);

      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      // Should not throw
      await expect(
        LogFileWriter.appendToLog(workspaceRoot, 42, "INFO", null, "Test message")
      ).resolves.toBeUndefined();

      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("[Nightgauge] Failed to write to log file")
      );

      warnSpy.mockRestore();
    });

    it("should handle appendFile failure gracefully", async () => {
      const error = new Error("ENOSPC: no space left on device");
      vi.mocked(fs.appendFile).mockRejectedValue(error);

      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      // Should not throw
      await expect(
        LogFileWriter.appendToLog(workspaceRoot, 42, "INFO", null, "Test message")
      ).resolves.toBeUndefined();

      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining("[Nightgauge] Failed to write to log file")
      );

      warnSpy.mockRestore();
    });

    it("should handle null issue number", async () => {
      await LogFileWriter.appendToLog(workspaceRoot, null, "INFO", null, "No issue context");

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.not.stringContaining("_null_"),
        expect.any(String),
        "utf-8"
      );

      // Should match pattern like 2026-02-04_session.log
      const callPath = vi.mocked(fs.appendFile).mock.calls[0][0] as string;
      expect(callPath).toMatch(/_session\.log$/);
    });
  });

  describe("getLogPath()", () => {
    it("should return full path with default config", () => {
      const today = new Date().toISOString().split("T")[0];
      const path = LogFileWriter.getLogPath(workspaceRoot, 42);
      expect(path).toBe(`/test/workspace/.nightgauge/logs/${today}_42_session.log`);
    });

    it("should use custom dir from config", () => {
      const today = new Date().toISOString().split("T")[0];
      const path = LogFileWriter.getLogPath(workspaceRoot, 42, {
        dir: "custom/path",
      });
      expect(path).toBe(`/test/workspace/custom/path/${today}_42_session.log`);
    });
  });

  describe("exists()", () => {
    it("should return true when log file exists", async () => {
      vi.mocked(fs.access).mockResolvedValue();

      const result = await LogFileWriter.exists(workspaceRoot, 42);
      expect(result).toBe(true);
    });

    it("should return false when log file does not exist", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await LogFileWriter.exists(workspaceRoot, 42);
      expect(result).toBe(false);
    });
  });

  describe("formatEntry()", () => {
    it("should format entry with stage", () => {
      const timestamp = new Date("2026-02-04T14:30:00.000Z");
      const entry = LogFileWriter.formatEntry("INFO", "feature-dev", "Test message", timestamp);
      expect(entry).toBe("[2026-02-04T14:30:00.000Z] [INFO] [feature-dev] Test message");
    });

    it("should format entry without stage", () => {
      const timestamp = new Date("2026-02-04T14:30:00.000Z");
      const entry = LogFileWriter.formatEntry("ERROR", null, "Error occurred", timestamp);
      expect(entry).toBe("[2026-02-04T14:30:00.000Z] [ERROR] Error occurred");
    });

    it("should uppercase level", () => {
      const timestamp = new Date("2026-02-04T14:30:00.000Z");
      const entry = LogFileWriter.formatEntry("debug", null, "Debug info", timestamp);
      expect(entry).toContain("[DEBUG]");
    });
  });

  describe("truncateForLog()", () => {
    it("should return original message when under limit", () => {
      const message = "Short message";
      expect(LogFileWriter.truncateForLog(message)).toBe(message);
    });

    it("should return original message when exactly at limit", () => {
      const message = "a".repeat(200);
      expect(LogFileWriter.truncateForLog(message)).toBe(message);
    });

    it("should truncate with char count for messages just over limit", () => {
      const message = "a".repeat(300);
      const result = LogFileWriter.truncateForLog(message);
      expect(result).toContain("a".repeat(200));
      expect(result).toContain("... [truncated, 300 chars total]");
    });

    it("should truncate with KB label for large messages", () => {
      const message = "a".repeat(5120); // 5KB
      const result = LogFileWriter.truncateForLog(message);
      expect(result).toContain("a".repeat(200));
      expect(result).toContain("... [truncated, 5.0KB total]");
    });

    it("should handle empty string", () => {
      expect(LogFileWriter.truncateForLog("")).toBe("");
    });

    it("should respect custom maxChars parameter", () => {
      const message = "a".repeat(100);
      const result = LogFileWriter.truncateForLog(message, 50);
      expect(result).toContain("a".repeat(50));
      expect(result).toContain("... [truncated, 100 chars total]");
    });

    it("should show KB for messages over 1024 chars", () => {
      const message = "x".repeat(2048);
      const result = LogFileWriter.truncateForLog(message);
      expect(result).toContain("2.0KB total");
    });

    it("should show chars for messages at exactly 1024", () => {
      const message = "x".repeat(1024);
      const result = LogFileWriter.truncateForLog(message);
      expect(result).toContain("1024 chars total");
    });
  });

  describe("readEntriesForIssue()", () => {
    it("should return empty array when retain: false", async () => {
      const result = await LogFileWriter.readEntriesForIssue(workspaceRoot, 42, { retain: false });
      expect(result).toEqual([]);
      expect(fs.readdir).not.toHaveBeenCalled();
    });

    it("should return empty array when log directory does not exist", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(
        Object.assign(new Error("ENOENT: no such file or directory"), {
          code: "ENOENT",
        })
      );

      const result = await LogFileWriter.readEntriesForIssue(workspaceRoot, 42);
      expect(result).toEqual([]);
    });

    it("should return empty array when no files match issue number", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([
        "2026-03-06_99_session.log",
        "2026-03-06_session.log",
      ] as any);

      const result = await LogFileWriter.readEntriesForIssue(workspaceRoot, 42);
      expect(result).toEqual([]);
    });

    it("should parse entries from matching log file", async () => {
      vi.mocked(fs.readdir).mockResolvedValue(["2026-03-06_42_session.log"] as any);
      vi.mocked(fs.readFile).mockResolvedValue(
        "[2026-03-06T10:00:00.000Z] [INFO] [feature-dev] Starting implementation\n[2026-03-06T10:01:00.000Z] [ERROR] Test failed\n" as any
      );

      const result = await LogFileWriter.readEntriesForIssue(workspaceRoot, 42);

      expect(result).toHaveLength(2);
      expect(result[0].level).toBe("INFO");
      expect(result[0].stage).toBe("feature-dev");
      expect(result[0].text).toBe("Starting implementation");
      expect(result[1].level).toBe("ERROR");
      expect(result[1].stage).toBeNull();
      expect(result[1].text).toBe("Test failed");
    });

    it("should merge multiple files in chronological order", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([
        "2026-03-06_42_session.log",
        "2026-03-07_42_session.log",
      ] as any);
      vi.mocked(fs.readFile)
        .mockResolvedValueOnce("[2026-03-06T10:00:00.000Z] [INFO] Day 1 entry\n" as any)
        .mockResolvedValueOnce("[2026-03-07T10:00:00.000Z] [INFO] Day 2 entry\n" as any);

      const result = await LogFileWriter.readEntriesForIssue(workspaceRoot, 42);

      expect(result).toHaveLength(2);
      expect(result[0].text).toBe("Day 1 entry");
      expect(result[1].text).toBe("Day 2 entry");
    });

    it("should skip unreadable files and return entries from readable ones", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([
        "2026-03-06_42_session.log",
        "2026-03-07_42_session.log",
      ] as any);
      vi.mocked(fs.readFile)
        .mockRejectedValueOnce(new Error("EACCES: permission denied"))
        .mockResolvedValueOnce("[2026-03-07T10:00:00.000Z] [INFO] Readable entry\n" as any);

      const result = await LogFileWriter.readEntriesForIssue(workspaceRoot, 42);

      expect(result).toHaveLength(1);
      expect(result[0].text).toBe("Readable entry");
    });

    it("should use custom log dir from config", async () => {
      vi.mocked(fs.readdir).mockResolvedValue([] as any);

      await LogFileWriter.readEntriesForIssue(workspaceRoot, 42, {
        retain: true,
        dir: "custom/logs",
      });

      expect(fs.readdir).toHaveBeenCalledWith("/test/workspace/custom/logs");
    });
  });

  describe("parseLogLine()", () => {
    it("should parse valid line with stage", () => {
      const result = LogFileWriter.parseLogLine(
        "[2026-03-06T10:00:00.000Z] [INFO] [feature-dev] Starting implementation"
      );
      expect(result).not.toBeNull();
      expect(result!.level).toBe("INFO");
      expect(result!.stage).toBe("feature-dev");
      expect(result!.text).toBe("Starting implementation");
      expect(result!.timestamp).toEqual(new Date("2026-03-06T10:00:00.000Z"));
    });

    it("should parse valid line without stage (stage is null)", () => {
      const result = LogFileWriter.parseLogLine(
        "[2026-03-06T10:00:00.000Z] [ERROR] Something went wrong"
      );
      expect(result).not.toBeNull();
      expect(result!.level).toBe("ERROR");
      expect(result!.stage).toBeNull();
      expect(result!.text).toBe("Something went wrong");
    });

    it("should return null for empty line", () => {
      expect(LogFileWriter.parseLogLine("")).toBeNull();
      expect(LogFileWriter.parseLogLine("   ")).toBeNull();
    });

    it("should return null for malformed line (no brackets)", () => {
      expect(LogFileWriter.parseLogLine("just some text")).toBeNull();
    });

    it("should return null for invalid timestamp", () => {
      const result = LogFileWriter.parseLogLine("[not-a-date] [INFO] Some message");
      expect(result).toBeNull();
    });

    it("should handle message text that contains brackets", () => {
      const result = LogFileWriter.parseLogLine(
        "[2026-03-06T10:00:00.000Z] [INFO] Message with [brackets] inside"
      );
      expect(result).not.toBeNull();
      expect(result!.text).toBe("Message with [brackets] inside");
    });
  });

  describe("integration scenarios", () => {
    it("should handle rapid successive writes", async () => {
      const promises = [];
      for (let i = 0; i < 10; i++) {
        promises.push(
          LogFileWriter.appendToLog(workspaceRoot, 42, "INFO", "feature-dev", `Message ${i}`)
        );
      }

      await Promise.all(promises);

      // Should have appended 10 times
      expect(fs.appendFile).toHaveBeenCalledTimes(10);
    });

    it("should handle different log levels", async () => {
      const levels = ["INFO", "DEBUG", "WARNING", "ERROR", "TOOL", "USER"];

      for (const level of levels) {
        await LogFileWriter.appendToLog(workspaceRoot, 42, level, "test-stage", `${level} message`);
      }

      expect(fs.appendFile).toHaveBeenCalledTimes(levels.length);
    });

    it("should handle special characters in message", async () => {
      const specialMessage = 'Line with "quotes", tabs\t, and newlines\n';

      await LogFileWriter.appendToLog(workspaceRoot, 42, "INFO", null, specialMessage);

      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.any(String),
        expect.stringContaining(specialMessage),
        "utf-8"
      );
    });
  });

  describe("listLogs()", () => {
    it("returns descriptors sorted newest first", async () => {
      vi.mocked(fs.readdir).mockResolvedValueOnce([
        "2026-01-10_42_session.log",
        "2026-03-01_99_session.log",
        "2026-02-14_17_session.log",
      ] as any);

      const descriptors = await LogFileWriter.listLogs(workspaceRoot);

      expect(descriptors.map((d) => d.issueNumber)).toEqual([99, 17, 42]);
      expect(descriptors[0].filePath).toContain("2026-03-01_99_session.log");
      expect(descriptors.every((d) => d.startedAt instanceof Date)).toBe(true);
    });

    it("skips files that do not match the session log pattern", async () => {
      vi.mocked(fs.readdir).mockResolvedValueOnce([
        "2026-03-01_99_session.log",
        "not-a-log.txt",
        "2026-03-02_session.log", // no issue number — skip
        "bad-date_7_session.log",
      ] as any);

      const descriptors = await LogFileWriter.listLogs(workspaceRoot);

      expect(descriptors.map((d) => d.issueNumber)).toEqual([99]);
    });

    it("returns empty when retain is false", async () => {
      const descriptors = await LogFileWriter.listLogs(workspaceRoot, { retain: false });

      expect(descriptors).toEqual([]);
      expect(fs.readdir).not.toHaveBeenCalled();
    });

    it("returns empty when log directory does not exist", async () => {
      vi.mocked(fs.readdir).mockRejectedValueOnce(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const descriptors = await LogFileWriter.listLogs(workspaceRoot);

      expect(descriptors).toEqual([]);
    });

    it("drops files older than max_age_days", async () => {
      const today = new Date();
      const toIso = (d: Date) => d.toISOString().split("T")[0];
      const recent = toIso(today);
      const old = toIso(new Date(today.getTime() - 10 * 24 * 60 * 60 * 1000));

      vi.mocked(fs.readdir).mockResolvedValueOnce([
        `${recent}_1_session.log`,
        `${old}_2_session.log`,
      ] as any);

      const descriptors = await LogFileWriter.listLogs(workspaceRoot, { max_age_days: 3 });

      expect(descriptors.map((d) => d.issueNumber)).toEqual([1]);
    });

    it("truncates to max_count after age filter", async () => {
      const today = new Date();
      const toIso = (d: Date, offsetDays: number) =>
        new Date(today.getTime() - offsetDays * 24 * 60 * 60 * 1000).toISOString().split("T")[0];

      vi.mocked(fs.readdir).mockResolvedValueOnce([
        `${toIso(today, 0)}_1_session.log`,
        `${toIso(today, 1)}_2_session.log`,
        `${toIso(today, 2)}_3_session.log`,
      ] as any);

      const descriptors = await LogFileWriter.listLogs(workspaceRoot, { max_count: 2 });

      expect(descriptors.map((d) => d.issueNumber)).toEqual([1, 2]);
    });

    it("uses custom dir from config", async () => {
      vi.mocked(fs.readdir).mockResolvedValueOnce([] as any);

      await LogFileWriter.listLogs(workspaceRoot, { dir: "custom/log/dir" });

      expect(fs.readdir).toHaveBeenCalledWith(expect.stringContaining("custom/log/dir"));
    });
  });

  describe("cleanupLogs()", () => {
    it("deletes files beyond max_count keeping newest", async () => {
      const today = new Date();
      const toIso = (offsetDays: number) =>
        new Date(today.getTime() - offsetDays * 24 * 60 * 60 * 1000).toISOString().split("T")[0];

      vi.mocked(fs.readdir).mockResolvedValueOnce([
        `${toIso(0)}_1_session.log`,
        `${toIso(1)}_2_session.log`,
        `${toIso(2)}_3_session.log`,
        `${toIso(3)}_4_session.log`,
      ] as any);
      vi.mocked(fs.unlink).mockResolvedValue();

      const result = await LogFileWriter.cleanupLogs(workspaceRoot, { max_count: 2 });

      expect(result.kept).toBe(2);
      expect(result.deleted).toBe(2);
      expect(result.failed).toBe(0);
      expect(fs.unlink).toHaveBeenCalledTimes(2);
    });

    it("deletes files older than max_age_days", async () => {
      const today = new Date();
      const toIso = (d: Date) => d.toISOString().split("T")[0];
      const recent = toIso(today);
      const old = toIso(new Date(today.getTime() - 10 * 24 * 60 * 60 * 1000));

      vi.mocked(fs.readdir).mockResolvedValueOnce([
        `${recent}_1_session.log`,
        `${old}_2_session.log`,
      ] as any);
      vi.mocked(fs.unlink).mockResolvedValue();

      const result = await LogFileWriter.cleanupLogs(workspaceRoot, { max_age_days: 3 });

      expect(result.kept).toBe(1);
      expect(result.deleted).toBe(1);
    });

    it("returns zero counts when log directory does not exist", async () => {
      vi.mocked(fs.readdir).mockRejectedValueOnce(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const result = await LogFileWriter.cleanupLogs(workspaceRoot);

      expect(result).toEqual({ kept: 0, deleted: 0, failed: 0 });
    });

    it("tracks unlink failures without aborting", async () => {
      const today = new Date();
      const toIso = (offsetDays: number) =>
        new Date(today.getTime() - offsetDays * 24 * 60 * 60 * 1000).toISOString().split("T")[0];

      vi.mocked(fs.readdir).mockResolvedValueOnce([
        `${toIso(0)}_1_session.log`,
        `${toIso(1)}_2_session.log`,
        `${toIso(2)}_3_session.log`,
      ] as any);
      vi.mocked(fs.unlink).mockRejectedValueOnce(new Error("EACCES")).mockResolvedValueOnce();

      const result = await LogFileWriter.cleanupLogs(workspaceRoot, { max_count: 1 });

      expect(result.kept).toBe(1);
      expect(result.deleted).toBe(1);
      expect(result.failed).toBe(1);
    });
  });

  describe("readLog()", () => {
    it("parses entries in file order", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(
        "[2026-03-01T10:00:00.000Z] [INFO] [feature-dev] first line\n" +
          "[2026-03-01T10:00:01.000Z] [ERROR] second line\n"
      );

      const entries = await LogFileWriter.readLog("/fake/path.log");

      expect(entries).toHaveLength(2);
      expect(entries[0].level).toBe("INFO");
      expect(entries[0].stage).toBe("feature-dev");
      expect(entries[0].text).toBe("first line");
      expect(entries[1].level).toBe("ERROR");
      expect(entries[1].stage).toBeNull();
      expect(entries[1].text).toBe("second line");
    });

    it("returns empty array when file cannot be read", async () => {
      vi.mocked(fs.readFile).mockRejectedValueOnce(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const entries = await LogFileWriter.readLog("/missing/path.log");

      expect(entries).toEqual([]);
    });

    it("skips malformed lines silently", async () => {
      vi.mocked(fs.readFile).mockResolvedValueOnce(
        "[2026-03-01T10:00:00.000Z] [INFO] good line\n" +
          "not a real log line\n" +
          "\n" +
          "[2026-03-01T10:00:02.000Z] [DEBUG] another good line\n"
      );

      const entries = await LogFileWriter.readLog("/fake/path.log");

      expect(entries).toHaveLength(2);
      expect(entries[0].text).toBe("good line");
      expect(entries[1].text).toBe("another good line");
    });
  });
});
