/**
 * Local record of an active free trial, used to render a countdown in the UI.
 *
 * The platform's license `validate` response does not flag a license as a trial
 * and does not surface the run allowance, so we persist what we DO know at the
 * moment the trial is issued — the `startTrial` command has the full TrialResult
 * (tier, expiry, run allowance) in hand. The record is written there, read by
 * the status bar, and cleared when a paid license is activated or on sign-out.
 *
 * Backed by `context.globalState` (a vscode.Memento) so it survives the window
 * reload that applies the trial license.
 *
 * @see Issue #1138 - Commercialization: in-extension free trial
 */

import type * as vscode from "vscode";

const TRIAL_STATE_KEY = "nightgauge.trial.v1";
const DAY_MS = 24 * 60 * 60 * 1000;

export interface TrialRecord {
  /** Tier granted by the trial (today always "pro"). */
  tier: string;
  /** ISO 8601 trial expiry. */
  expiresAt: string;
  /** Client-enforced run allowance for the trial period. */
  runAllowance: number;
  /** ISO 8601 timestamp the trial was started locally. */
  startedAt: string;
}

/** Derived display state for the status bar / tooltips. */
export interface TrialStatus {
  /** A trial record exists and has not yet expired. */
  active: boolean;
  /** A trial record exists but its expiry has passed. */
  expired: boolean;
  /** Whole days remaining (ceil), 0 once expired. */
  daysRemaining: number;
  /** The underlying record. */
  record: TrialRecord;
}

/**
 * Persists + reads the active-trial record. A thin wrapper over a Memento; two
 * instances over the same `context.globalState` observe the same data, so the
 * status bar and the commands can each construct their own.
 */
export class TrialStateStore {
  constructor(private readonly memento: vscode.Memento) {}

  get(): TrialRecord | undefined {
    return this.memento.get<TrialRecord>(TRIAL_STATE_KEY);
  }

  async set(record: TrialRecord): Promise<void> {
    await this.memento.update(TRIAL_STATE_KEY, record);
  }

  async clear(): Promise<void> {
    await this.memento.update(TRIAL_STATE_KEY, undefined);
  }

  /**
   * Derived trial status, or null when no trial record exists or the stored
   * expiry is unparseable. `now` is injectable for testing.
   */
  status(now: number = Date.now()): TrialStatus | null {
    const record = this.get();
    if (!record) return null;
    const expiry = new Date(record.expiresAt).getTime();
    if (Number.isNaN(expiry)) return null;

    const msLeft = expiry - now;
    if (msLeft <= 0) {
      return { active: false, expired: true, daysRemaining: 0, record };
    }
    return {
      active: true,
      expired: false,
      daysRemaining: Math.ceil(msLeft / DAY_MS),
      record,
    };
  }
}
