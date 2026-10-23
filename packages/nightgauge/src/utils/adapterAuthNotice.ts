/**
 * adapterAuthNotice — the operator surface for a pipeline-start adapter auth
 * failure (#1168).
 *
 * ## Why this module exists at all
 *
 * When the adapter auth pre-flight refuses to launch, the run stops before any
 * stage executes, at zero token cost, and the Go scheduler correctly routes the
 * kind as retryable infra with no pause (#1169). Both of those are right. The
 * defect was that the whole thing happened in silence: `HeadlessOrchestrator`
 * logged `Adapter auth pre-flight failed` to the output channel and that was
 * the entire user-facing surface — no notification, no status entry, nothing.
 *
 * From the operator's chair: autonomous mode is started, the panel says
 * `running`, five issues dispatch, and eighty seconds later everything is
 * stopped with no explanation. The observed report was *"I restarted autonomous
 * and everything stopped."* The remedy is a single command that the pre-flight
 * **already knows** and already puts in its error string — it was simply never
 * shown to a human.
 *
 * ## WHO OWNS THE TELLING (the crux of #1168 + #1169 together)
 *
 * Exactly one layer tells the operator, and it is this one — reached from
 * `HeadlessOrchestrator`'s pre-flight gate.
 *
 * `ConcurrentPipelineManager`'s `HALT_SKIP_ENVIRONMENTAL` branch is deliberately
 * SILENT for `adapter_auth_failed`. It is tempting to give it a toast like the
 * `api_overloaded` and network-blip branches beside it, and that is the wrong
 * layer for three reasons:
 *
 *  1. It only runs for queued/concurrent dispatches. A single manual run hits
 *     the same pre-flight and deserves the same surface.
 *  2. All it has is the composed failure STRING. This layer has the structured
 *     failures — adapter name, and the SDK's per-adapter `suggestedFix` — so it
 *     can name the remedy without parsing anything back out of prose.
 *  3. Only the pre-flight sees the later SUCCESS. "Clears once the adapter
 *     authenticates" is not expressible in a failure branch; it is expressible
 *     here, via {@link clearAdapterAuthNotice}.
 *
 * If a toast is ever added to that branch too, the operator gets told twice for
 * one condition. `tests/utils/adapterAuthNotice.test.ts` pins the single owner.
 *
 * ## Why NOT an Action Center card
 *
 * A standing condition that auto-resolves is exactly what ADR-015 is for, and
 * it was the first thing tried. The extension cannot express one. It may only
 * REPORT a condition over the closed `attention.raise` allowlist, and
 * `TestNoRaiseableProducerIsStandingWithoutRetraction`
 * (`internal/ipc/attention_raise_test.go`) forbids a raiseable producer from
 * being `Standing` — there is no sweep behind that verb to reconcile against,
 * so a standing card raised through it would inherit ADR-015 §M suppression and
 * go permanently silent after the first dismissal. The alternative, a real
 * sweep producer in `internal/attention/sweep/`, is handed a repo and a
 * `forge.ForgeClient` — it has no adapter state at all, and the daemon does not
 * run the adapter pre-flight. Giving it one would mean a second, independent
 * auth probe in a second layer: two authorities for one answer.
 *
 * So the surface stays where the knowledge is. (Go's existing `auth-preflight`
 * producer is a different condition — GITHUB auth at dispatch — and is already
 * declared deferred on the extension path in `terminal_behaviors.json`.)
 *
 * ## Deduplication
 *
 * Five issues failed in the observed incident. Five identical toasts is its own
 * defect, so the outstanding set is keyed **per adapter, not per issue** and
 * lives at module scope: every `HeadlessOrchestrator` in the extension host
 * (one per concurrent slot) shares it. The check-and-set is synchronous, so a
 * burst of concurrent dispatches cannot race past it.
 *
 * ## Credential hygiene
 *
 * The probe's `reason` embeds adapter CLI output — an auth-state blob. It is
 * never surfaced. Only the adapter NAME and the SDK's static `suggestedFix`
 * remedy string reach the operator; the diagnostic detail stays in the output
 * channel, where it already was.
 *
 * @see Issue #1168 - adapter auth failure has no user-facing surface
 * @see Issue #1169 - adapter_auth_failed must not halt the queue
 */

import * as vscode from "vscode";

/**
 * One failed adapter probe, reduced to the two fields that are safe to show.
 *
 * Structurally a subset of the SDK's `AdapterAuthFailure`, minus `reason` —
 * the field that carries probe output. Taking a narrowed shape rather than the
 * SDK type is the point: the credential-bearing field is not in scope here, so
 * it cannot be surfaced by accident.
 */
export interface AdapterAuthNoticeInput {
  /** Adapter name, e.g. `claude-headless`. */
  adapter: string;
  /** The SDK's static per-adapter remedy, e.g. "Run `claude auth login`". */
  suggestedFix: string;
  /**
   * True when the probe TIMED OUT rather than coming back definitively logged
   * out. Changes the wording (auth could not be VERIFIED vs. is missing); the
   * remedy is the same either way.
   */
  timedOut: boolean;
}

/**
 * Adapters currently surfaced to the operator, with the remedy each was
 * surfaced with. Module scope on purpose — see "Deduplication" above.
 */
const outstanding = new Map<string, AdapterAuthNoticeInput>();

/**
 * The surfaces this module drives. Injectable so tests observe the decision
 * rather than the VSCode host.
 */
export interface AdapterAuthNoticeSurface {
  /**
   * Raise the operator-visible warning — the `api_overloaded` toast idiom from
   * `ConcurrentPipelineManager`, at the severity this condition earns.
   */
  warn(message: string): void;
  /**
   * Reflect the STANDING condition, called on every state change including the
   * transition back to empty.
   *
   * A toast is fire-and-forget: VSCode gives no way to retract one, so a toast
   * alone can never satisfy "the surface clears once the adapter
   * authenticates". This is the half that can — an entry that persists exactly
   * as long as the condition does.
   */
  setStanding(adapters: AdapterAuthNoticeInput[]): void;
}

/** The status entry backing {@link AdapterAuthNoticeSurface.setStanding}. */
let standingItem: vscode.StatusBarItem | undefined;

function defaultSurface(): AdapterAuthNoticeSurface {
  return {
    warn: (message) => {
      void vscode.window.showWarningMessage(message);
    },
    setStanding: (adapters) => {
      if (adapters.length === 0) {
        standingItem?.hide();
        return;
      }
      standingItem ??= vscode.window.createStatusBarItem(vscode.StatusBarAlignment?.Left ?? 1, 100);
      standingItem.text = `$(warning) Adapter auth: ${adapters.map((a) => a.adapter).join(", ")}`;
      standingItem.tooltip = formatAdapterAuthNotice(adapters);
      standingItem.show();
    },
  };
}

let surface: AdapterAuthNoticeSurface = defaultSurface();

/**
 * Install the surface. Tests pass a recorder; production uses the default.
 */
export function setAdapterAuthNoticeSurface(next: AdapterAuthNoticeSurface): void {
  surface = next;
}

/** Test seam: forget every outstanding notice AND restore the default surface. */
export function resetAdapterAuthNotices(): void {
  outstanding.clear();
  standingItem?.dispose();
  standingItem = undefined;
  surface = defaultSurface();
}

/** The adapters currently surfaced, for the status entry and for tests. */
export function outstandingAdapterAuthNotices(): AdapterAuthNoticeInput[] {
  return [...outstanding.values()];
}

/**
 * Render the operator-facing text for one or more failed adapters.
 *
 * Deliberately built from `adapter` and `suggestedFix` only. The message says
 * plainly that nothing will run until it is fixed, because that is the whole
 * difference between this and the transient toasts it sits beside: an overload
 * clears on its own, an auth lapse cannot.
 */
export function formatAdapterAuthNotice(failures: AdapterAuthNoticeInput[]): string {
  const parts = failures.map((f) => {
    const state = f.timedOut
      ? `${f.adapter} auth could not be verified (probe timed out)`
      : `${f.adapter} is not authenticated`;
    return `${state} — ${f.suggestedFix}`;
  });
  return (
    `Nightgauge: adapter auth pre-flight failed — no pipeline can start until this is fixed ` +
    `(no tokens were spent). ${parts.join(" ")}`
  );
}

/**
 * Surface an adapter auth pre-flight failure, once per adapter.
 *
 * @returns the adapters that were newly surfaced by THIS call — empty when
 * every adapter was already outstanding, which is the deduplicated case.
 */
export function reportAdapterAuthFailure(failures: AdapterAuthNoticeInput[]): string[] {
  const fresh: AdapterAuthNoticeInput[] = [];
  for (const failure of failures) {
    if (outstanding.has(failure.adapter)) continue;
    // Store the narrowed shape explicitly rather than the caller's object: a
    // caller passing the SDK's full AdapterAuthFailure would otherwise park
    // `reason` (probe output) in module state one refactor away from a surface.
    const entry: AdapterAuthNoticeInput = {
      adapter: failure.adapter,
      suggestedFix: failure.suggestedFix,
      timedOut: failure.timedOut,
    };
    outstanding.set(failure.adapter, entry);
    fresh.push(entry);
  }
  if (fresh.length === 0) return [];

  surface.warn(formatAdapterAuthNotice(fresh));
  surface.setStanding(outstandingAdapterAuthNotices());
  return fresh.map((f) => f.adapter);
}

/**
 * Auto-resolve: the adapter authenticated, so the standing condition is over.
 *
 * Called from the pre-flight's PASS path. Clearing also re-arms the notice, so
 * a later lapse is surfaced again rather than being swallowed by a stale
 * dedupe entry — the failure mode that makes "tell them once" turn into "tell
 * them once, ever".
 *
 * @returns the adapters that actually had an outstanding notice.
 */
export function clearAdapterAuthNotice(adapters: readonly string[]): string[] {
  const cleared: string[] = [];
  for (const adapter of adapters) {
    if (outstanding.delete(adapter)) cleared.push(adapter);
  }
  if (cleared.length > 0) {
    surface.setStanding(outstandingAdapterAuthNotices());
  }
  return cleared;
}
