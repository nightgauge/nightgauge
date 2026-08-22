/**
 * `nightgauge.enableClaudeUsageStatusLine` (Issue #730).
 *
 * Wires `nightgauge hook claude-statusline` into Claude Code's `statusLine`
 * setting, which is what keeps the footer's Max-plan meter current.
 *
 * ## Why an explicit command rather than doing it on activation
 *
 * `~/.claude/settings.json` is the operator's file, not the extension's, and
 * `statusLine` is a visible surface they may already have opinions about.
 * Silently claiming it on activation would be the extension rearranging
 * another tool's UI without being asked. So the change is a command, it shows
 * the exact before/after before writing, and it round-trips: invoking it while
 * already wired offers to unwire and restores whatever was there first.
 *
 * The footer's tooltip carries a link to this command whenever the `claude`
 * adapter is reporting something other than a subscription window, so the
 * operator finds it at the moment they are looking at the wrong number.
 *
 * @see src/services/usage/claudeStatusLineSetup.ts — the settings rules
 * @see docs/decisions/018-adapter-usage-quota-model.md
 */

import * as vscode from "vscode";
import { BinaryResolver } from "../services/BinaryResolver";
import { describeLastObserved, probeClaudeFeedHealth } from "../services/usage/claudeFeedHealth";
import { repairStaleClaudeStatusLine } from "../services/usage/claudeStatusLineRepair";
import {
  buildStatusLineCommand,
  claudeSettingsPath,
  readClaudeSettings,
  readStatusLineState,
  resolveSettingsTarget,
  withStatusLineUnwired,
  withStatusLineWired,
  writeClaudeSettings,
} from "../services/usage/claudeStatusLineSetup";

export const ENABLE_CLAUDE_USAGE_STATUS_LINE_COMMAND = "nightgauge.enableClaudeUsageStatusLine";

/**
 * Register the command.
 *
 * Every failure path ends in a message the operator can act on. This touches a
 * file outside the workspace, so "it silently did nothing" is the one outcome
 * that would be worse than an error.
 */
export function registerEnableClaudeUsageStatusLineCommand(): vscode.Disposable {
  return vscode.commands.registerCommand(ENABLE_CLAUDE_USAGE_STATUS_LINE_COMMAND, async () => {
    const binaryPath = await BinaryResolver.fromVSCode().resolve();
    if (binaryPath === null) {
      void vscode.window.showErrorMessage(
        "Nightgauge: could not locate the nightgauge binary, so the Claude usage feed cannot be wired up. " +
          "Set nightgauge.backend.binaryPath and try again."
      );
      return;
    }

    const settingsPath = await resolveSettingsTarget(claudeSettingsPath());
    const settings = await readClaudeSettings(settingsPath);
    if (settings === null) {
      void vscode.window.showErrorMessage(
        `Nightgauge: ${settingsPath} is not a JSON object, so it was left untouched. ` +
          "Fix or remove it and run this command again."
      );
      return;
    }

    const state = readStatusLineState(settings);
    if (state.wired) {
      // "Is a command wired" is not "is the feed working" (#810). Asking the
      // first and answering as though it were the second is how an operator
      // whose feed had named a deleted binary for two days got told it was
      // already enabled, with Disable as the only offered action.
      const health = await probeClaudeFeedHealth({ settingsPath });
      if (health.state === "healthy") {
        await offerToUnwire(settingsPath, settings, state.delegate);
        return;
      }
      await offerToRepair(settingsPath, settings, state.delegate, health);
      return;
    }

    const nextCommand = buildStatusLineCommand(binaryPath, state.command);
    const preserved =
      state.command === null
        ? "You have no status line configured, so this becomes it."
        : `Your existing status line is preserved — it runs via --delegate and its output is what you keep seeing.`;
    const choice = await vscode.window.showInformationMessage(
      "Show your Claude Max usage in the Nightgauge footer?",
      {
        modal: true,
        detail:
          `${settingsPath}\n\n` +
          `statusLine.command:\n  ${nextCommand}\n\n` +
          `${preserved}\n\n` +
          "Claude Code hands its status line command your account's five-hour and weekly " +
          "utilization on every render. Nightgauge records it so the footer can show how much " +
          "of your plan is left instead of a dollar estimate. Nothing is sent anywhere — the " +
          "reading is written to ~/.nightgauge/usage/claude-rate-limits.json.",
      },
      "Enable"
    );
    if (choice !== "Enable") {
      return;
    }

    try {
      await writeClaudeSettings(settingsPath, withStatusLineWired(settings, binaryPath));
    } catch (error) {
      void vscode.window.showErrorMessage(
        `Nightgauge: failed to update ${settingsPath}: ${error instanceof Error ? error.message : String(error)}`
      );
      return;
    }

    // Claude Code reads settings.json per session, so an already-open session
    // keeps its old status line. Saying so avoids the operator concluding it
    // did not work when the footer stays on dollars for another few minutes.
    void vscode.window.showInformationMessage(
      "Nightgauge: Claude usage feed enabled. Start or restart a Claude Code session — the footer " +
        "switches to your plan's five-hour and weekly windows once the first reading arrives."
    );
  });
}

/**
 * Invoked when the feed is wired but is NOT producing readings.
 *
 * Names which of the two failures it found and when the feed last worked, and
 * offers Repair as the primary action. Disable stays available — an operator
 * who wants out should not have to fix a thing first — but it is no longer the
 * only choice, and the message no longer claims everything is fine.
 */
async function offerToRepair(
  settingsPath: string,
  settings: Record<string, unknown>,
  delegate: string | null,
  health: Awaited<ReturnType<typeof probeClaudeFeedHealth>>
): Promise<void> {
  const diagnosis =
    health.reason === "binary-missing"
      ? `The wired command names ${health.binary}, which no longer exists — an extension update ` +
        "replaces that directory."
      : health.reason === "unrecognized-command"
        ? "The wired status line is not in a shape Nightgauge wrote, so it cannot be checked or " +
          "repaired automatically."
        : "The wiring looks right, so nothing may be wrong: readings only arrive while a Claude " +
          "Code session is rendering its status line.";

  const canRepair = health.reason === "binary-missing";
  const actions = canRepair ? ["Repair", "Disable"] : ["Disable"];
  const choice = await vscode.window.showInformationMessage(
    canRepair
      ? "The Claude usage feed is enabled but not working."
      : "The Claude usage feed is enabled but has not recorded anything.",
    {
      modal: true,
      detail:
        `${settingsPath}\n\n${diagnosis}\n\n` + `Feed status: ${describeLastObserved(health)}.`,
    },
    ...actions
  );

  if (choice === "Repair") {
    const outcome = await repairStaleClaudeStatusLine({ settingsPath });
    if (outcome === "repaired") {
      void vscode.window.showInformationMessage(
        "Nightgauge: Claude usage feed repaired. Start or restart a Claude Code session — the " +
          "footer switches back to your plan's windows once the first reading arrives."
      );
    } else {
      void vscode.window.showErrorMessage(
        "Nightgauge: could not repair the Claude usage feed — no nightgauge binary resolved. " +
          "Set nightgauge.backend.binaryPath and try again. Your settings were left untouched."
      );
    }
    return;
  }
  if (choice === "Disable") {
    await unwire(settingsPath, settings, delegate);
  }
}

/** Invoked when the feed is wired AND working: offer the round trip back. */
async function offerToUnwire(
  settingsPath: string,
  settings: Record<string, unknown>,
  delegate: string | null
): Promise<void> {
  const restores =
    delegate === null
      ? "The statusLine setting will be removed."
      : `Your previous status line will be restored:\n  ${delegate}`;
  const choice = await vscode.window.showInformationMessage(
    "The Claude usage feed is already enabled.",
    { modal: true, detail: `${settingsPath}\n\n${restores}` },
    "Disable"
  );
  if (choice !== "Disable") {
    return;
  }
  await unwire(settingsPath, settings, delegate);
}

/**
 * The round trip back, shared by both prompts.
 *
 * `withStatusLineUnwired` lifts the delegate back into place, so disabling a
 * BROKEN feed restores the operator's own status line exactly as disabling a
 * healthy one does — the guarantee does not get weaker because the feed was
 * unhealthy when they gave up on it.
 */
async function unwire(
  settingsPath: string,
  settings: Record<string, unknown>,
  _delegate: string | null
): Promise<void> {
  try {
    await writeClaudeSettings(settingsPath, withStatusLineUnwired(settings));
  } catch (error) {
    void vscode.window.showErrorMessage(
      `Nightgauge: failed to update ${settingsPath}: ${error instanceof Error ? error.message : String(error)}`
    );
    return;
  }
  void vscode.window.showInformationMessage(
    "Nightgauge: Claude usage feed disabled. The footer falls back to locally-derived spend once " +
      "the last reading's window resets."
  );
}
