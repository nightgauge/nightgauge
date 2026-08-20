/**
 * PipelineSummaryHtmlPartialState.test.ts — Issue #767
 *
 * `getStageTimelineHtml()` used to index `state.stages[name].status` for six
 * hardcoded stage names with no guard against a `PipelineState.stages` that
 * is missing any of them — a run still in flight, a partially written state
 * file, or a skipped stage. The throw happened after the webview panel was
 * already created, so the user saw a blank tab with no error.
 *
 * This covers the case directly: a `stages` record with only one of the six
 * keys present must render, not throw, and must render the missing stages as
 * a distinct "not started" state rather than omitting them.
 */

import { describe, it, expect, vi } from "vitest";
import { getPipelineSummaryHtml } from "../../../src/views/summary/PipelineSummaryHtml";
import type { PipelineState } from "../../../src/services/PipelineStateService";

vi.mock("vscode", () => ({
  Uri: {
    joinPath: vi.fn(() => ({ fsPath: "/mock/path" })),
  },
  workspace: {
    workspaceFolders: undefined,
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

const mockWebview = {
  cspSource: "vscode-webview://mock",
  asWebviewUri: vi.fn((uri: unknown) => uri),
} as unknown as import("vscode").Webview;

function partialState(): PipelineState {
  return {
    issue_number: 767,
    title: "Partial stages record",
    branch: "fix/767-partial-stages",
    // Only one of the six timeline stages is present — a run still in
    // flight, or a partially written state.json, looks exactly like this.
    stages: {
      "feature-dev": { status: "running" },
    },
    started_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:05:00Z",
  } as PipelineState;
}

describe("PipelineSummaryHtml — partial stages record (Issue #767)", () => {
  it("does not throw when the stages record is missing hardcoded stage keys", () => {
    expect(() => getPipelineSummaryHtml(mockWebview, partialState())).not.toThrow();
  });

  it("renders every one of the six timeline stages, present or not", () => {
    const html = getPipelineSummaryHtml(mockWebview, partialState());

    for (const label of [
      "Issue Pickup",
      "Feature Planning",
      "Feature Development",
      "Feature Validation",
      "PR Creation",
      "PR Merge",
    ]) {
      expect(html).toContain(label);
    }
  });

  it("renders a missing stage as a distinct not-started state, not as absence", () => {
    const html = getPipelineSummaryHtml(mockWebview, partialState());

    // The one present stage renders its real status class...
    expect(html).toContain("status-running");
    // ...and every absent stage falls back to the pending/"not started" class
    // rather than being silently dropped from the timeline.
    expect(html).toContain("status-pending");
  });

  it("does not throw when stages is entirely empty", () => {
    const empty: PipelineState = { ...partialState(), stages: {} };
    expect(() => getPipelineSummaryHtml(mockWebview, empty)).not.toThrow();
  });
});
