/**
 * ThrottleCommandHandler — answers the platform's `throttle` command by
 * bringing this window's dispatch onto its workspace's throttle (#2337).
 *
 * The platform publishes a `throttle` straight to the agent a workspace is
 * linked to, never through its router, whenever an owner or admin sets or
 * clears the workspace's concurrency cap. Every connection that shares the
 * agent id receives it. The payload (`{action: "set", maxConcurrent,
 * resumeAt}` or `{action: "cleared"}`) does not name the workspace, and the
 * windows of one machine share the agent while each serves its own
 * workspace, so the payload is never applied as it stands: the command makes
 * the window read its own workspace's throttle (WorkspaceThrottleSync) and
 * apply that. Commands handled out of order therefore cannot undo a newer
 * cap, since every read reflects the platform's state when it was made.
 *
 * The window acknowledges the command `applied` once its read is applied,
 * and `rejected` with `apply-failed` when the read failed. The platform keeps
 * the first acknowledgement; another window's later one is refused and
 * logged, with nothing left undone, because each window reads for itself. A
 * payload that is not a valid set or clear is refused `invalid-payload`. A
 * window with no platform session follows no throttle, and leaves the command
 * to the windows that do. Delivery is at least once, and each command id is
 * answered once (CommandRedeliveryGuard).
 *
 * @see WorkspaceThrottleSync — reads the workspace's throttle
 * @see WorkspaceThrottle — the cap and its persistence
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import { CommandRedeliveryGuard } from "./CommandRedeliveryGuard";
import type { IpcClient } from "./IpcClient";
import { parseThrottleCommand } from "./WorkspaceThrottle";
import type { ThrottleRefreshResult, WorkspaceThrottleSync } from "./WorkspaceThrottleSync";
import type { Logger } from "../utils/logger";

export const THROTTLE_COMMAND_TYPE = "throttle";

const APPLY_FAILED_DETAIL = "apply-failed: the agent could not read the workspace throttle";

interface ThrottleAck {
  agentId: string;
  outcome: "applied" | "rejected";
  detail?: string;
}

export class ThrottleCommandHandler implements CommandHandler {
  private agentId: string | null = null;
  private readonly redelivery = new CommandRedeliveryGuard<ThrottleAck>();

  constructor(
    private readonly sync: Pick<WorkspaceThrottleSync, "isActive" | "refresh">,
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

  /** Bring dispatch onto the workspace's throttle and acknowledge, once per command id. */
  consume(cmd: ReceivedCommand): Promise<void> {
    if (!this.sync.isActive() && !this.redelivery.remembers(cmd.id)) {
      this.logger.info(
        "ThrottleCommandHandler: no platform session in this window — leaving the throttle to the windows that have one",
        { commandId: cmd.id }
      );
      return Promise.resolve();
    }
    return this.redelivery.consume(
      cmd.id,
      () => this.decide(cmd),
      (ack) => this.acknowledge(cmd, ack)
    );
  }

  /** Read and apply the workspace's throttle, and decide the ack. Never throws. */
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
    let result: ThrottleRefreshResult;
    try {
      result = await this.sync.refresh();
    } catch (err) {
      this.logger.error("ThrottleCommandHandler: reading the workspace throttle threw", {
        commandId: cmd.id,
        err: err instanceof Error ? err.message : String(err),
      });
      result = "failed";
    }
    if (result === "inactive") {
      // Signed out while the read was in flight: this window follows no
      // throttle now, and leaves the command to one that does.
      this.logger.info("ThrottleCommandHandler: the session ended — not acknowledging", {
        commandId: cmd.id,
      });
      return null;
    }
    if (result === "failed") {
      return agentId ? { agentId, outcome: "rejected", detail: APPLY_FAILED_DETAIL } : null;
    }
    this.logger.info("ThrottleCommandHandler: applied the workspace throttle", {
      commandId: cmd.id,
      announced: parsed.throttle,
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
      // Every window of the machine reads the throttle and acknowledges it;
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
