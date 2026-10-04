/**
 * #2411: the Output window finds a slot by repository and issue number. Two
 * repositories' issues with one number run in two slots, so a lookup by the
 * number alone could route one run's output to the other's slot.
 */
import { describe, it, expect, beforeEach } from "vitest";
import { OutputWindowState } from "../../../src/views/outputWindow/OutputWindowState";

describe("OutputWindowState slot lookup by repository and number (#2411)", () => {
  let state: OutputWindowState;

  beforeEach(() => {
    state = new OutputWindowState();
    state.registerSlot(0, 21, "Platform 21", "example-org/platform");
    state.registerSlot(1, 21, "App 21", "example-org/app");
  });

  it("finds each same-numbered issue's own slot", () => {
    expect(state.findSlotIndexByIssue(21, "example-org/platform")).toBe(0);
    expect(state.findSlotIndexByIssue(21, "example-org/app")).toBe(1);
    expect(state.getSlotByIssueNumber(21, "example-org/app")?.slotIndex).toBe(1);
  });

  it("matches the repository case-insensitively", () => {
    expect(state.findSlotIndexByIssue(21, "Example-Org/App")).toBe(1);
  });

  it("finds no slot for a third repository's issue with the number", () => {
    expect(state.findSlotIndexByIssue(21, "example-org/infra")).toBeUndefined();
  });

  it("does not guess when the number names several slots and no repository is given", () => {
    expect(state.findSlotIndexByIssue(21)).toBeUndefined();
    expect(state.getSlotByIssueNumber(21)).toBeUndefined();
  });

  it("falls back to the number when it names exactly one slot", () => {
    state.registerSlot(2, 34, "Only 34", "example-org/app");
    expect(state.findSlotIndexByIssue(34)).toBe(2);
  });

  it("matches a slot registered without a repository by number", () => {
    state.registerSlot(2, 55, "Unknown-repo 55");
    expect(state.findSlotIndexByIssue(55, "example-org/app")).toBe(2);
    expect(state.findSlotIndexByIssue(55)).toBe(2);
  });

  it("prefers the running slot over an archived tab of the same issue", () => {
    state.registerArchivedSlot(5, 77, "Issue #77");
    state.registerSlot(6, 77, "Live 77");
    expect(state.findSlotIndexByIssue(77)).toBe(6);
  });

  it("keeps the single-repository case unchanged", () => {
    const single = new OutputWindowState();
    single.registerSlot(0, 21, "Platform 21", "example-org/platform");
    expect(single.findSlotIndexByIssue(21, "example-org/platform")).toBe(0);
    expect(single.findSlotIndexByIssue(21)).toBe(0);
  });
});

describe("OutputWindowState.hasSlotLoggingIssue (#2411)", () => {
  it("holds a log only for a slot with its number writing under its root", () => {
    const state = new OutputWindowState();
    state.registerSlot(0, 21, "App 21", "example-org/app");
    state.setSlotLogRoot(0, "/repos/app");

    // The app run does not hold the platform repository's log for #21.
    expect(state.hasSlotLoggingIssue(21, "/repos/platform")).toBe(false);
    expect(state.hasSlotLoggingIssue(21, "/repos/app")).toBe(true);
    expect(state.hasSlotLoggingIssue(21, "/repos/app/")).toBe(true);
    expect(state.hasSlotLoggingIssue(22, "/repos/app")).toBe(false);
  });
});
