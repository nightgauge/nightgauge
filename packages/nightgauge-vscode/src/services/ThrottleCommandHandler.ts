/**
 * ThrottleCommandHandler — applies the platform's `throttle` command to this
 * window's dispatch and acknowledges it (#2337).
 *
 * The platform publishes a `throttle` straight to the agent a workspace is
 * linked to, never through its router, whenever an owner or admin sets or
 * clears the workspace's concurrency cap. Every connection that shares the
 * agent id receives it, and unlike a run verb it concerns all of them: each
 * window applies it to its own dispatch (WorkspaceThrottleState →
 * ConcurrentPipelineManager.setWorkspaceThrottle) and acknowledges it
 * `applied`. The platform keeps the first acknowledgement; another window's
 * later one is refused and logged, with nothing left undone.
 *
 * A payload that is not a valid set or clear is acknowledged `rejected` with
 * `invalid-payload`. The platform retires an older undelivered throttle
 * command when it queues a new one, so a replayed backlog holds at most the
 * newest; a copy older than the throttle already applied is still refused,
 * as `superseded`, rather than undoing the newer cap. Delivery is at least
 * once, and each command id is applied and acknowledged once
 * (CommandRedeliveryGuard).
 *
 * @see WorkspaceThrottle — the cap, its persistence, and the registration path
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import { CommandRedeliveryGuard } from "./CommandRedeliveryGuard";
import type { IpcClient } from "./IpcClient";
import { parseThrottleCommand, type WorkspaceThrottleState } from "./WorkspaceThrottle";
import type { Logger } from "../utils/logger";

export const THROTTLE_COMMAND_TYPE = "throttle";

const SUPERSEDED_DETAIL = "superseded: a newer throttle command is already applied";
const APPLY_FAILED_DETAIL = "apply-failed: the agent could not apply the throttle";

interface ThrottleAck {
  agentId: string;
  outcome: "applied" | "rejected";
  detail?: string;
}

export class ThrottleCommandHandler implements CommandHandler {
  private agentId: string | null = null;
  private readonly redelivery = new CommandRedeliveryGuard<ThrottleAck>();
  /** When the newest throttle command applied here was queued (ms), for ordering. */
  private appliedQueuedAt = Number.NEGATIVE_INFINITY;

  constructor(
    private readonly throttle: Pick<WorkspaceThrottleState, "apply">,
    private readonly ipcClient: Pick<IpcClient, "agentAcknowledgeCommand">,
    private readonly logger: Logger
  ) {}

  /** The agent this window's own command stream belongs to. */
  setAgentId(agentId: string): void {
    this.agentId = agentId;
  }

  handle(cmd: ReceivedCommand): void {
    if (cmd.type !== THROTTLE_COMMAND_TYPE) return;
    void this.consume(cmd);
  }

  /** Apply one throttle command and acknowledge it, once per command id. */
  consume(cmd: ReceivedCommand): Promise<void> {
    return this.redelivery.consume(
      cmd.id,
      () => this.decide(cmd),
      (ack) => this.acknowledge(cmd, ack)
    );
  }

  /** Apply the command and decide its ack. Never throws. */
  private async decide(cmd: ReceivedCommand): Promise<ThrottleAck | null> {
    // A relayed command names the agent it was addressed to (#2335).
    const agentId = cmd.agentId ?? this.agentId;
    if (!agentId || !cmd.id) {
      this.logger.warn("ThrottleCommandHandler: no agent id or command id — cannot acknowledge", {
        commandId: cmd.id,
      });
    }
    const parsed = parseThrottleCommand(cmd.payload);
    if ("invalid" in parsed) {
      this.logger.warn("ThrottleCommandHandler: invalid throttle payload", {
        commandId: cmd.id,
        reason: parsed.invalid,
      });
      return agentId
        ? { agentId, outcome: "rejected", detail: `invalid-payload: ${parsed.invalid}` }
        : null;
    }
    const queuedAt = Date.parse(cmd.createdAt);
    if (Number.isFinite(queuedAt) && queuedAt < this.appliedQueuedAt) {
      this.logger.info("ThrottleCommandHandler: a newer throttle is already applied", {
        commandId: cmd.id,
        createdAt: cmd.createdAt,
      });
      return agentId ? { agentId, outcome: "rejected", detail: SUPERSEDED_DETAIL } : null;
    }
    try {
      await this.throttle.apply(parsed.throttle);
    } catch (err) {
      this.logger.error("ThrottleCommandHandler: applying the throttle failed", {
        commandId: cmd.id,
        err: err instanceof Error ? err.message : String(err),
      });
      return agentId ? { agentId, outcome: "rejected", detail: APPLY_FAILED_DETAIL } : null;
    }
    if (Number.isFinite(queuedAt)) this.appliedQueuedAt = queuedAt;
    this.logger.info("ThrottleCommandHandler: applied", {
      commandId: cmd.id,
      throttle: parsed.throttle,
    });
    return agentId ? { agentId, outcome: "applied" } : null;
  }

  /** Send the decided ack. Resolves whether the platform accepted it. */
  private async acknowledge(cmd: ReceivedCommand, ack: ThrottleAck): Promise<boolean> {
    if (!cmd.id) return false;
    try {
      await this.ipcClient.agentAcknowledgeCommand(ack.agentId, cmd.id, ack.outcome, ack.detail);
      return true;
    } catch (err) {
      // Every window of the machine applies the throttle and acknowledges it;
      // the platform keeps the first ack and refuses the others.
      this.logger.warn(
        "ThrottleCommandHandler: ack not accepted (another window may have sent it)",
        {
          commandId: cmd.id,
          outcome: ack.outcome,
          err: err instanceof Error ? err.message : String(err),
        }
      );
      return false;
    }
  }
}
