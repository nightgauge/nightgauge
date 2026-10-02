/**
 * TriggerCommandHandler — processes `trigger`-type agent commands received via
 * AgentCommandStreamService.
 *
 * Flow:
 *   1. Reject if issueNumber is already running (concurrent guard)
 *   2. Fetch the issue's title + labels (best-effort) so the run has a real
 *      branch name and pipeline state
 *   3. Refuse, acked rejected, an issue queued or on its way to a slot here
 *      for another platform run (#2344)
 *   4. Ack the command via Go IPC → receive runId
 *   5. Place the run through ConcurrentPipelineManager (#2344): on the
 *      issue's dispatch already under way here, or enqueued (with
 *      repoOverride) so the queue has something to dequeue, then start the
 *      local pipeline
 *
 * The platform publishes the trigger payload with SEPARATE `owner` and `repo`
 * fields (see pipeline-trigger-dispatcher-service.ts) — the dashboard can
 * trigger any repo linked to the team workspace, not just the agent's primary
 * repo, so the enqueue routes via repoOverride.
 *
 * @see Issue #3551 — Handle trigger command ack and start pipeline
 * @see Issue #4118 — Trigger acked but never ran because the issue was never enqueued
 * @see Issue #4117 — Resolve the target repo against the open workspace before
 *   ack/enqueue so a repo that isn't open in a multi-root .code-workspace
 *   fails fast instead of acking a command the runner can never execute
 * A trigger may carry a remote run request (#1656, ADR-022 § 2): optional
 * `adapter` and `model` fields naming what the run must execute on. Go decides
 * whether this machine can serve them (`queue.validatePin`) BEFORE the ack. A
 * refusal is acked `{outcome: "rejected", detail}` and nothing is queued, so
 * the requester sees why and is never served by another adapter or model.
 *
 * @see AgentCommandStreamService — SSE source that dispatches commands here
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import type { IpcClient } from "./IpcClient";
import type { ConcurrentPipelineManager } from "./ConcurrentPipelineManager";
import type { IssueQueueService } from "./IssueQueueService";
import type { WorkspaceManager } from "./WorkspaceManager";
import type { Logger } from "../utils/logger";

/**
 * The public reason a trigger is refused when its issue is already queued or
 * on its way to a slot here for another platform run (#2344): this agent
 * runs an issue once at a time, so it cannot serve a second run of it.
 */
const ALREADY_QUEUED_DETAIL =
  "already-queued: the issue is already queued on this agent for another run";

interface TriggerPayload {
  owner: string;
  repo: string;
  issueNumber: number;
  stage?: string;
  /** Remote run request (#1656): the adapter id the run must execute on. */
  adapter?: unknown;
  /** Remote run request (#1656): the `-m` value, provider-key form. */
  model?: unknown;
}

export class TriggerCommandHandler implements CommandHandler {
  private agentId: string | null = null;

  constructor(
    private readonly ipcClient: IpcClient,
    private readonly concurrentManager: ConcurrentPipelineManager,
    private readonly queueService: IssueQueueService,
    private readonly logger: Logger,
    /**
     * Optional. When provided, a trigger's {owner, repo} is resolved against
     * the open workspace (WorkspaceManager.findRepositoryByGitHub) before
     * ack/enqueue, so a repo that isn't open in this workspace — e.g. a
     * multi-root .code-workspace where the platform triggers a repo the user
     * hasn't added as a folder — fails fast with a clear log instead of
     * acking a command that ConcurrentPipelineManager will silently drop
     * later at dispatch time. Undefined preserves pre-#4117 behavior
     * (resolution deferred entirely to dispatch time).
     * @see Issue #4117
     */
    private readonly workspaceManager?: WorkspaceManager
  ) {}

  /** Called by AgentCommandStreamService.start(agentId) to provide the agentId. */
  setAgentId(agentId: string): void {
    this.agentId = agentId;
  }

  handle(cmd: ReceivedCommand): void {
    if (cmd.type !== "trigger") return;
    void this.handleTrigger(cmd);
  }

  private async handleTrigger(cmd: ReceivedCommand): Promise<void> {
    // A trigger the daemon relayed from its own agent names that agent, and
    // the ack must name it too (#2335); this window's own stream delivers
    // triggers addressed to this window's agent.
    const agentId = cmd.agentId ?? this.agentId;
    if (!agentId) {
      this.logger.warn("TriggerCommandHandler: agentId not set, dropping trigger", {
        commandId: cmd.id,
      });
      return;
    }

    const payload = cmd.payload as TriggerPayload;
    if (
      typeof payload?.issueNumber !== "number" ||
      typeof payload?.owner !== "string" ||
      !payload.owner ||
      typeof payload?.repo !== "string" ||
      !payload.repo
    ) {
      this.logger.warn(
        "TriggerCommandHandler: invalid trigger payload — need owner, repo, issueNumber",
        { commandId: cmd.id, payload }
      );
      return;
    }

    const { owner, repo, issueNumber } = payload;

    // Resolve the target repo against the open workspace BEFORE ack/enqueue.
    // A trigger for a repo that isn't open in this workspace (multi-root
    // .code-workspace mismatch) can never be dispatched — fail fast with a
    // clear log rather than acking a command and enqueuing an item that
    // ConcurrentPipelineManager.resolveWorktreeManager will silently drop at
    // fillSlots() time. workspaceManager is optional (undefined in
    // single-root / no-multi-repo setups) — skip the check entirely then.
    // @see Issue #4117
    if (
      this.workspaceManager &&
      !this.workspaceManager.findRepositoryByGitHub(`${owner}/${repo}`)
    ) {
      this.logger.warn(
        "TriggerCommandHandler: no matching repo open in this workspace — dropping trigger " +
          "(open the target repo as a workspace folder, or add it to .vscode/nightgauge-workspace.yaml)",
        { owner, repo, issueNumber, commandId: cmd.id }
      );
      return;
    }

    // Concurrent guard — reject if issueNumber already has an active slot.
    if (this.concurrentManager.isRunning(issueNumber)) {
      this.logger.warn(
        "TriggerCommandHandler: concurrent trigger rejected — issue already running",
        { issueNumber, commandId: cmd.id }
      );
      return;
    }

    // Fetch the real issue title + labels so the queued item drives a meaningful
    // branch name (feat/<n>-<slug>) and pre-seeds pipeline state. Best-effort:
    // a transient fetch failure must not block an explicit run request — the
    // issue-pickup stage re-fetches the authoritative issue context downstream,
    // so a placeholder title is acceptable as a fallback.
    let title = `Issue #${issueNumber}`;
    let labels: string[] = [];
    try {
      const issue = await this.ipcClient.issueView(owner, repo, issueNumber);
      if (issue?.title) title = issue.title;
      if (Array.isArray(issue?.labels)) labels = issue.labels;
    } catch (err) {
      this.logger.warn("TriggerCommandHandler: issueView failed — using placeholder title", {
        issueNumber,
        repo: `${owner}/${repo}`,
        err: err instanceof Error ? err.message : String(err),
      });
    }

    // Remote run request (#1656). Go is the one authority on whether this
    // machine can serve the pair; a refusal is acked as rejected with the
    // reason, and nothing is queued. A payload without the fields skips this
    // entirely and behaves exactly as before.
    const requested = await this.checkRequestedPin(agentId, cmd.id, payload, labels);
    if (requested === "refused") return;

    // Checked again just before the ack (#2344): the issue's slot may have
    // opened while the issue was fetched, and an issue queued or on its way
    // to a slot here for another platform run cannot serve this one.
    const conflict = await this.concurrentManager.remoteTriggerConflict(
      issueNumber,
      `${owner}/${repo}`
    );
    if (conflict === "running") {
      this.logger.warn(
        "TriggerCommandHandler: concurrent trigger rejected — issue already running",
        { issueNumber, commandId: cmd.id }
      );
      return;
    }
    if (conflict === "busy") {
      this.logger.warn(
        "TriggerCommandHandler: the issue is already queued here for another platform run — acking as rejected",
        { issueNumber, repo: `${owner}/${repo}`, commandId: cmd.id }
      );
      try {
        await this.ipcClient.agentAcknowledgeCommand(
          agentId,
          cmd.id,
          "rejected",
          ALREADY_QUEUED_DETAIL
        );
      } catch (err) {
        this.logger.error("TriggerCommandHandler: rejected ack failed", {
          commandId: cmd.id,
          err: err instanceof Error ? err.message : String(err),
        });
      }
      return;
    }

    // Ack must complete before pipeline starts (AC#1). The ack returns the
    // platform runId the dashboard polls for status and that RunVerbCommandHandler
    // uses to route a cancel to the right slot.
    let runId: string;
    try {
      const ackResult = await this.ipcClient.agentAcknowledgeCommand(agentId, cmd.id);
      runId = ackResult.runId;
    } catch (err) {
      this.logger.error("TriggerCommandHandler: ack failed — pipeline not started", {
        commandId: cmd.id,
        err: err instanceof Error ? err.message : String(err),
      });
      return;
    }

    this.logger.info("TriggerCommandHandler: ack succeeded, enqueuing issue", {
      issueNumber,
      repo: `${owner}/${repo}`,
      commandId: cmd.id,
      runId,
    });

    // This window holds the run from the ack on (#2340). The manager places
    // it in one queue turn (#2344): on the issue's dispatch already under way
    // here, or in the queue, where the item carries the run id the slot will
    // adopt. enqueue() can trigger a debounced fillSlots via onItemAdded; that
    // fill's dequeue waits for the turn. The queued item routes to the
    // triggered repo through repoOverride, independent of the workspace's
    // primary repo, so a dashboard trigger can run any repo linked to the team
    // workspace. Without the enqueue the command acked but the run never
    // started (#4118).
    const enqueue = async (): Promise<boolean> =>
      (await this.queueService.enqueue(issueNumber, title, labels, undefined, {
        repoOverride: { owner, repo },
        // Adopt the ack runId as the pipeline-run id (via the Go queue item's
        // RemoteRunID) so command.runId === pipeline_runs.runId and the
        // dashboard's run deep-link resolves instead of 404ing (#4120).
        remoteRunId: runId,
        // Only a remote run request adds keys; without one the options are
        // exactly what they were before #1656.
        ...(requested
          ? { requestedAdapter: requested.adapter, requestedModel: requested.model }
          : {}),
      })) !== null;
    let placement: Awaited<ReturnType<ConcurrentPipelineManager["placeRemoteRun"]>>;
    try {
      // An epic is queued as its sub-issues, each dispatched as a run of its
      // own (IssueQueueService.enqueueEpic), so no queued item carries the
      // epic's platform run id and nothing here serves that run to place.
      if (labels.includes("type:epic")) {
        placement = (await enqueue()) ? "queued" : "not-queued";
      } else {
        placement = await this.concurrentManager.placeRemoteRun(
          { remoteRunId: runId, issueNumber, repo: `${owner}/${repo}` },
          enqueue
        );
      }
    } catch (err) {
      this.logger.error("TriggerCommandHandler: enqueue failed — pipeline not started", {
        issueNumber,
        commandId: cmd.id,
        runId,
        err: err instanceof Error ? err.message : String(err),
      });
      return;
    }
    switch (placement) {
      case "not-queued":
        this.logger.error(
          "TriggerCommandHandler: enqueue refused (stop in progress?) — pipeline not started",
          { issueNumber, commandId: cmd.id, runId }
        );
        return;
      case "running":
      case "busy":
        // The issue's slot opened, or it was queued for another platform
        // run, while the trigger was acked: nothing here serves this run.
        this.logger.error(
          "TriggerCommandHandler: the issue is already running or queued for another run here — this run was acked but is not served",
          { issueNumber, commandId: cmd.id, runId, placement }
        );
        return;
      case "attached":
        this.logger.info(
          "TriggerCommandHandler: the issue's dispatch already under way here serves the run",
          { issueNumber, commandId: cmd.id, runId }
        );
        return;
      case "queued":
        break;
    }

    // Explicitly fill slots now. A dashboard trigger is an on-demand request to
    // run THIS issue, so it starts regardless of the queue's autoStart config
    // (onItemAdded only auto-fills when autoStart is enabled).
    try {
      await this.concurrentManager.fillSlots();
    } catch (err) {
      this.logger.error("TriggerCommandHandler: pipeline start failed", {
        issueNumber,
        commandId: cmd.id,
        runId,
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }

  /**
   * Validate a trigger's remote run request (#1656). Returns undefined when
   * the payload names neither field, the accepted pair, or "refused" after
   * acking the command as rejected with the reason.
   */
  private async checkRequestedPin(
    agentId: string,
    commandId: string,
    payload: TriggerPayload,
    labels: string[]
  ): Promise<{ adapter: string; model?: string } | undefined | "refused"> {
    if (payload.adapter === undefined && payload.model === undefined) return undefined;

    // `reason` is the ack's public detail: a fixed category and at most an
    // adapter id or variable names (#1656). `localReason` is the full story,
    // logged here and never sent to the hosted service.
    let reason: string | undefined;
    let localReason: string | undefined;
    if (
      (payload.adapter !== undefined && typeof payload.adapter !== "string") ||
      (payload.model !== undefined && typeof payload.model !== "string")
    ) {
      reason = "invalid-request: adapter and model must be strings";
    } else if (labels.includes("type:epic")) {
      reason = "epic-not-pinnable";
      localReason = "a requested adapter and model cannot apply to an epic; trigger its sub-issues";
    } else {
      try {
        const verdict = await this.ipcClient.queueValidatePin(
          payload.adapter as string | undefined,
          payload.model as string | undefined,
          payload.owner,
          payload.repo,
          payload.issueNumber
        );
        if (!verdict.ok) reason = verdict.reason || "validation-failed";
      } catch (err) {
        reason = "validation-unavailable";
        localReason = err instanceof Error ? err.message : String(err);
      }
    }

    if (reason === undefined) {
      return { adapter: payload.adapter as string, model: (payload.model as string) || undefined };
    }

    this.logger.warn("TriggerCommandHandler: remote run request refused — acking as rejected", {
      commandId,
      issueNumber: payload.issueNumber,
      reason,
      ...(localReason ? { localReason } : {}),
    });
    try {
      await this.ipcClient.agentAcknowledgeCommand(agentId, commandId, "rejected", reason);
    } catch (err) {
      this.logger.error("TriggerCommandHandler: rejected ack failed", {
        commandId,
        err: err instanceof Error ? err.message : String(err),
      });
    }
    return "refused";
  }
}
