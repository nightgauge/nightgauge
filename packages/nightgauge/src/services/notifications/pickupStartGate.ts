/**
 * Holds a run's `issue-pickup` start until the run proves it is going
 * somewhere (#2914).
 *
 * A run that defers at issue-pickup (an unresolved blocked finding, an open
 * dependency) ends with `outcome_type: "deferred"` before any later stage
 * starts. Posting the chat message on the pickup start therefore left one
 * fresh message per attempt — 82 for a single blocked issue. The notifiers
 * instead `hold` the start, `release` it on the first later stage or on a
 * non-deferred finalization (so a pickup FAILURE still notifies), and `drop`
 * it when the run finalizes as deferred.
 */

/** Outcome a run that defers at pickup finalizes with. */
export const DEFERRED_OUTCOME = "deferred";

export class PickupStartGate<T> {
  private readonly held = new Map<string, T>();

  /** Record a pickup start instead of posting it. Replaces any earlier hold. */
  hold(key: string, start: T): void {
    this.held.set(key, start);
  }

  /** Whether a start is held for `key`. */
  has(key: string): boolean {
    return this.held.has(key);
  }

  /** Return and clear the held start, or `undefined` when none is held. */
  release(key: string): T | undefined {
    const start = this.held.get(key);
    this.held.delete(key);
    return start;
  }

  /** Discard the held start without posting it. */
  drop(key: string): void {
    this.held.delete(key);
  }

  /**
   * Settle a held start at finalization: drop it for a deferred outcome and
   * return `undefined`; otherwise release it. A missing outcome fails open
   * (releases), so a real failure is never hidden.
   */
  settle(key: string, outcomeType: string | undefined): T | undefined {
    if (outcomeType === DEFERRED_OUTCOME) {
      this.drop(key);
      return undefined;
    }
    return this.release(key);
  }

  clear(): void {
    this.held.clear();
  }
}
