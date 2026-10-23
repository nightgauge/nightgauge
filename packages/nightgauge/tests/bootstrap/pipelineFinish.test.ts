/**
 * pipelineFinish.test.ts
 *
 * Issue #1188 — the interactive pipeline's "no next stage" terminal
 * (`handlePipelineComplete` in services.ts) had no behavioural coverage:
 * releasing the run identity first, notifying, showing the summary panel,
 * or falling back to a reset prompt could all be deleted without turning
 * the suite red.
 *
 * This file imports the REAL `handleInteractivePipelineComplete` and
 * executes it with stub dependencies, so each behaviour is observed rather
 * than spelled.
 */

import { describe, it, expect, vi } from "vitest";
import {
  handleInteractivePipelineComplete,
  COMPLETE_AND_RESET_ACTION,
  type InteractivePipelineCompleteDeps,
} from "../../src/bootstrap/pipelineFinish";
import type { PipelineState } from "../../src/services/PipelineStateService";

const ISSUE_NUMBER = 42;
const STATE = { issue_number: ISSUE_NUMBER } as unknown as PipelineState;

/** Builds stub deps plus the shared call-order log the assertions read. */
function makeDeps(overrides: Partial<InteractivePipelineCompleteDeps> = {}) {
  const calls: string[] = [];

  const releaseRunIdentity = vi.fn((reason: string, issueNumber: number) => {
    calls.push(`release:${reason}:${issueNumber}`);
  });
  const notifyPipelineComplete = vi.fn((issueNumber: number) => {
    calls.push(`notify:${issueNumber}`);
  });
  const showSummary = vi.fn(async (state: PipelineState) => {
    calls.push(`summary:${state.issue_number}`);
  });
  const showCompletionPrompt = vi.fn(async (issueNumber: number) => {
    calls.push(`prompt:${issueNumber}`);
    return undefined as string | undefined;
  });
  const resetPipeline = vi.fn(async (issueNumber?: number) => {
    calls.push(`reset:${issueNumber}`);
  });

  const deps: InteractivePipelineCompleteDeps = {
    releaseRunIdentity,
    notifyPipelineComplete,
    getState: async () => STATE,
    showSummary,
    showCompletionPrompt,
    resetPipeline,
    ...overrides,
  };

  return {
    deps,
    calls,
    releaseRunIdentity,
    notifyPipelineComplete,
    showSummary,
    showCompletionPrompt,
    resetPipeline,
  };
}

describe("handleInteractivePipelineComplete (Issue #1188)", () => {
  it("releases the run identity before anything else, even when the summary panel throws", async () => {
    const h = makeDeps({
      getState: async () => {
        throw new Error("state read failed");
      },
      showCompletionPrompt: async () => undefined,
    });

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.calls[0]).toBe(`release:pipeline-complete:${ISSUE_NUMBER}`);
    expect(h.releaseRunIdentity).toHaveBeenCalledWith("pipeline-complete", ISSUE_NUMBER);
  });

  it("notifies, then shows the summary panel when state is available", async () => {
    const h = makeDeps();

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.calls).toEqual([
      `release:pipeline-complete:${ISSUE_NUMBER}`,
      `notify:${ISSUE_NUMBER}`,
      `summary:${ISSUE_NUMBER}`,
    ]);
    expect(h.showCompletionPrompt).not.toHaveBeenCalled();
    expect(h.resetPipeline).not.toHaveBeenCalled();
  });

  it("falls back to the completion prompt when there is no pipeline state", async () => {
    const h = makeDeps({ getState: async () => null });

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.showSummary).not.toHaveBeenCalled();
    expect(h.showCompletionPrompt).toHaveBeenCalledWith(ISSUE_NUMBER);
  });

  it("falls back to the completion prompt when showing the summary panel throws", async () => {
    const h = makeDeps({
      showSummary: vi.fn(async () => {
        throw new Error("panel failed to open");
      }),
    });

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.showCompletionPrompt).toHaveBeenCalledWith(ISSUE_NUMBER);
  });

  it("resets the pipeline only when the operator picks Complete & Reset", async () => {
    const h = makeDeps({
      getState: async () => null,
      showCompletionPrompt: vi.fn(async () => COMPLETE_AND_RESET_ACTION),
    });

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.resetPipeline).toHaveBeenCalledWith(ISSUE_NUMBER);
  });

  it("does not reset the pipeline when the operator keeps it open", async () => {
    const h = makeDeps({
      getState: async () => null,
      showCompletionPrompt: vi.fn(async () => "Keep Open"),
    });

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.resetPipeline).not.toHaveBeenCalled();
  });

  it("does not throw when the notification service is absent", async () => {
    const h = makeDeps({ notifyPipelineComplete: null });

    await expect(handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER)).resolves.toBeUndefined();

    expect(h.showSummary).toHaveBeenCalled();
  });

  it("goes straight to the completion prompt when no pipeline state service exists", async () => {
    const h = makeDeps({ getState: null });

    await handleInteractivePipelineComplete(h.deps, ISSUE_NUMBER);

    expect(h.showSummary).not.toHaveBeenCalled();
    expect(h.showCompletionPrompt).toHaveBeenCalledWith(ISSUE_NUMBER);
  });
});
