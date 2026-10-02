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
 * TriggerCommandHandler, and the run verbs by RunVerbCommandHandler. A type
 * neither handles is acknowledged `rejected` as unsupported, so it ends with
 * an answer instead of expiring unacknowledged.
 */

import {
  parseCommandFrame,
  type CommandHandler,
  type ReceivedCommand,
} from "./AgentCommandStreamService";
import type { IpcClient } from "./IpcClient";
import { isRunVerb, RUN_VERB_COMMAND_TYPES } from "./RunVerbCommandHandler";
import type { Logger } from "../utils/logger";

/**
 * Every command type the platform's command router delivers to an agent: the
 * trigger dispatcher's `trigger` and the run verbs PipelineCommandsService
 * queues. (`attention_resolve` and `throttle` are published to a named agent
 * directly, never through the router; the queue_* types are never created.)
 */
export const ROUTER_DELIVERED_COMMAND_TYPES = ["trigger", ...RUN_VERB_COMMAND_TYPES] as const;

/** The IPC event the daemon relays a command on (#2335). */
export const AGENT_COMMAND_RELAY_EVENT = "agent.command";

type AgentScopedHandler = Pick<CommandHandler, "handle"> & { setAgentId(agentId: string): void };

export class AgentCommandDispatcher implements CommandHandler {
  private agentId: string | null = null;

  constructor(
    private readonly trigger: AgentScopedHandler,
    private readonly verbs: AgentScopedHandler,
    private readonly ipcClient: Pick<IpcClient, "agentAcknowledgeCommand">,
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

  private async refuseUnsupported(cmd: ReceivedCommand): Promise<void> {
    const agentId = cmd.agentId ?? this.agentId;
    this.logger.warn(
      "AgentCommandDispatcher: unsupported command type — acknowledging as rejected",
      {
        type: cmd.type,
        commandId: cmd.id,
      }
    );
    if (!agentId || !cmd.id) return;
    try {
      await this.ipcClient.agentAcknowledgeCommand(
        agentId,
        cmd.id,
        "rejected",
        `unsupported-command: this agent does not handle ${cmd.type ? `"${cmd.type.slice(0, 64)}"` : "untyped"} commands`
      );
    } catch (err) {
      this.logger.error("AgentCommandDispatcher: ack failed", {
        type: cmd.type,
        commandId: cmd.id,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}
