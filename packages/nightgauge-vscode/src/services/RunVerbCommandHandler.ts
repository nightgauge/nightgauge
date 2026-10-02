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
 *   - `already_resolved` with a fixed reason: the run was already in the state
 *     the verb asks for, a pause of a paused run or a resume of a run that is
 *     not paused (#2341). The platform keeps the status the verb set;
 *   - `rejected` with a fixed reason: it could not act (a pause or resume of
 *     a run still queued here, the run has no state to pause yet, or the run
 *     ended after this window was found to hold it), it was an `approve` or
 *     `reject`, or its payload had no runId. On a pause or resume the
 *     platform then restores the run's earlier status. A cancel of a run
 *     still queued here applies (#2344).
 *
 * `approve` and `reject` name a run's `stage` and `gateType`, but no local
 * run ever waits at a gate a platform decision could release (#2336): the
 * pipeline evaluates its quality gates itself and fails the stage when one
 * fails, the architecture-approval check ends the run before feature-dev and
 * is approved on the issue, and an attention decision request has its own
 * command, `attention_resolve`. The holder refuses both verbs with
 * `no-approval-gate` (docs/GO_BINARY.md § The daemon's platform agent).
 *
 * `pause` and `resume` reuse the local Pause/Resume Pipeline mechanism (#423):
 * the run's state service is marked paused, the stage in flight finishes, the
 * stage loop holds at the next boundary, and a resume lets the same run
 * continue with the next stage.
 *
 * The platform delivers at least once, so the same command can arrive twice.
 * Each command id is carried out and acknowledged once
 * (CommandRedeliveryGuard): a later copy re-sends the first copy's ack only
 * when that ack did not reach the platform.
 *
 * A pause or resume that took effect also shows in this window the way the
 * local Pause/Resume Pipeline commands show it (RemotePauseUi).
 *
 * Only the window that holds the run consumes a verb (#2340). The platform
 * sends a command to every connection that shares the agent id, and the agent
 * identity is per machine, so every window on this machine receives the verb;
 * the first ack ends the command. A window that does not hold the run (no
 * slot carries the runId, and no item queued for it is on its way to a slot
 * there) does not apply the verb. The holder answers whatever repositories it
 * has open now. A no-op ack from another window would race the holder's ack,
 * and a `rejected` pause or resume makes the platform undo the hold the
 * holder applied.
 *
 * When no window holds the run, the verb is still refused, `no-active-run`,
 * within seconds (#2357), through the machine's RemoteRunLedger: the holder
 * claims the command's one answer before it applies the verb, and a window
 * that does not hold the run waits UNHELD_VERB_GRACE_MS, then refuses only
 * when it still does not hold it, no live window of the machine lists the
 * run as held, and it claims the answer first. So the platform receives one
 * acknowledgement per command, and never a refusal ahead of the holder's.
 * Without a ledger (no machine-state directory) such a verb is left alone and
 * expires.
 *
 * Replaces the separate Cancel/Approve/RejectCommandHandler classes, which
 * acted on the run and acknowledged nothing.
 *
 * @see AgentCommandDispatcher — routes each delivered command here or to the trigger handler
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import { CommandRedeliveryGuard } from "./CommandRedeliveryGuard";
import type { ConcurrentPipelineManager, RemoteVerbResult } from "./ConcurrentPipelineManager";
import type { IpcClient } from "./IpcClient";
import type { WorkspaceManager } from "./WorkspaceManager";
import type { Logger } from "../utils/logger";
import type { RemotePauseUi } from "../utils/pauseUi";
import type { RemoteRunLedger } from "./RemoteRunLedger";

/** The verbs that act on an existing run, as the platform names them. */
export const RUN_VERB_COMMAND_TYPES = ["cancel", "approve", "reject", "pause", "resume"] as const;
export type RunVerbCommandType = (typeof RUN_VERB_COMMAND_TYPES)[number];

export function isRunVerb(type: string): type is RunVerbCommandType {
  return (RUN_VERB_COMMAND_TYPES as readonly string[]).includes(type);
}

/** The results that find the run already in the state the verb asks for. */
type AlreadyResolvedResult = "already-paused" | "not-paused";

/**
 * The public reason a no-op ack carries: a fixed category and a short gloss,
 * never anything local (paths, error text). The run is already in the
 * requested state (acked `already_resolved`, #2341)...
 */
const ALREADY_RESOLVED_DETAIL: Record<AlreadyResolvedResult, string> = {
  "already-paused": "already-paused: the run is already paused",
  "not-paused": "not-paused: the run is not paused",
};
/** ...or the holder could not act on it (acked `rejected`). */
const NO_OP_DETAIL: Record<Exclude<RemoteVerbResult, "applied" | AlreadyResolvedResult>, string> = {
  "no-active-run": "no-active-run: no pipeline on this agent carries this runId",
  "not-started": "not-started: the run is queued on this agent and has not started yet",
  "no-run-state": "no-run-state: the run has no local state to pause yet",
  "resume-in-window":
    "resume-in-window: a window reload ended the paused run; only its window can resume it",
};

const INVALID_PAYLOAD_DETAIL = "invalid-payload: runId is required";
const NO_APPROVAL_GATE_DETAIL =
  "no-approval-gate: runs on this agent never wait at an approval gate";
const APPLY_FAILED_DETAIL = "apply-failed: the agent could not carry out the command";

function isAlreadyResolved(result: RemoteVerbResult): result is AlreadyResolvedResult {
  return result in ALREADY_RESOLVED_DETAIL;
}

/**
 * How long a window that does not hold a run waits before it may refuse a
 * verb for it (#2357): long enough for the holder to have claimed the answer
 * and for a trigger being accepted to have queued its run.
 */
export const UNHELD_VERB_GRACE_MS = 2_000;

/** The machine's record of which window answers a verb (#2357). */
export type UnheldVerbLedger = Pick<RemoteRunLedger, "heldElsewhere" | "claimAnswer">;

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    timer.unref?.();
  });
}

export type RunVerbTarget = Pick<
  ConcurrentPipelineManager,
  "holdsRemoteRun" | "cancelByRemoteRunId" | "pauseByRemoteRunId" | "resumeByRemoteRunId"
>;

/** The ack a consumed verb gets, decided once per command id. */
interface VerbAck {
  /** The agent the ack names; null when none is known yet. */
  agentId: string | null;
  outcome: "applied" | "already_resolved" | "rejected";
  detail?: string;
}

/** The run a verb names: its payload's non-empty `runId`, or null. */
function runIdOf(cmd: ReceivedCommand): string | null {
  const runId = (cmd.payload as { runId?: unknown } | null | undefined)?.runId;
  return typeof runId === "string" && runId !== "" ? runId : null;
}

export class RunVerbCommandHandler implements CommandHandler {
  private agentId: string | null = null;
  private readonly redelivery = new CommandRedeliveryGuard<VerbAck>();
  /** Commands waiting out the grace before a refusal no window holds (#2357). */
  private readonly waitingRefusals = new Set<string>();

  constructor(
    private readonly runs: RunVerbTarget,
    private readonly ipcClient: Pick<IpcClient, "agentAcknowledgeCommand">,
    private readonly logger: Logger,
    /**
     * Optional, as for TriggerCommandHandler: when present, a verb with no
     * runId whose repo is not open in this workspace is left for the window
     * that has it. A verb that names a run goes by who holds the run.
     */
    private readonly workspaceManager?: Pick<WorkspaceManager, "findRepositoryByGitHub">,
    /** Optional: shows an applied pause or resume in this window. */
    private readonly pauseUi?: RemotePauseUi,
    /**
     * Optional (#2357): the machine's ledger, through which a verb no window
     * holds is refused after `graceMs` (default UNHELD_VERB_GRACE_MS).
     */
    private readonly unheld?: { ledger: UnheldVerbLedger; graceMs?: number }
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
   * Apply one verb and acknowledge it, once per command id. Resolves once the
   * ack has been sent or has failed, so a caller can await the whole
   * consumption.
   */
  async consume(cmd: ReceivedCommand, verb: RunVerbCommandType): Promise<void> {
    // Only the holder answers (#2340), whichever repositories the window has
    // open now: a run it holds stays its own when a manifest reload drops the
    // run's repository. A copy of a command this window already consumed
    // belongs to that decision, even when the run has ended since: its ack
    // may still have to be re-sent. A verb with no runId names no run to
    // hold; every window that has the repo refuses it the same way.
    const runId = runIdOf(cmd);
    if (runId === null) {
      if (this.notThisWindow(cmd)) {
        this.logger.info(
          "RunVerbCommandHandler: repo not open in this workspace — leaving the command for the window that has it",
          { verb, owner: cmd.owner, repo: cmd.repo, commandId: cmd.id }
        );
        return;
      }
    } else if (!this.redelivery.remembers(cmd.id)) {
      if (!(await this.runs.holdsRemoteRun(runId))) {
        this.logger.info(
          "RunVerbCommandHandler: this window does not hold the run — leaving the command for the window that does",
          { verb, runId, commandId: cmd.id }
        );
        return this.refuseIfNobodyHolds(cmd, verb, runId);
      }
      // The holder claims the command's one answer before it applies the
      // verb, so no window that waited out the grace refuses it (#2357).
      await this.unheld?.ledger.claimAnswer(cmd.id);
    }
    return this.consumeAsHolder(cmd, verb, runId);
  }

  private consumeAsHolder(
    cmd: ReceivedCommand,
    verb: RunVerbCommandType,
    runId: string | null
  ): Promise<void> {
    return this.redelivery.consume(
      cmd.id,
      () => this.decide(cmd, verb, runId),
      (ack) => this.acknowledge(cmd, verb, ack)
    );
  }

  /**
   * Refuse a verb for a run no window of the machine holds (#2357), once the
   * grace has passed: unless this window holds the run by then (a trigger it
   * was accepting queued it), another live window lists it, or another
   * window answered the command first.
   */
  private async refuseIfNobodyHolds(
    cmd: ReceivedCommand,
    verb: RunVerbCommandType,
    runId: string
  ): Promise<void> {
    const unheld = this.unheld;
    if (!unheld || this.waitingRefusals.has(cmd.id)) return;
    this.waitingRefusals.add(cmd.id);
    try {
      await delay(unheld.graceMs ?? UNHELD_VERB_GRACE_MS);
      if (this.redelivery.remembers(cmd.id)) return;
      if (await this.runs.holdsRemoteRun(runId)) {
        await unheld.ledger.claimAnswer(cmd.id);
        return this.consumeAsHolder(cmd, verb, runId);
      }
      if (await unheld.ledger.heldElsewhere(runId)) {
        this.logger.debug("RunVerbCommandHandler: another window holds the run — it answers", {
          verb,
          runId,
          commandId: cmd.id,
        });
        return;
      }
      if (!(await unheld.ledger.claimAnswer(cmd.id))) {
        this.logger.debug("RunVerbCommandHandler: another window answered the command", {
          verb,
          runId,
          commandId: cmd.id,
        });
        return;
      }
      this.logger.info("RunVerbCommandHandler: no window holds the run — refusing the verb", {
        verb,
        runId,
        commandId: cmd.id,
      });
      const agentId = cmd.agentId ?? this.agentId;
      return this.redelivery.consume(
        cmd.id,
        async () => ({ agentId, outcome: "rejected", detail: NO_OP_DETAIL["no-active-run"] }),
        (ack) => this.acknowledge(cmd, verb, ack)
      );
    } finally {
      this.waitingRefusals.delete(cmd.id);
    }
  }

  /** Carry the verb out and decide its ack. Never throws. */
  private async decide(
    cmd: ReceivedCommand,
    verb: RunVerbCommandType,
    runId: string | null
  ): Promise<VerbAck> {
    // A relayed command names the agent it was addressed to; this window's
    // own stream delivers commands addressed to its own agent.
    const agentId = cmd.agentId ?? this.agentId;
    if (runId === null) {
      this.logger.warn("RunVerbCommandHandler: missing runId in payload", {
        verb,
        commandId: cmd.id,
      });
      return { agentId, outcome: "rejected", detail: INVALID_PAYLOAD_DETAIL };
    }
    if (verb === "approve" || verb === "reject") {
      this.logger.info("RunVerbCommandHandler: no local run waits at an approval gate", {
        verb,
        runId,
        commandId: cmd.id,
      });
      return { agentId, outcome: "rejected", detail: NO_APPROVAL_GATE_DETAIL };
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
      return { agentId, outcome: "rejected", detail: APPLY_FAILED_DETAIL };
    }

    if (result === "applied") {
      this.logger.info("RunVerbCommandHandler: applied", { verb, runId, commandId: cmd.id });
      await this.showInWindow(verb, runId);
      return { agentId, outcome: "applied" };
    }
    if (isAlreadyResolved(result)) {
      this.logger.info("RunVerbCommandHandler: the run is already in the requested state", {
        verb,
        runId,
        result,
        commandId: cmd.id,
      });
      return { agentId, outcome: "already_resolved", detail: ALREADY_RESOLVED_DETAIL[result] };
    }
    this.logger.warn("RunVerbCommandHandler: nothing to act on — no-op", {
      verb,
      runId,
      result,
      commandId: cmd.id,
    });
    return { agentId, outcome: "rejected", detail: NO_OP_DETAIL[result] };
  }

  /** Show an applied pause or resume the way the local commands do. */
  private async showInWindow(verb: RunVerbCommandType, runId: string): Promise<void> {
    if (!this.pauseUi || (verb !== "pause" && verb !== "resume")) return;
    try {
      await (verb === "pause" ? this.pauseUi.paused(runId) : this.pauseUi.resumed(runId));
    } catch (err) {
      this.logger.warn("RunVerbCommandHandler: could not show the run's new state", {
        verb,
        runId,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  private apply(
    verb: Exclude<RunVerbCommandType, "approve" | "reject">,
    runId: string
  ): Promise<RemoteVerbResult> {
    switch (verb) {
      case "cancel":
        return this.runs.cancelByRemoteRunId(runId);
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

  /** Send the decided ack. Resolves whether the platform accepted it. */
  private async acknowledge(
    cmd: ReceivedCommand,
    verb: RunVerbCommandType,
    ack: VerbAck
  ): Promise<boolean> {
    if (!ack.agentId || !cmd.id) {
      this.logger.warn("RunVerbCommandHandler: cannot acknowledge — no agent id or command id", {
        verb,
        commandId: cmd.id,
      });
      return false;
    }
    try {
      await this.ipcClient.agentAcknowledgeCommand(ack.agentId, cmd.id, ack.outcome, ack.detail);
      return true;
    } catch (err) {
      this.logger.error("RunVerbCommandHandler: ack failed", {
        verb,
        commandId: cmd.id,
        outcome: ack.outcome,
        err: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }
}
