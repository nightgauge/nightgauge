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
 * Repo-scoped cards (issue #93) reach the same quick-pick with two additions.
 * `Open in browser` leads the list whenever the card carries a `context.url`,
 * because for a condition no verb in the registry can repair — a red default
 * branch, a PR waiting on a reviewer — following the link IS the action.
 * `Mute until this changes` appears on standing cards: it silences alerting
 * without resolving, so the card stays in the inbox at its severity and
 * re-alerts the moment the condition's fingerprint moves.
 *
 * This module also wires the chrome effects that ride the same
 * `attention.event` push the tree provider already folds: the view badge
 * (alert-worthy blocking count — muted and acknowledged excluded), the
 * `viewsWelcome` empty-state context key, a view-header line naming any
 * fleet-wide blocker so it is legible without expanding a node, and a toast on
 * genuinely new or materially changed blocking requests with an "Open Action
 * Center" button that focuses the view — no polling anywhere.
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
  /** The repo-scoped sweep (issue #93). Optional so a window without one still
   * gets the full run-scoped Action Center. */
  sweep?: { sweep(trigger: "manual" | "view-refresh"): Promise<unknown> };
}

/** Best-effort local actor for the resolution audit trail — never blocks or throws. */
function resolveActor(): string | undefined {
  try {
    return os.userInfo().username || undefined;
  } catch {
    return undefined;
  }
}

/** A quick-pick entry for a declared option, the link, or the "Custom steer…" escape hatch. */
interface AttentionPickItem extends vscode.QuickPickItem {
  optionId?: string;
  isSteer?: boolean;
  openUrl?: string;
  muteAction?: "mute" | "unmute";
}

export function buildPickItems(request: AttentionRequestView): AttentionPickItem[] {
  const items: AttentionPickItem[] = [];

  // The link comes FIRST when the card has one. For a repo-scoped card whose
  // only declared option is a dismiss — a red default branch, a PR waiting on a
  // reviewer — no verb in the registry can fix the condition, so the honest
  // primary action is "go look at the thing". Burying that under the options
  // would make the quick-pick's default the one choice that changes nothing.
  if (request.context.url) {
    items.push({
      label: "$(link-external) Open in browser",
      description: request.context.blocker
        ? `Opens the ${request.context.blocker.split(":")[0]} this card is about`
        : "Opens the forge object this card is about",
      openUrl: request.context.url,
    });
  }

  items.push(
    ...request.options.map((opt) => ({
      label: opt.label,
      description: describeAttentionOption(opt, request),
      optionId: opt.id,
    }))
  );

  // Mute/unmute are offered on STANDING cards only. On an event-scoped card
  // there is no condition to mute "until it changes" — the fingerprint that
  // mute is measured against does not exist, so the entry would promise
  // semantics the record cannot deliver.
  if (request.standing) {
    items.push(
      request.lifecycle.muted
        ? {
            label: "$(bell) Unmute",
            description: "Restores alerting on this condition.",
            muteAction: "unmute" as const,
          }
        : {
            label: "$(bell-slash) Mute until this changes",
            description:
              "Keeps the card, stops the alerts. Re-alerts if the condition itself changes.",
            muteAction: "mute" as const,
          }
    );
  }

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

/** Open a card's `context.url` in the operator's browser. */
export async function openAttentionLink(
  request: AttentionRequestView,
  logger: Logger
): Promise<void> {
  const url = request.context.url;
  if (!url) return;
  try {
    await vscode.env.openExternal(vscode.Uri.parse(url));
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    logger.warn("nightgauge.attentionOpenLink failed", { error: message, id: request.id, url });
    vscode.window.showErrorMessage(`Nightgauge: Could not open ${url}`);
  }
}

/** Mute or unmute a standing card. Neither resolves it — the card stays in the
 * inbox at its severity, silenced until its condition changes. */
export async function setAttentionMute(
  request: AttentionRequestView,
  action: "mute" | "unmute",
  logger: Logger
): Promise<void> {
  const ipcClient = IpcClient.getInstance();
  try {
    const result =
      action === "mute"
        ? await ipcClient.attentionMute(request.id, resolveActor())
        : await ipcClient.attentionUnmute(request.id, resolveActor());
    if (action === "mute" && !result.muted) {
      // The store declined — the request already reached a terminal state.
      vscode.window.showInformationMessage(
        "Nightgauge: This request is already closed — nothing to mute."
      );
      return;
    }
    vscode.window.showInformationMessage(
      action === "mute"
        ? "Nightgauge: Muted — this card stays in the inbox and re-alerts if the condition changes."
        : "Nightgauge: Unmuted — alerting restored."
    );
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    logger.error(`nightgauge.attention${action === "mute" ? "Mute" : "Unmute"} failed`, {
      error: message,
      id: request.id,
    });
    vscode.window.showErrorMessage(`Nightgauge: Could not ${action} the request — ${message}`);
  }
}

/** The full click-to-resolve flow: quick-pick, then dispatch to the chosen path. */
export async function resolveAttentionRequest(
  request: AttentionRequestView,
  logger: Logger
): Promise<void> {
  const picked = await vscode.window.showQuickPick(buildPickItems(request), {
    title: request.title,
    placeHolder: "Choose how to resolve this decision",
  });
  if (!picked) return;

  if (picked.openUrl) {
    await openAttentionLink(request, logger);
    return;
  }
  if (picked.muteAction) {
    await setAttentionMute(request, picked.muteAction, logger);
    return;
  }
  if (picked.isSteer) {
    await resolveWithSteer(request, logger);
    return;
  }
  if (!picked.optionId) return;
  await runResolve(request, picked.optionId, picked.label, undefined, logger);
}

/**
 * True for a request that should INTERRUPT, as opposed to one the tree should
 * merely re-render.
 *
 * `created` always qualifies. `updated` qualifies only for a STANDING card,
 * because the two write paths mean different things by it: reconciliation
 * (issue #92) emits `updated` only when the condition's fingerprint materially
 * moved — a second check going red — and `refreshed` for the nine re-observations
 * that changed nothing, which is what makes `updated` genuine news there. The
 * event-scoped `raise` path emits `updated` for any re-raise of an open request,
 * identical payload included, so toasting it would fire on every retry of the
 * same run-scoped condition. `refreshed`, `acknowledged`, `muted` and the
 * terminal actions never interrupt, and a muted card never interrupts at all.
 *
 * This is the surface-side counterpart of the store's `JournalEntry.ShouldNotify`:
 * alerting, not rendering. Every transition still reaches the tree.
 */
export function isToastWorthy(evt: AttentionEvent): boolean {
  const req = evt.request;
  if (evt.action !== "created" && !(evt.action === "updated" && req.standing)) return false;
  if (req.lifecycle.muted) return false;
  if (req.lifecycle.state !== "open") return false;
  return req.severity === "blocking_run" || req.severity === "blocking_fleet";
}

export function registerAttentionCommands(deps: AttentionCommandDeps): vscode.Disposable[] {
  const { provider, treeView, logger, sweep } = deps;
  const disposables: vscode.Disposable[] = [];

  // Badge (alert-worthy blocking count) + the viewsWelcome empty-state context
  // key + the view-header summary — all driven off the same tree-data change
  // the provider already fires on every `attention.event` fold. No separate IPC
  // subscription.
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
    // A fleet-wide blocker has to be legible without expanding anything. The
    // header description survives a collapsed tree and a scrolled sidebar,
    // which the card itself does not — and an invisible fleet blocker is the
    // exact failure the epic was opened for.
    const fleetBlocker = provider.getTopFleetBlocker();
    treeView.description = fleetBlocker?.title;
  };
  updateChrome();
  disposables.push(provider.onDidChangeTreeData(updateChrome));

  // Toast on an alert-worthy blocking transition — driven by the same
  // `attention.event` push, re-broadcast by the provider after it folds the
  // event into tree state. No polling. A refreshed standing card re-renders in
  // the tree and is deliberately silent here: the sweep re-observes a red
  // `main` every cycle, and toasting each observation is how an operator learns
  // to dismiss the notification without reading it.
  disposables.push(
    provider.onDidReceiveEvent((evt) => {
      if (!isToastWorthy(evt)) return;
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
      // Trigger 2 of the sweep's four invocation points: an explicit refresh is
      // the operator asking for the CURRENT state of the repo, which the local
      // store only knows if something evaluated it. Fire and forget — the tree
      // re-reads immediately, and any card the sweep raises arrives through the
      // `attention.event` push a moment later.
      void sweep?.sweep("view-refresh");
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
    vscode.commands.registerCommand("nightgauge.attentionSweep", async () => {
      if (!sweep) {
        vscode.window.showInformationMessage(
          "Nightgauge: The repo-scoped sweep is not available in this window."
        );
        return;
      }
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Nightgauge: Checking repositories for blockers…",
          cancellable: false,
        },
        async () => {
          await sweep.sweep("manual");
        }
      );
      await provider.refresh();
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

  disposables.push(
    vscode.commands.registerCommand(
      "nightgauge.attentionOpenLink",
      async (item?: AttentionRequestTreeItem) => {
        const request = item?.request;
        if (!request) return;
        await openAttentionLink(request, logger);
      }
    )
  );

  disposables.push(
    vscode.commands.registerCommand(
      "nightgauge.attentionMute",
      async (item?: AttentionRequestTreeItem) => {
        const request = item?.request;
        if (!request) return;
        await setAttentionMute(request, "mute", logger);
      }
    )
  );

  disposables.push(
    vscode.commands.registerCommand(
      "nightgauge.attentionUnmute",
      async (item?: AttentionRequestTreeItem) => {
        const request = item?.request;
        if (!request) return;
        await setAttentionMute(request, "unmute", logger);
      }
    )
  );

  return disposables;
}
