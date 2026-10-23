import type * as vscode from "vscode";
import type { ExtensionContext } from "vscode";
import type { HeadlessOrchestrator } from "../services/HeadlessOrchestrator";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { Logger } from "../utils/logger";
import type { StatusBarManager } from "../utils/statusBar";
import type { OutputWindow } from "../views";
import { RecoveryCoordinator } from "../orchestrator/recovery/RecoveryCoordinator";
import { RecoveryDialog } from "../views/recovery";
import { registerRetryFromPhaseCommand } from "./retryFromPhase";
import { registerRetryStageCommand } from "./retryStage";

/** Wire retry commands and their shared structured-recovery presenter together. */
export function registerRetryCommands(deps: {
  context: ExtensionContext;
  orchestrator: HeadlessOrchestrator | null;
  stateService: PipelineStateService | null;
  logger: Logger;
  statusBar: StatusBarManager;
  outputWindow: OutputWindow;
}): vscode.Disposable[] {
  const { context, orchestrator, stateService, logger, statusBar, outputWindow } = deps;
  const recoveryCoordinator = orchestrator
    ? new RecoveryCoordinator(orchestrator, new RecoveryDialog(context.extensionUri), logger)
    : null;

  return [
    registerRetryStageCommand(
      orchestrator,
      stateService,
      logger,
      statusBar,
      outputWindow,
      recoveryCoordinator?.present
    ),
    registerRetryFromPhaseCommand(
      orchestrator,
      stateService,
      logger,
      statusBar,
      outputWindow,
      recoveryCoordinator?.present
    ),
    ...(recoveryCoordinator ? [recoveryCoordinator] : []),
  ];
}
