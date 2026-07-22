/**
 * RejectCommandHandler — processes `reject`-type agent commands received via
 * AgentCommandStreamService.
 *
 * Flow:
 *   1. Extract runId from the command payload
 *   2. Find the active slot by runId via ConcurrentPipelineManager
 *   3. If found: call rejectByRunId (resolves the approval gate with false)
 *   4. If not found: log warning and return (no-op per AC#3)
 *
 * @see Issue #3553 — Handle approve/reject command — forward gate approval to waiting pipeline
 * @see AgentCommandStreamService — SSE source that dispatches commands here
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import type { ConcurrentPipelineManager } from "./ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

interface RejectPayload {
  runId: string;
}

export class RejectCommandHandler implements CommandHandler {
  constructor(
    private readonly concurrentManager: ConcurrentPipelineManager,
    private readonly logger: Logger
  ) {}

  handle(cmd: ReceivedCommand): void {
    if (cmd.type !== "reject") return;
    void this.handleReject(cmd);
  }

  private async handleReject(cmd: ReceivedCommand): Promise<void> {
    const payload = cmd.payload as RejectPayload;
    const runId = payload?.runId;

    if (!runId) {
      this.logger.warn("RejectCommandHandler: missing runId in payload", {
        commandId: cmd.id,
      });
      return;
    }

    const found = this.concurrentManager.rejectByRunId(runId);

    if (!found) {
      this.logger.warn(
        "RejectCommandHandler: no active pipeline waiting at gate for runId — no-op",
        { runId, commandId: cmd.id }
      );
      return;
    }

    this.logger.info("RejectCommandHandler: gate rejected", {
      runId,
      commandId: cmd.id,
    });
  }
}
