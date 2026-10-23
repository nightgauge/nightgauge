/**
 * The IPC surface the UI actually uses (#2103).
 *
 * The whole tier runs against `demo/ipc-stub.cjs` (see launch.ts), which logs
 * every request. The harness writes a marker before each case, so this suite
 * only has to *exercise* the surfaces demo mode must drive — every tree view
 * and every dashboard tab — for the log to attribute each method to them.
 * `launch.ts --write-ipc-inventory` turns that log into
 * `demo/ipc-inventory.json`.
 */

import * as assert from "node:assert/strict";
import * as fs from "node:fs";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedPanels, capturedTreeProviders, notificationsSince } from "../observe.js";
import { delay, materializePopulatedFixture, waitFor } from "../fixture.js";
import { EXPECTED_TREE_VIEW_IDS } from "./treeviews.suite.js";
import { VALID_TABS } from "../../../src/views/dashboard/DashboardHtml.js";

/** Long enough for a tab's lazy load to issue its requests. */
const TAB_SETTLE_MS = 750;

function ipcLog(): string {
  const file = process.env.NIGHTGAUGE_DEMO_IPC_LOG;
  if (!file) {
    throw new Error("NIGHTGAUGE_DEMO_IPC_LOG is unset — the launcher did not wire the IPC stub.");
  }
  return file;
}

function loggedMethods(): string[] {
  if (!fs.existsSync(ipcLog())) return [];
  return fs
    .readFileSync(ipcLog(), "utf8")
    .split("\n")
    .filter((line) => line.trim())
    .map((line) => (JSON.parse(line) as { method?: string }).method)
    .filter((method): method is string => typeof method === "string");
}

suite("ipc inventory", () => {
  test("the extension talked to the IPC stub during activation", () => {
    const methods = loggedMethods();
    assert.ok(
      methods.length > 0,
      `The IPC stub logged no requests to ${ipcLog()}. Either nightgauge.backend.binaryPath ` +
        "did not reach the extension or the extension never started its backend."
    );
  });

  test("the populated fixture is in place", () => {
    materializePopulatedFixture();
  });

  for (const viewId of EXPECTED_TREE_VIEW_IDS) {
    test(`tree view ${viewId}`, async () => {
      const captured = capturedTreeProviders().find((entry) => entry.viewId === viewId);
      assert.ok(captured, `No provider captured for ${viewId}`);
      const provider = captured.provider;
      const roots = (await provider.getChildren(undefined)) ?? [];
      // One level down: repositories and status groups fetch on expansion.
      for (const root of roots.slice(0, 10)) {
        await provider.getTreeItem(root);
        await provider.getChildren(root);
      }
      await delay(TAB_SETTLE_MS);
    });
  }

  test("dashboard opens", async () => {
    await vscode.commands.executeCommand("nightgauge.showDashboard");
    await waitFor(
      () => capturedPanels().find((p) => p.viewType === "nightgaugeDashboard" && !p.disposed),
      5_000,
      "the dashboard panel"
    );
    await delay(TAB_SETTLE_MS);
  });

  for (const tab of VALID_TABS) {
    test(`dashboard tab ${tab}`, async () => {
      const panel = capturedPanels().find(
        (p) => p.viewType === "nightgaugeDashboard" && !p.disposed
      );
      assert.ok(panel, "The dashboard panel is not open");
      assert.ok(
        panel.messageListeners.length > 0,
        "The dashboard registered no webview message listener to receive a tab click"
      );
      // Exactly what the webview posts when the tab is clicked.
      for (const listener of panel.messageListeners) {
        await listener({ type: "selectTab", tab });
      }
      await delay(TAB_SETTLE_MS);
    });
  }

  test("dashboard closes", () => {
    for (const panel of capturedPanels().filter((p) => !p.disposed)) {
      panel.panel.dispose();
    }
  });

  test("no error notification was shown while running against the stub", () => {
    const errors = notificationsSince(0).filter((entry) => entry.severity === "error");
    assert.deepEqual(
      errors.map((entry) => entry.message),
      [],
      "The extension raised error toast(s) against the IPC stub"
    );
  });
});
