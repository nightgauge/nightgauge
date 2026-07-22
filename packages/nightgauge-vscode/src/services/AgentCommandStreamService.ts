/**
 * AgentCommandStreamService — fetch-based SSE client for GET /v1/agents/{id}/commands.
 *
 * Opens an SSE connection after agent registration and dispatches `event: command`
 * events to a registered CommandHandler. Persists Last-Event-ID in globalState for
 * at-least-once delivery across extension reloads.
 *
 * @see Issue #3550 — VSCode subscribe to GET /v1/agents/{id}/commands
 * @see AgentHeartbeatService — sibling service using same agentId source
 */

import * as vscode from "vscode";
import type { ITokenStorage } from "../platform/TokenStorage";
import type { Logger } from "../utils/logger";

const LAST_EVENT_ID_KEY = "nightgauge.agentCommands.lastEventId";
const BACKOFF_DELAYS_MS = [1_000, 2_000, 4_000, 8_000, 16_000, 30_000] as const;
const JITTER_RATIO = 0.2;

export interface ReceivedCommand {
  id: string;
  type: string;
  payload: unknown;
  createdAt: string;
}

export interface CommandHandler {
  handle(cmd: ReceivedCommand): void;
  /**
   * Optional: receive the agentId once the stream starts. Handlers that ack
   * commands back to the platform (e.g. TriggerCommandHandler →
   * POST /v1/agents/{agentId}/commands/{id}/ack) need it before they can run.
   */
  setAgentId?(agentId: string): void;
}

export class AgentCommandStreamService implements vscode.Disposable {
  private abortController: AbortController | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private agentId: string | null = null;
  private reconnectAttempt = 0;

  constructor(
    private readonly getPlatformUrl: () => string,
    private readonly tokenStorage: ITokenStorage,
    private readonly context: vscode.ExtensionContext,
    private readonly logger: Logger,
    private readonly onCommand: CommandHandler
  ) {}

  /** Call once agentId is available from registration. No-op if already started or agentId is empty. */
  start(agentId: string): void {
    if (!agentId || this.agentId !== null) return;
    this.agentId = agentId;
    // Provide the agentId to the handler(s) before opening the stream so an
    // incoming trigger can be acked immediately (#3551). Without this the
    // command arrives but TriggerCommandHandler drops it ("agentId not set").
    this.onCommand.setAgentId?.(agentId);
    void this.connect(agentId);
  }

  dispose(): void {
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.abortController?.abort();
    this.abortController = null;
    this.agentId = null;
  }

  private async connect(agentId: string): Promise<void> {
    if (this.abortController) return;

    const token = await this.tokenStorage.retrieve("accessToken");
    if (!token) {
      this.logger.warn("AgentCommandStreamService: no access token, skipping connect");
      return;
    }

    const lastEventId = this.context.globalState.get<string>(LAST_EVENT_ID_KEY) ?? undefined;
    this.abortController = new AbortController();
    const { signal } = this.abortController;

    this.logger.info("AgentCommandStreamService connecting", { agentId, lastEventId });

    void this.runStream(agentId, token, lastEventId, signal)
      .then(() => {
        if (signal.aborted) return;
        this.logger.warn("AgentCommandStreamService stream closed by server");
        this.scheduleReconnect(agentId);
      })
      .catch((err) => {
        if (signal.aborted) return;
        this.logger.warn("AgentCommandStreamService stream error", {
          error: err instanceof Error ? err.message : String(err),
        });
        this.scheduleReconnect(agentId);
      });
  }

  private async runStream(
    agentId: string,
    token: string,
    lastEventId: string | undefined,
    signal: AbortSignal
  ): Promise<void> {
    const url = `${this.getPlatformUrl()}/v1/agents/${agentId}/commands`;
    const headers: Record<string, string> = {
      Accept: "text/event-stream",
      Authorization: `Bearer ${token}`,
      "Cache-Control": "no-cache",
    };
    if (lastEventId) {
      headers["Last-Event-ID"] = lastEventId;
    }

    const response = await fetch(url, { headers, signal });

    if (!response.ok || !response.body) {
      throw new Error(`SSE connect failed: ${response.status} ${response.statusText}`);
    }

    this.reconnectAttempt = 0;
    this.logger.info("AgentCommandStreamService stream connected", { agentId });

    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";

    try {
      while (!signal.aborted) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split("\n");
        buffer = lines.pop() ?? "";

        let eventId: string | null = null;
        let eventType: string | null = null;
        let eventData: string | null = null;

        for (const line of lines) {
          if (line.startsWith("id:")) {
            eventId = line.slice(3).trim();
          } else if (line.startsWith("event:")) {
            eventType = line.slice(6).trim();
          } else if (line.startsWith("data:")) {
            eventData = line.slice(5).trim();
          } else if (line === "" && eventData !== null) {
            if (eventId) {
              await this.context.globalState.update(LAST_EVENT_ID_KEY, eventId);
            }
            if (eventType === "command" || eventType === null) {
              this.handleCommandEvent(eventData);
            }
            eventId = null;
            eventType = null;
            eventData = null;
          }
        }
      }
    } finally {
      reader.releaseLock();
    }
  }

  private handleCommandEvent(data: string): void {
    if (data.startsWith(":")) return; // keepalive comment

    let cmd: ReceivedCommand;
    try {
      // The platform publishes the command id under `commandId` (see the
      // platform's pipeline-command-router-service), but ReceivedCommand and
      // its handlers read `id`. Normalize so the ack — which threads `id`
      // through to agent.acknowledgeCommand — doesn't send an empty commandId
      // and fail with "commandId is required", leaving the run unstarted (#3551).
      const raw = JSON.parse(data) as ReceivedCommand & { commandId?: string };
      cmd = { ...raw, id: raw.id ?? raw.commandId ?? "" };
    } catch {
      this.logger.warn("AgentCommandStreamService: malformed command JSON, skipping");
      return;
    }

    try {
      this.onCommand.handle(cmd);
    } catch (err) {
      this.logger.warn("AgentCommandStreamService: CommandHandler threw", {
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private nextBackoffDelayMs(): number {
    const idx = Math.min(this.reconnectAttempt, BACKOFF_DELAYS_MS.length - 1);
    const base = BACKOFF_DELAYS_MS[idx];
    const jitter = base * JITTER_RATIO * (Math.random() * 2 - 1);
    return Math.max(0, Math.round(base + jitter));
  }

  private scheduleReconnect(agentId: string): void {
    this.abortController = null;
    const delay = this.nextBackoffDelayMs();
    this.reconnectAttempt += 1;
    this.logger.warn(
      `AgentCommandStreamService reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`
    );
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.connect(agentId);
    }, delay);
  }
}
