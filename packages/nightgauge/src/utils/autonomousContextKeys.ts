/**
 * Single source of truth for the VSCode `setContext` keys that drive the
 * pipeline view's autonomous-mode toolbar buttons (#3309).
 *
 * The pipeline view title bar surfaces a different button set per autonomous
 * state:
 *
 *   stopped / complete / budget_exhausted / crashed → Run
 *   running                                          → Pause + Stop
 *   paused / safety_tripped                          → Resume + Stop
 *
 * Without this helper, the historical `nightgauge.autonomousRunning`
 * context key was set in 12+ places to a binary running/not-running flag,
 * which left the user with no button at all when status was paused or
 * safety_tripped — they had to discover the command palette entry. Routing
 * every status transition through this helper guarantees the toolbar always
 * matches the live status.
 */
import * as vscode from "vscode";
import { AutonomousActivityState } from "./autonomousActivityState";

export type AutonomousLifecycleStatus =
  | "running"
  | "paused"
  | "safety_tripped"
  | "stopped"
  | "complete"
  | "budget_exhausted"
  | "crashed"
  | "init"
  | "cancelled";

const RUNNING_STATUSES: ReadonlySet<string> = new Set<string>(["running"]);
const RESUMABLE_STATUSES: ReadonlySet<string> = new Set<string>(["paused", "safety_tripped"]);

/**
 * Set the two `when`-clause context keys that drive the toolbar.
 *   - `nightgauge.autonomousRunning` — true iff the dispatch loop is
 *     actively running candidates. Used to show Pause + Stop and to gate
 *     things like the pickup-issue button that need a clean-slate state.
 *   - `nightgauge.autonomousResumable` — true iff the user can recover
 *     by clicking Resume (paused or safety_tripped). Drives the visible
 *     Resume button and a still-visible Stop button.
 *
 * The two keys are mutually exclusive: a status is either Running, Resumable,
 * or neither (idle/terminal) — never both. The Run button's `when` clause
 * checks `!autonomousRunning && !autonomousResumable` so it only appears when
 * the user has no other recovery action available.
 */
export function setAutonomousContextKeys(status: string): void {
  const isRunning = RUNNING_STATUSES.has(status);
  const isResumable = RESUMABLE_STATUSES.has(status);
  vscode.commands.executeCommand("setContext", "nightgauge.autonomousRunning", isRunning);
  vscode.commands.executeCommand("setContext", "nightgauge.autonomousResumable", isResumable);
  // Feed the in-process demand-driven-fetch gate (#360). Every autonomous
  // status transition already routes through here, so this keeps the tree
  // providers' background-poll gate in sync without any polling of its own.
  AutonomousActivityState.instance.setStatus(status);
}

/** Test-only — observe how a given status maps to the two context flags. */
export function _classifyForTests(status: string): { running: boolean; resumable: boolean } {
  return {
    running: RUNNING_STATUSES.has(status),
    resumable: RESUMABLE_STATUSES.has(status),
  };
}
