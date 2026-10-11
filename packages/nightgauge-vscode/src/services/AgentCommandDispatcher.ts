/**
 * AgentCommandDispatcher — the one CommandHandler every platform agent
 * command reaches in the extension, whichever agent it was addressed to.
 *
 * Two sources deliver here:
 *   - this window's AgentCommandStreamService, for commands addressed to the
 *     extension's own agent;
 *   - the daemon's relay (IPC event `agent.command`, #2335), for commands the
 *     platform addressed to the daemon's agent. The daemon declares the
 *     workspace's repos too, so the platform's router may place a trigger or
 *     a verb on either agent; the extension runs the pipelines, so it carries
 *     out both, and acknowledges under the agent id the platform addressed.
 *
 * Every command type the platform's router delivers to an agent is consumed
 * and acknowledged by exactly one handler (#2334): `trigger` by
 * TriggerCommandHandler, and the run verbs by RunVerbCommandHandler. The
 * workspace `throttle`, which the platform publishes to the workspace's agent
 * directly, goes to ThrottleCommandHandler (#2337). A `queue_add` (#2440) is
 * handed to the daemon, which adds the issue to its queue and acknowledges
 * it. A type none of them handles is acknowledged `rejected` as unsupported,
 * once however often it is delivered, so it ends with an answer instead of
 * expiring unacknowledged.
 */

import {
  parseCommandFrame,
  type CommandHandler,
  type ReceivedCommand,
} from "./AgentCommandStreamService";
import { CommandRedeliveryGuard } from "./CommandRedeliveryGuard";
import type { IpcClient } from "./IpcClient";
import { isRunVerb, RUN_VERB_COMMAND_TYPES } from "./RunVerbCommandHandler";
import { THROTTLE_COMMAND_TYPE } from "./ThrottleCommandHandler";
import type { Logger } from "../utils/logger";

/**
 * Every command type the platform's command router delivers to an agent that
 * this window acknowledges itself: a `trigger`, and the run verbs a client
 * issues against an existing run. The router also delivers `queue_add`
 * (#2440), which the window hands to the daemon (QUEUE_ADD_COMMAND_TYPE).
 * (`attention_resolve` and `throttle` are published to a named agent
 * directly, never through the router; the daemon applies the first and this
 * dispatcher the second.)
 */
export const ROUTER_DELIVERED_COMMAND_TYPES = ["trigger", ...RUN_VERB_COMMAND_TYPES] as const;

/**
 * A queue item added in the cloud (#2440). The platform routes it to any
 * agent covering the repository, this window's included, but the issue goes
 * to the daemon's queue: the window hands its copy to the daemon
 * (`agent.queueAdd`), which handles it exactly as one on its own agent's
 * stream and acknowledges it under this window's agent id.
 */
export const QUEUE_ADD_COMMAND_TYPE = "queue_add";

/**
 * The IPC event the daemon relays a command on (#2335). Its data is
 * `{agentId, frame}`, the Go side's ipc.AgentCommandEvent; the shape is pinned
 * on both sides by internal/ipc/testdata/agent-command-event.json.
 */
export const AGENT_COMMAND_RELAY_EVENT = "agent.command";

/**
 * Route the commands the daemon relays from its own agent to the dispatcher
 * (#2335). Returns the subscription, for the extension to dispose.
 */
export function subscribeToDaemonRelay(
  ipcClient: Pick<IpcClient, "on">,
  dispatcher: Pick<AgentCommandDispatcher, "handleRelayed">
): { dispose(): void } {
  return ipcClient.on(AGENT_COMMAND_RELAY_EVENT, (event) => dispatcher.handleRelayed(event));
}

/**
 * The conversation turn command types (#1569). The Go daemon handles both on
 * its own agent command stream and never relays them; this dispatcher leaves
 * one that reaches it unacknowledged, because whichever acknowledgement
 * arrives first decides a turn.
 */
export const CONVERSATION_TURN_COMMAND_TYPES: readonly string[] = [
  "conversation_turn",
  "conversation_turn_cancel",
];

type AgentScopedHandler = Pick<CommandHandler, "handle"> & { setAgentId(agentId: string): void };

export class AgentCommandDispatcher implements CommandHandler {
  private agentId: string | null = null;
  private readonly refusals = new CommandRedeliveryGuard<{ agentId: string; detail: string }>();

  constructor(
    private readonly trigger: AgentScopedHandler,
    private readonly verbs: AgentScopedHandler,
    private readonly throttle: AgentScopedHandler,
    private readonly ipcClient: Pick<IpcClient, "agentAcknowledgeCommand" | "agentQueueAdd">,
    private readonly logger: Logger
  ) {}

  /**
   * The agent of this window's own command stream. Every handler that acks
   * needs it before the first command arrives; AgentCommandStreamService.start
   * calls this before it connects.
   */
  setAgentId(agentId: string): void {
    this.agentId = agentId;
    this.trigger.setAgentId(agentId);
    this.verbs.setAgentId(agentId);
    this.throttle.setAgentId(agentId);
  }

  handle(cmd: ReceivedCommand): void {
    if (cmd.type === "trigger") {
      this.trigger.handle(cmd);
      return;
    }
    if (isRunVerb(cmd.type)) {
      this.verbs.handle(cmd);
      return;
    }
    if (cmd.type === THROTTLE_COMMAND_TYPE) {
      this.throttle.handle(cmd);
      return;
    }
    if (cmd.type === QUEUE_ADD_COMMAND_TYPE) {
      void this.handToDaemon(cmd);
      return;
    }
    if (CONVERSATION_TURN_COMMAND_TYPES.includes(cmd.type)) {
      // Never acknowledged here, not even to refuse it (#1569): the first
      // acknowledgement decides a turn, and the Go daemon hosts turns.
      this.logger.warn("AgentCommandDispatcher: conversation turn command left to the daemon", {
        type: cmd.type,
        commandId: cmd.id,
      });
      return;
    }
    void this.refuseUnsupported(cmd);
  }

  /**
   * Handle a command the daemon relayed from its own agent: `{agentId, frame}`
   * where frame is the command exactly as the platform sent it.
   */
  handleRelayed(event: unknown): void {
    const { agentId, frame } = (event ?? {}) as { agentId?: unknown; frame?: unknown };
    let cmd: ReceivedCommand | null;
    try {
      cmd = parseCommandFrame(frame);
    } catch {
      cmd = null;
    }
    if (typeof agentId !== "string" || agentId === "" || !cmd) {
      this.logger.warn("AgentCommandDispatcher: malformed relayed command, skipping");
      return;
    }
    this.handle({ ...cmd, agentId });
  }

  /**
   * Hand a `queue_add` to the daemon (#2440). It decides, claims and
   * acknowledges the command itself, so a copy delivered again is safe to
   * hand over again. When the daemon cannot take it (no platform configured
   * there) the command is left unacknowledged: another connection of this
   * agent may still take it, and otherwise it expires on the platform.
   */
  private async handToDaemon(cmd: ReceivedCommand): Promise<void> {
    const agentId = cmd.agentId ?? this.agentId;
    if (!agentId || !cmd.id) {
      this.logger.warn("AgentCommandDispatcher: queue_add with no agent or command id, skipping", {
        commandId: cmd.id,
      });
      return;
    }
    try {
      await this.ipcClient.agentQueueAdd(agentId, cmd.id, cmd.payload);
    } catch (err) {
      this.logger.warn("AgentCommandDispatcher: the daemon did not take a queue_add", {
        commandId: cmd.id,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private refuseUnsupported(cmd: ReceivedCommand): Promise<void> {
    this.logger.warn(
      "AgentCommandDispatcher: unsupported command type — acknowledging as rejected",
      {
        type: cmd.type,
        commandId: cmd.id,
      }
    );
    return this.refusals.consume(
      cmd.id,
      async () => {
        const agentId = cmd.agentId ?? this.agentId;
        if (!agentId || !cmd.id) return null;
        const detail = `unsupported-command: this agent does not handle ${cmd.type ? `"${cmd.type.slice(0, 64)}"` : "untyped"} commands`;
        return { agentId, detail };
      },
      async ({ agentId, detail }) => {
        try {
          await this.ipcClient.agentAcknowledgeCommand(agentId, cmd.id, "rejected", detail);
          return true;
        } catch (err) {
          this.logger.error("AgentCommandDispatcher: ack failed", {
            type: cmd.type,
            commandId: cmd.id,
            err: err instanceof Error ? err.message : String(err),
          });
          return false;
        }
      }
    );
  }
}
