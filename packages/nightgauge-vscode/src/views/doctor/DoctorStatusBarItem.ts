/**
 * DoctorStatusBarItem — the doctor's verdict in the status bar (ADR-025).
 *
 *   - `Nightgauge: ✓ healthy`   no blocker or warning
 *   - `Nightgauge: N warnings`  warnings only
 *   - `Nightgauge: N blockers`  at least one blocker (blockers win)
 *
 * The text carries the state; the background color only repeats it. Clicking
 * opens the Doctor panel. The item follows every doctor result the extension
 * receives (a panel scan, a fix, a re-check) and scans in the background at
 * most once every ten minutes, without the opt-in adapter probe.
 */

import * as vscode from "vscode";
import type { DoctorSummary } from "../../services/IpcClientBase";
import type { DoctorService } from "./DoctorService";

export const RUN_DOCTOR_COMMAND = "nightgauge.runDoctor";

/** A background scan runs at most this often. */
export const BACKGROUND_INTERVAL_MS = 10 * 60 * 1000;

/** How often the item asks whether a background scan is due. */
const TICK_MS = 60 * 1000;

/** The first background scan waits this long, so activation is not slowed. */
const FIRST_SCAN_DELAY_MS = 15 * 1000;

function count(n: number, one: string): string {
  return `${n} ${n === 1 ? one : `${one}s`}`;
}

/** The status bar text for a doctor summary. */
export function doctorStatusText(summary: DoctorSummary): string {
  if (summary.blocker > 0) return `Nightgauge: ${count(summary.blocker, "blocker")}`;
  if (summary.warning > 0) return `Nightgauge: ${count(summary.warning, "warning")}`;
  return "Nightgauge: ✓ healthy";
}

/** The tooltip: every severity count, and what a click does. */
export function doctorStatusTooltip(summary: DoctorSummary, failure?: string): string {
  const lines = [
    `Doctor: ${count(summary.blocker, "blocker")}, ${count(summary.warning, "warning")}, ` +
      `${summary.housekeeping} housekeeping, ${summary.info} info.`,
  ];
  if (failure) lines.push(`The last background check failed: ${failure}`);
  lines.push("Click to open the Doctor panel.");
  return lines.join("\n");
}

export interface DoctorStatusBarOptions {
  /** Whether a background scan may run now (false in a window with no workspace). */
  enabled: () => boolean;
  tickMs?: number;
  firstScanDelayMs?: number;
}

export class DoctorStatusBarItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;
  private readonly disposables: vscode.Disposable[] = [];
  private readonly timers: Array<ReturnType<typeof setTimeout>> = [];
  private interval: ReturnType<typeof setInterval> | undefined;

  constructor(
    private readonly service: DoctorService,
    private readonly options: DoctorStatusBarOptions
  ) {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 92);
    this.item.name = "Nightgauge Doctor";
    this.item.command = RUN_DOCTOR_COMMAND;
    this.disposables.push(
      service.onDidChange((result) => this.render(result.summary)),
      service.onDidFail((err) => this.renderFailure(err))
    );
    if (service.latest) this.render(service.latest.summary);

    this.timers.push(
      setTimeout(() => this.tick(), options.firstScanDelayMs ?? FIRST_SCAN_DELAY_MS)
    );
    this.interval = setInterval(() => this.tick(), options.tickMs ?? TICK_MS);
  }

  /** Run a background scan when one is due. Exposed for tests. */
  tick(): void {
    if (!this.options.enabled() || this.service.running) return;
    if (this.service.sinceLastRun() < BACKGROUND_INTERVAL_MS) return;
    this.service.run({ probeAdapters: false }).catch(() => {
      // onDidFail renders the failure.
    });
  }

  private render(summary: DoctorSummary, failure?: string): void {
    const text = doctorStatusText(summary);
    this.item.text = text;
    this.item.tooltip = doctorStatusTooltip(summary, failure);
    this.item.accessibilityInformation = {
      label: `${text}. Open the Doctor panel.`,
      role: "button",
    };
    this.item.backgroundColor =
      summary.blocker > 0
        ? new vscode.ThemeColor("statusBarItem.errorBackground")
        : summary.warning > 0
          ? new vscode.ThemeColor("statusBarItem.warningBackground")
          : undefined;
    this.item.show();
  }

  private renderFailure(err: Error): void {
    const latest = this.service.latest;
    if (latest) this.render(latest.summary, err.message);
    else this.item.hide();
  }

  dispose(): void {
    for (const t of this.timers) clearTimeout(t);
    if (this.interval) clearInterval(this.interval);
    while (this.disposables.length) this.disposables.pop()?.dispose();
    this.item.dispose();
  }
}
