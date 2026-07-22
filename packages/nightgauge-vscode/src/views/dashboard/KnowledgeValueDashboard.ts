/**
 * KnowledgeValueDashboard — webview panel for the KB Value dashboard (#3600).
 *
 * Calls `knowledge.metrics` twice on each refresh (current + prior window) so
 * the renderer can show delta-vs-prior totals (ADR-002). 60 s in-memory cache
 * keyed on `windowDays` debounces window-selector spam; auto-refresh and the
 * Refresh button explicitly invalidate the cache (ADR-004).
 */

import * as vscode from "vscode";
import { IpcClient } from "../../services/IpcClient";
import type { KnowledgeMetricsResult } from "../../services/IpcClientBase";
import { getKnowledgeValueDashboardHtml } from "./KnowledgeValueDashboardHtml";
import {
  computeDelta,
  type KnowledgeValueState,
  type WindowDays,
} from "./KnowledgeValueDashboardTypes";
import { Logger } from "../../utils/logger";

type WebviewMessage = { type: "refresh"; windowDays?: number };

const CACHE_TTL_MS = 60_000;
const AUTO_REFRESH_MS = 5 * 60_000;

interface CacheEntry {
  current: KnowledgeMetricsResult;
  prior: KnowledgeMetricsResult;
  loadedAt: number;
}

export class KnowledgeValueDashboard implements vscode.Disposable {
  private panel: vscode.WebviewPanel | undefined;
  private disposables: vscode.Disposable[] = [];
  private autoRefreshTimer: ReturnType<typeof setInterval> | undefined;
  private state: KnowledgeValueState = {
    windowDays: 7,
    current: null,
    prior: null,
    delta: null,
    loadedAt: 0,
    loading: false,
    error: null,
  };
  private cache = new Map<WindowDays, CacheEntry>();
  private logger = new Logger("Nightgauge Knowledge Value");

  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly ipc: IpcClient = IpcClient.getInstance()
  ) {}

  show(): void {
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      return;
    }

    this.panel = vscode.window.createWebviewPanel(
      "incrediKnowledgeValueDashboard",
      "Knowledge Value",
      vscode.ViewColumn.One,
      {
        enableScripts: true,
        retainContextWhenHidden: true,
        localResourceRoots: [vscode.Uri.joinPath(this.extensionUri, "src", "views", "dashboard")],
      }
    );

    this.panel.webview.onDidReceiveMessage(
      (msg: WebviewMessage) => this.handleMessage(msg),
      undefined,
      this.disposables
    );
    this.panel.onDidDispose(() => this.handleClosed(), undefined, this.disposables);
    this.panel.onDidChangeViewState(
      () => this.handleVisibilityChange(),
      undefined,
      this.disposables
    );

    this.render();
    void this.refresh(this.state.windowDays, { invalidate: false });
    this.startAutoRefresh();
  }

  isVisible(): boolean {
    return this.panel?.visible ?? false;
  }

  dispose(): void {
    this.stopAutoRefresh();
    if (this.panel) {
      this.panel.dispose();
      this.panel = undefined;
    }
    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }
    this.logger.dispose();
  }

  private async handleMessage(msg: WebviewMessage): Promise<void> {
    if (msg.type === "refresh") {
      const days = normalizeWindowDays(msg.windowDays);
      await this.refresh(days, { invalidate: true });
    }
  }

  private handleClosed(): void {
    this.stopAutoRefresh();
    this.panel = undefined;
  }

  private handleVisibilityChange(): void {
    if (!this.panel) return;
    if (this.panel.visible) {
      this.startAutoRefresh();
    } else {
      this.stopAutoRefresh();
    }
  }

  private startAutoRefresh(): void {
    this.stopAutoRefresh();
    this.autoRefreshTimer = setInterval(() => {
      if (this.panel?.visible) {
        void this.refresh(this.state.windowDays, { invalidate: true });
      }
    }, AUTO_REFRESH_MS);
  }

  private stopAutoRefresh(): void {
    if (this.autoRefreshTimer) {
      clearInterval(this.autoRefreshTimer);
      this.autoRefreshTimer = undefined;
    }
  }

  private async refresh(windowDays: WindowDays, opts: { invalidate: boolean }): Promise<void> {
    this.state.windowDays = windowDays;
    this.state.loading = true;
    this.state.error = null;
    this.render();

    try {
      const cached = !opts.invalidate ? this.cache.get(windowDays) : undefined;
      if (cached && Date.now() - cached.loadedAt < CACHE_TTL_MS) {
        this.applyResult(windowDays, cached.current, cached.prior, cached.loadedAt);
        return;
      }
      // Two IPC calls: current window then prior window (ADR-002).
      const current = await this.ipc.knowledgeMetrics(windowDays, 30);
      const prior = await this.ipc.knowledgeMetrics(windowDays * 2, 30);
      const loadedAt = Date.now();
      this.cache.set(windowDays, { current, prior, loadedAt });
      this.applyResult(windowDays, current, prior, loadedAt);
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      this.state.loading = false;
      this.state.error = msg;
      this.logger.debug("refresh:error", { windowDays, error: msg });
      this.render();
    }
  }

  private applyResult(
    windowDays: WindowDays,
    current: KnowledgeMetricsResult,
    prior: KnowledgeMetricsResult,
    loadedAt: number
  ): void {
    this.state.current = current;
    // Prior is "windowDays*2 back to windowDays" — subtract current from the
    // double-window aggregate to surface the previous-window-only totals.
    const priorOnly: KnowledgeMetricsResult = {
      ...prior,
      totals: {
        ...prior.totals,
        writes: Math.max(0, prior.totals.writes - current.totals.writes),
        reads: Math.max(0, prior.totals.reads - current.totals.reads),
        recalls: Math.max(0, prior.totals.recalls - current.totals.recalls),
        recall_hits: Math.max(0, prior.totals.recall_hits - current.totals.recall_hits),
        graduations: Math.max(0, prior.totals.graduations - current.totals.graduations),
      },
    };
    this.state.prior = priorOnly;
    this.state.delta = computeDelta(current, priorOnly);
    this.state.loadedAt = loadedAt;
    this.state.loading = false;
    this.state.windowDays = windowDays;
    this.render();
  }

  private render(): void {
    if (!this.panel) return;
    this.panel.webview.html = getKnowledgeValueDashboardHtml(this.state);
  }
}

function normalizeWindowDays(d: number | undefined): WindowDays {
  if (d === 30) return 30;
  if (d === 90) return 90;
  return 7;
}
