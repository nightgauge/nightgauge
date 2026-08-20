/**
 * Fixture-workspace helpers for the VSCode host smoke tier.
 *
 * The tier runs against ONE VSCode window, and moves that window's single
 * workspace folder between two states:
 *
 *   empty     — the folder as opened: no `.nightgauge/` at all. Nothing in
 *               `activationEvents` matches, so the extension is inert until
 *               the activation suite calls `activate()` explicitly. That is
 *               what makes "was activation clean" answerable at all: if the
 *               window auto-activated on open, activation would race the
 *               loading of this test module and any rejection it threw would
 *               already be lost by the time observers were installed.
 *   populated — `.nightgauge/{config.yaml,pipeline/state.json,knowledge/…}`
 *               copied in from `tests/fixtures/vscode-host/populated/`.
 *
 * One window rather than two launches: a second `runTests()` costs another
 * VSCode boot for no additional signal, and the empty→populated transition
 * has to be observable from inside a single process anyway for the tree-view
 * cases to compare the two.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";

/** Absolute path of the (temp) folder VSCode opened as the workspace. */
export function workspaceRoot(): string {
  const folder = vscode.workspace.workspaceFolders?.[0];
  if (!folder) {
    throw new Error(
      "No workspace folder is open. The launcher must pass a folder to " +
        "runTests({ launchArgs: [folder] }) — several providers are only constructed " +
        "when a workspace root exists, so a folderless window would silently test less."
    );
  }
  return folder.uri.fsPath;
}

/** Source tree holding the committed `populated` fixture content. */
function populatedFixtureSource(): string {
  const source = process.env.NIGHTGAUGE_HOST_FIXTURE_SOURCE;
  if (!source) {
    throw new Error(
      "NIGHTGAUGE_HOST_FIXTURE_SOURCE is unset — the launcher did not pass the fixture " +
        "path through extensionTestsEnv."
    );
  }
  if (!fs.existsSync(source)) {
    throw new Error(`Fixture source does not exist: ${source}`);
  }
  return source;
}

export function isPopulated(): boolean {
  return fs.existsSync(path.join(workspaceRoot(), ".nightgauge"));
}

/**
 * Copy the committed `populated` fixture into the open workspace folder.
 *
 * Deliberately additive and idempotent: providers may already hold a handle
 * to the folder, and nothing here should invalidate it.
 */
export function materializePopulatedFixture(): void {
  const root = workspaceRoot();
  copyTree(populatedFixtureSource(), root);
  if (!isPopulated()) {
    throw new Error(`Populated fixture did not land under ${root}/.nightgauge`);
  }
}

function copyTree(from: string, to: string): void {
  fs.mkdirSync(to, { recursive: true });
  for (const entry of fs.readdirSync(from, { withFileTypes: true })) {
    const src = path.join(from, entry.name);
    const dest = path.join(to, entry.name);
    if (entry.isDirectory()) {
      copyTree(src, dest);
    } else if (entry.isFile()) {
      fs.copyFileSync(src, dest);
    }
  }
}

/** Resolve after `ms`, without blocking the extension host. */
export function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Poll `probe` until it returns a truthy value or the budget runs out.
 *
 * Webview HTML is assigned synchronously by most of these views but
 * asynchronously by a few (the Dashboard renders after its first data read),
 * so "is the body non-empty" is a question with a settling time.
 */
export async function waitFor<T>(
  probe: () => T | undefined | null,
  budgetMs: number,
  what: string
): Promise<T> {
  const deadline = Date.now() + budgetMs;
  for (;;) {
    const value = probe();
    if (value) {
      return value;
    }
    if (Date.now() >= deadline) {
      throw new Error(`Timed out after ${budgetMs}ms waiting for ${what}`);
    }
    await delay(50);
  }
}
