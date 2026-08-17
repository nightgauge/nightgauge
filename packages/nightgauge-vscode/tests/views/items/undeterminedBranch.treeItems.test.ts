/**
 * Undetermined-branch rendering in the pipeline tree items (#448 reader sweep).
 *
 * #448 relaxed `schemas/pipelineState.ts` so `branch` may be `""`, extending
 * #397's empty-means-undetermined contract from the history record to pipeline
 * state, and deleted the six `feat/{N}` fabrications that the old `.min(1)`
 * had made necessary. The data is honest now — but three tree readers still
 * interpolated the value bare, so an undetermined branch surfaced as a blank
 * description or a dangling "**Branch:**" label with nothing after it.
 *
 * These tests pin BOTH halves of the sweep, because either one alone is
 * satisfiable by a wrong fix:
 *
 *   - `""` renders the shared `UNDETERMINED_BRANCH_LABEL` — never an empty
 *     segment, never a dangling label, and never a fabricated `feat/{N}`
 *     (reintroducing the placeholder here would undo the whole point).
 *   - a real branch renders exactly as it did before — a sweep that labelled
 *     everything "undetermined" would pass a naive one-sided test.
 *
 * The label is deliberately imported from `DashboardComponents` rather than
 * restated: one vocabulary for one state. The Go side announces the same fact
 * in `notifyComplete` (`internal/ipc/server_history_record_test.go`
 * `TestNotifyComplete_AnnouncesUndeterminedBranch`), and the dashboard already
 * renders it through `getBranchDisplayText` (#397/#450).
 */

import { describe, it, expect } from "vitest";
import { IssueTreeItem } from "../../../src/views/items/IssueTreeItem";
import { CompletedIssueTreeItem } from "../../../src/views/items/CompletedIssueTreeItem";
import { FailedIssueTreeItem } from "../../../src/views/items/FailedIssueTreeItem";
import { UNDETERMINED_BRANCH_LABEL } from "../../../src/views/dashboard/DashboardComponents";
import type { IssueReference, FailedIssueReference } from "../../../src/types/completedIssues";

/** Read a tooltip's markdown source regardless of the declared union type. */
function tooltipText(item: { tooltip?: unknown }): string {
  const tooltip = item.tooltip as { value?: string } | string | undefined;
  return typeof tooltip === "string" ? tooltip : (tooltip?.value ?? "");
}

const completed = (branch: string, labels?: string[]): IssueReference => ({
  issue_number: 448,
  title: "Pipeline-state path still fabricates feat/{N}",
  branch,
  timestamp: "2026-08-16T10:30:00Z",
  ...(labels ? { labels } : {}),
});

const failed = (branch: string): FailedIssueReference => ({
  ...completed(branch),
  failed_stage: "issue-pickup",
  error: "worktree creation failed before a branch existed",
  retry_count: 0,
});

describe("IssueTreeItem — undetermined branch (#448)", () => {
  it("labels an undetermined branch in the description instead of showing nothing", () => {
    const item = new IssueTreeItem({ number: 448, title: "Test Issue", branch: "" });

    expect(item.description).toBe(UNDETERMINED_BRANCH_LABEL);
    expect(item.description).not.toBe("");
    // The fabrication #448 deleted must not creep back in as a display default.
    expect(item.description).not.toContain("feat/448");
  });

  it("keeps the size badge attached to the label, not to a leading blank", () => {
    const item = new IssueTreeItem({
      number: 448,
      title: "Test Issue",
      branch: "",
      labels: ["size:M"],
    });

    expect(item.description).toBe(`${UNDETERMINED_BRANCH_LABEL} [M]`);
    // The pre-fix output was " [M]" — a badge floating after an empty segment.
    expect(item.description as string).not.toMatch(/^\s*\[/);
  });

  it("labels an undetermined branch in the tooltip instead of an empty code span", () => {
    const item = new IssueTreeItem({ number: 448, title: "Test Issue", branch: "" });
    const text = tooltipText(item);

    expect(text).toContain(`Branch: ${UNDETERMINED_BRANCH_LABEL}`);
    // Pre-fix this was "Branch: ``\n\n" — a label with an empty span after it.
    expect(text).not.toContain("Branch: ``");
    expect(text).not.toContain("feat/448");
  });

  it("renders a real branch exactly as before — description and tooltip", () => {
    const item = new IssueTreeItem({
      number: 448,
      title: "Test Issue",
      branch: "fix/448-undetermined-branch-reader-sweep",
      labels: ["size:M"],
    });

    expect(item.description).toBe("fix/448-undetermined-branch-reader-sweep [M]");
    expect(tooltipText(item)).toContain("Branch: `fix/448-undetermined-branch-reader-sweep`");
    expect(tooltipText(item)).not.toContain(UNDETERMINED_BRANCH_LABEL);
  });

  it("applies an explicit empty branch through update() rather than ignoring it", () => {
    const item = new IssueTreeItem({
      number: 448,
      title: "Test Issue",
      branch: "fix/448-undetermined-branch-reader-sweep",
    });

    // `""` is a value (undetermined), not an absence. A truthiness guard here
    // would leave the stale branch name on screen forever.
    item.update({ branch: "" });

    expect(item.getInfo().branch).toBe("");
    expect(item.description).toBe(UNDETERMINED_BRANCH_LABEL);
    expect(tooltipText(item)).toContain(`Branch: ${UNDETERMINED_BRANCH_LABEL}`);
  });

  it("still leaves the branch untouched when update() omits it", () => {
    const item = new IssueTreeItem({
      number: 448,
      title: "Test Issue",
      branch: "fix/448-undetermined-branch-reader-sweep",
    });

    item.update({ title: "Renamed" });

    expect(item.getInfo().branch).toBe("fix/448-undetermined-branch-reader-sweep");
    expect(item.description).toBe("fix/448-undetermined-branch-reader-sweep");
  });
});

describe("CompletedIssueTreeItem — undetermined branch (#448)", () => {
  it("labels an undetermined branch instead of leaving the field dangling", () => {
    const text = tooltipText(new CompletedIssueTreeItem(completed("")));

    expect(text).toContain(`**Branch:** ${UNDETERMINED_BRANCH_LABEL}`);
    // Pre-fix: "**Branch:** \n\n" — a bold label followed by nothing.
    expect(text).not.toContain("**Branch:** \n");
    expect(text).not.toContain("feat/448");
  });

  it("renders a real branch unchanged", () => {
    const text = tooltipText(new CompletedIssueTreeItem(completed("feat/42-dark-mode")));

    expect(text).toContain("**Branch:** feat/42-dark-mode");
    expect(text).not.toContain(UNDETERMINED_BRANCH_LABEL);
  });
});

describe("FailedIssueTreeItem — undetermined branch (#448)", () => {
  it("labels an undetermined branch instead of leaving the field dangling", () => {
    const text = tooltipText(new FailedIssueTreeItem(failed("")));

    expect(text).toContain(`**Branch:** ${UNDETERMINED_BRANCH_LABEL}`);
    expect(text).not.toContain("**Branch:** \n");
    expect(text).not.toContain("feat/448");
  });

  it("renders a real branch unchanged", () => {
    const text = tooltipText(new FailedIssueTreeItem(failed("feat/43-auth")));

    expect(text).toContain("**Branch:** feat/43-auth");
    expect(text).not.toContain(UNDETERMINED_BRANCH_LABEL);
  });
});
