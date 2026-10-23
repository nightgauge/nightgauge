/**
 * Notifier — pluggable contract for chat-notifier providers.
 *
 * Lifted from `DiscordService` so multiple providers (Discord, Mattermost,
 * Slack, Teams) can be registered and fanned out via `NotificationDispatcher`.
 *
 * @see Issue #3372
 * @see NotificationDispatcher — fan-out implementation over `Notifier[]`
 */

import type { PipelineStateService } from "../PipelineStateService";
import type { EventKey } from "../../config/schema";

/**
 * Minimal payload for the lifecycle entry-points (`onPipelineStart` /
 * `onPipelineUpdate`). Kept additive so Wave 2 dispatchers can extend it
 * without breaking existing notifiers.
 *
 * `eventKey` is optional for backward compatibility — existing callers that
 * pass ctx without this field will have all events delivered (default behavior).
 *
 * @see Issue #3374 — per-channel routing rules
 */
export interface PipelineEventContext {
  issueNumber: number;
  /**
   * The issue's repository, `owner/name`, when known. A notifier keys a run by
   * repository and number (`slotKey`): two repositories' issues with one
   * number are two runs (#2408).
   */
  repo?: string;
  stage?: string;
  state?: unknown;
  /** Routing key for per-channel filter evaluation. Absent = deliver to all. */
  eventKey?: EventKey;
}

export type { EventKey };

/**
 * Lifecycle surface consumed by `bootstrap/services.ts`. Implementations are
 * responsible for their own event sourcing (e.g., subscribing internally to
 * `PipelineStateService` events) until Wave 2 lifts that into the dispatcher.
 */
export interface Notifier {
  initialize(): Promise<void>;
  onPipelineStart(ctx: PipelineEventContext): void;
  onPipelineUpdate(ctx: PipelineEventContext): void;
  /**
   * Terminal flush (#1127): the run is terminal AND its final metadata is
   * written, so render the card once more from the state carried in `ctx`.
   *
   * This is deliberately not `onPipelineUpdate` with a terminal state. An
   * update is coalesced behind a debounce and may already have fired against
   * an earlier state; this call must render, unconditionally, from the state
   * that is final. It is idempotent — a notifier that has already flushed a
   * run ignores a second call.
   */
  onPipelineFinal(ctx: PipelineEventContext): void;
  /**
   * Follow one concurrent slot's state service. The slot is named by
   * repository and number: a notifier keys the run's subscription, card and
   * thread by `slotKey(repoSlug, issueNumber)`, so another repository's issue
   * with the same number running at once is a separate run (#2408).
   */
  subscribeToSlot(
    issueNumber: number,
    slotStateService: PipelineStateService,
    repoSlug?: string
  ): void;
  unsubscribeFromSlot(issueNumber: number, repoSlug?: string): void;
  dispose(): void;
}
