/**
 * OutputWindowState.fullFidelity.test.ts
 *
 * #192 — the disk session log must carry FULL entry content. The 200-char
 * UI truncation leaked into the only persistent record, and collapsed
 * code-block bodies lived solely in `details` (never written to disk) — the
 * forbidden `gh pr merge --admin` attempt left no trace. Disk writes now
 * include the details body under a generous per-entry cap.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../src/utils/log-file-writer")>();
  return {
    ...actual,
    LogFileWriter: {
      appendToLog: vi.fn().mockResolvedValue(undefined),
      // Real truncation logic so cap behavior is exercised.
      truncateForLog: actual.LogFileWriter.truncateForLog.bind(actual.LogFileWriter),
    },
  };
});

import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";
import {
  LogFileWriter,
  DEFAULT_DISK_LOG_MAX_ENTRY_CHARS,
} from "../../../src/utils/log-file-writer";

const appendSpy = vi.mocked(LogFileWriter.appendToLog);

function lastWrittenMessage(): string {
  expect(appendSpy).toHaveBeenCalled();
  return appendSpy.mock.calls[appendSpy.mock.calls.length - 1][4] as string;
}

describe("OutputWindowState full-fidelity disk logging (#192)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    vi.clearAllMocks();
    state = new OutputWindowState();
    state.setLogConfig("/workspace/repo");
  });

  it("writes collapsed entries' details bodies to disk", () => {
    const commandBody = Array.from(
      { length: 12 },
      (_, i) => `line ${i}: gh pr merge 276 --squash`
    ).join("\n");

    state.addEntry("Code block (12 lines)", "info", undefined, {
      collapsible: true,
      details: commandBody,
    });

    const written = lastWrittenMessage();
    expect(written).toContain("Code block (12 lines)");
    expect(written).toContain("line 11: gh pr merge 276 --squash");
  });

  it("no longer truncates at 200 chars — entries persist in full", () => {
    const longLine = "x".repeat(5000);
    state.addEntry(longLine, "info");
    expect(lastWrittenMessage()).toBe(longLine);
  });

  it("caps pathological entries at the 64KB default", () => {
    const huge = "y".repeat(DEFAULT_DISK_LOG_MAX_ENTRY_CHARS + 1000);
    state.addEntry(huge, "info");
    const written = lastWrittenMessage();
    expect(written.length).toBeLessThan(huge.length);
    expect(written).toContain("[truncated");
  });

  it("honors pipeline.logs.max_entry_chars overrides", () => {
    state.setLogConfig("/workspace/repo", { max_entry_chars: 2048 });
    const body = "z".repeat(4000);
    state.addEntry(body, "info");
    const written = lastWrittenMessage();
    expect(written.length).toBeLessThan(3000);
    expect(written).toContain("[truncated");
  });
});
