/**
 * DoctorService — the extension's one handle on the daemon's doctor
 * (ADR-025). The Doctor panel and the status bar health item both read it,
 * so a scan either of them starts updates the other.
 *
 * Everything goes through the `doctor.*` IPC methods; nothing here spawns a
 * process. A scan is single-flight: a second `run()` while one is in flight
 * joins it, and the daemon serializes doctor calls anyway.
 *
 * The adapter probe is opt-in and sends a real (tiny) model request, so only
 * a scan the operator asked for (the panel) passes adapters. A background
 * scan leaves it out and keeps the adapter findings of the last scan that
 * probed them, so the status bar does not flicker between the two.
 */

import * as vscode from "vscode";
import type {
  DoctorApplyRemedyResult,
  DoctorFinding,
  DoctorProgressEvent,
  DoctorRecheckResult,
  DoctorRunResult,
  DoctorSummary,
} from "../../services/IpcClientBase";

/** The slice of the IPC client the doctor uses; tests pass a fake. */
export interface DoctorIpc {
  doctorRun(only?: string[], severity?: string[], adapters?: string[]): Promise<DoctorRunResult>;
  doctorApplyRemedy(
    fingerprint: string,
    remedyId: string,
    confirm?: boolean,
    dryRun?: boolean
  ): Promise<DoctorApplyRemedyResult>;
  doctorRecheck(code?: string, check?: string): Promise<DoctorRecheckResult>;
  on(event: string, handler: (data: unknown) => void): { dispose: () => void };
}

export interface DoctorRunOptions {
  /** Probe the pipeline's adapters (the panel does; the background scan does not). */
  probeAdapters: boolean;
}

/** The check that owns every adapter-health finding (NGD100 upward). */
export const ADAPTERS_CHECK = "adapters";

const SEVERITIES = ["blocker", "warning", "housekeeping", "info"] as const;

/** Count findings per severity, as the daemon's `summary` does. */
export function summarize(findings: readonly DoctorFinding[]): DoctorSummary {
  const out: DoctorSummary = { blocker: 0, warning: 0, housekeeping: 0, info: 0 };
  for (const f of findings) {
    if ((SEVERITIES as readonly string[]).includes(f.severity)) out[f.severity] += 1;
  }
  return out;
}

/** A result whose findings changed: summary and exit fields re-derived. */
export function withFindings(result: DoctorRunResult, findings: DoctorFinding[]): DoctorRunResult {
  const summary = summarize(findings);
  const exitCode = summary.blocker > 0 ? 2 : summary.warning > 0 ? 1 : 0;
  return { ...result, findings, summary, exit_code: exitCode, healthy: exitCode < 2 };
}

export class DoctorService implements vscode.Disposable {
  private latestResult: DoctorRunResult | undefined;
  private inFlight: Promise<DoctorRunResult> | undefined;
  private lastRunAt = 0;
  private readonly subscriptions: Array<{ dispose: () => void }> = [];

  private readonly changed = new vscode.EventEmitter<DoctorRunResult>();
  private readonly progressed = new vscode.EventEmitter<DoctorProgressEvent>();
  private readonly failed = new vscode.EventEmitter<Error>();

  /** Any change to the latest state: a scan, a fix or a re-check. */
  readonly onDidChange = this.changed.event;
  /** One `doctor.progress` event from the daemon, in check order. */
  readonly onProgress = this.progressed.event;
  /** A scan failed; the latest state is unchanged. */
  readonly onDidFail = this.failed.event;

  constructor(
    private readonly ipc: DoctorIpc,
    private readonly resolveAdapters: () => string[] = () => [],
    private readonly now: () => number = Date.now
  ) {
    this.subscriptions.push(
      ipc.on("doctor.progress", (data) => this.progressed.fire(data as DoctorProgressEvent))
    );
  }

  get latest(): DoctorRunResult | undefined {
    return this.latestResult;
  }

  get running(): boolean {
    return this.inFlight !== undefined;
  }

  /** Milliseconds since the last completed scan; Infinity before the first. */
  sinceLastRun(): number {
    return this.lastRunAt === 0 ? Infinity : this.now() - this.lastRunAt;
  }

  /** Scan every check. Joins a scan already in flight. */
  run(options: DoctorRunOptions): Promise<DoctorRunResult> {
    if (this.inFlight) return this.inFlight;
    const adapters = options.probeAdapters ? this.safeAdapters() : undefined;
    const scan = this.ipc
      .doctorRun(undefined, undefined, adapters && adapters.length ? adapters : undefined)
      .then((raw) => {
        const findings = raw.findings ?? [];
        let result = withFindings({ ...raw, findings }, findings);
        if (!options.probeAdapters && this.latestResult) {
          // Carry the last probed adapter findings over a scan that did not probe.
          const carried = (this.latestResult.findings ?? []).filter(
            (f) => f.check === ADAPTERS_CHECK
          );
          result = withFindings({ ...result, adapters: this.latestResult.adapters }, [
            ...findings.filter((f) => f.check !== ADAPTERS_CHECK),
            ...carried,
          ]);
        }
        this.latestResult = result;
        this.lastRunAt = this.now();
        this.changed.fire(result);
        return result;
      })
      .catch((err: unknown) => {
        const error = err instanceof Error ? err : new Error(String(err));
        this.failed.fire(error);
        throw error;
      })
      .finally(() => {
        this.inFlight = undefined;
      });
    this.inFlight = scan;
    return scan;
  }

  /** Apply (or, with dryRun, preview) one remedy of one finding. */
  async applyRemedy(
    fingerprint: string,
    remedyId: string,
    opts: { confirm?: boolean; dryRun?: boolean } = {}
  ): Promise<DoctorApplyRemedyResult> {
    const result = await this.ipc.doctorApplyRemedy(
      fingerprint,
      remedyId,
      opts.confirm || undefined,
      opts.dryRun || undefined
    );
    if (!opts.dryRun) {
      if (result.outcome === "fixed" || result.outcome === "stale") {
        this.replaceFinding(fingerprint, undefined);
      } else if (result.outcome === "still-present" && result.finding) {
        this.replaceFinding(fingerprint, result.finding);
      }
    }
    return result;
  }

  /** Re-run the check that owns `finding`; the latest state takes its findings. */
  async recheck(finding: DoctorFinding): Promise<DoctorRecheckResult> {
    const result = await this.ipc.doctorRecheck(undefined, finding.check);
    if (this.latestResult) {
      const others = (this.latestResult.findings ?? []).filter((f) => f.check !== result.check);
      this.update(withFindings(this.latestResult, [...others, ...(result.findings ?? [])]));
    }
    return result;
  }

  /** The finding with this fingerprint in the latest state. */
  find(fingerprint: string): DoctorFinding | undefined {
    return (this.latestResult?.findings ?? []).find((f) => f.fingerprint === fingerprint);
  }

  private replaceFinding(fingerprint: string, next: DoctorFinding | undefined): void {
    if (!this.latestResult) return;
    const findings: DoctorFinding[] = [];
    for (const f of this.latestResult.findings ?? []) {
      if (f.fingerprint !== fingerprint) findings.push(f);
      else if (next) findings.push(next);
    }
    this.update(withFindings(this.latestResult, findings));
  }

  private update(result: DoctorRunResult): void {
    this.latestResult = result;
    this.changed.fire(result);
  }

  private safeAdapters(): string[] {
    try {
      return this.resolveAdapters();
    } catch {
      return [];
    }
  }

  dispose(): void {
    while (this.subscriptions.length) this.subscriptions.pop()?.dispose();
    this.changed.dispose();
    this.progressed.dispose();
    this.failed.dispose();
  }
}
