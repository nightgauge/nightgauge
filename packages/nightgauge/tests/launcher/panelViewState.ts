/**
 * Report line for a demo panel's view state (#3662). Kept free of `vscode`
 * so it is unit-tested here; `tests/vscode-host/` forbids `*.test.ts`.
 */

/** One sample of a panel's `visible`/`active` state. */
export interface PanelViewState {
  /** `Date.now()` when the sample was taken. */
  at: number;
  visible: boolean;
  active: boolean;
  /**
   * `created` and `html` read the extension host's cached fields, which start
   * `visible: true` before the workbench has laid anything out. Only
   * `viewState` (an `onDidChangeViewState` event) is the workbench's answer.
   */
  cause: "created" | "html" | "viewState";
}

const describeState = (s: PanelViewState) =>
  `${s.visible ? "visible" : "hidden"}${s.active ? "+active" : ""}`;

/**
 * The panel's view state as the workbench reported it. VS Code builds a
 * webview's document only once the panel is laid out, so this separates a
 * panel hidden behind another editor from one that was shown and still never
 * loaded. The workbench sends changes only, so a panel visible from creation
 * may have no entry at all: absence of a change is not "never shown", and
 * the `created`/`html` samples (the host's cached default) are printed but
 * never treated as a layout report.
 */
export function viewStateLine(states: readonly PanelViewState[], createdAt: number): string {
  const reported = states.filter((s) => s.cause === "viewState");
  const cached = states.find((s) => s.cause === "created");
  const initial = cached
    ? `host default at creation ${describeState(cached)}, not a layout report`
    : "no creation sample";
  if (reported.length === 0) {
    return `  panel view state: the workbench sent no view-state change (${initial})`;
  }
  const changes = reported.map((s) => `${describeState(s)}@+${s.at - createdAt}ms`).join(", ");
  return (
    `  panel view state: now ${describeState(reported[reported.length - 1])}; ` +
    `workbench changes ${changes} (${initial})`
  );
}
