/**
 * RemoteCommandStatusService unit tests.
 *
 * Verifies status bar updates, notification firing, and notification
 * suppression based on config.remote.notifyOnPipelineRun.
 *
 * @see Issue #2170 — Add IPC bridge for remote command status
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({
  StatusBarAlignment: { Left: 1, Right: 2 },
  window: {
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
    showInformationMessage: vi.fn(),
  },
}));

import * as vscode from "vscode";
import { RemoteCommandStatusService } from "../../src/services/RemoteCommandStatusService";
import { RemoteCommandStatusBarItem } from "../../src/platform/RemoteCommandStatusBarItem";
import type {
  RemoteGetCommandHistoryResult,
  RemotePollingStatus,
} from "../../src/services/IpcClientBase";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeCommand(id: string, type: string): RemoteGetCommandHistoryResult["commands"][number] {
  return {
    id,
    type,
    status: "success",
    receivedAt: new Date().toISOString(),
    completedAt: new Date().toISOString(),
    durationMs: 100,
  };
}

function makeIpcClient(
  overrides: {
    isConnected?: boolean;
    commands?: RemoteGetCommandHistoryResult["commands"];
    polling?: boolean;
  } = {}
) {
  const historyResult: RemoteGetCommandHistoryResult = {
    commands: overrides.commands ?? [],
  };
  const pollingStatus: RemotePollingStatus = {
    active: overrides.polling ?? false,
    pendingCount: 0,
    errorCount: 0,
  };
  return {
    get isConnected() {
      return overrides.isConnected ?? true;
    },
    remoteGetCommandHistory: vi.fn().mockResolvedValue(historyResult),
    remoteGetPollingStatus: vi.fn().mockResolvedValue(pollingStatus),
  };
}

function makeConfigBridge(notifyOnPipelineRun: boolean) {
  return {
    getEffectiveConfig: () => ({
      config: {
        remote: { notifyOnPipelineRun },
      },
    }),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("RemoteCommandStatusService", () => {
  let statusBarItem: RemoteCommandStatusBarItem;

  beforeEach(() => {
    vi.clearAllMocks();
    statusBarItem = new RemoteCommandStatusBarItem();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("does not poll when IPC client is not connected", async () => {
    const ipc = makeIpcClient({ isConnected: false });
    const svc = new RemoteCommandStatusService(ipc as never, statusBarItem, makeConfigBridge(true));
    svc.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.remoteGetCommandHistory).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("polls immediately on start and updates status bar", async () => {
    const ipc = makeIpcClient({
      commands: [makeCommand("cmd-1", "pipeline.run")],
      polling: false,
    });
    const updateSpy = vi.spyOn(statusBarItem, "update");

    const svc = new RemoteCommandStatusService(
      ipc as never,
      statusBarItem,
      makeConfigBridge(false)
    );
    svc.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(ipc.remoteGetCommandHistory).toHaveBeenCalled();
    expect(updateSpy).toHaveBeenCalledWith(false, 1);
    svc.dispose();
  });

  it("shows notification for new pipeline.run when notifyOnPipelineRun=true", async () => {
    const ipc = makeIpcClient({
      commands: [makeCommand("cmd-new", "pipeline.run")],
    });
    const svc = new RemoteCommandStatusService(ipc as never, statusBarItem, makeConfigBridge(true));
    svc.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).toHaveBeenCalledWith(
      expect.stringContaining("pipeline.run")
    );
    svc.dispose();
  });

  it("does NOT show notification when notifyOnPipelineRun=false", async () => {
    const ipc = makeIpcClient({
      commands: [makeCommand("cmd-no-notify", "pipeline.run")],
    });
    const svc = new RemoteCommandStatusService(
      ipc as never,
      statusBarItem,
      makeConfigBridge(false)
    );
    svc.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("does NOT show notification for already-seen command IDs", async () => {
    const cmd = makeCommand("cmd-seen", "pipeline.run");
    const ipc = makeIpcClient({ commands: [cmd] });
    const svc = new RemoteCommandStatusService(ipc as never, statusBarItem, makeConfigBridge(true));
    svc.start();
    await vi.advanceTimersByTimeAsync(0);
    // First poll — notification fires
    expect(vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).toHaveBeenCalledTimes(
      1
    );
    (vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).mockClear();

    // Second poll with same command ID — no duplicate notification
    await vi.advanceTimersByTimeAsync(5_000);
    expect(vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).not.toHaveBeenCalled();

    svc.dispose();
  });

  it("does NOT show notification for non-pipeline.run command types", async () => {
    const ipc = makeIpcClient({
      commands: [makeCommand("cmd-cfg", "config.reload")],
    });
    const svc = new RemoteCommandStatusService(ipc as never, statusBarItem, makeConfigBridge(true));
    svc.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(vscode.window.showInformationMessage as ReturnType<typeof vi.fn>).not.toHaveBeenCalled();
    svc.dispose();
  });

  it("reflects active polling in status bar", async () => {
    const ipc = makeIpcClient({ polling: true, commands: [] });
    const updateSpy = vi.spyOn(statusBarItem, "update");

    const svc = new RemoteCommandStatusService(
      ipc as never,
      statusBarItem,
      makeConfigBridge(false)
    );
    svc.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(updateSpy).toHaveBeenCalledWith(true, 0);
    svc.dispose();
  });

  it("stops polling on dispose()", async () => {
    const ipc = makeIpcClient();
    const svc = new RemoteCommandStatusService(
      ipc as never,
      statusBarItem,
      makeConfigBridge(false)
    );
    svc.start();
    await vi.advanceTimersByTimeAsync(0);
    const callsBefore = (ipc.remoteGetCommandHistory as ReturnType<typeof vi.fn>).mock.calls.length;
    svc.dispose();
    await vi.advanceTimersByTimeAsync(10_000);
    expect((ipc.remoteGetCommandHistory as ReturnType<typeof vi.fn>).mock.calls.length).toBe(
      callsBefore
    );
  });
});
