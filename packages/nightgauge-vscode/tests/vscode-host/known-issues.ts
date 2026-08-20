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
 * `nightgauge.selectAll` — the only implementation is
 * `ProjectBoardTreeProvider.selectAll()`, a method that no command binds to.
 * The palette entry "Nightgauge: Select All Issues" throws
 * `command 'nightgauge.selectAll' not found` when a user picks it. This is
 * the exact failure #745 predicted, found on the tier's first run.
 */
export const CONTRIBUTED_WITHOUT_REGISTRATION: readonly string[] = ["nightgauge.selectAll"];

/**
 * Registered at runtime but absent from `contributes.commands`.
 *
 * These are invisible to the palette and to the `generate-package-contributions --check`
 * drift gate, so nothing else in the repository can see them at all. Most look
 * deliberate — a command bound to a tree item, a status-bar item, or a
 * notification button does not need a palette entry — but "deliberate" is a
 * judgement no tool has ever made about this list, and a few (`saveQueryAs`,
 * `openAnalyticsDashboard`, `openAuditDashboard`, `openComplianceReports`,
 * `openCostForecast`) read like user-facing features with no way to reach them.
 *
 * Excluded from this list, structurally rather than by name: the five
 * commands VSCode synthesizes for every contributed view
 * (`.focus`, `.open`, `.removeView`, `.resetViewLocation`,
 * `.toggleVisibility`). Those are the platform's, not ours — see
 * `syntheticViewCommandIds()` in `commands.suite.ts`.
 */
export const REGISTERED_WITHOUT_CONTRIBUTION: readonly string[] = [
  "nightgauge.activeKnowledge.openFile",
  "nightgauge.activeKnowledge.refresh",
  "nightgauge.autonomousClearIssueFailures",
  "nightgauge.configureForgeInstance",
  "nightgauge.fixAutoMergeSetting",
  "nightgauge.openAnalyticsDashboard",
  "nightgauge.openAuditDashboard",
  "nightgauge.openComplianceReports",
  "nightgauge.openCostForecast",
  "nightgauge.openCurrentTabInBrowser",
  "nightgauge.pipelineConnectivityAction",
  "nightgauge.platform.switchEnvironment",
  "nightgauge.reconnectEventStreams",
  "nightgauge.refreshKnowledgeView",
  "nightgauge.retryWorkspaceSync",
  "nightgauge.runSettingsMigration",
  "nightgauge.saveQueryAs",
  "nightgauge.showNotifierSettings",
  "nightgauge.showPipelineSummary",
  "nightgauge.showPlatformStatus",
];

/**
 * Not enforced by an assertion — recorded here because the smoke tier is how
 * it was found, and the next person to read this file should know.
 *
 * `getStageTimelineHtml()` in `src/views/summary/PipelineSummaryHtml.ts`
 * indexes `state.stages[name].status` for each of six hardcoded stage names
 * with no guard, but `PipelineState.stages` is a `Record<string, …>` with no
 * such guarantee. Any state missing one key — a run still in flight, a
 * partially written state file, a stage that was skipped — throws a
 * TypeError inside `updatePanel()`, after the panel has already been
 * created. The user gets an empty Pipeline Summary tab and no error.
 *
 * Out of scope for #745 (do not fix the product from the tier that found the
 * bug); the smoke fixture supplies all six keys so the case exercises the
 * panel rather than this defect.
 */
export const PIPELINE_SUMMARY_PARTIAL_STATE_NOTE =
  "PipelineSummaryHtml.getStageTimelineHtml() throws on a PipelineState missing any of its " +
  "six hardcoded stage keys; the panel renders blank.";

export interface KnownFault {
  /** Matched against the fault's stringified reason/stack. */
  readonly signature: RegExp;
  readonly note: string;
}

/**
 * Unhandled rejections that escape during startup.
 *
 * Two are product defects; one is an artefact of the host VSCode refusing
 * modal dialogs in test mode. They are listed together because the assertion
 * is the same either way — anything NOT listed fails the run.
 */
export const KNOWN_STARTUP_REJECTIONS: readonly KnownFault[] = [
  {
    signature: /Go backend not connected[\s\S]*AttentionTreeProvider\.refresh/,
    note:
      "PRODUCT BUG: the attention sweep started 4s into activate() calls " +
      "AttentionTreeProvider.refresh() without awaiting or catching it. With no Go " +
      "backend (any first launch, and every CI runner) the IPC call rejects and the " +
      "rejection is unhandled.",
  },
  {
    signature: /Go backend not connected[\s\S]*IssueQueueService\.getQueue/,
    note:
      "PRODUCT BUG: same shape as the attention sweep — IssueQueueService.getQueue() " +
      "is invoked during startup with no catch, and rejects when the Go backend is absent.",
  },
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
