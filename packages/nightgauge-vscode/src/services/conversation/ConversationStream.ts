/**
 * ConversationStream — one conversation's frame stream
 * (`GET /v1/conversations/{id}/stream`) for the chat panel (#1571).
 *
 * It reuses the shared SSE transport (backoff, token refresh, Last-Event-ID
 * resume). The cursor lives in memory for the life of one open conversation:
 * the panel seeds its transcript from the thread read on every open, and the
 * stream's first connect replays the log, which the transcript applies
 * idempotently. A reconnect resumes after the last frame received.
 */

import type * as vscode from "vscode";
import { PlatformSseClient, type SseStreamStatus } from "../PlatformSseClient";
import type { Logger } from "../../utils/logger";
import { parseFrame, type ConversationFrame } from "./conversationWire";
import { isPathId } from "./ConversationClient";

export interface ConversationStreamHandlers {
  onFrame(frame: ConversationFrame): void;
  onStatus(status: SseStreamStatus): void;
  /** The service answered 404: the conversation is gone or no longer readable. */
  onNotFound(): void;
}

export interface ConversationStreamHandle {
  close(): void;
}

export type OpenConversationStream = (
  conversationId: string,
  handlers: ConversationStreamHandlers
) => Promise<ConversationStreamHandle | null>;

/** An in-memory memento: the cursor is per open, never persisted. */
class MemoryCursor implements Pick<vscode.Memento, "get" | "update"> {
  private readonly values = new Map<string, unknown>();
  get<T>(key: string, defaultValue?: T): T | undefined {
    return (this.values.has(key) ? this.values.get(key) : defaultValue) as T | undefined;
  }
  update(key: string, value: unknown): Thenable<void> {
    this.values.set(key, value);
    return Promise.resolve();
  }
}

export function createConversationStreamOpener(deps: {
  getPlatformUrl: () => string;
  getAccessToken: () => Promise<string | null>;
  forceRefresh: () => Promise<string | null>;
  logger: Logger;
}): OpenConversationStream {
  return async (conversationId, handlers) => {
    if (!isPathId(conversationId)) return null;
    const token = await deps.getAccessToken();
    if (!token) return null;
    const client = new PlatformSseClient({
      context: { globalState: new MemoryCursor() },
      logger: deps.logger,
      lastEventIdKey: "conversation.lastEventId",
      onEvent: (data) => {
        const frame = parseFrame(data);
        if (frame) handlers.onFrame(frame);
      },
      onStatusChanged: (status) => handlers.onStatus(status),
      onAuthRequired: deps.forceRefresh,
      onNotFound: () => handlers.onNotFound(),
    });
    client.connect(`${deps.getPlatformUrl()}/v1/conversations/${conversationId}/stream`, token);
    return { close: () => client.dispose() };
  };
}
