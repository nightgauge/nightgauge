/**
 * RemoteCommandStatusService — polls Go IPC for remote command history and
 * polling status, drives the RemoteCommandStatusBarItem, and fires configurable
 * VSCode notifications when new pipeline.run commands are received.
 *
 * Polling interval: 5 seconds. Stops automatically when the IPC client is not
 * connected. Debounces notifications by tracking last-seen command IDs to
 * prevent duplicate alerts for the same command.
 *
 * @see Issue #2170 — Add IPC bridge for remote command status
 */

import * as vscode from "vscode";
import type { IpcClient } from "./IpcClient";
import type { RemoteCommandStatusBarItem } from "../platform/RemoteCommandStatusBarItem";
import type { ConfigBridge } from "./ConfigBridge";

/** Polling interval in milliseconds. */
const POLL_INTERVAL_MS = 5_000;

/**
 * RemoteCommandStatusService polls the Go binary's remote command state and
 * updates the status bar + notifications accordingly.
 */
export class RemoteCommandStatusService implements vscode.Disposable {
  private pollInterval: ReturnType<typeof setInterval> | null = null;
  private lastSeenCommandIds = new Set<string>();

  constructor(
    private readonly ipcClient: IpcClient,
    private readonly statusBarItem: RemoteCommandStatusBarItem,
    private readonly configBridge: ConfigBridge
  ) {}

  /** Start polling. Idempotent — calling start() multiple times is safe. */
  start(): void {
    if (this.pollInterval !== null) {
      return;
    }
    // Run once immediately, then on interval
    void this.poll();
    this.pollInterval = setInterval(() => {
      void this.poll();
    }, POLL_INTERVAL_MS);
  }

  /** Stop polling. */
  stop(): void {
    if (this.pollInterval !== null) {
      clearInterval(this.pollInterval);
      this.pollInterval = null;
    }
  }

  dispose(): void {
    this.stop();
  }

  /** Perform one poll cycle. Errors are swallowed to avoid spamming logs. */
  private async poll(): Promise<void> {
    if (!this.ipcClient.isConnected) {
      return;
    }

    let historyResult: Awaited<ReturnType<IpcClient["remoteGetCommandHistory"]>> | null;
    let pollingStatus: Awaited<ReturnType<IpcClient["remoteGetPollingStatus"]>> | null;

    try {
      [historyResult, pollingStatus] = await Promise.all([
        this.ipcClient.remoteGetCommandHistory(),
        this.ipcClient.remoteGetPollingStatus(),
      ]);
    } catch {
      // IPC not available — leave status bar unchanged
      return;
    }

    const commands = historyResult?.commands ?? [];
    const isPolling = pollingStatus?.active ?? false;

    // Update status bar
    this.statusBarItem.update(isPolling, commands.length);

    // Detect new pipeline.run commands and notify if configured
    const notifyEnabled =
      this.configBridge.getEffectiveConfig()?.config?.remote?.notifyOnPipelineRun ?? true;
    if (notifyEnabled) {
      for (const cmd of commands) {
        if (cmd.type === "pipeline.run" && !this.lastSeenCommandIds.has(cmd.id)) {
          vscode.window.showInformationMessage("Nightgauge: Remote pipeline.run received");
        }
      }
    }

    // Update seen IDs regardless of notification toggle so we don't fire
    // retroactive notifications if the user re-enables the config.
    for (const cmd of commands) {
      this.lastSeenCommandIds.add(cmd.id);
    }
  }
}
