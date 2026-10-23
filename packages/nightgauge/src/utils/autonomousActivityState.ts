/**
 * AutonomousActivityState — in-process signal for "is the autonomous dispatch
 * loop actively running right now?".
 *
 * Issue #360. The extension must NOT poll GitHub in the background when
 * autonomous mode is off. Tree providers read this singleton to gate their
 * timer-driven / high-frequency reactive board fetches: demand-driven fetching
 * (activation, view expand, manual refresh, low-frequency SSE status events)
 * stays on regardless; background polling only runs while autonomous is active.
 *
 * The value is fed from exactly one place — `setAutonomousContextKeys()` in
 * autonomousContextKeys.ts — which every autonomous status transition already
 * routes through (statusChanged event, status probes, pause/resume/stop). That
 * keeps the gate in sync with the Go scheduler without any polling of its own.
 *
 * "Active" means the dispatch loop is genuinely running candidates
 * (status === "running"). paused / safety_tripped / stopped / terminal states
 * are all treated as inactive: no background traffic is warranted because no
 * autonomous work is being served.
 *
 * Deliberately self-contained: it uses its own tiny listener set rather than a
 * `vscode.EventEmitter`, so it constructs safely in every unit-test environment
 * (including the many that mock the `vscode` module without `EventEmitter`) —
 * `setAutonomousContextKeys()` is called from a broad set of tests and must
 * never throw just to update the gate.
 */

/** Minimal disposable shape (structurally a vscode.Disposable). */
interface Disposable {
  dispose(): void;
}

/** Statuses in which the dispatch loop is actively running candidates. */
const ACTIVE_STATUSES: ReadonlySet<string> = new Set<string>(["running"]);

export class AutonomousActivityState {
  private static _instance: AutonomousActivityState | null = null;

  private readonly listeners = new Set<(active: boolean) => void>();
  private _active = false;

  static get instance(): AutonomousActivityState {
    if (!AutonomousActivityState._instance) {
      AutonomousActivityState._instance = new AutonomousActivityState();
    }
    return AutonomousActivityState._instance;
  }

  /** Test-only — drop the singleton so each test starts from a clean state. */
  static resetForTests(): void {
    AutonomousActivityState._instance = null;
  }

  /**
   * Subscribe to active-state flips. Fires with the new active-state (boolean).
   * Returns a disposable to unsubscribe.
   */
  onDidChange(listener: (active: boolean) => void): Disposable {
    this.listeners.add(listener);
    return { dispose: () => this.listeners.delete(listener) };
  }

  /** True iff the autonomous dispatch loop is actively running candidates. */
  isActive(): boolean {
    return this._active;
  }

  /**
   * Update from an autonomous lifecycle status. Fires onDidChange only when the
   * derived active-state actually flips, so subscribers don't churn on
   * running→running or paused→stopped transitions that don't change gating.
   */
  setStatus(status: string): void {
    const next = ACTIVE_STATUSES.has(status);
    if (next === this._active) return;
    this._active = next;
    for (const listener of [...this.listeners]) {
      listener(next);
    }
  }

  dispose(): void {
    this.listeners.clear();
  }
}
