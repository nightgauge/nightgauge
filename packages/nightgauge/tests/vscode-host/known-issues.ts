/**
 * Findings this tier surfaced on its first run, recorded rather than fixed.
 *
 * The point of a smoke tier is to make a class of failure observable. The
 * first time it runs it observes the backlog of failures that accumulated
 * while nothing was looking — and fixing product code from inside the test
 * PR would bury exactly the evidence the tier exists to produce.
 *
 * So each list below is a **shrinking baseline**, not an exemption:
 *
 *  - a new entry appearing in reality that is not listed here FAILS the run;
 *  - a listed entry is expected to be deleted when its issue is fixed.
 *
 * Nothing here is a wildcard, and nothing here is "known flaky". Each entry
 * names one concrete thing that is wrong, or one concrete thing about the
 * test environment that is not wrong.
 */

/**
 * Contributed in `src/manifest/index.ts` (and therefore in
 * `package.json contributes.commands`) with no `registerCommand` anywhere in
 * `src/`.
 *
 * Empty: `nightgauge.selectAll` was the only entry (#764) — its contribution
 * and orphaned `ProjectBoardTreeProvider.selectAll()` implementation were
 * both deleted rather than wired up, since the provider they belonged to is
 * never instantiated post-#2184 (the top-level project board views it
 * rendered were removed; status groups now live under each repository in the
 * Repositories view).
 */
export const CONTRIBUTED_WITHOUT_REGISTRATION: readonly string[] = [];

/** Why a registered command carries no `contributes.commands` entry. */
export type UncontributedReason =
  /** Invoked by clicking a `TreeItem` whose `command` field names it directly. */
  | "tree-item"
  /** Invoked by clicking a `StatusBarItem` whose `command` field names it. */
  | "status-bar"
  /** Invoked via `executeCommand` from inside one of our own webview panels
   * (a button, a link) — reachable, just not from the palette. */
  | "webview-internal"
  /** Invoked via `executeCommand` as the action button on a notification or
   * a follow-up prompt, never directly by name. */
  | "notification"
  /** NOT classified as deliberate — genuinely unreachable, but resolving it
   * (contribute or delete) depends on a fact this repo cannot check. Kept in
   * the baseline as a tracked finding rather than silently mislabeled. */
  | "unverified-external";

export interface UncontributedCommand {
  readonly id: string;
  readonly reason: UncontributedReason;
  /** Where the binding actually lives, so the classification is checkable. */
  readonly note: string;
}

/**
 * Registered at runtime but absent from `contributes.commands`.
 *
 * These are invisible to the palette and to the `generate-package-contributions --check`
 * drift gate, so nothing else in the repository can see them at all.
 * `commands.suite.ts` asserts on the `id`s below — every entry is classified,
 * not merely counted, and `nightgauge.autonomousClearQuotaCooldown`-shaped
 * bugs (a real UI-facing feature with nothing wiring it up) are not allowed to
 * hide behind "most of this list looks deliberate" (#766).
 *
 * Fixed in #766: `saveQueryAs`, `autonomousClearIssueFailures`,
 * `refreshKnowledgeView`, `runSettingsMigration`,
 * `showNotifierSettings`, and `showPipelineSummary` were all genuinely
 * unreachable (no tree item, status bar, webview, or notification bound any
 * of them) and are now contributed. `openAnalyticsDashboard`,
 * `openAuditDashboard`, `openComplianceReports`, and `openCostForecast` are
 * NOT fixed — see the note on each below.
 *
 * Excluded from this list, structurally rather than by name: the five
 * commands VSCode synthesizes for every contributed view
 * (`.focus`, `.open`, `.removeView`, `.resetViewLocation`,
 * `.toggleVisibility`). Those are the platform's, not ours — see
 * `syntheticViewCommandIds()` in `commands.suite.ts`.
 */
export const REGISTERED_WITHOUT_CONTRIBUTION: readonly UncontributedCommand[] = [
  {
    id: "nightgauge.configureForgeInstance",
    reason: "webview-internal",
    note: "views/settings/SettingsPanel.ts — invoked from the Settings webview's forge section.",
  },
  {
    id: "nightgauge.fixAutoMergeSetting",
    reason: "notification",
    note: "bootstrap/services.ts — action button on the auto-merge-misconfigured notification.",
  },
  {
    id: "nightgauge.openCurrentTabInBrowser",
    reason: "webview-internal",
    note: 'views/dashboard/Dashboard.ts — invoked from a dashboard webview "open in browser" action.',
  },
  {
    id: "nightgauge.pipelineConnectivityAction",
    reason: "status-bar",
    note: "PipelineConnectivityStatusItem — its default commandId, bound on click.",
  },
  {
    id: "nightgauge.platform.switchEnvironment",
    reason: "status-bar",
    note: "PlatformEnvironmentStatusBarItem — its default commandId, bound on click.",
  },
  {
    id: "nightgauge.reconnectEventStreams",
    reason: "status-bar",
    note: "EventStreamStatusBarItem — its default commandId, bound on click.",
  },
  {
    id: "nightgauge.retryWorkspaceSync",
    reason: "status-bar",
    note: "WorkspaceSyncStatusItem's RETRY_COMMAND — bound on click.",
  },
  {
    id: "nightgauge.showPlatformStatus",
    reason: "status-bar",
    note: "PlatformStatusBarItem / PlatformStatusBar — both set it as their status bar command.",
  },
];

export interface KnownFault {
  /** Matched against the fault's stringified reason/stack. */
  readonly signature: RegExp;
  readonly note: string;
}

/**
 * Unhandled rejections that escape during startup.
 *
 * The one remaining entry is an artefact of the host VSCode refusing modal
 * dialogs in test mode, not a product defect — recorded here because the
 * assertion is the same either way: anything NOT listed fails the run.
 */
export const KNOWN_STARTUP_REJECTIONS: readonly KnownFault[] = [
  {
    signature: /DialogService: refused to show dialog in tests/,
    note:
      "TEST ENVIRONMENT, not a defect: the telemetry consent prompt is a modal, and a " +
      "VSCode window launched with --extensionTestsPath rejects every modal by design. " +
      "In a real window the prompt resolves with the user's answer.",
  },
];

/** Faults with no matching entry above — the ones that must fail the run. */
export function unexpectedFaults<T extends { detail: string }>(faults: readonly T[]): T[] {
  return faults.filter(
    (fault) => !KNOWN_STARTUP_REJECTIONS.some((known) => known.signature.test(fault.detail))
  );
}
