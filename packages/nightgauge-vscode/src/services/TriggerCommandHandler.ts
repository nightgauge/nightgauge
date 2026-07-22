/**
 * TriggerCommandHandler — processes `trigger`-type agent commands received via
 * AgentCommandStreamService.
 *
 * Flow:
 *   1. Fetch the issue's title + labels (best-effort) so the run has a real
 *      branch name and pipeline state
 *   2. Ack the command via Go IPC → receive runId
 *   3. Reject if issueNumber is already running (concurrent guard)
 *   4. Enqueue the issue (with repoOverride) so the queue has something to
 *      dequeue, then start the local pipeline via ConcurrentPipelineManager
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
 * @see AgentCommandStreamService — SSE source that dispatches commands here
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import type { IpcClient } from "./IpcClient";
import type { ConcurrentPipelineManager } from "./ConcurrentPipelineManager";
import type { IssueQueueService } from "./IssueQueueService";
import type { WorkspaceManager } from "./WorkspaceManager";
import type { Logger } from "../utils/logger";

interface TriggerPayload {
  owner: string;
  repo: string;
  issueNumber: number;
  stage?: string;
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
    const agentId = this.agentId;
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

    // Ack must complete before pipeline starts (AC#1). The ack returns the
    // platform runId the dashboard polls for status and that CancelCommandHandler
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

    // Store runId BEFORE the issue can be dequeued so the slot adopts it when it
    // opens (#3552). enqueue() can trigger a debounced fillSlots via onItemAdded,
    // so the pending runId must be in place first.
    this.concurrentManager.setPendingRunId(issueNumber, runId);

    // Enqueue the issue so fillSlots() has something to dequeue. Without this the
    // command acked but the run never started — fillSlots found an empty queue
    // (#4118). The repoOverride routes the queued item to the triggered repo,
    // independent of the workspace's primary repo, so a dashboard trigger can run
    // any repo linked to the team workspace.
    try {
      const queued = await this.queueService.enqueue(issueNumber, title, labels, undefined, {
        repoOverride: { owner, repo },
        // Adopt the ack runId as the pipeline-run id (via the Go queue item's
        // RemoteRunID) so command.runId === pipeline_runs.runId and the
        // dashboard's run deep-link resolves instead of 404ing (#4120). This is
        // the same value passed to setPendingRunId above for cancel-routing.
        remoteRunId: runId,
      });
      if (!queued) {
        this.logger.error(
          "TriggerCommandHandler: enqueue refused (stop in progress?) — pipeline not started",
          { issueNumber, commandId: cmd.id, runId }
        );
        this.concurrentManager.clearPendingRunId(issueNumber);
        return;
      }
    } catch (err) {
      this.logger.error("TriggerCommandHandler: enqueue failed — pipeline not started", {
        issueNumber,
        commandId: cmd.id,
        runId,
        err: err instanceof Error ? err.message : String(err),
      });
      this.concurrentManager.clearPendingRunId(issueNumber);
      return;
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
}
