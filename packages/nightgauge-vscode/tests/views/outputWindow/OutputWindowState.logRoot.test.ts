/**
 * OutputWindowState.logRoot.test.ts
 *
 * #191 — disk session logs must land in the RUN's target repo, not
 * workspaceFolders[0]'s git root. Per-slot roots win (concurrent runs in
 * different repos each log to their own repo), then the sequential run's
 * root, then the bootstrap default for non-run output.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/log-file-writer")>()),
  LogFileWriter: {
    appendToLog: vi.fn().mockResolvedValue(undefined),
    truncateForLog: vi.fn((s: string) => s),
  },
}));

import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";
import { LogFileWriter } from "../../../src/utils/log-file-writer";

const appendSpy = vi.mocked(LogFileWriter.appendToLog);

describe("OutputWindowState disk-log root resolution (#191)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    vi.clearAllMocks();
    state = new OutputWindowState();
    state.setLogConfig("/workspace/first-repo");
  });

  function lastLogRoot(): string {
    expect(appendSpy).toHaveBeenCalled();
    return appendSpy.mock.calls[appendSpy.mock.calls.length - 1][0] as string;
  }

  it("defaults to the bootstrap workspace root for non-run output", () => {
    state.addEntry("hello", "info");
    expect(lastLogRoot()).toBe("/workspace/first-repo");
  });

  it("routes a slot's entries to that slot's repo root", () => {
    state.setSlotLogRoot(1, "/workspace/target-repo");
    state.addEntry("slot line", "info", undefined, { slotIndex: 1 });
    expect(lastLogRoot()).toBe("/workspace/target-repo");
  });

  it("keeps concurrent slots in different repos separated", () => {
    state.setSlotLogRoot(0, "/workspace/repo-a");
    state.setSlotLogRoot(1, "/workspace/repo-b");

    state.addEntry("a", "info", undefined, { slotIndex: 0 });
    const rootA = lastLogRoot();
    state.addEntry("b", "info", undefined, { slotIndex: 1 });
    const rootB = lastLogRoot();

    expect(rootA).toBe("/workspace/repo-a");
    expect(rootB).toBe("/workspace/repo-b");
  });

  it("uses the sequential run root when no slot root matches", () => {
    state.setRunLogRoot("/workspace/target-repo");
    state.addEntry("run line", "info");
    expect(lastLogRoot()).toBe("/workspace/target-repo");
  });

  it("falls back to the bootstrap root after clearing per-run state", () => {
    state.setRunLogRoot("/workspace/target-repo");
    state.setRunLogRoot(null);
    state.setSlotLogRoot(1, "/workspace/repo-b");
    state.setSlotLogRoot(1, null);

    state.addEntry("after clear", "info", undefined, { slotIndex: 1 });
    expect(lastLogRoot()).toBe("/workspace/first-repo");
  });
});
