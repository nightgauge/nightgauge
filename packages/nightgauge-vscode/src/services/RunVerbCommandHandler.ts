/**
 * RunVerbCommandHandler — carries out the platform verbs that act on an
 * existing run and acknowledges every one it consumes exactly once (#2334).
 *
 * The verbs are `cancel`, `approve`, `reject`, `pause` and `resume`. The
 * platform queues one when a client (the phone app, the dashboard) acts on a
 * run, routes it to the agent covering the run's repo, and records it
 * `acked` only when that agent acknowledges it
 * (POST /v1/agents/{agentId}/commands/{id}/ack). Before #2334 nothing here
 * acknowledged a verb, so every one sat `routing` until its TTL and expired,
 * whether or not it had been carried out, and `pause`/`resume` were not
 * handled at all.
 *
 * Every consumed verb is acknowledged once, and the ack's outcome tells an
 * applied verb from a no-op:
 *   - `applied`: the verb took effect on a local run;
 *   - `rejected` with a fixed reason: it found nothing to act on (no local run
 *     carries the runId, no gate is waiting, the run is already paused or not
 *     paused) or its payload had no runId.
 *
 * `pause` and `resume` reuse the local Pause/Resume Pipeline mechanism (#423):
 * the run's state service is marked paused, the stage in flight finishes, the
 * stage loop holds at the next boundary, and a resume lets the same run
 * continue with the next stage.
 *
 * A verb for a repo that is not open in this window is not consumed here: the
 * agent identity is per machine, so another window on this machine may hold
 * the run, and a no-op ack from this one would race its applied ack. It is
 * left for that window, exactly as TriggerCommandHandler leaves a trigger.
 *
 * Replaces the separate Cancel/Approve/RejectCommandHandler classes, which
 * acted on the run and acknowledged nothing.
 *
 * @see AgentCommandDispatcher — routes each delivered command here or to the trigger handler
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import type { ConcurrentPipelineManager, RemoteVerbResult } from "./ConcurrentPipelineManager";
import type { IpcClient } from "./IpcClient";
import type { WorkspaceManager } from "./WorkspaceManager";
import type { Logger } from "../utils/logger";

/** The verbs that act on an existing run, as the platform names them. */
export const RUN_VERB_COMMAND_TYPES = ["cancel", "approve", "reject", "pause", "resume"] as const;
export type RunVerbCommandType = (typeof RUN_VERB_COMMAND_TYPES)[number];

export function isRunVerb(type: string): type is RunVerbCommandType {
  return (RUN_VERB_COMMAND_TYPES as readonly string[]).includes(type);
}

/**
 * The public reason a no-op ack carries: a fixed category and a short gloss,
 * never anything local (paths, error text).
 */
const NO_OP_DETAIL: Record<Exclude<RemoteVerbResult, "applied">, string> = {
  "no-active-run": "no-active-run: no pipeline on this agent carries this runId",
  "no-waiting-gate": "no-waiting-gate: the run is not waiting at an approval gate",
  "already-paused": "already-paused: the run is already paused",
  "not-paused": "not-paused: the run is not paused",
  "no-run-state": "no-run-state: the run has no local state to pause yet",
};
const INVALID_PAYLOAD_DETAIL = "invalid-payload: runId is required";
const APPLY_FAILED_DETAIL = "apply-failed: the agent could not carry out the command";

export type RunVerbTarget = Pick<
  ConcurrentPipelineManager,
  | "cancelByRemoteRunId"
  | "approveByRemoteRunId"
  | "rejectByRemoteRunId"
  | "pauseByRemoteRunId"
  | "resumeByRemoteRunId"
>;

export class RunVerbCommandHandler implements CommandHandler {
  private agentId: string | null = null;

  constructor(
    private readonly runs: RunVerbTarget,
    private readonly ipcClient: Pick<IpcClient, "agentAcknowledgeCommand">,
    private readonly logger: Logger,
    /**
     * Optional, as for TriggerCommandHandler: when present, a verb whose repo
     * is not open in this workspace is left for the window that has it.
     */
    private readonly workspaceManager?: Pick<WorkspaceManager, "findRepositoryByGitHub">
  ) {}

  /** The agent this window's own command stream belongs to. */
  setAgentId(agentId: string): void {
    this.agentId = agentId;
  }

  handle(cmd: ReceivedCommand): void {
    if (!isRunVerb(cmd.type)) return;
    void this.consume(cmd, cmd.type);
  }

  /**
   * Apply one verb and acknowledge it. Resolves once the ack has been sent or
   * has failed, so a caller can await the whole consumption.
   */
  async consume(cmd: ReceivedCommand, verb: RunVerbCommandType): Promise<void> {
    if (this.notThisWindow(cmd)) {
      this.logger.info(
        "RunVerbCommandHandler: repo not open in this workspace — leaving the command for the window that has it",
        { verb, owner: cmd.owner, repo: cmd.repo, commandId: cmd.id }
      );
      return;
    }

    const runId = (cmd.payload as { runId?: unknown } | null | undefined)?.runId;
    if (typeof runId !== "string" || runId === "") {
      this.logger.warn("RunVerbCommandHandler: missing runId in payload", {
        verb,
        commandId: cmd.id,
      });
      await this.acknowledge(cmd, verb, "rejected", INVALID_PAYLOAD_DETAIL);
      return;
    }

    let result: RemoteVerbResult;
    try {
      result = await this.apply(verb, runId);
    } catch (err) {
      this.logger.error("RunVerbCommandHandler: applying the verb failed", {
        verb,
        runId,
        commandId: cmd.id,
        err: err instanceof Error ? err.message : String(err),
      });
      await this.acknowledge(cmd, verb, "rejected", APPLY_FAILED_DETAIL);
      return;
    }

    if (result === "applied") {
      this.logger.info("RunVerbCommandHandler: applied", { verb, runId, commandId: cmd.id });
      await this.acknowledge(cmd, verb, "applied");
      return;
    }
    this.logger.warn("RunVerbCommandHandler: nothing to act on — no-op", {
      verb,
      runId,
      result,
      commandId: cmd.id,
    });
    await this.acknowledge(cmd, verb, "rejected", NO_OP_DETAIL[result]);
  }

  private apply(
    verb: RunVerbCommandType,
    runId: string
  ): Promise<RemoteVerbResult> | RemoteVerbResult {
    switch (verb) {
      case "cancel":
        return this.runs.cancelByRemoteRunId(runId);
      case "approve":
        return this.runs.approveByRemoteRunId(runId);
      case "reject":
        return this.runs.rejectByRemoteRunId(runId);
      case "pause":
        return this.runs.pauseByRemoteRunId(runId);
      case "resume":
        return this.runs.resumeByRemoteRunId(runId);
    }
  }

  private notThisWindow(cmd: ReceivedCommand): boolean {
    if (!this.workspaceManager || !cmd.owner || !cmd.repo) return false;
    return !this.workspaceManager.findRepositoryByGitHub(`${cmd.owner}/${cmd.repo}`);
  }

  private async acknowledge(
    cmd: ReceivedCommand,
    verb: RunVerbCommandType,
    outcome: "applied" | "rejected",
    detail?: string
  ): Promise<void> {
    // A relayed command names the agent it was addressed to; this window's
    // own stream delivers commands addressed to its own agent.
    const agentId = cmd.agentId ?? this.agentId;
    if (!agentId || !cmd.id) {
      this.logger.warn("RunVerbCommandHandler: cannot acknowledge — no agent id or command id", {
        verb,
        commandId: cmd.id,
      });
      return;
    }
    try {
      await this.ipcClient.agentAcknowledgeCommand(agentId, cmd.id, outcome, detail);
    } catch (err) {
      this.logger.error("RunVerbCommandHandler: ack failed", {
        verb,
        commandId: cmd.id,
        outcome,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}
