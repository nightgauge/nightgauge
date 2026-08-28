/**
 * #1051 — the `Nightgauge` output channel had no durable sink.
 *
 * `Logger`'s only sink was `channel.appendLine`; it never referenced
 * `LogFileWriter`, whose `appendToLog` had exactly one caller, in the
 * output-window view. So the channel and the per-issue session log were two
 * DISJOINT streams — the session log carried stage execution detail, the
 * channel carried extension lifecycle, config resolution, board sync, gate
 * results and auto-cleanup — and only one of them survived the window closing.
 *
 * Neither surface alone can reconstruct a run. That is what made post-hoc
 * diagnosis depend on a human watching a panel live.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      name: "Nightgauge",
      append: vi.fn(),
      appendLine: vi.fn(),
      replace: vi.fn(),
      clear: vi.fn(),
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

const appendToLog = vi.fn().mockResolvedValue(undefined);
vi.mock("../../src/utils/log-file-writer", () => ({
  LogFileWriter: {
    appendToLog: (...args: unknown[]) => appendToLog(...args),
  },
}));

import {
  Logger,
  installLogDiskSink,
  resetMainChannelForTests,
  getPrefixedMainChannel,
} from "../../src/utils/logger";

/** The sink is fire-and-forget behind a dynamic import; let the microtasks run. */
const flush = () => new Promise((r) => setTimeout(r, 0));

describe("Logger disk sink (#1051)", () => {
  beforeEach(() => {
    appendToLog.mockClear();
    resetMainChannelForTests();
  });

  afterEach(() => {
    resetMainChannelForTests();
  });

  it("writes info output to disk once a sink is installed", async () => {
    installLogDiskSink("/repo");
    const log = new Logger("Nightgauge");

    log.info("Auto-cleanup: deleted 1 stale local branch(es)", { branches: ["docs/x"] });
    await flush();

    expect(appendToLog).toHaveBeenCalledTimes(1);
    const [root, issueNumber, level, , body] = appendToLog.mock.calls[0];
    expect(root).toBe("/repo");
    // null issue number keeps these in a daily file rather than growing the
    // 50k-line per-issue run logs, and leaves readEntriesForIssue untouched.
    expect(issueNumber).toBeNull();
    expect(level).toBe("INFO");
    expect(body).toContain("Auto-cleanup: deleted 1 stale local branch(es)");
  });

  it("writes nothing when no sink is installed", async () => {
    const log = new Logger("Nightgauge");

    log.info("lifecycle line");
    log.warn("a warning");
    await flush();

    expect(appendToLog).not.toHaveBeenCalled();
  });

  it("excludes DEBUG by default and includes it when asked", async () => {
    installLogDiskSink("/repo");
    const log = new Logger("Nightgauge");

    log.debug("noisy internal detail");
    await flush();
    expect(appendToLog).not.toHaveBeenCalled();

    installLogDiskSink("/repo", undefined, { debug: true });
    log.debug("noisy internal detail");
    await flush();
    expect(appendToLog).toHaveBeenCalledTimes(1);
  });

  it("redacts secrets on the path to disk", async () => {
    installLogDiskSink("/repo");
    const log = new Logger("Nightgauge");

    log.info("token in hand", { token: "ghp_abcdefghijklmnopqrstuvwxyz0123456789" });
    await flush();

    const body = appendToLog.mock.calls[0][4] as string;
    expect(body).not.toContain("ghp_abcdefghijklmnopqrstuvwxyz0123456789");
  });

  it("captures output from a prefixed channel — the path board sync uses", async () => {
    installLogDiskSink("/repo");
    const channel = getPrefixedMainChannel("pipeline");

    channel.appendLine("[ProjectBoardService] Config loaded via IPC: project=6");
    await flush();

    expect(appendToLog).toHaveBeenCalledTimes(1);
    const body = appendToLog.mock.calls[0][4] as string;
    expect(body).toContain("[pipeline]");
    expect(body).toContain("ProjectBoardService");
  });

  it("does not let a disk failure escape into the caller", async () => {
    appendToLog.mockRejectedValueOnce(new Error("disk full"));
    installLogDiskSink("/repo");
    const log = new Logger("Nightgauge");

    // Logging must never be able to fail the thing it is observing.
    expect(() => log.error("something broke")).not.toThrow();
    await flush();
  });
});
