/**
 * PipelineConnectivityStatusItem — status bar entry that surfaces pipeline
 * connectivity state when a pipeline stage is running and the network is
 * degraded or offline (Issue #3203).
 *
 * Visibility rules:
 *   - Hidden when ConnectivityStateBus.state === "online", regardless of
 *     pipeline activity.
 *   - Visible when state is "degraded" or "offline" AND a pipeline process
 *     is active (`hasActiveProcess()` returns true). This avoids cluttering
 *     the status bar when the user is idle.
 *
 * Click handler offers two actions:
 *   - "Cancel running pipelines" — kills all active stage subprocesses via
 *     `killAllActiveProcesses()`. The pipeline's failure handler reports the
 *     terminal kind as a normal cancel.
 *   - "Keep waiting" — closes the quick pick; the stall-ticker continues to
 *     suspend kill checks while offline.
 *
 * The item polls `hasActiveProcess()` once per second while the bus is in a
 * non-online state so visibility tracks pipeline lifecycle without taking a
 * direct dependency on every pipeline-start/end event.
 */

import * as vscode from "vscode";
import { ConnectivityStateBus } from "../platform/ConnectivityStateBus";
import { hasActiveProcess, killAllActiveProcesses } from "../utils/skillRunner";
import type { ConnectionState } from "../platform/types";

const POLL_INTERVAL_MS = 1_000;

interface StateDisplay {
  icon: string;
  label: string;
  background: vscode.ThemeColor | undefined;
}

const DEGRADED_DISPLAY: StateDisplay = {
  icon: "$(warning)",
  label: "Pipeline: connectivity unstable",
  background: new vscode.ThemeColor("statusBarItem.warningBackground"),
};

const OFFLINE_DISPLAY: StateDisplay = {
  icon: "$(error)",
  label: "Pipeline paused — offline",
  background: new vscode.ThemeColor("statusBarItem.errorBackground"),
};

export class PipelineConnectivityStatusItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;
  private readonly disposables: vscode.Disposable[] = [];
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private currentState: ConnectionState;

  constructor(commandId = "nightgauge.pipelineConnectivityAction") {
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      96 // Just to the right of PlatformStatusBarItem (97)
    );
    this.item.command = commandId;

    this.currentState = ConnectivityStateBus.state;

    this.disposables.push(
      ConnectivityStateBus.onChanged((evt) => {
        this.currentState = evt.current;
        this.updatePolling();
        this.render();
      }),
      vscode.commands.registerCommand(commandId, () => this.onClick())
    );

    this.updatePolling();
    this.render();
  }

  private updatePolling(): void {
    const shouldPoll = this.currentState !== "online";
    if (shouldPoll && this.pollTimer === null) {
      this.pollTimer = setInterval(() => this.render(), POLL_INTERVAL_MS);
    } else if (!shouldPoll && this.pollTimer !== null) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  private render(): void {
    const visible = this.currentState !== "online" && hasActiveProcess();
    if (!visible) {
      this.item.hide();
      return;
    }
    const display = this.currentState === "offline" ? OFFLINE_DISPLAY : DEGRADED_DISPLAY;
    this.item.text = `${display.icon} ${display.label}`;
    this.item.backgroundColor = display.background;
    this.item.tooltip =
      this.currentState === "offline"
        ? "Network is offline. The pipeline stall timer is suspended; the stage will resume when connectivity returns. Click to cancel or wait."
        : "Network connectivity is unstable. The pipeline is still running but stalls are being tolerated. Click for options.";
    this.item.show();
  }

  private async onClick(): Promise<void> {
    if (!hasActiveProcess()) {
      void vscode.window.showInformationMessage(
        "No pipeline is currently running. Connectivity-pause has nothing to act on."
      );
      return;
    }
    const cancel = "Cancel running pipelines";
    const wait = "Keep waiting";
    const choice = await vscode.window.showQuickPick(
      [
        {
          label: `$(stop) ${cancel}`,
          description: "Terminate all active pipeline stages",
          value: cancel,
        },
        {
          label: `$(clock) ${wait}`,
          description: "Leave the pipeline paused until connectivity returns",
          value: wait,
        },
      ],
      {
        placeHolder:
          this.currentState === "offline"
            ? "Pipeline paused — network is offline"
            : "Pipeline running on unstable connection",
        ignoreFocusOut: true,
      }
    );
    if (!choice) return;
    if (choice.value === cancel) {
      killAllActiveProcesses();
      void vscode.window.showInformationMessage("Cancelled all running pipelines.");
    }
  }

  dispose(): void {
    if (this.pollTimer !== null) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
    for (const d of this.disposables) {
      d.dispose();
    }
    this.item.dispose();
  }
}
