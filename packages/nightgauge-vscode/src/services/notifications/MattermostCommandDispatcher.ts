/**
 * MattermostCommandDispatcher — routes parsed slash-command events
 * from the Go inbound receiver to IPC pipeline methods and posts the
 * formatted response back to Mattermost via `response_url`.
 *
 * The dispatcher subscribes to `IpcClient.onMattermostCommand`,
 * resolves owner/repo/workspace once per command from
 * `configGetProjectConfig`, calls the appropriate IPC method, and
 * fires a fire-and-forget POST to the `response_url` supplied by
 * Mattermost.
 *
 * Authorization is exposed as a constructor hook so #3377 can fill
 * in a real check without changing this file. Default is
 * allow-all so the dispatcher remains functional until #3377 lands.
 *
 * @see Issue #3376 — implementation
 * @see Issue #3377 — authorization (sibling)
 */

import * as vscode from "vscode";
import type { IpcClient } from "../IpcClient";
import type { MattermostSlashEvent, MattermostParsedCommand } from "../IpcClientBase";
import type { Logger } from "../../utils/logger";
import {
  formatError,
  formatHealth,
  formatHelp,
  formatNotAuthorizedWithDetail,
  formatUnmapped,
  formatPause,
  formatQueueAdd,
  formatQueueList,
  formatQueueRemove,
  formatResume,
  formatRunAck,
  formatStatus,
  formatStop,
  formatUnknown,
  type MattermostResponse,
} from "./MattermostCommandFormatter";

export interface AuthorizeResult {
  allowed: boolean;
  reason: string;
  mappedIdentity?: string;
}

/** async authorization hook — receives the full event so the implementation
 * can inspect user_id, channel_id, and the parsed command type + repo slug. */
export type AuthorizeFn = (event: MattermostSlashEvent) => Promise<AuthorizeResult>;

/** Resolved workspace context cached per-dispatcher. */
interface ProjectContext {
  owner: string;
  projectNumber: number;
  defaultRepo?: string;
  workspaceRoot?: string;
}

export class MattermostCommandDispatcher implements vscode.Disposable {
  private readonly disposables: vscode.Disposable[] = [];
  private projectCtx: ProjectContext | null = null;

  constructor(
    private readonly ipc: IpcClient,
    private readonly logger: Logger,
    private readonly authorize: AuthorizeFn = async () => ({
      allowed: true,
      reason: "allow-all default",
    })
  ) {
    this.disposables.push(
      this.ipc.onMattermostCommand((event) => {
        // Fire-and-forget — the IPC handler already acknowledged the
        // webhook synchronously, so dispatcher work happens off the
        // request path.
        void this.handleCommand(event);
      })
    );
    this.logger.info("MattermostCommandDispatcher initialized");
  }

  dispose(): void {
    for (const d of this.disposables) {
      d.dispose();
    }
    this.disposables.length = 0;
  }

  private async handleCommand(event: MattermostSlashEvent): Promise<void> {
    const userId = event.user_id ?? "";
    const channelId = event.channel_id ?? "";

    const authResult = await this.authorize(event);
    if (!authResult.allowed) {
      this.logger.warn("MattermostCommandDispatcher: unauthorized command rejected", {
        userId,
        channelId,
        type: event.parsed_command?.type,
        reason: authResult.reason,
      });
      // Distinguish unmapped users from authorized-but-insufficient-access.
      const response =
        authResult.reason === "unmapped"
          ? formatUnmapped()
          : formatNotAuthorizedWithDetail(
              event.parsed_command?.type ?? "run",
              this.projectCtx?.defaultRepo ?? "this repo"
            );
      await this.reply(event, response);
      return;
    }

    try {
      const response = await this.dispatch(event.parsed_command);
      await this.reply(event, response);
    } catch (err) {
      this.logger.error("MattermostCommandDispatcher: command failed", {
        type: event.parsed_command?.type,
        err: err instanceof Error ? err.message : String(err),
      });
      await this.reply(event, formatError(err));
    }
  }

  private async dispatch(cmd: MattermostParsedCommand): Promise<MattermostResponse> {
    switch (cmd.type) {
      case "status":
        return this.dispatchStatus(cmd);
      case "run":
        return this.dispatchRun(cmd);
      case "pause":
        await this.ipc.pipelineSetPaused(0, true);
        return formatPause();
      case "resume":
        await this.ipc.pipelineSetPaused(0, false);
        return formatResume();
      case "stop":
        return this.dispatchStop(cmd);
      case "queue.add":
        return this.dispatchQueueAdd(cmd);
      case "queue.remove":
        if (!cmd.issue_number) {
          return formatQueueRemove(undefined);
        }
        await this.ipc.queueRemove(cmd.issue_number);
        return formatQueueRemove(cmd.issue_number);
      case "queue.list": {
        const state = await this.ipc.queueList();
        return formatQueueList(state);
      }
      case "health":
        return this.dispatchHealth();
      case "help":
        return formatHelp();
      case "unknown":
      default:
        return formatUnknown(cmd.raw_text);
    }
  }

  // ─── Per-command handlers ────────────────────────────────────────────────

  private async dispatchStatus(cmd: MattermostParsedCommand): Promise<MattermostResponse> {
    const ctx = await this.getProjectContext();
    if (!ctx) {
      return formatStatus(null, cmd.issue_number);
    }

    const executions = await this.ipc.executionList();
    const target = cmd.issue_number
      ? executions.find((e) => e.issueNumber === cmd.issue_number)
      : executions[0];
    if (!target) {
      return formatStatus(null, cmd.issue_number);
    }

    const status = await this.ipc.pipelineStatus(ctx.owner, ctx.projectNumber, target.id);
    return formatStatus(status, target.issueNumber);
  }

  private async dispatchRun(cmd: MattermostParsedCommand): Promise<MattermostResponse> {
    if (!cmd.issue_number) {
      return {
        response_type: "ephemeral",
        text: "Usage: `run <issue-number> [--repo owner/slug]`",
      };
    }
    await this.getProjectContext();
    const { owner, repo } = this.resolveOwnerRepo(cmd.repo);
    if (!owner || !repo) {
      return {
        response_type: "ephemeral",
        text: "No repo configured. Pass `--repo owner/slug` or set `project.owner` and `project.default_repo` in `.nightgauge/config.yaml`.",
      };
    }
    await this.ipc.queueAdd(owner, repo, cmd.issue_number);
    return formatRunAck(cmd.issue_number);
  }

  private async dispatchStop(cmd: MattermostParsedCommand): Promise<MattermostResponse> {
    const executions = await this.ipc.executionList();
    const target = cmd.issue_number
      ? executions.find((e) => e.issueNumber === cmd.issue_number)
      : executions[0];
    if (!target) {
      return formatStop(null);
    }
    await this.ipc.pipelineStop(target.id);
    return formatStop(target.issueNumber);
  }

  private async dispatchQueueAdd(cmd: MattermostParsedCommand): Promise<MattermostResponse> {
    if (!cmd.issue_number) {
      return formatQueueAdd(undefined);
    }
    await this.getProjectContext();
    const { owner, repo } = this.resolveOwnerRepo(cmd.repo);
    if (!owner || !repo) {
      return {
        response_type: "ephemeral",
        text: "No repo configured. Pass `--repo owner/slug` or set `project.owner` and `project.default_repo` in `.nightgauge/config.yaml`.",
      };
    }
    await this.ipc.queueAdd(owner, repo, cmd.issue_number);
    return formatQueueAdd(cmd.issue_number);
  }

  private async dispatchHealth(): Promise<MattermostResponse> {
    const ctx = await this.getProjectContext();
    const workspaceRoot = ctx?.workspaceRoot ?? "";
    const analysis = await this.ipc.healthAnalyze(workspaceRoot);
    return formatHealth(analysis);
  }

  // ─── Context resolution ──────────────────────────────────────────────────

  private async getProjectContext(): Promise<ProjectContext | null> {
    if (this.projectCtx) return this.projectCtx;
    try {
      const result = await this.ipc.configGetProjectConfig();
      this.projectCtx = {
        owner: result.owner,
        projectNumber: result.projectNumber,
        defaultRepo: result.defaultRepo,
        workspaceRoot: vscode.workspace.workspaceFolders?.[0]?.uri.fsPath,
      };
      return this.projectCtx;
    } catch (err) {
      this.logger.warn("MattermostCommandDispatcher: configGetProjectConfig failed", {
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  private resolveOwnerRepo(repoFlag?: string): { owner?: string; repo?: string } {
    if (repoFlag && repoFlag.includes("/")) {
      const [owner, repo] = repoFlag.split("/", 2);
      return { owner, repo };
    }
    const ctx = this.projectCtx;
    if (!ctx) return {};
    return { owner: ctx.owner, repo: ctx.defaultRepo };
  }

  // ─── Mattermost response_url POST ────────────────────────────────────────

  private async reply(event: MattermostSlashEvent, response: MattermostResponse): Promise<void> {
    const url = event.response_url;
    if (!url) {
      // Outgoing webhooks may omit response_url; nothing to do.
      return;
    }
    try {
      const res = await fetch(url, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body: JSON.stringify(response),
      });
      if (!res.ok) {
        this.logger.warn("MattermostCommandDispatcher: response_url POST failed", {
          status: res.status,
        });
      }
    } catch (err) {
      this.logger.warn("MattermostCommandDispatcher: response_url POST threw", {
        err: err instanceof Error ? err.message : String(err),
      });
    }
  }
}
