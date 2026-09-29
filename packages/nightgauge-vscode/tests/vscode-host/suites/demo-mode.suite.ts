/**
 * Demo-mode awareness (#2108, ADR-026 decision 4). The host tier's backend is
 * the demo IPC stub, whose `ipc.ready` carries `demo: true`, so once the
 * extension has connected the "Demo" status bar badge must be up.
 *
 * Not covered here: playing the reference scenario's UI steps. The stub is
 * wired through `nightgauge.backend.binaryPath`, which takes no arguments,
 * so the tier cannot pass it `--scenario`.
 */

import * as assert from "node:assert/strict";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedStatusBarItems } from "../observe.js";
import { waitFor } from "../fixture.js";

suite("demo mode", () => {
  test("the demo daemon's handshake shows the Demo badge", async () => {
    // Any IPC call connects the backend; the handshake follows.
    await vscode.commands.executeCommand("nightgauge.showDashboard");
    const badge = await waitFor(
      () =>
        capturedStatusBarItems().find(
          (item) => item.id === "nightgauge.demoMode" && item.text.includes("Demo")
        ),
      10_000,
      "the Demo status bar badge"
    );
    assert.match(badge.accessibilityInformation?.label ?? "", /demo mode/i);
  });
});
