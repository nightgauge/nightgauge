/**
 * RecoveryDialogHtml snapshot tests — lock the visual contract for the
 * Recovery Dialog so theme/layout regressions are caught in CI.
 *
 * @see Issue #3239
 */

import { describe, expect, it } from "vitest";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";
import { getRecoveryDialogHtml } from "../../../src/views/recovery/RecoveryDialogHtml";

const mockWebview = { cspSource: "test-csp" } as any;

function normalize(html: string): string {
  return html
    .replace(/nonce-[A-Za-z0-9]{32}/g, "nonce-NONCE")
    .replace(/nonce="[A-Za-z0-9]{32}"/g, 'nonce="NONCE"');
}

const baseMissingInput: RecoveryRequiredPayload = {
  issueNumber: 42,
  triggeringStage: "feature-dev",
  producingStage: "feature-planning",
  errorKind: "MISSING_INPUT_FILE",
  errorDetail:
    "Cannot start feature-dev: required input file .nightgauge/pipeline/planning-42.json is missing.",
  runState: "paused",
  availableActions: ["open-run-state-directory", "cancel"],
};

describe("getRecoveryDialogHtml — snapshot per error kind", () => {
  it("renders MISSING_INPUT_FILE with the observational action set", () => {
    expect(normalize(getRecoveryDialogHtml(mockWebview, baseMissingInput))).toMatchSnapshot();
  });

  it("renders CONTEXT_SCHEMA_ERROR with no producer", () => {
    const payload: RecoveryRequiredPayload = {
      ...baseMissingInput,
      errorKind: "CONTEXT_SCHEMA_ERROR",
      producingStage: null,
      errorDetail: "schema validation failed: foo.bar must be a string",
      runState: "aborted",
      availableActions: ["open-run-state-directory", "cancel"],
    };
    expect(normalize(getRecoveryDialogHtml(mockWebview, payload))).toMatchSnapshot();
  });

  it("renders RUN_STATE_MISSING with minimal action set", () => {
    const payload: RecoveryRequiredPayload = {
      ...baseMissingInput,
      errorKind: "RUN_STATE_MISSING",
      producingStage: null,
      errorDetail: "No run-state.json found for issue #42.",
      runState: "none",
      availableActions: ["open-run-state-directory", "cancel"],
    };
    expect(normalize(getRecoveryDialogHtml(mockWebview, payload))).toMatchSnapshot();
  });
});

describe("getRecoveryDialogHtml — basic structure", () => {
  it("includes the issue number in the title", () => {
    const html = getRecoveryDialogHtml(mockWebview, baseMissingInput);
    expect(html).toContain("Issue #42");
  });

  it("escapes user-supplied error detail", () => {
    const payload: RecoveryRequiredPayload = {
      ...baseMissingInput,
      errorDetail: "<script>alert(1)</script>",
    };
    const html = getRecoveryDialogHtml(mockWebview, payload);
    expect(html).not.toContain("<script>alert(1)</script>");
    expect(html).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
  });

  it("does not render an action that is not in availableActions", () => {
    const payload: RecoveryRequiredPayload = {
      ...baseMissingInput,
      availableActions: ["cancel"],
    };
    const html = getRecoveryDialogHtml(mockWebview, payload);
    expect(html).not.toContain('data-action="open-run-state-directory"');
    expect(html).toContain('data-action="cancel"');
    expect(html).not.toContain('data-action="discard-run"');
    expect(html).not.toContain('data-action="restart-from-beginning"');
    expect(html).not.toContain('data-action="resume-from-paused-stage"');
    expect(html).not.toContain('data-action="run-producing-stage"');
  });
});
