/**
 * NotificationDispatcher — fans pipeline lifecycle calls out to a `Notifier[]`.
 *
 * Each lifecycle call is wrapped in per-notifier error isolation so a single
 * misbehaving notifier cannot prevent the others from receiving the event.
 * `subscribeToSlot` bookkeeping is symmetric — `unsubscribeFromSlot` only
 * touches notifiers that successfully recorded a subscription.
 *
 * Per-channel routing is supported via an optional `NotificationRouter`. When
 * notifiers are supplied as `{ id, notifier }` pairs, the router's `shouldDeliver`
 * is consulted before each fan-out call. Plain `Notifier` entries (no id) always
 * receive every event (backward compat / ADR-001 pair-registry pattern).
 *
 * @see Issue #3372 — initial dispatcher
 * @see Issue #3374 — per-channel routing rules + pair-registry
 */

import * as vscode from "vscode";
import type { PipelineStateService } from "../PipelineStateService";
import type { Logger } from "../../utils/logger";
import type { Notifier, PipelineEventContext } from "./types";
import { DEFAULT_ROUTER, type NotificationRouter } from "./NotificationRouter";
import type { EventKey } from "../../config/schema";

/** A notifier with an explicit routing id (for per-channel filter evaluation). */
export interface NotifierEntry {
  id: string;
  notifier: Notifier;
}

/** Accepts either plain Notifier (legacy, no routing) or NotifierEntry (with routing id). */
export type NotifierInput = NotifierEntry | Notifier;

function isNotifierEntry(n: NotifierInput): n is NotifierEntry {
  return typeof (n as NotifierEntry).id === "string" && (n as NotifierEntry).notifier !== undefined;
}

export class NotificationDispatcher implements Notifier, vscode.Disposable {
  private readonly entries: NotifierEntry[];
  private readonly router: NotificationRouter;
  private readonly subscribedSlots = new Map<number, Notifier[]>();

  constructor(
    notifiers: NotifierInput[],
    private readonly logger: Logger,
    router: NotificationRouter = DEFAULT_ROUTER
  ) {
    this.router = router;
    this.entries = notifiers.map((n, i) =>
      isNotifierEntry(n) ? n : { id: `__notifier_${i}`, notifier: n }
    );
  }

  async initialize(): Promise<void> {
    const results = await Promise.allSettled(
      this.entries.map(({ notifier }) => notifier.initialize())
    );
    for (const result of results) {
      if (result.status === "rejected") {
        this.logger.warn("Notifier initialize() rejected", { error: result.reason });
      }
    }
  }

  onPipelineStart(ctx: PipelineEventContext): void {
    const eventKey: EventKey = ctx.eventKey ?? "pipeline.start";
    for (const { id, notifier } of this.entries) {
      if (!this.router.shouldDeliver(id, eventKey)) continue;
      try {
        notifier.onPipelineStart(ctx);
      } catch (error) {
        this.logger.warn("Notifier onPipelineStart() threw", { error });
      }
    }
  }

  onPipelineUpdate(ctx: PipelineEventContext): void {
    const eventKey: EventKey = ctx.eventKey ?? "pipeline.update";
    for (const { id, notifier } of this.entries) {
      if (!this.router.shouldDeliver(id, eventKey)) continue;
      try {
        notifier.onPipelineUpdate(ctx);
      } catch (error) {
        this.logger.warn("Notifier onPipelineUpdate() threw", { error });
      }
    }
  }

  subscribeToSlot(
    issueNumber: number,
    slotStateService: PipelineStateService,
    repoSlug?: string
  ): void {
    const subscribed: Notifier[] = [];
    for (const { notifier } of this.entries) {
      try {
        notifier.subscribeToSlot(issueNumber, slotStateService, repoSlug);
        subscribed.push(notifier);
      } catch (error) {
        this.logger.warn("Notifier subscribeToSlot() threw", { error, issueNumber });
      }
    }
    this.subscribedSlots.set(issueNumber, subscribed);
  }

  unsubscribeFromSlot(issueNumber: number): void {
    const subscribed = this.subscribedSlots.get(issueNumber);
    if (!subscribed) return;
    for (const notifier of subscribed) {
      try {
        notifier.unsubscribeFromSlot(issueNumber);
      } catch (error) {
        this.logger.warn("Notifier unsubscribeFromSlot() threw", { error, issueNumber });
      }
    }
    this.subscribedSlots.delete(issueNumber);
  }

  dispose(): void {
    for (const { notifier } of this.entries) {
      try {
        notifier.dispose();
      } catch (error) {
        this.logger.warn("Notifier dispose() threw", { error });
      }
    }
    this.subscribedSlots.clear();
  }
}
