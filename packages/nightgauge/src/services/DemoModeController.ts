/**
 * Demo-mode awareness (#2108, ADR-026 decisions 4-6).
 *
 * The demo daemon announces itself with `ipc.ready { demo: true }`. For the
 * life of that connection the extension:
 *
 *   - sets the `nightgauge.demoMode` context key and shows a "Demo" status
 *     bar badge that no setting can hide, so a recording never passes for a
 *     real run (decision 4);
 *   - honours `demo.uiStep { command, args }` for a fixed allowlist of UI
 *     actions defined here, and logs and drops everything else (decision 5);
 *   - keeps the platform SSE streams and the agent heartbeat stopped,
 *     whatever the settings say (decision 6). {@link isDemoMode} is the gate
 *     those services check before starting.
 *
 * `demo.uiStep` is inert unless the connected daemon announced demo mode: a
 * real binary cannot drive the UI through it. The badge and the gate clear
 * on disconnect and on any `ipc.ready` without `demo: true`.
 */

import * as vscode from "vscode";
import { VALID_TABS } from "../views/dashboard/DashboardHtml";

/** Context key set while a demo daemon is connected. */
export const DEMO_CONTEXT_KEY = "nightgauge.demoMode";

/** Scenario view names → the Nightgauge view each one focuses. */
export const DEMO_VIEW_TARGETS: Readonly<Record<string, string>> = {
  "pipeline-tree": "nightgauge.pipelineView",
  repositories: "nightgauge.repositoriesView",
  attention: "nightgauge.attentionView",
  knowledge: "nightgauge.knowledgeView",
  "query-results": "nightgauge.queryResults",
};

/**
 * The only UI actions a scenario can trigger. Mirrored by `UI_ACTIONS` in
 * `demo/daemon/scenario.cjs`, which rejects anything else at load time.
 */
export const DEMO_UI_ACTIONS = [
  "dashboard.open",
  "dashboard.tab",
  "view.focus",
  "pipeline.expandActiveIssue",
] as const;
export type DemoUiAction = (typeof DEMO_UI_ACTIONS)[number];

let demoModeActive = false;

/** True while a daemon that announced `demo: true` is connected. */
export function isDemoMode(): boolean {
  return demoModeActive;
}

/** The IPC surface the controller listens on (a subset of IpcClient). */
export interface DemoIpc {
  on(event: string, handler: (data: unknown) => void): { dispose: () => void };
  readonly onDidChangeStatus: vscode.Event<boolean>;
  /** The current connection's `ipc.ready` payload, when it already arrived. */
  readonly readyPayload?: unknown;
}

/** The UI the allowlisted actions drive. */
export interface DemoUiTargets {
  showDashboard(): void;
  selectDashboardTab(tab: string): boolean;
  revealActiveIssue(): Promise<boolean>;
}

export interface DemoHost {
  createStatusBarItem(): vscode.StatusBarItem;
  executeCommand(command: string, ...args: unknown[]): Thenable<unknown>;
  log(message: string): void;
  /** Stops platform SSE and the agent heartbeat when demo mode begins. */
  stopPlatformServices(): void;
}

export class DemoModeController implements vscode.Disposable {
  private badge: vscode.StatusBarItem | null = null;
  private readonly subscriptions: Array<{ dispose: () => void }> = [];

  constructor(
    ipc: DemoIpc,
    private readonly ui: DemoUiTargets,
    private readonly host: DemoHost
  ) {
    const onReady = (data: unknown): void => {
      if ((data as { demo?: unknown } | null)?.demo === true) this.enter();
      else this.leave();
    };
    this.subscriptions.push(
      ipc.on("ipc.ready", onReady),
      ipc.on("demo.uiStep", (data) => {
        void this.handleUiStep(data);
      }),
      ipc.onDidChangeStatus((connected) => {
        if (!connected) this.leave();
      })
    );
    // The handshake may have landed during activation, before this listener.
    if (ipc.readyPayload) onReady(ipc.readyPayload);
  }

  get active(): boolean {
    return demoModeActive;
  }

  private enter(): void {
    if (demoModeActive) return;
    demoModeActive = true;
    this.host.stopPlatformServices();
    void this.host.executeCommand("setContext", DEMO_CONTEXT_KEY, true);
    const badge = this.host.createStatusBarItem();
    badge.name = "Nightgauge Demo Mode";
    badge.text = "$(beaker) Demo";
    badge.tooltip = "Nightgauge is connected to the demo daemon. Nothing shown here is a real run.";
    badge.accessibilityInformation = {
      label: "Nightgauge demo mode: connected to the demo daemon, not a real run",
      role: "status",
    };
    badge.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
    badge.color = new vscode.ThemeColor("statusBarItem.warningForeground");
    badge.show();
    this.badge = badge;
    this.host.log("Demo mode: the connected daemon announced demo: true");
  }

  private leave(): void {
    if (!demoModeActive) return;
    demoModeActive = false;
    this.badge?.dispose();
    this.badge = null;
    void this.host.executeCommand("setContext", DEMO_CONTEXT_KEY, false);
    this.host.log("Demo mode ended");
  }

  /** Runs one allowlisted action; returns whether it ran. */
  async handleUiStep(data: unknown): Promise<boolean> {
    if (!demoModeActive) {
      this.host.log("demo.uiStep dropped: the connected daemon is not in demo mode");
      return false;
    }
    const step = (data ?? {}) as { command?: unknown; args?: unknown };
    const command = step.command;
    const args = (step.args && typeof step.args === "object" ? step.args : {}) as Record<
      string,
      unknown
    >;
    const target = typeof args.target === "string" ? args.target : undefined;
    const drop = (reason: string): false => {
      this.host.log(`demo.uiStep dropped: ${reason}`);
      return false;
    };
    if (typeof command !== "string" || !(DEMO_UI_ACTIONS as readonly string[]).includes(command)) {
      return drop(`${JSON.stringify(command)} is not an allowlisted action`);
    }
    switch (command as DemoUiAction) {
      case "dashboard.open":
        this.ui.showDashboard();
        return true;
      case "dashboard.tab":
        if (!target || !(VALID_TABS as readonly string[]).includes(target)) {
          return drop(`unknown dashboard tab ${JSON.stringify(target)}`);
        }
        this.ui.showDashboard();
        return this.ui.selectDashboardTab(target);
      case "view.focus": {
        const viewId = target ? DEMO_VIEW_TARGETS[target] : undefined;
        if (!viewId) return drop(`unknown view ${JSON.stringify(target)}`);
        await this.host.executeCommand(`${viewId}.focus`);
        return true;
      }
      case "pipeline.expandActiveIssue":
        return this.ui.revealActiveIssue();
    }
  }

  dispose(): void {
    this.leave();
    for (const s of this.subscriptions) s.dispose();
    this.subscriptions.length = 0;
  }
}
