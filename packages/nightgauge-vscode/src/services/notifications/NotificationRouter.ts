/**
 * NotificationRouter — precompiled per-channel routing filter for multi-notifier dispatch.
 *
 * Filter sets (allowlists, suppresslists) are compiled once at construction time
 * using JS `Set` for O(1) per-call lookup. No allocation occurs per lifecycle call.
 *
 * Default behavior when no rules are configured: every notifier receives every event.
 * Unknown notifier ids (not in the routing table) also receive every event (backward compat).
 *
 * @see Issue #3374
 * @see ADR-001 — pair-registry pattern (Notifier interface unchanged)
 * @see ADR-002 — precompiled filter sets
 */

import type { EventKey, NotifierRoutingRule } from "../../config/schema";

export class NotificationRouter {
  // notifier id → compiled allowlist (null means all events allowed)
  private readonly allowlists = new Map<string, Set<EventKey> | null>();
  // notifier id → compiled suppresslist
  private readonly suppresslists = new Map<string, Set<EventKey>>();

  constructor(rules: NotifierRoutingRule[]) {
    for (const rule of rules) {
      this.allowlists.set(
        rule.id,
        rule.events && rule.events.length > 0 ? new Set(rule.events) : null
      );
      this.suppresslists.set(
        rule.id,
        rule.suppress && rule.suppress.length > 0 ? new Set(rule.suppress) : new Set()
      );
    }
  }

  /**
   * Returns true when notifier `id` should receive `eventKey`.
   *
   * Resolution order:
   * 1. No rules configured → deliver (default behavior)
   * 2. Notifier id not in routing table → deliver (backward compat)
   * 3. Event in suppress list → block
   * 4. Allowlist is null (empty or absent events array) → deliver
   * 5. Event in allowlist → deliver; otherwise block
   */
  shouldDeliver(id: string, eventKey: EventKey): boolean {
    if (this.allowlists.size === 0) return true;
    if (!this.allowlists.has(id)) return true;

    if (this.suppresslists.get(id)?.has(eventKey)) return false;

    // get() returns Set<EventKey> | null (stored sentinel for "all events") | undefined.
    // has() guard above ensures the key exists, so undefined is not possible here.
    const allowlist = this.allowlists.get(id) as Set<EventKey> | null;
    if (allowlist === null) return true;
    return allowlist.has(eventKey);
  }
}

/** Sentinel router that delivers every event to every notifier. Used when no notifiers: block is configured. */
export const DEFAULT_ROUTER = new NotificationRouter([]);
