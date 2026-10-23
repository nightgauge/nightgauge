/**
 * Activation-time repair of a stale Claude usage feed wiring (Issue #807).
 *
 * ## The failure this exists for
 *
 * Wiring the feed writes an absolute, version-stamped extension bundle path
 * into `~/.claude/settings.json`. An extension update installs a new
 * version-stamped directory and removes the old one, so the wired command
 * names a binary that is gone. Claude Code keeps invoking it on every render,
 * the invocation fails silently, and nothing has written to
 * `ClaudeRateLimitStore` since the update. The footer then falls back to
 * locally-derived dollar windows — reporting a *different billing model* than
 * the operator is on, which is exactly what ADR 018 exists to prevent.
 *
 * Nothing notices, because `readStatusLineState` decides `wired` from the verb
 * substring alone. Recognising a command is not the same as knowing it runs.
 *
 * ## Why this one is allowed to write without asking
 *
 * `enableClaudeUsageStatusLine` is deliberately an explicit, confirmed command:
 * claiming another tool's `statusLine` on activation would be the extension
 * rearranging a surface it was not invited to. This path is different in kind.
 * It changes nothing about the operator's intent — they already enabled the
 * feed — it only re-points a path the extension itself wrote and then
 * invalidated by updating. Leaving it broken and prompting would be asking the
 * operator to approve the repair of our own breakage on every update.
 *
 * The restraint lives in `repairStatusLineBinary`: a command shape this
 * extension did not write is never touched, and a stale path with no
 * resolvable replacement is left exactly as it is.
 *
 * @see src/services/usage/claudeStatusLineSetup.ts — the settings rules
 * @see docs/GO_BINARY.md#hook-diagnostics-never-touch-stdout-356 — the same
 *      stale-recorded-bundle divergence class, on the Go side
 */

import { BinaryResolver } from "../BinaryResolver";
import {
  claudeSettingsPath,
  readClaudeSettings,
  repairStatusLineBinary,
  resolveSettingsTarget,
  writeClaudeSettings,
  type StatusLineRepairResult,
} from "./claudeStatusLineSetup";

/** Minimal logger surface, so this module needs no `vscode` import. */
export interface RepairLogger {
  info(message: string): void;
  warn(message: string): void;
}

export interface RepairStaleStatusLineDeps {
  resolveBinary?: () => Promise<string | null>;
  settingsPath?: string;
  logger?: RepairLogger;
}

/**
 * Re-point the wired status line at a live binary, once, at activation.
 *
 * Never throws: this runs on the activation path and a settings file the
 * extension cannot read is not a reason to fail activation. Every outcome that
 * is not a plain "healthy" is logged, because a feed that quietly stays dead is
 * the defect being fixed.
 */
export async function repairStaleClaudeStatusLine(
  deps: RepairStaleStatusLineDeps = {}
): Promise<StatusLineRepairResult["outcome"] | "error"> {
  const log = deps.logger;
  try {
    const settingsPath = await resolveSettingsTarget(deps.settingsPath ?? claudeSettingsPath());
    const settings = await readClaudeSettings(settingsPath);
    if (settings === null) {
      log?.warn(
        `Claude usage feed: ${settingsPath} is not a JSON object, so it was left untouched.`
      );
      return "error";
    }

    const resolveBinary = deps.resolveBinary ?? (() => BinaryResolver.fromVSCode().resolve());
    const result = await repairStatusLineBinary(settings, { resolveBinary });

    switch (result.outcome) {
      case "repaired":
        // Written through writeClaudeSettings/resolveSettingsTarget like every
        // other mutation here: atomic rename, symlink followed not replaced.
        await writeClaudeSettings(settingsPath, result.settings);
        log?.info(
          `Claude usage feed: the wired binary ${result.staleBinary} no longer exists ` +
            `(an extension update removed it). Re-pointed the status line at ${result.binary}.`
        );
        break;
      case "unresolvable":
        log?.warn(
          `Claude usage feed: the wired binary ${result.staleBinary} no longer exists and no ` +
            "nightgauge binary could be resolved, so ~/.claude/settings.json was left untouched. " +
            "The feed will stay dead until a binary is available — set nightgauge.backend.binaryPath."
        );
        break;
      case "unrecognized":
        log?.warn(
          "Claude usage feed: the wired status line is not in a shape this extension wrote, " +
            "so it was left untouched. Re-run “Show my 5-hour and weekly limits” if the feed is dead."
        );
        break;
      default:
        break;
    }
    return result.outcome;
  } catch (error) {
    log?.warn(
      `Claude usage feed: stale-path check failed: ${error instanceof Error ? error.message : String(error)}`
    );
    return "error";
  }
}
