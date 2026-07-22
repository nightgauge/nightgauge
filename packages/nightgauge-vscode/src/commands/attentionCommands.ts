/**
 * Action Center commands — Refresh, and the quick-pick resolve flow (ADR 015
 * §E, Issue #325).
 *
 * `nightgauge.attentionResolve` is bound to a card's click (TreeItem.command)
 * AND its inline `view/item/context` icon, so either affordance opens the
 * same quick-pick: the request's declared options (label + a client-derived
 * consequence description) plus, when the request declares `steer.enabled`, a
 * "Custom steer…" entry.
 *
 * "Custom steer" is NOT a third kind of action — `attention.resolve` always
 * requires a concrete `option_id` (ADR 015 §J: options are commands, the
 * registry is the security boundary). Picking it applies the request's own
 * `default_action` (the producer's declared *safe* choice) while attaching
 * the typed text as `steer_text`, which rides the existing feedback-context
 * path as pinned, non-blocking guidance (ADR 015 §G) rather than inventing a
 * new mutation. A request whose `default_action` is `expire_noop` (no
 * concrete option) has no safe vehicle for steer-only resolution — the flow
 * tells the operator to pick a listed option instead.
 *
 * This module also wires the two chrome effects that ride the same
 * `attention.event` push the tree provider already folds: the view badge
 * (open blocking-request count) and the `viewsWelcome` empty-state context
 * key, plus a toast on every newly **created** blocking request with an
 * "Open Action Center" button that focuses the view — no polling anywhere.
 */

import * as vscode from "vscode";
import * as os from "node:os";
import { IpcClient } from "../services/IpcClient";
import type {
  AttentionRequestView,
  AttentionEvent,
  AttentionOption,
} from "../services/IpcClientBase";
import {
  AttentionTreeProvider,
  AttentionRequestTreeItem,
  describeAttentionOption,
} from "../views/attention";
import type { Logger } from "../utils/logger";

export interface AttentionCommandDeps {
  provider: AttentionTreeProvider;
  treeView: vscode.TreeView<vscode.TreeItem>;
  logger: Logger;
}

/** Best-effort local actor for the resolution audit trail — never blocks or throws. */
function resolveActor(): string | undefined {
  try {
    return os.userInfo().username || undefined;
  } catch {
    return undefined;
  }
}

/** A quick-pick entry for a declared option, or the "Custom steer…" escape hatch. */
interface AttentionPickItem extends vscode.QuickPickItem {
  optionId?: string;
  isSteer?: boolean;
}

function buildPickItems(request: AttentionRequestView): AttentionPickItem[] {
  const items: AttentionPickItem[] = request.options.map((opt) => ({
    label: opt.label,
    description: describeAttentionOption(opt),
    optionId: opt.id,
  }));
  if (request.steer?.enabled) {
    items.push({
      label: "$(comment) Custom steer…",
      description:
        request.steer.hint || "Add free-text guidance for the pipeline without picking an action",
      isSteer: true,
    });
  }
  return items;
}

/** Apply a resolve call with progress + success/failure toast (scope item 4). */
async function runResolve(
  request: AttentionRequestView,
  optionId: string,
  optionLabel: string,
  steerText: string | undefined,
  logger: Logger
): Promise<void> {
  const ipcClient = IpcClient.getInstance();
  try {
    let ok = false;
    let alreadyResolved = false;
    await vscode.window.withProgress(
      {
        location: vscode.ProgressLocation.Notification,
        title: `Nightgauge: Resolving "${optionLabel}"…`,
        cancellable: false,
      },
      async () => {
        const result = await ipcClient.attentionResolve(
          request.id,
          optionId,
          resolveActor(),
          steerText
        );
        ok = result.ok;
        alreadyResolved = result.alreadyResolved;
      }
    );
    if (alreadyResolved) {
      vscode.window.showInformationMessage(
        "Nightgauge: This request was already resolved elsewhere."
      );
    } else if (ok) {
      vscode.window.showInformationMessage(`Nightgauge: Resolved — ${optionLabel}.`);
    } else {
      vscode.window.showWarningMessage(
        `Nightgauge: Resolution recorded, but applying "${optionLabel}" failed. Check the output log.`
      );
    }
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    logger.error("nightgauge.attentionResolve failed", {
      error: message,
      id: request.id,
      optionId,
    });
    vscode.window.showErrorMessage(`Nightgauge: Could not resolve the request — ${message}`);
  }
}

/** The "Custom steer…" path: apply the request's default_action while attaching free text. */
async function resolveWithSteer(request: AttentionRequestView, logger: Logger): Promise<void> {
  const defaultOption: AttentionOption | undefined = request.options.find(
    (o) => o.id === request.default_action
  );
  if (!defaultOption) {
    vscode.window.showErrorMessage(
      "Nightgauge: This request has no safe default action to steer through — pick one of the listed options instead."
    );
    return;
  }
  const steerText = await vscode.window.showInputBox({
    title: "Steer the pipeline",
    prompt: `Free-text guidance, applied as pinned context alongside "${defaultOption.label}".`,
    placeHolder: "e.g. skip acme-web this wave, it's a flaky test",
    ignoreFocusOut: true,
  });
  if (steerText === undefined) return; // cancelled
  const trimmed = steerText.trim();
  if (!trimmed) return;
  await runResolve(request, defaultOption.id, defaultOption.label, trimmed, logger);
}

/** The full click-to-resolve flow: quick-pick, then dispatch to the option or steer path. */
export async function resolveAttentionRequest(
  request: AttentionRequestView,
  logger: Logger
): Promise<void> {
  const picked = await vscode.window.showQuickPick(buildPickItems(request), {
    title: request.title,
    placeHolder: "Choose how to resolve this decision",
  });
  if (!picked) return;

  if (picked.isSteer) {
    await resolveWithSteer(request, logger);
    return;
  }
  if (!picked.optionId) return;
  await runResolve(request, picked.optionId, picked.label, undefined, logger);
}

/** True for a newly created, still-open, blocking-severity request (toast-worthy). */
function isNewBlockingRequest(evt: AttentionEvent): boolean {
  return (
    evt.action === "created" &&
    evt.request.lifecycle.state === "open" &&
    (evt.request.severity === "blocking_run" || evt.request.severity === "blocking_fleet")
  );
}

export function registerAttentionCommands(deps: AttentionCommandDeps): vscode.Disposable[] {
  const { provider, treeView, logger } = deps;
  const disposables: vscode.Disposable[] = [];

  // Badge (open blocking-request count) + the viewsWelcome empty-state context
  // key — both driven off the same tree-data change the provider already
  // fires on every `attention.event` fold. No separate IPC subscription.
  const updateChrome = () => {
    const blockingCount = provider.getOpenBlockingCount();
    treeView.badge =
      blockingCount > 0
        ? {
            value: blockingCount,
            tooltip: `${blockingCount} blocking decision${blockingCount === 1 ? "" : "s"} pending`,
          }
        : undefined;
    void vscode.commands.executeCommand(
      "setContext",
      "nightgauge.attentionHasRequests",
      provider.hasAny()
    );
  };
  updateChrome();
  disposables.push(provider.onDidChangeTreeData(updateChrome));

  // Toast on new blocking request (scope item 3) — driven by the same
  // `attention.event` push, re-broadcast by the provider after it folds the
  // event into tree state. No polling.
  disposables.push(
    provider.onDidReceiveEvent((evt) => {
      if (!isNewBlockingRequest(evt)) return;
      vscode.window
        .showWarningMessage(`Nightgauge: ${evt.request.title}`, "Open Action Center")
        .then((action) => {
          if (action === "Open Action Center") {
            void vscode.commands.executeCommand("nightgauge.attentionView.focus");
          }
        });
    })
  );

  disposables.push(
    vscode.commands.registerCommand("nightgauge.attentionRefresh", async () => {
      try {
        await provider.refresh();
      } catch (err) {
        logger.warn("nightgauge.attentionRefresh failed", {
          error: err instanceof Error ? err.message : String(err),
        });
      }
    })
  );

  disposables.push(
    vscode.commands.registerCommand(
      "nightgauge.attentionResolve",
      async (item?: AttentionRequestTreeItem) => {
        const request = item?.request;
        if (!request) return;
        await resolveAttentionRequest(request, logger);
      }
    )
  );

  return disposables;
}
