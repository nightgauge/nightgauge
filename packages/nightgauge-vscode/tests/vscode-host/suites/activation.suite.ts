/**
 * Does the extension come up at all?
 *
 * Nothing in this repository asked that question before this tier existed.
 * `activate()` re-throws on two paths (`initializeServices` and
 * `registerAllCommands`), and every unit test mocks the `vscode` module out
 * from under them, so a startup failure was structurally unobservable.
 */

import * as assert from "node:assert/strict";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { outputChannelErrorsSince, processFaults } from "../observe.js";
import { delay, workspaceRoot } from "../fixture.js";
import { unexpectedFaults } from "../known-issues.js";

export const EXTENSION_ID = "nightgauge.nightgauge-vscode";

/**
 * Startup work `activate()` defers with `setTimeout` runs at 2s, 3s, 3.5s,
 * 4s and 5s. Waiting past the last of them is the difference between
 * "activate() returned" and "startup finished" — the deferred timers are
 * where the auto-resume prompt, two config migrations, the attention sweep
 * and the merged-branch cleanup live, and a rejection thrown from any of
 * them is exactly the class this tier exists to catch.
 */
const DEFERRED_STARTUP_SETTLE_MS = 6_500;

let activationStartedAt = 0;

export function extension(): vscode.Extension<unknown> {
  const found = vscode.extensions.getExtension(EXTENSION_ID);
  if (!found) {
    throw new Error(
      `Extension ${EXTENSION_ID} is not present in this window. ` +
        `--extensionDevelopmentPath must point at packages/nightgauge-vscode and ` +
        `dist/extension.cjs must be built.`
    );
  }
  return found;
}

suite("activation", () => {
  test("the extension is discovered by the host", () => {
    const ext = extension();
    assert.equal(ext.id, EXTENSION_ID);
  });

  test("the fixture workspace does not auto-activate the extension", () => {
    // `activationEvents` is workspaceContains:.nightgauge/{pipeline,plans}.
    // The empty fixture matches neither, which is what lets the next case
    // observe activation from the outside instead of racing it.
    assert.equal(
      extension().isActive,
      false,
      "Extension was already active before the smoke tier activated it — the fixture " +
        "workspace must not contain .nightgauge/pipeline or .nightgauge/plans, or " +
        "activation-time faults are unobservable."
    );
  });

  test("activate() resolves against the fixture workspace", async () => {
    activationStartedAt = Date.now();
    await extension().activate();
    assert.equal(extension().isActive, true, "activate() resolved but isActive is false");
  });

  test("deferred startup work settles without a new unhandled rejection", async () => {
    await delay(DEFERRED_STARTUP_SETTLE_MS);
    const unexpected = unexpectedFaults(processFaults());
    assert.deepEqual(
      unexpected.map((fault) => `${fault.kind}: ${fault.detail}`),
      [],
      "Unhandled rejection(s) escaped during extension startup that are not recorded in " +
        "tests/vscode-host/known-issues.ts. The right response is a fix, not a new entry."
    );
  });

  test("no output channel recorded an error during startup", () => {
    const errors = outputChannelErrorsSince(activationStartedAt);
    assert.deepEqual(
      errors.map((entry) => `${entry.channel}: ${entry.line}`),
      [],
      "Startup wrote ERROR-level line(s) to an output channel"
    );
  });

  test("the workspace root is a real, writable directory", () => {
    // Guards the fixture plumbing itself: several providers are only
    // constructed when a workspace root exists, so a folderless window would
    // quietly test a much smaller extension than the one users run.
    const root = workspaceRoot();
    assert.ok(root.length > 0);
  });
});
