/**
 * Pure gating logic for the first-run Getting Started onboarding panel
 * (Issue #4155). Kept side-effect-free and framework-free — no `vscode`
 * import — so it is trivially unit-testable without a VSCode host, and so
 * the rule it encodes ("show once, only for an uninitialized workspace") is
 * verifiable independent of webview/globalState plumbing.
 *
 * @see GettingStartedPanel.ts for the webview this gate controls
 * @see ../../commands/quickstart.ts for the `repoInitialized` signal this
 *      gate consumes (reused, not re-derived)
 */

export interface OnboardingGateInput {
  /**
   * Result of `isRepoInitialized()` — true once `.nightgauge/config.yaml`
   * exists for the current workspace.
   */
  repoInitialized: boolean;
  /**
   * Whether the Getting Started panel has already been auto-shown once for
   * this VSCode installation (persisted in `context.globalState`).
   */
  alreadyShown: boolean;
}

/**
 * Decide whether extension activation should auto-open the Getting Started
 * panel. Auto-open fires exactly once per install, and only while the
 * workspace has not yet run repo-init — once either condition flips
 * (repo initialized, or the panel has already auto-shown), activation never
 * auto-opens it again. The user can always reopen it manually via the
 * `nightgauge.showGettingStarted` command, regardless of this gate.
 */
export function shouldAutoShowGettingStarted(input: OnboardingGateInput): boolean {
  return !input.repoInitialized && !input.alreadyShown;
}
