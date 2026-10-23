/**
 * #1168 — the default (production) adapter-auth notice surface.
 *
 * `HeadlessOrchestrator.adapterAuthSurface.test.ts` drives the real pre-flight
 * gate with the surface INJECTED, so it pins the decision: what the operator is
 * told and how often. This file pins the other half — that the default surface,
 * the one that actually runs in the extension host, does something. A toast
 * cannot be retracted by VSCode, so "the surface clears once the adapter
 * authenticates" is carried by a status entry; if that entry were a no-op the
 * injected-surface tests would still pass and the operator would still be
 * looking at nothing.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as vscode from "vscode";
import {
  clearAdapterAuthNotice,
  formatAdapterAuthNotice,
  outstandingAdapterAuthNotices,
  reportAdapterAuthFailure,
  resetAdapterAuthNotices,
} from "../../src/utils/adapterAuthNotice";

const CLAUDE = {
  adapter: "claude-headless",
  suggestedFix: "Run `claude auth login` (install via `brew install claude` if missing).",
  timedOut: false,
};
const CODEX = {
  adapter: "codex",
  suggestedFix: "Run `codex login` (install via `npm install -g @openai/codex` if missing).",
  timedOut: false,
};

/** The status entry the default `setStanding` creates on first use. */
function statusItem() {
  const created = vi.mocked(vscode.window.createStatusBarItem).mock.results;
  return created.length > 0 ? (created[created.length - 1].value as any) : undefined;
}

describe("adapterAuthNotice default surface (#1168)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetAdapterAuthNotices();
  });

  afterEach(() => {
    resetAdapterAuthNotices();
  });

  // RED-PROOF: in defaultSurface().warn, replace the
  // `vscode.window.showWarningMessage(message)` call with `void message;`.
  // Compiles, and every injected-surface test stays green.
  it("raises a real VSCode warning naming the adapter and the remedy", () => {
    reportAdapterAuthFailure([CLAUDE]);

    expect(vi.mocked(vscode.window.showWarningMessage)).toHaveBeenCalledTimes(1);
    const shown = vi.mocked(vscode.window.showWarningMessage).mock.calls[0][0] as string;
    expect(shown).toContain("claude-headless");
    expect(shown).toContain("claude auth login");
  });

  // RED-PROOF: in defaultSurface().setStanding, return early before
  // `standingItem.show()` (i.e. make the standing half a no-op). Compiles;
  // observed red here and nowhere else.
  it("keeps a status entry up for exactly as long as the condition lasts", () => {
    reportAdapterAuthFailure([CLAUDE]);
    const item = statusItem();
    expect(item, "no status entry was ever created").toBeDefined();
    expect(item.show).toHaveBeenCalled();
    expect(item.text).toContain("claude-headless");
    // The tooltip carries the remedy; the blob-free contract is the same one
    // the toast is held to, since both come from formatAdapterAuthNotice.
    expect(item.tooltip).toContain("claude auth login");

    item.hide.mockClear();
    clearAdapterAuthNotice(["claude-headless"]);
    expect(item.hide).toHaveBeenCalled();
  });

  it("dedupes per adapter, and tracks two failed adapters independently", () => {
    expect(reportAdapterAuthFailure([CLAUDE, CODEX])).toEqual(["claude-headless", "codex"]);
    // A second burst for the same two adapters says nothing new.
    expect(reportAdapterAuthFailure([CLAUDE, CODEX])).toEqual([]);
    expect(vi.mocked(vscode.window.showWarningMessage)).toHaveBeenCalledTimes(1);

    // Authenticating one leaves the other standing — the condition is
    // per-adapter, so retracting it must be too.
    expect(clearAdapterAuthNotice(["codex"])).toEqual(["codex"]);
    expect(outstandingAdapterAuthNotices()).toEqual([CLAUDE]);
    // …and clearing an adapter with nothing outstanding is a silent no-op, so
    // the pass path can call it unconditionally.
    expect(clearAdapterAuthNotice(["codex"])).toEqual([]);
  });

  it("builds its text from the adapter name and remedy only", () => {
    // The rendered string is the whole contract with the operator: it must be
    // derivable from the two safe fields, with nothing else available to leak.
    const text = formatAdapterAuthNotice([CLAUDE, { ...CODEX, timedOut: true }]);
    expect(text).toContain("claude-headless is not authenticated");
    expect(text).toContain("codex auth could not be verified");
    expect(text).toContain("claude auth login");
    expect(text).toContain("codex login");
    expect(text).toMatch(/no tokens were spent/i);
  });
});
