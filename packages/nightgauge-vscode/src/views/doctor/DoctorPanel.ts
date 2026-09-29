/**
 * DoctorPanel — singleton webview for the doctor's findings (ADR-025).
 *
 * Opening it runs `doctor.run` and streams `doctor.progress` into the page.
 * Each card's actions go back to the daemon through the `doctor.*` IPC
 * methods; the webview never spawns a process and never names a verb.
 *
 * The webview posts only `{ type, fingerprint, remedyId }`. Every message is
 * validated here: an unknown type or an extra field drops it, and the
 * fingerprint must be in the current scan with a remedy of the kind the
 * action needs. After a fix the card shows the verified outcome in place,
 * without re-rendering the page.
 */

import * as vscode from "vscode";
import { getNonce } from "../dashboard/DashboardComponents.js";
import type {
  DoctorApplyRemedyResult,
  DoctorFinding,
  DoctorRemedy,
} from "../../services/IpcClientBase";
import {
  docsUrl,
  firstAllowedLink,
  FINGERPRINT_RE,
  isAllowedLink,
  remediesOf,
  REMEDY_ID_RE,
  renderDoctorHtml,
  safeFixes,
  type DoctorViewState,
} from "./DoctorHtml.js";
import type { DoctorService } from "./DoctorService.js";

export const DOCTOR_VIEW_TYPE = "nightgaugeDoctor";

/** The message types the webview may post. Anything else is dropped. */
export const DOCTOR_MESSAGE_TYPES = [
  "run",
  "fix",
  "review",
  "open",
  "openDocs",
  "recheck",
  "fixAllSafe",
] as const;
export type DoctorMessageType = (typeof DOCTOR_MESSAGE_TYPES)[number];

export interface DoctorMessage {
  type: DoctorMessageType;
  fingerprint?: string;
  remedyId?: string;
}

const ALLOWED_KEYS = new Set(["type", "fingerprint", "remedyId"]);

/** Shape-check a webview message. Returns undefined to drop it. */
export function parseDoctorMessage(raw: unknown): DoctorMessage | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const msg = raw as Record<string, unknown>;
  if (Object.keys(msg).some((k) => !ALLOWED_KEYS.has(k))) return undefined;
  if (!(DOCTOR_MESSAGE_TYPES as readonly unknown[]).includes(msg.type)) return undefined;
  if (msg.fingerprint !== undefined) {
    if (typeof msg.fingerprint !== "string" || !FINGERPRINT_RE.test(msg.fingerprint)) {
      return undefined;
    }
  }
  if (msg.remedyId !== undefined) {
    if (typeof msg.remedyId !== "string" || !REMEDY_ID_RE.test(msg.remedyId)) return undefined;
  }
  return msg as unknown as DoctorMessage;
}

/** What a card shows after an apply, from the verified outcome. */
export function describeOutcome(
  result: DoctorApplyRemedyResult,
  finding: DoctorFinding
): { state: "resolved" | "present" | "failed" | "idle"; text: string } {
  const detail = result.detail ? `: ${result.detail}` : "";
  switch (result.outcome) {
    case "fixed":
      return {
        state: "resolved",
        text: `Fixed. Verified: the ${finding.check} check no longer reports it.`,
      };
    case "still-present":
      return {
        state: "present",
        text: "Still present after the fix. The new evidence is under Details.",
      };
    case "stale":
      return {
        state: "resolved",
        text: "No longer in the current scan. Run the doctor again to refresh the page.",
      };
    case "blocked":
      return { state: "failed", text: `Blocked${detail}. Nothing was changed.` };
    case "conflict":
      return { state: "failed", text: `Conflict${detail}. Nothing was overwritten.` };
    case "skipped":
      return { state: "idle", text: `Skipped${detail}.` };
    default:
      if (result.action === "awaiting-consent") {
        return { state: "idle", text: "Not applied: this fix needs your confirmation." };
      }
      return { state: "idle", text: `Not applied${detail}.` };
  }
}

function evidencePairs(evidence: Record<string, string> | null | undefined): [string, string][] {
  return Object.entries(evidence ?? {}).sort(([a], [b]) => a.localeCompare(b));
}

export class DoctorPanel implements vscode.Disposable {
  private static currentPanel: DoctorPanel | undefined;

  private panel: vscode.WebviewPanel | undefined;
  private readonly disposables: vscode.Disposable[] = [];
  private state: DoctorViewState = { phase: "running", progress: [] };
  private readonly busy = new Set<string>();
  private scanning = false;

  private constructor(private service: DoctorService) {}

  /**
   * Reveal the panel and run a scan. A second call reuses the webview and
   * runs again (joining a scan already in flight).
   */
  static show(service: DoctorService): DoctorPanel {
    if (!DoctorPanel.currentPanel) {
      DoctorPanel.currentPanel = new DoctorPanel(service);
    } else {
      DoctorPanel.currentPanel.service = service;
    }
    const current = DoctorPanel.currentPanel;
    current.reveal();
    if (!current.scanning) void current.runScan();
    return current;
  }

  static get current(): DoctorPanel | undefined {
    return DoctorPanel.currentPanel;
  }

  private render(): void {
    if (!this.panel) return;
    this.panel.webview.html = renderDoctorHtml(this.state, {
      cspSource: this.panel.webview.cspSource,
      nonce: getNonce(),
    });
  }

  private post(message: Record<string, unknown>): void {
    void this.panel?.webview.postMessage(message);
  }

  private reveal(): void {
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      return;
    }
    this.panel = vscode.window.createWebviewPanel(
      DOCTOR_VIEW_TYPE,
      "Nightgauge: Doctor",
      vscode.ViewColumn.One,
      { enableScripts: true, retainContextWhenHidden: true, localResourceRoots: [] }
    );
    this.render();
    this.panel.webview.onDidReceiveMessage(
      (msg) => this.handleMessage(msg),
      undefined,
      this.disposables
    );
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
  }

  /** Run `doctor.run`, streaming progress into the page. */
  async runScan(): Promise<void> {
    if (this.scanning) return;
    this.scanning = true;
    this.state = { phase: "running", progress: [], result: this.state.result };
    this.busy.clear();
    this.render();
    const progress = this.service.onProgress((event) => {
      this.state.progress.push(event);
      this.post({ type: "progress", event });
    });
    try {
      const result = await this.service.run({ probeAdapters: true });
      this.state = {
        phase: "ready",
        result,
        progress: this.state.progress,
        generatedAt: new Date().toLocaleString(),
      };
    } catch (err) {
      this.state = {
        phase: "error",
        progress: this.state.progress,
        error: err instanceof Error ? err.message : String(err),
      };
    } finally {
      progress.dispose();
      this.scanning = false;
    }
    this.render();
  }

  /** Validate one webview message and act on it. Exposed for tests. */
  async handleMessage(raw: unknown): Promise<void> {
    const msg = parseDoctorMessage(raw);
    if (!msg) return;
    if (msg.type === "run") {
      await this.runScan();
      return;
    }
    if (this.state.phase !== "ready") return;
    if (msg.type === "fixAllSafe") {
      await this.fixAllSafe();
      return;
    }
    const finding = msg.fingerprint ? this.service.find(msg.fingerprint) : undefined;
    if (!finding) return;
    const remedy = msg.remedyId
      ? remediesOf(finding).find((r) => r.id === msg.remedyId)
      : undefined;
    switch (msg.type) {
      case "fix":
        if (remedy?.kind === "auto") await this.apply(finding, remedy, false);
        return;
      case "review":
        if (remedy?.kind === "confirm") await this.review(finding, remedy);
        return;
      case "open":
        if (remedy?.kind === "manual") await this.openLink(firstAllowedLink(remedy));
        return;
      case "openDocs":
        await this.openLink(docsUrl(finding));
        return;
      case "recheck":
        await this.recheck(finding);
        return;
    }
  }

  private async withBusy(fingerprint: string, text: string, work: () => Promise<void>) {
    if (this.busy.has(fingerprint)) return;
    this.busy.add(fingerprint);
    this.post({ type: "busy", fingerprint, busy: true, text });
    try {
      await work();
    } catch (err) {
      this.post({
        type: "outcome",
        fingerprint,
        state: "failed",
        text: `Failed: ${err instanceof Error ? err.message : String(err)}`,
      });
    } finally {
      this.busy.delete(fingerprint);
    }
  }

  private postOutcome(finding: DoctorFinding, result: DoctorApplyRemedyResult): void {
    const { state, text } = describeOutcome(result, finding);
    const evidence =
      result.outcome === "still-present"
        ? evidencePairs(result.evidence ?? result.finding?.evidence)
        : undefined;
    this.post({ type: "outcome", fingerprint: finding.fingerprint, state, text, evidence });
  }

  private apply(finding: DoctorFinding, remedy: DoctorRemedy, confirm: boolean): Promise<void> {
    return this.withBusy(finding.fingerprint, "Applying the fix…", async () => {
      const result = await this.service.applyRemedy(finding.fingerprint, remedy.id, { confirm });
      this.postOutcome(finding, result);
    });
  }

  /** Confirm remedies: preview in the card, then ask with a modal. */
  private review(finding: DoctorFinding, remedy: DoctorRemedy): Promise<void> {
    return this.withBusy(finding.fingerprint, "Previewing the fix…", async () => {
      const preview = await this.service.applyRemedy(finding.fingerprint, remedy.id, {
        dryRun: true,
      });
      if (preview.outcome === "stale") {
        this.postOutcome(finding, preview);
        return;
      }
      const text = preview.preview || remedy.preview || remedy.summary;
      this.post({ type: "preview", fingerprint: finding.fingerprint, preview: text });
      const choice = await vscode.window.showWarningMessage(
        `${finding.code}: ${remedy.summary}?`,
        {
          modal: true,
          detail: remedy.reversible ? text : `${text}\n\nThis cannot be undone.`,
        },
        "Fix"
      );
      if (choice !== "Fix") {
        this.post({
          type: "outcome",
          fingerprint: finding.fingerprint,
          state: "idle",
          text: "Not applied.",
        });
        return;
      }
      this.post({
        type: "busy",
        fingerprint: finding.fingerprint,
        busy: true,
        text: "Applying the fix…",
      });
      const result = await this.service.applyRemedy(finding.fingerprint, remedy.id, {
        confirm: true,
      });
      this.postOutcome(finding, result);
    });
  }

  private recheck(finding: DoctorFinding): Promise<void> {
    return this.withBusy(finding.fingerprint, `Re-running ${finding.check}…`, async () => {
      const result = await this.service.recheck(finding);
      const still = (result.findings ?? []).find((f) => f.fingerprint === finding.fingerprint);
      if (still) {
        this.post({
          type: "outcome",
          fingerprint: finding.fingerprint,
          state: "present",
          text: "Still present. The latest evidence is under Details.",
          evidence: evidencePairs(still.evidence),
        });
      } else if (result.status === "passed" || result.status === "failed") {
        this.post({
          type: "outcome",
          fingerprint: finding.fingerprint,
          state: "resolved",
          text: `No longer detected: the ${result.check} check no longer reports it.`,
        });
      } else {
        this.post({
          type: "outcome",
          fingerprint: finding.fingerprint,
          state: "failed",
          text: `Could not check again: the ${result.check} check ${result.status === "timeout" ? "timed out" : "was skipped"}.`,
        });
      }
    });
  }

  private async fixAllSafe(): Promise<void> {
    const findings = this.service.latest?.findings ?? [];
    for (const { finding, remedy } of safeFixes(findings)) {
      await this.apply(finding, remedy, false);
    }
  }

  private async openLink(url: string | undefined): Promise<void> {
    if (!url || !isAllowedLink(url)) return;
    await vscode.env.openExternal(vscode.Uri.parse(url, true));
  }

  private handlePanelClosed(): void {
    this.panel = undefined;
    DoctorPanel.currentPanel = undefined;
    while (this.disposables.length) {
      this.disposables.pop()?.dispose();
    }
  }

  dispose(): void {
    const panel = this.panel;
    this.handlePanelClosed();
    panel?.dispose();
  }
}
