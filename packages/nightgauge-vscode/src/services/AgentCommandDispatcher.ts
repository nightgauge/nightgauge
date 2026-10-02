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
 * directly, goes to ThrottleCommandHandler (#2337). A type none of them
 * handles is acknowledged `rejected` as unsupported, once however often it is
 * delivered, so it ends with an answer instead of expiring unacknowledged.
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
 * Every command type the platform's command router delivers to an agent: a
 * `trigger`, and the run verbs a client issues against an existing run.
 * (`attention_resolve` and `throttle` are published to a named agent
 * directly, never through the router; the daemon applies the first and this
 * dispatcher the second. The queue_* types are never created.)
 */
export const ROUTER_DELIVERED_COMMAND_TYPES = ["trigger", ...RUN_VERB_COMMAND_TYPES] as const;

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

type AgentScopedHandler = Pick<CommandHandler, "handle"> & { setAgentId(agentId: string): void };

export class AgentCommandDispatcher implements CommandHandler {
  private agentId: string | null = null;
  private readonly refusals = new CommandRedeliveryGuard<{ agentId: string; detail: string }>();

  constructor(
    private readonly trigger: AgentScopedHandler,
    private readonly verbs: AgentScopedHandler,
    private readonly throttle: AgentScopedHandler,
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
