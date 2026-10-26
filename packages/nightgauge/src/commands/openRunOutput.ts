/**
 * Open Run Output command — open a CLI-started run's session log (#586)
 *
 * A run started with `nightgauge run` in a terminal streams its adapter output
 * there, not to this extension. What the CLI does write is a session log of
 * stage, phase and scheduler narration (`cmd/nightgauge/run_session_log.go`),
 * which the run's tree node names once it exists. This opens it.
 */

import * as vscode from "vscode";

/** The run node this command is invoked from; `ConcurrentSlotTreeItem`. */
interface RunOutputItem {
  issueNumber?: number;
  runOutputLog?: string;
}

/** Register `nightgauge.openRunOutput`. */
export function registerOpenRunOutputCommand(): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.openRunOutput",
    async (item?: RunOutputItem) => {
      const logPath = item?.runOutputLog;
      if (!logPath) {
        const which = item?.issueNumber ? `issue #${item.issueNumber}` : "this run";
        vscode.window.showWarningMessage(
          `No run output found for ${which}. Use this on a CLI-started run once its first stage has begun.`
        );
        return;
      }
      try {
        const doc = await vscode.workspace.openTextDocument(vscode.Uri.file(logPath));
        await vscode.window.showTextDocument(doc, { preview: false });
      } catch (err) {
        vscode.window.showErrorMessage(
          `Could not open run output ${logPath}: ${err instanceof Error ? err.message : String(err)}`
        );
      }
    }
  );
}
