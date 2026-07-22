/**
 * CancelCommandHandler — processes `cancel`-type agent commands received via
 * AgentCommandStreamService.
 *
 * Flow:
 *   1. Extract runId from the command payload
 *   2. Find the active slot by runId via ConcurrentPipelineManager
 *   3. If found: call cancelByRunId (sets userCancelled=true + SIGTERM→10s→SIGKILL)
 *   4. If not found: log warning and return (no-op per AC#4)
 *
 * The terminal platform telemetry (pipeline_done, success=false) is emitted by
 * the normal completion path — HeadlessOrchestrator.firePipelineComplete fires
 * when the cancelled run's loop unwinds — so this handler no longer emits its
 * own event. (The old `pipeline_cancelled` event was never accepted by the
 * platform's event contract.)
 *
 * @see Issue #3552 — Handle cancel command gracefully
 * @see AgentCommandStreamService — SSE source that dispatches commands here
 */

import type { CommandHandler, ReceivedCommand } from "./AgentCommandStreamService";
import type { ConcurrentPipelineManager } from "./ConcurrentPipelineManager";
import type { Logger } from "../utils/logger";

interface CancelPayload {
  runId: string;
}

export class CancelCommandHandler implements CommandHandler {
  constructor(
    private readonly concurrentManager: ConcurrentPipelineManager,
    private readonly logger: Logger
  ) {}

  handle(cmd: ReceivedCommand): void {
    if (cmd.type !== "cancel") return;
    void this.handleCancel(cmd);
  }

  private async handleCancel(cmd: ReceivedCommand): Promise<void> {
    const payload = cmd.payload as CancelPayload;
    const runId = payload?.runId;

    if (!runId) {
      this.logger.warn("CancelCommandHandler: missing runId in payload", {
        commandId: cmd.id,
      });
      return;
    }

    const found = await this.concurrentManager.cancelByRunId(runId);

    if (!found) {
      this.logger.warn("CancelCommandHandler: no active pipeline for runId — no-op", {
        runId,
        commandId: cmd.id,
      });
      return;
    }

    this.logger.info("CancelCommandHandler: pipeline cancelled", {
      runId,
      commandId: cmd.id,
    });
  }
}
