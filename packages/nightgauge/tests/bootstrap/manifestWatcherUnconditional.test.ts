/**
 * manifestWatcherUnconditional.test.ts
 *
 * Issue #704 — `.vscode/nightgauge-workspace.yaml` must be watched in every
 * install, not only when the hosted service is enabled.
 *
 * The manifest watcher used to be registered inside
 *
 *   if (services.sessionManager && services.agentRegistrationService && workspaceManager) {
 *
 * whose real purpose was agent re-registration — the
 * `workspaceManager.reload()` inside it was incidental. `sessionManager` is
 * null unless `platformEnabled`, so editing the manifest took effect only for
 * operators signed in to the hosted service. Local-only mode, the product's
 * documented headline promise, had no watcher at all and needed a window
 * reload. Whether configuration applied depended on whether the operator held
 * an account, which is the opposite of the intended relationship.
 *
 * This is asserted against the source rather than by driving `activate()`:
 * that function takes a real `vscode.ExtensionContext` and builds the whole
 * service container, which is impractical to instantiate here — the same
 * constraint tests/bootstrap/duplicateRunRecordWritersRemoved.test.ts
 * documents. The claim under test is structural ("registration is not nested
 * inside the gate"), so a structural assertion is the honest instrument.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const EXTENSION_PATH = path.resolve(__dirname, "../../src/extension.ts");
const source = readFileSync(EXTENSION_PATH, "utf-8");

/** Byte offset of the manifest watcher's creation site. */
function manifestWatcherOffset(): number {
  const idx = source.indexOf('".vscode/nightgauge-workspace.yaml"');
  expect(idx, "manifest watcher pattern not found in extension.ts").toBeGreaterThan(-1);
  return idx;
}

/**
 * Offsets of every `if (...)` whose condition mentions a platform-only
 * service. These are the gates the watcher must not sit inside.
 */
function platformGateOffsets(): number[] {
  const offsets: number[] = [];
  const re = /if\s*\(\s*services[^)]*\b(sessionManager|agentRegistrationService)\b[^)]*\)\s*\{/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(source)) !== null) {
    offsets.push(m.index);
  }
  return offsets;
}

/**
 * Walk braces from `start` to find the offset just past its closing `}`.
 * Good enough for this file's formatting; string/comment contents could in
 * principle skew it, which is why the assertions below also check ordering
 * rather than relying on the span alone.
 */
function blockEnd(start: number): number {
  const open = source.indexOf("{", start);
  let depth = 0;
  for (let i = open; i < source.length; i++) {
    if (source[i] === "{") depth++;
    else if (source[i] === "}") {
      depth--;
      if (depth === 0) return i;
    }
  }
  return source.length;
}

describe("#704 workspace manifest watcher", () => {
  it("is registered outside every platform-gated block", () => {
    const watcher = manifestWatcherOffset();

    for (const gate of platformGateOffsets()) {
      const end = blockEnd(gate);
      const nested = watcher > gate && watcher < end;
      expect(
        nested,
        `manifest watcher at offset ${watcher} is nested inside the platform gate at ${gate}. ` +
          `That is the #704 defect: the watcher only exists when the hosted service is enabled.`
      ).toBe(false);
    }
  });

  it("watches create and delete, not only change", () => {
    // Creating a manifest in a previously single-repo workspace must switch to
    // multi-workspace mode live; deleting it must switch back.
    for (const handler of ["onDidChange", "onDidCreate", "onDidDelete"]) {
      expect(source, `watcher does not subscribe to ${handler}`).toContain(`watcher.${handler}(`);
    }
  });

  it("anchors the watch to the resolved workspace root, not workspaceFolders[0]", () => {
    // A manifest in any folder but the first was silently never watched.
    expect(source).toContain("workspaceManager.getWorkspaceRoot()");

    const watcher = manifestWatcherOffset();
    const windowStart = Math.max(0, watcher - 600);
    const window = source.slice(windowStart, watcher);
    expect(
      window.includes("workspaceFolders?.[0]"),
      "manifest watcher is still anchored to workspaceFolders[0]"
    ).toBe(false);
  });

  it("keeps agent re-registration gated on the hosted service", () => {
    // Decoupling the two concerns must not un-gate the one that genuinely
    // needs an account.
    const gates = platformGateOffsets();
    expect(gates.length, "no platform-gated block remains").toBeGreaterThan(0);

    const reRegister = source.indexOf("reRegisterAfterReload");
    expect(reRegister, "re-registration handler not found").toBeGreaterThan(-1);

    const insideSomeGate = gates.some((g) => reRegister > g && reRegister < blockEnd(g));
    expect(
      insideSomeGate,
      "agent re-registration is no longer gated on sessionManager/agentRegistrationService"
    ).toBe(true);
  });

  it("debounces reloads rather than reloading per filesystem event", () => {
    const watcher = manifestWatcherOffset();
    const region = source.slice(watcher, watcher + 2000);
    expect(region).toContain("clearTimeout");
    expect(region).toContain("setTimeout");
  });
});
