import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Mock } from "vitest";

// Mock child_process — we use promisify(execFile), so mock execFile
vi.mock("node:child_process", () => ({
  execFile: vi.fn(),
}));

// Mock fs/promises
vi.mock("node:fs/promises", () => ({
  mkdir: vi.fn().mockResolvedValue(undefined),
  appendFile: vi.fn().mockResolvedValue(undefined),
  readFile: vi.fn(),
}));

import * as childProcess from "node:child_process";
import * as fs from "node:fs/promises";
import { EscapedDefectDetector } from "../../analysis/EscapedDefectDetector.js";

// ── Helpers ────────────────────────────────────────────────────────

/**
 * Configure execFile mock to simulate a successful `gh api` response.
 * The mock uses the promisify custom symbol pattern so promisify() works.
 */
function mockGhSuccess(jobs: Array<{ name: string; text: string }>) {
  const execFileMock = childProcess.execFile as unknown as Mock;
  execFileMock.mockImplementation(
    (
      _cmd: string,
      _args: unknown[],
      callback: (err: Error | null, result: { stdout: string; stderr: string }) => void
    ) => {
      callback(null, { stdout: JSON.stringify(jobs) + "\n", stderr: "" });
    }
  );
}

function mockGhFailure(error: Error) {
  const execFileMock = childProcess.execFile as unknown as Mock;
  execFileMock.mockImplementation(
    (_cmd: string, _args: unknown[], callback: (err: Error | null, result?: unknown) => void) => {
      callback(error);
    }
  );
}

// ── Tests ──────────────────────────────────────────────────────────

describe("EscapedDefectDetector", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("parseTestFilesFromOutput()", () => {
    it("extracts test file paths from output text", () => {
      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const output = `
        FAIL packages/nightgauge-sdk/src/__tests__/analysis/foo.test.ts
        Expected 1 to equal 2
        at packages/nightgauge-sdk/src/__tests__/bar.test.ts:42
      `;

      const files = detector.parseTestFilesFromOutput(output);
      expect(files).toContain("packages/nightgauge-sdk/src/__tests__/analysis/foo.test.ts");
      expect(files).toContain("packages/nightgauge-sdk/src/__tests__/bar.test.ts");
    });

    it("returns empty array for unrecognized output", () => {
      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const output = "Build failed. No test files in this output.";
      const files = detector.parseTestFilesFromOutput(output);
      expect(files).toEqual([]);
    });

    it("deduplicates test file paths appearing multiple times", () => {
      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const output = `
        packages/sdk/src/__tests__/foo.test.ts
        packages/sdk/src/__tests__/foo.test.ts
        packages/sdk/src/__tests__/bar.test.ts
      `;
      const files = detector.parseTestFilesFromOutput(output);
      const fooCount = files.filter((f) => f.endsWith("foo.test.ts")).length;
      expect(fooCount).toBe(1);
    });
  });

  describe("detect()", () => {
    it("returns 0 gaps when all failing CI tests are in the selected set", async () => {
      const selectedTestFiles = [
        "packages/sdk/src/__tests__/foo.test.ts",
        "packages/sdk/src/__tests__/bar.test.ts",
      ];

      mockGhSuccess([
        {
          name: "test",
          text: "FAIL packages/sdk/src/__tests__/foo.test.ts",
        },
      ]);

      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const gaps = await detector.detect({
        issueNumber: 42,
        prNumber: 99,
        prSha: "abc123",
        selectedTestFiles,
        owner: "nightgauge",
        repo: "nightgauge",
      });

      expect(gaps).toHaveLength(0);
    });

    it("returns 1 gap when one CI failure is not in the selected set", async () => {
      const selectedTestFiles = ["packages/sdk/src/__tests__/foo.test.ts"];

      mockGhSuccess([
        {
          name: "unit-tests",
          text: "FAIL packages/sdk/src/__tests__/bar.test.ts", // NOT in selected set
        },
      ]);

      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const gaps = await detector.detect({
        issueNumber: 42,
        prNumber: 99,
        prSha: "abc123",
        selectedTestFiles,
        owner: "nightgauge",
        repo: "nightgauge",
      });

      expect(gaps).toHaveLength(1);
      expect(gaps[0].failing_test_file).toBe("packages/sdk/src/__tests__/bar.test.ts");
      expect(gaps[0].was_in_selected_set).toBe(false);
      expect(gaps[0].issue_number).toBe(42);
      expect(gaps[0].pr_number).toBe(99);
      expect(gaps[0].failing_ci_job).toBe("unit-tests");
    });

    it("returns empty gaps when gh CLI is unavailable (no throw)", async () => {
      mockGhFailure(new Error("gh: command not found"));

      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const gaps = await detector.detect({
        issueNumber: 1,
        prNumber: 2,
        prSha: "sha",
        selectedTestFiles: [],
        owner: "org",
        repo: "repo",
      });

      expect(gaps).toEqual([]);
    });

    it("returns empty gaps when no CI jobs are failing", async () => {
      mockGhSuccess([]);

      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const gaps = await detector.detect({
        issueNumber: 1,
        prNumber: 2,
        prSha: "sha",
        selectedTestFiles: [],
        owner: "org",
        repo: "repo",
      });

      expect(gaps).toEqual([]);
    });
  });

  describe("recordGaps()", () => {
    it("appends JSONL records for each gap", async () => {
      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const gaps = [
        {
          schema_version: "1.0" as const,
          record_type: "graph_gap" as const,
          issue_number: 42,
          pr_number: 99,
          detected_at: "2026-03-01T00:00:00.000Z",
          failing_ci_job: "unit-tests",
          failing_test_file: "packages/sdk/src/__tests__/foo.test.ts",
          was_in_selected_set: false as const,
          gap_description: "src/foo.ts → foo.test.ts edge missing",
        },
      ];

      await detector.recordGaps(gaps);

      const appendMock = fs.appendFile as Mock;
      expect(appendMock).toHaveBeenCalledOnce();
      const [, content] = appendMock.mock.calls[0] as [string, string, string];
      const parsed = JSON.parse(content.trim());
      expect(parsed.record_type).toBe("graph_gap");
      expect(parsed.issue_number).toBe(42);
    });

    it("is a no-op when gaps array is empty", async () => {
      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      await detector.recordGaps([]);

      const appendMock = fs.appendFile as Mock;
      expect(appendMock).not.toHaveBeenCalled();
    });
  });

  describe("readAll()", () => {
    it("returns empty array when file does not exist", async () => {
      (fs.readFile as Mock).mockRejectedValue(new Error("ENOENT"));
      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const records = await detector.readAll();
      expect(records).toEqual([]);
    });

    it("parses valid graph gap records from JSONL", async () => {
      const record = {
        schema_version: "1.0",
        record_type: "graph_gap",
        issue_number: 5,
        pr_number: 10,
        detected_at: "2026-03-01T00:00:00.000Z",
        failing_ci_job: "test-job",
        failing_test_file: "packages/sdk/src/__tests__/bar.test.ts",
        was_in_selected_set: false,
        gap_description: "edge missing",
      };
      (fs.readFile as Mock).mockResolvedValue(JSON.stringify(record) + "\n");

      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const records = await detector.readAll();

      expect(records).toHaveLength(1);
      expect(records[0].issue_number).toBe(5);
      expect(records[0].was_in_selected_set).toBe(false);
    });

    it("skips malformed lines and continues", async () => {
      const validRecord = {
        schema_version: "1.0",
        record_type: "graph_gap",
        issue_number: 7,
        pr_number: 12,
        detected_at: "2026-03-02T00:00:00.000Z",
        failing_ci_job: "build",
        failing_test_file: "packages/sdk/src/__tests__/baz.test.ts",
        was_in_selected_set: false,
        gap_description: "edge missing",
      };
      const content = ["NOT VALID{{{}", JSON.stringify(validRecord), ""].join("\n");

      (fs.readFile as Mock).mockResolvedValue(content);

      const detector = new EscapedDefectDetector("/tmp/gaps.jsonl");
      const records = await detector.readAll();

      expect(records).toHaveLength(1);
      expect(records[0].issue_number).toBe(7);
    });
  });
});
