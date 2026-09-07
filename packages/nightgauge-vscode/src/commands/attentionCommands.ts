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
 * `Open in browser` leads the list whenever the card HAS a forge URL —
 * declared by the producer or derived from repo + issue by `attentionCardUrl`
 * (#1509) — because for a condition no verb in the registry can repair — a red
 * default branch, a PR waiting on a reviewer — following the link IS the
 * action. `View details` follows it whenever the card has a body: the card asks
 * for a decision and the body is the evidence for it, and nothing else in this
 * flow renders it.
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
import * as fs from "node:fs";
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
  attentionCardUrl,
} from "../views/attention";
import { pipelineFileCandidates } from "../utils/issueContextCandidates";
import type { Logger } from "../utils/logger";

export interface AttentionCommandDeps {
  provider: AttentionTreeProvider;
  treeView: vscode.TreeView<vscode.TreeItem>;
  logger: Logger;
  /** The repo-scoped sweep (issue #93). Optional so a window without one still
   * gets the full run-scoped Action Center. */
  sweep?: { sweep(trigger: "manual" | "view-refresh"): Promise<unknown> };
}

/**
 * The shortest actor the daemon will record (`attention.MinActorLen`), which is
 * in turn the shortest the platform mirror accepts. Kept here so a name this
 * side cannot use is dropped rather than sent and refused (#1539).
 */
const MIN_ACTOR_LENGTH = 3;

/** Best-effort local actor for the resolution audit trail — never blocks or throws. */
function resolveActor(): string | undefined {
  try {
    const name = os.userInfo().username?.trim();
    // A name too short to be recorded is worse than no name: the daemon refuses
    // it and the operator's click fails, whereas `undefined` lets the daemon
    // label the resolution with the surface that actually acted ("vscode").
    return name && name.length >= MIN_ACTOR_LENGTH ? name : undefined;
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
  /** Renders the card's evidence in an editor. Resolves nothing. */
  viewDetails?: boolean;
}

export function buildPickItems(request: AttentionRequestView): AttentionPickItem[] {
  const items: AttentionPickItem[] = [];

  // The link comes FIRST when the card has one. For a repo-scoped card whose
  // only declared option is a dismiss — a red default branch, a PR waiting on a
  // reviewer — no verb in the registry can fix the condition, so the honest
  // primary action is "go look at the thing". Burying that under the options
  // would make the quick-pick's default the one choice that changes nothing.
  const url = attentionCardUrl(request);
  if (url) {
    items.push({
      label: "$(link-external) Open in browser",
      description: request.context.blocker
        ? `Opens the ${request.context.blocker.split(":")[0]} this card is about`
        : "Opens the forge object this card is about",
      openUrl: url,
    });
  }

  // The evidence entry. A card that asks for a high-impact approval and shows
  // only four verbs is asking the operator to decide blind: the gate's reason
  // lives in `body`, which the quick pick never rendered, and the plan being
  // approved lives in a file on disk. Neither is reachable from here without
  // this entry, and reading them must not resolve anything.
  if (request.body.trim()) {
    items.push({
      label: "$(eye) View details",
      description: "Opens the card's reason, options and plan in the editor. Changes nothing.",
      viewDetails: true,
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

/** Open the card's forge URL — declared or derived — in the operator's browser. */
export async function openAttentionLink(
  request: AttentionRequestView,
  logger: Logger
): Promise<void> {
  const url = attentionCardUrl(request);
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

/** The producer id the Go orchestrator raises architecture-approval cards under
 * (`producerArchitectureApprove` in `internal/orchestrator/attention_wiring.go`).
 * Only these cards have a local plan file to append. */
const ARCHITECTURE_APPROVAL_PRODUCER = "architecture-approval";

/**
 * The plan the operator is being asked to approve, read from disk.
 *
 * `planning-{N}.json` is written by the planning stage into whichever root the
 * run actually used — the extension's `<repo>/.worktrees/issue-N`, the Go
 * manager's `<repo>/.nightgauge/worktrees/<name>-issue-N`, or the repo root
 * when the run took no worktree. `pipelineFileCandidates` is the shared list of
 * those layouts (#994/#1206); reading only one of them is how a reader learns
 * to report "no plan" for the majority of runs.
 *
 * Returns undefined when nothing is on disk — the card still renders, minus the
 * plan section. Never throws.
 */
export function readArchitecturePlan(request: AttentionRequestView): string | undefined {
  const issue = request.context.issue;
  if (!issue || request.producer !== ARCHITECTURE_APPROVAL_PRODUCER) return undefined;
  const roots = (vscode.workspace.workspaceFolders ?? []).map((f) => f.uri.fsPath);
  for (const root of roots) {
    const candidates = pipelineFileCandidates(
      root,
      "",
      request.context.repo,
      issue,
      `planning-${issue}.json`
    );
    for (const file of candidates) {
      try {
        const raw = fs.readFileSync(file, "utf8");
        // Pretty-printed when it parses, raw when it does not — an unparseable
        // plan is still the plan, and hiding it would leave the operator with
        // less than they had.
        let rendered = raw;
        try {
          rendered = JSON.stringify(JSON.parse(raw), null, 2);
        } catch {
          /* keep the raw text */
        }
        return `${file}\n\n\`\`\`json\n${rendered}\n\`\`\``;
      } catch {
        // Next candidate.
      }
    }
  }
  return undefined;
}

/**
 * The markdown document behind `View details` — the whole card, in the order an
 * operator reads it: what is being asked, why, what each option would do, and
 * (for an architecture approval) the actual plan.
 */
export function renderAttentionDetails(request: AttentionRequestView): string {
  const lines: string[] = [`# ${request.title}`, ""];

  const facts: string[] = [
    `**Producer:** ${request.producer}`,
    `**Severity:** ${request.severity}`,
  ];
  const number = request.context.issue || request.context.pr;
  facts.push(
    number ? `**Scope:** ${request.context.repo}#${number}` : `**Scope:** ${request.context.repo}`
  );
  if (request.context.stage) facts.push(`**Stage:** ${request.context.stage}`);
  const url = attentionCardUrl(request);
  if (url) facts.push(`**Link:** ${url}`);
  lines.push(facts.join("  \n"), "");

  if (request.body.trim()) {
    lines.push("## Why this card exists", "", request.body.trim(), "");
  }

  if (request.options.length > 0) {
    lines.push("## Options", "");
    for (const opt of request.options) {
      lines.push(`- **${opt.label}** — ${describeAttentionOption(opt, request)}`);
    }
    lines.push("");
  }

  const plan = readArchitecturePlan(request);
  if (plan) {
    lines.push("## The plan being approved", "", plan, "");
  }

  lines.push("_Reading this changed nothing — the card is still open._");
  return lines.join("\n");
}

/** Open the card's evidence as a read-only markdown document. Resolves nothing. */
export async function showAttentionDetails(
  request: AttentionRequestView,
  logger: Logger
): Promise<void> {
  try {
    const doc = await vscode.workspace.openTextDocument({
      language: "markdown",
      content: renderAttentionDetails(request),
    });
    await vscode.window.showTextDocument(doc, { preview: true });
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    logger.warn("nightgauge.attentionViewDetails failed", { error: message, id: request.id });
    vscode.window.showErrorMessage(`Nightgauge: Could not open the card details — ${message}`);
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
  if (picked.viewDetails) {
    // Deliberately does NOT re-open the quick pick: the operator is being sent
    // to a document to read, and re-entering this function from itself makes
    // the flow unbounded. Clicking the card again returns here.
    await showAttentionDetails(request, logger);
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
