/**
 * The one place a card's forge link is decided (#1509).
 *
 * A card carries `context.url` only when its producer had a URL to hand — the
 * sweep producers do, because the forge API handed them one for the failing
 * check run or the blocked PR. Every per-issue producer in
 * `attention_wiring.go` raises with `Context{Repo, Issue}` and no URL, because
 * nothing in that code path ever fetched one. The result was a card asking for
 * a high-impact decision with no way to look at the thing being decided:
 * "Architecture approval required — #801" offered approve / leave / mute /
 * steer and not one entry that showed the issue.
 *
 * So the link is DERIVED here rather than added to each producer. This is the
 * seam every card passes through on its way to an operator — the quick pick,
 * the tree item's `.link` context value, and the tooltip all read it — which
 * makes it the only spot that covers all three producer families at once: the
 * two Go schedulers, the repo-scoped sweep, and the extension's own
 * `attention.raise` path. It also covers cards ALREADY on disk, raised before
 * this change, which a Go-side fill in `raiseAttention` could not: those
 * records are persisted with an empty `url` and are never re-raised while
 * their condition holds.
 *
 * A producer that knows better still wins — an explicit `context.url` is
 * returned untouched, which is how a non-GitHub forge and the sweep's precise
 * deep links keep their own URLs.
 */

import type { AttentionRequestView } from "../../services/IpcClientBase";

/** Forge web base for a derived link. Matches the `https://github.com/${repo}/issues/${n}`
 * literal the extension already uses for run slots, Discord and notifications. */
const FORGE_WEB_BASE = "https://github.com";

/** "owner/name" — anything else is not a repo slug we can build a path from. */
const REPO_SLUG = /^[A-Za-z0-9._-]+\/[A-Za-z0-9._-]+$/;

/**
 * The URL a card should link to, or `undefined` when the card names nothing
 * linkable (a fleet-scoped card with no repo, a repo-scoped card with neither
 * issue nor PR).
 */
export function attentionCardUrl(request: AttentionRequestView): string | undefined {
  const ctx = request.context;
  if (ctx.url) return ctx.url;
  if (!ctx.repo || !REPO_SLUG.test(ctx.repo)) return undefined;
  if (ctx.issue && ctx.issue > 0) return `${FORGE_WEB_BASE}/${ctx.repo}/issues/${ctx.issue}`;
  if (ctx.pr && ctx.pr > 0) return `${FORGE_WEB_BASE}/${ctx.repo}/pull/${ctx.pr}`;
  return undefined;
}
