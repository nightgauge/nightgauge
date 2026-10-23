/**
 * OutputWindowOverviewDom — pure DOM helpers for the Overview card runtime.
 *
 * The Overview cards in the Output webview are driven by:
 *   1. A 1 Hz tick that recomputes elapsed text from `data-started-at`
 *      (so we don't pay 1/sec postMessage overhead per slot).
 *   2. An `overview-card-update` message handler that patches stage,
 *      status, cost, tokens, phase, and elapsed-anchor in place — without
 *      re-rendering the whole panel.
 *
 * Both routines must work identically inside the WebView (where they're
 * embedded into the inline `<script>` via `Function.prototype.toString`)
 * and inside Vitest jsdom tests (where they're imported directly). Keeping
 * them in a single TS module guarantees the unit tests exercise the same
 * source that ships to the WebView (Issue #3010).
 *
 * IMPORTANT: these functions must remain pure JS once compiled — no class
 * references, no module-level imports beyond types. They are stringified
 * verbatim into the WebView script.
 */

/** Subset of the `overview-card-update` message that this DOM helper consumes. */
export interface OverviewCardUpdatePayload {
  slotIndex: number;
  status: string;
  statusLabel: string;
  stageLabel: string;
  costUsd: number;
  inputTokens: number;
  outputTokens: number;
  cacheTokens: number;
  startedAt?: number | null;
  completedAt?: number | null;
  currentPhase?: { name: string; index: number; total: number } | null;
}

// Local structural DOM types — the package's tsconfig has only the ES2022 lib,
// so we cannot reference the global `ParentNode` / `HTMLElement`. Both the
// browser DOM and the test's FakeEl satisfy this surface.
interface DomLikeElement {
  textContent: string;
  className: string;
  dataset: Record<string, string | undefined>;
  querySelector(selector: string): DomLikeElement | null;
}
interface DomLikeRoot {
  querySelector(selector: string): DomLikeElement | null;
  querySelectorAll(selector: string): ArrayLike<DomLikeElement> & {
    forEach(cb: (node: DomLikeElement) => void): void;
  };
}

/**
 * Run one tick of the elapsed-time updater across all running cards under
 * `root`. Skips cards that are already completed, and silently skips cards
 * with no `data-started-at` (initial render before stage start).
 */
export function tickOverviewCardElapsed(root: DomLikeRoot, nowMs: number): void {
  const cards = root.querySelectorAll(".overview-card-elapsed[data-started-at]");
  cards.forEach(function (node: DomLikeElement) {
    const el = node;
    if (el.dataset.completedAt) return;
    const started = parseInt(el.dataset.startedAt || "0", 10);
    if (!started) return;
    const total = Math.max(0, Math.floor((nowMs - started) / 1000));
    const s = total % 60;
    const m = Math.floor(total / 60) % 60;
    const h = Math.floor(total / 3600);
    el.textContent = h > 0 ? h + "h " + m + "m " + s + "s" : m > 0 ? m + "m " + s + "s" : s + "s";
  });
}

/**
 * Apply an `overview-card-update` message to the matching card under `root`.
 *
 * Establish-once semantics for `data-started-at` and `data-completed-at`
 * (Issue #3010 root cause): the slot's startedAt is immutable once
 * registered, so re-stamping it on every token-delta patch was resetting
 * the elapsed math to ~0 each tick — visible to the user as "frozen at
 * 2s". We set the attributes only on first arrival of a non-null value
 * and never delete them.
 *
 * Phase patch: the `.overview-card-phase` span is always present in
 * initial HTML and styled with `:empty { display: none }`, so the patch
 * only manipulates `textContent`.
 */
export function applyOverviewCardUpdate(root: DomLikeRoot, u: OverviewCardUpdatePayload): void {
  const card = root.querySelector('.overview-card[data-slot="' + u.slotIndex + '"]');
  if (!card) return;

  const statusBadge = card.querySelector(".overview-status-badge");
  if (statusBadge) {
    statusBadge.className = "overview-status-badge overview-status-" + u.status;
    statusBadge.textContent = u.statusLabel;
  }

  const stageEl = card.querySelector(".overview-card-stage");
  if (stageEl) stageEl.textContent = u.stageLabel || "—";

  const costEl = card.querySelector(".overview-card-cost");
  if (costEl) costEl.textContent = "$" + (u.costUsd || 0).toFixed(4);

  const tokensEl = card.querySelector(".overview-card-tokens");
  if (tokensEl) {
    tokensEl.textContent =
      (u.inputTokens || 0).toLocaleString() +
      " in · " +
      (u.outputTokens || 0).toLocaleString() +
      " out · " +
      (u.cacheTokens || 0).toLocaleString() +
      " cache";
  }

  const phaseEl = card.querySelector(".overview-card-phase");
  if (phaseEl) {
    if (u.currentPhase) {
      phaseEl.textContent =
        u.currentPhase.name + " · " + u.currentPhase.index + "/" + u.currentPhase.total;
    } else {
      phaseEl.textContent = "";
    }
  }

  const elapsedEl = card.querySelector(".overview-card-elapsed");
  if (elapsedEl) {
    if (u.startedAt != null && !elapsedEl.dataset.startedAt) {
      elapsedEl.dataset.startedAt = String(u.startedAt);
    }
    if (u.completedAt != null) {
      elapsedEl.dataset.completedAt = String(u.completedAt);
      const startMs = parseInt(elapsedEl.dataset.startedAt || String(u.completedAt), 10);
      const total = Math.max(0, Math.floor((u.completedAt - startMs) / 1000));
      const s = total % 60;
      const m = Math.floor(total / 60) % 60;
      const h = Math.floor(total / 3600);
      elapsedEl.textContent =
        h > 0 ? h + "h " + m + "m " + s + "s" : m > 0 ? m + "m " + s + "s" : s + "s";
    }
    // Note: never delete data-completed-at on a patch lacking it — a
    // running slot's update simply doesn't advance the completion stamp.
  }
}
