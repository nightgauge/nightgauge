/**
 * pauseUi.test.ts
 *
 * A pause or resume from the platform shows in the window exactly as the
 * local Pause/Resume Pipeline commands show it (#2334 review): the status bar
 * item, and the `nightgauge.pipelinePaused` / `nightgauge.pipelineRunning`
 * context keys that put Resume or Pause in the pipeline view's title bar.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { createRemotePauseUi, resumePointOf, runningStageOf } from "../../src/utils/pauseUi";
import type { PipelineState } from "../../src/services/PipelineStateService";

function state(stages: Record<string, string>): PipelineState {
  return {
    issue_number: 7,
    title: "t",
    branch: "b",
    started_at: "2026-10-01T00:00:00.000Z",
    stages: Object.fromEntries(
      Object.entries(stages).map(([stage, status]) => [stage, { status, auto_retry_count: 0 }])
    ),
  } as unknown as PipelineState;
}

function contextKeys(): Array<[string, unknown]> {
  return vi
    .mocked(vscode.commands.executeCommand)
    .mock.calls.filter(([command]) => command === "setContext")
    .map(([, key, value]) => [key as string, value]);
}

describe("pauseUi", () => {
  beforeEach(() => vi.clearAllMocks());

  it("finds the stage a pause lets finish, and where a resume picks up", () => {
    const running = state({
      "pipeline-start": "complete",
      "issue-pickup": "complete",
      "feature-planning": "running",
      "feature-dev": "pending",
    });
    expect(runningStageOf(running)).toBe("feature-planning");
    expect(resumePointOf(running)).toEqual({
      lastCompletedStage: "issue-pickup",
      nextStage: null,
    });

    const held = state({
      "pipeline-start": "complete",
      "issue-pickup": "skipped",
      "feature-planning": "complete",
      "feature-dev": "pending",
    });
    expect(runningStageOf(held)).toBeNull();
    expect(resumePointOf(held)).toEqual({
      lastCompletedStage: "feature-planning",
      nextStage: "feature-dev",
    });
  });

  it("shows a platform pause as the paused status bar item and context key", async () => {
    const statusBar = { showPaused: vi.fn(), showRunning: vi.fn() };
    const stateOf = vi
      .fn()
      .mockResolvedValue(state({ "pipeline-start": "complete", "feature-dev": "running" }));

    await createRemotePauseUi(statusBar as never, stateOf).paused("run-1");

    expect(stateOf).toHaveBeenCalledWith("run-1");
    expect(statusBar.showPaused).toHaveBeenCalledWith("feature-dev");
    expect(contextKeys()).toContainEqual(["nightgauge.pipelinePaused", true]);
  });

  it("shows a platform resume as the running item at the next stage, with Pause back", async () => {
    const statusBar = { showPaused: vi.fn(), showRunning: vi.fn() };
    const stateOf = vi
      .fn()
      .mockResolvedValue(state({ "pipeline-start": "complete", "feature-dev": "pending" }));

    await createRemotePauseUi(statusBar as never, stateOf).resumed("run-1");

    expect(statusBar.showRunning).toHaveBeenCalledWith("feature-dev");
    expect(contextKeys()).toEqual(
      expect.arrayContaining([
        ["nightgauge.pipelinePaused", false],
        ["nightgauge.pipelineRunning", true],
      ])
    );
  });

  it("still shows the change when the run's state cannot be read", async () => {
    const statusBar = { showPaused: vi.fn(), showRunning: vi.fn() };
    const ui = createRemotePauseUi(statusBar as never, vi.fn().mockResolvedValue(null));

    await ui.paused("gone");
    await ui.resumed("gone");

    expect(statusBar.showPaused).toHaveBeenCalledWith(undefined);
    expect(statusBar.showRunning).toHaveBeenCalledWith("pipeline-start");
  });
});
