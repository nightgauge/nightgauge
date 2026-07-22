/**
 * OutputWindowState.slotIdentity.test.ts
 *
 * #307 — multi-repo concurrent runs cross-contaminated per-run state: a slot's
 * session-log events were filed under a SHARED "current" issue number
 * (`this.issueNumber`, last written by whichever sibling slot most recently hit
 * a stage boundary) and, for unattributed lines, under the ACTIVE UI tab
 * (`activeSlotIndex`). Both are shared mutable identity. The disk write must
 * instead bind the (repo root × issue number) pair to the EXPLICIT owning slot
 * captured at stage spawn, so a slot's bytes always land in its own
 * (repo × issue) log — never a sibling's.
 *
 * These tests reproduce the live dogfood incident (2026-07-19): platform#209
 * and infra#160 dispatched together; #209's feature-dev events leaked into a
 * `160` log and #160's into a `209` log as the shared identity flipped.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

vi.mock("../../../src/utils/log-file-writer", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../src/utils/log-file-writer")>()),
  LogFileWriter: {
    appendToLog: vi.fn().mockResolvedValue(undefined),
    truncateForLog: vi.fn((s: string) => s),
  },
}));

import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";
import { LogFileWriter } from "../../../src/utils/log-file-writer";

const appendSpy = vi.mocked(LogFileWriter.appendToLog);

/** The (root, issueNumber) pair of the most recent disk write. */
function lastWrite(): { root: string; issue: number | null } {
  expect(appendSpy).toHaveBeenCalled();
  const call = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
  return { root: call[0] as string, issue: call[1] as number | null };
}

describe("OutputWindowState per-slot session-log identity (#307)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    vi.clearAllMocks();
    state = new OutputWindowState();
    state.setLogConfig("/ws/bootstrap");

    // Two concurrent slots in different repos, each with its own issue.
    state.registerSlot(0, 209, "platform work", "owner/bowlsheet-platform");
    state.registerSlot(1, 160, "infra work", "owner/bowlsheet-infra");
    state.setSlotLogRoot(0, "/ws/platform");
    state.setSlotLogRoot(1, "/ws/infra");
  });

  it("routes each slot's events to its OWN (repo × issue) even as the shared current issue flips", () => {
    // Sibling slot 1 just advanced a stage → the old code set this shared
    // scalar to 160, stealing slot 0's log identity.
    state.setIssueNumber(160);
    state.addEntry("platform dev line", "info", "feature-dev", { slotIndex: 0 });
    const platform = lastWrite();
    expect(platform.root).toBe("/ws/platform");
    expect(platform.issue).toBe(209); // NOT the shared 160

    // Now slot 0 advances → shared scalar flips to 209; slot 1's line must
    // still land in infra's 160 log, not a 209 log.
    state.setIssueNumber(209);
    state.addEntry("infra dev line", "info", "feature-dev", { slotIndex: 1 });
    const infra = lastWrite();
    expect(infra.root).toBe("/ws/infra");
    expect(infra.issue).toBe(160); // NOT the shared 209
  });

  it("never pairs one slot's repo root with another slot's issue number", () => {
    state.setIssueNumber(999); // some unrelated shared value
    for (const [slot, root, issue] of [
      [0, "/ws/platform", 209],
      [1, "/ws/infra", 160],
    ] as const) {
      state.addEntry("line", "info", "feature-dev", { slotIndex: slot });
      const w = lastWrite();
      expect(w.root).toBe(root);
      expect(w.issue).toBe(issue);
    }
  });

  it("does NOT attribute an unattributed line to the active UI tab's slot", () => {
    // User is viewing the infra tab; the old code let `activeSlotIndex` route a
    // slot-less disk write into infra's (repo × issue) log.
    state.setActiveSlot(1);
    state.setIssueNumber(500);
    state.addEntry("global, non-slot line", "info");
    const w = lastWrite();
    expect(w.root).toBe("/ws/bootstrap"); // bootstrap root, not /ws/infra
    expect(w.issue).toBe(500); // shared fallback, not infra's 160
  });

  it("getSlotIssueNumber returns the slot's immutable issue regardless of the shared scalar", () => {
    state.setIssueNumber(160);
    expect(state.getSlotIssueNumber(0)).toBe(209);
    expect(state.getSlotIssueNumber(1)).toBe(160);
    expect(state.getSlotIssueNumber(7)).toBeUndefined();
  });
});

/**
 * Spawn-instant identity (#307 follow-up).
 *
 * Post-fix verification of #311 found one residual gap: at the exact moment a
 * NEW slot spawns, its very first emitted line (the "Starting <stage> for
 * issue #N..." dispatch preamble) could still cross to a neighbor's identity
 * because the caller (bootstrap/services.ts onSlotStarted) fired that first
 * line via updateStage()/onStageChanged() BEFORE calling registerSlotInfo(),
 * i.e. before this slot's own `slotInfos` entry existed. Two concrete disk
 * incidents: bowlsheet-infra's #96-tagged log held dashboard#96's issue
 * number in a file whose lines were actually infra#163's; bowlsheet-flutter's
 * #209-tagged log held platform#209's number for what was actually flutter#303.
 *
 * Both incidents match a slot registering AFTER a stale identity is already
 * available for `addEntry` to fall back to — either the shared scalar
 * (restored from a persisted prior session, or set by a sibling slot's
 * transition) or a previous occupant of the same slot index whose
 * `slotInfos` entry had not yet been overwritten. These tests pin the
 * contract callers must uphold: register the slot BEFORE emitting its first
 * line, and the very first entry lands under the slot's OWN identity even
 * with a stale value sitting in every fallback path.
 */
describe("OutputWindowState spawn-instant identity (#307 follow-up)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    vi.clearAllMocks();
    state = new OutputWindowState();
    state.setLogConfig("/ws/bootstrap");
  });

  it("resolves a brand-new slot's first line correctly even with a stale shared scalar from a sibling", () => {
    // Simulates: dashboard's slot (index 0) transitioned first and left the
    // shared scalar on its own issue number, 96.
    state.setIssueNumber(96);

    // NOW infra's slot spawns at a FRESH index (1) that has never been used.
    // Correct call order (the fix): setSlotLogRoot + registerSlot BEFORE the
    // first addEntry for this slot.
    state.setSlotLogRoot(1, "/ws/infra");
    state.registerSlot(1, 163, "infra work", "owner/bowlsheet-infra");

    // This is the dispatch preamble — the very FIRST line ever emitted for
    // slot 1, tagged with slot 1 as its explicit owner.
    state.addEntry("Starting issue-pickup for issue #163...", "info", "issue-pickup", {
      slotIndex: 1,
    });

    const call = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
    expect(call[0]).toBe("/ws/infra"); // own root, not dashboard's
    expect(call[1]).toBe(163); // own issue, NOT the stale shared 96
  });

  it("resolves correctly even when the slot index is reused and still holds a prior occupant's identity", () => {
    // Slot index 2 was previously dashboard#96 in this same session.
    state.setSlotLogRoot(2, "/ws/dashboard");
    state.registerSlot(2, 96, "dashboard work", "owner/bowlsheet-dashboard");
    state.addEntry("dashboard line", "info", "feature-dev", { slotIndex: 2 });
    const priorWrite = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
    expect(priorWrite[1]).toBe(96);

    // Slot 2 is now reused for a NEW dispatch: flutter#303. The fix requires
    // the caller to re-register BEFORE emitting flutter#303's first line —
    // this re-registration must fully overwrite the stale #96 entry so the
    // very next addEntry for slot 2 never resolves the old occupant's issue.
    state.setSlotLogRoot(2, "/ws/flutter");
    state.registerSlot(2, 303, "flutter work", "owner/bowlsheet-flutter");
    state.addEntry("Starting issue-pickup for issue #303...", "info", "issue-pickup", {
      slotIndex: 2,
    });

    const newWrite = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
    expect(newWrite[0]).toBe("/ws/flutter");
    expect(newWrite[1]).toBe(303); // NOT the stale prior occupant's 96
  });

  it("demonstrates the pre-fix ordering bug: emitting before registerSlot falls through to a stale identity", () => {
    // This reproduces the DEFECT the ordering fix eliminates at the
    // call-site level (bootstrap/services.ts). OutputWindowState itself is
    // unchanged by this fix — it has always resolved strictly from whatever
    // is registered at call time. The bug was entirely about caller order:
    // services.ts previously called updateStage() (which synchronously
    // emits this exact preamble) BEFORE registerSlotInfo(). Reproducing that
    // wrong order here shows why: with no owning-slot record yet, addEntry
    // falls through to the shared scalar — a neighbor's stale value.
    state.setIssueNumber(96); // stale, set by a sibling slot's transition
    state.setSlotLogRoot(3, "/ws/infra"); // root IS set first (the #216 fix)

    // registerSlot has NOT been called yet for slot 3 — this is the bug.
    state.addEntry("Starting issue-pickup for issue #163...", "info", "issue-pickup", {
      slotIndex: 3,
    });

    const call = appendSpy.mock.calls[appendSpy.mock.calls.length - 1];
    expect(call[0]).toBe("/ws/infra"); // root already correct (#216)
    expect(call[1]).toBe(96); // BUG: tag falls through to the stale scalar
  });
});
