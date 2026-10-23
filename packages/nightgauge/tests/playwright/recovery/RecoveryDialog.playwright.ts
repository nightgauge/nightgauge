import { expect, test } from "@playwright/test";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";
import { getRecoveryDialogHtml } from "../../../src/views/recovery/RecoveryDialogHtml.js";
import {
  dispatchInboundMessage,
  getPostedMessages,
  loadWebview,
} from "../helpers/webview-loader.js";

const payload: RecoveryRequiredPayload = {
  issueNumber: 793,
  triggeringStage: "feature-dev",
  errorKind: "RUN_STATE_MISSING",
  errorDetail: "No run-state.json was found.",
  runState: "none",
  availableActions: ["open-run-state-directory", "cancel"],
};

test("directory inspection re-enables recovery actions for a second click", async ({ page }) => {
  const html = getRecoveryDialogHtml({ cspSource: "'unsafe-inline'" } as never, payload);
  await loadWebview(page, html);

  await expect(page.locator('[data-action="discard-run"]')).toHaveCount(0);
  await expect(page.locator('[data-action="resume-from-paused-stage"]')).toHaveCount(0);
  await expect(page.locator('[data-action="restart-from-beginning"]')).toHaveCount(0);
  await expect(page.locator('[data-action="run-producing-stage"]')).toHaveCount(0);

  await page.click('[data-action="open-run-state-directory"]');
  await expect(page.locator(".action-button").first()).toBeDisabled();
  await dispatchInboundMessage(page, { type: "recoveryActionComplete" });
  await expect(page.locator('[data-action="open-run-state-directory"]')).toBeEnabled();
  await expect(page.locator('[data-action="cancel"]')).toBeEnabled();
  await page.click('[data-action="cancel"]');

  expect(await getPostedMessages(page)).toEqual([
    { type: "action", action: "open-run-state-directory", confirmed: true },
    { type: "action", action: "cancel", confirmed: true },
  ]);
});
