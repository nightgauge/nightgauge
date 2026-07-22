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
  subscribeToSlot(
    issueNumber: number,
    slotStateService: PipelineStateService,
    repoSlug?: string
  ): void;
  unsubscribeFromSlot(issueNumber: number): void;
  dispose(): void;
}
