import { describe, expect, it } from "vitest";

import { ToolCallLog, TOOL_CALL_LOG_MAX_ENTRIES } from "../../src/utils/skillRunner";

/**
 * Tool-call visibility was lost for every run since #143 removed
 * `Dashboard.writeBackupHistoryRecord` — the only writer that ever persisted
 * `tool_calls` into run history. #144 restores it by generalizing the
 * existing `RecentBashRing` pattern (Bash-only) to a `ToolCallLog` that
 * captures every tool, forwarded through the existing Go-authoritative
 * `pipeline.stageResult` channel instead of reintroducing a second writer.
 *
 * These tests prove the fix is not Bash-only: a non-Bash tool (Read/Edit)
 * must produce an entry, which the old deleted writer did and a
 * Bash-scoped replacement would not.
 */
describe("ToolCallLog — all-tools call log (Issue #144)", () => {
  it("records a non-Bash tool_use + tool_result pair", () => {
    const log = new ToolCallLog();

    log.observeToolUse("Read", { file_path: "internal/state/history.go" }, "tool-1");
    log.observeToolResult("tool-1", false, "file contents");

    expect(log.size).toBe(1);
    const [entry] = log.snapshot();
    expect(entry.tool).toBe("Read");
    expect(entry.target).toBe("internal/state/history.go");
    expect(entry.result).toBe("file contents");
    expect(entry.error).toBeUndefined();
    expect(entry.timestamp).toBeDefined();
  });

  it("records an Edit tool call and marks a failed result as an error", () => {
    const log = new ToolCallLog();

    log.observeToolUse("Edit", { file_path: "src/foo.ts" }, "tool-2");
    log.observeToolResult("tool-2", true, "old_string not found");

    const [entry] = log.snapshot();
    expect(entry.tool).toBe("Edit");
    expect(entry.error).toBe("old_string not found");
    expect(entry.result).toBeUndefined();
  });

  it("captures Bash calls too — it is a superset, not a replacement", () => {
    const log = new ToolCallLog();
    log.observeToolUse("Bash", { command: "go test ./..." }, "tool-3");
    log.observeToolResult("tool-3", false, "ok");

    expect(log.size).toBe(1);
    expect(log.snapshot()[0].tool).toBe("Bash");
  });

  it("dedupes a tool_use delivered under the same id twice", () => {
    const log = new ToolCallLog();
    log.observeToolUse("Read", { file_path: "a.ts" }, "dup-id");
    log.observeToolUse("Read", { file_path: "a.ts" }, "dup-id");
    expect(log.size).toBe(1);
  });

  it("bounds retention at TOOL_CALL_LOG_MAX_ENTRIES", () => {
    const log = new ToolCallLog();
    for (let i = 0; i < TOOL_CALL_LOG_MAX_ENTRIES + 25; i++) {
      log.observeToolUse("Read", { file_path: `file-${i}.ts` }, `id-${i}`);
    }
    expect(log.size).toBe(TOOL_CALL_LOG_MAX_ENTRIES);
    // Oldest-first: the retained window should end on the most recent entry.
    const snapshot = log.snapshot();
    expect(snapshot[snapshot.length - 1].target).toBe(`file-${TOOL_CALL_LOG_MAX_ENTRIES + 24}.ts`);
  });

  it("ignores a call with no tool name", () => {
    const log = new ToolCallLog();
    log.observeToolUse("", { file_path: "a.ts" }, "id-empty");
    expect(log.size).toBe(0);
  });

  it("a tool_result with no matching tool_use is a no-op", () => {
    const log = new ToolCallLog();
    log.observeToolResult("unknown-id", false, "result");
    expect(log.size).toBe(0);
  });
});
