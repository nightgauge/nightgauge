/**
 * The one USD formatter for the whole extension (#333 decision E).
 *
 * Precision is tiered by magnitude so a sub-cent cost is never rendered as
 * free and a real dollar figure still reads as currency: 4 decimals under a
 * cent, 3 under a dollar, 2 from a dollar up. This used to exist twice — a
 * flat 3-decimal version in `services/notifications/transport.ts` and this
 * tiered one in `utils/tokenParser.ts` — so the same run rendered `$1.518` in
 * a notifier embed and `$1.52` in the tree view.
 *
 * It lives in `utils/` rather than in the notifier transport because both a
 * util (`tokenParser`) and a service (`notifications/transport`) need it, and
 * `utils/ → services/` is the one direction the transport module documents as
 * avoided. `utils → utils` and `services → utils` are both clean.
 */
export function formatCost(usd: number): string {
  if (usd < 0.01) return `$${usd.toFixed(4)}`;
  if (usd < 1) return `$${usd.toFixed(3)}`;
  return `$${usd.toFixed(2)}`;
}
