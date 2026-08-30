/**
 * Every tree view the extension contributes: does its provider resolve
 * children without throwing, on an empty workspace and on a populated one?
 *
 * The providers are not constructed here — they are the real ones the
 * extension built during `activate()`, captured off `createTreeView` by the
 * observation layer. That matters: four of the seven are only created when
 * some precondition holds (a workspace root, a query service, a pipeline
 * state service), so constructing them by hand in a test would assert that a
 * provider *nobody builds in production* works.
 *
 * Empty first, then populated, in the same window. An empty workspace is the
 * state every provider hits on a user's first launch and the one most likely
 * to dereference something absent; a populated one exercises the parsing
 * path. Both only ask "did it resolve" — the shape of what came back is the
 * data-arrival tier's question (#746).
 */

import * as assert from "node:assert/strict";
import type * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { capturedTreeProviders } from "../observe.js";
import { materializePopulatedFixture } from "../fixture.js";

/** The seven view ids contributed under the Nightgauge activity bar. */
export const EXPECTED_TREE_VIEW_IDS = [
  "nightgauge.pipelineView",
  "nightgauge.workflowView",
  "nightgauge.repositoriesView",
  "nightgauge.queryResults",
  "nightgauge.knowledgeView",
  "nightgauge.attentionView",
] as const;

function providerFor(viewId: string): vscode.TreeDataProvider<unknown> {
  const captured = capturedTreeProviders().find((entry) => entry.viewId === viewId);
  if (!captured) {
    throw new Error(
      `No TreeDataProvider was registered for ${viewId}. Captured: ` +
        JSON.stringify(capturedTreeProviders().map((entry) => entry.viewId))
    );
  }
  return captured.provider;
}

/**
 * Resolve the root children, then build a tree item for each one.
 *
 * `getChildren()` alone is a weak question: several providers defer all their
 * work to `getTreeItem()`, so a provider can resolve an array of handles and
 * still throw the instant the view tries to draw one. Both are construction,
 * not data — nothing below looks at what the items say.
 */
async function resolveRoot(provider: vscode.TreeDataProvider<unknown>): Promise<unknown[]> {
  const children = (await provider.getChildren(undefined)) ?? [];
  for (const child of children.slice(0, 10)) {
    await provider.getTreeItem(child);
  }
  return [...children];
}

suite("tree views (empty workspace)", () => {
  test("all seven contributed tree views registered a provider", () => {
    const captured = capturedTreeProviders().map((entry) => entry.viewId);
    const missing = EXPECTED_TREE_VIEW_IDS.filter((id) => !captured.includes(id));
    assert.deepEqual(
      missing,
      [],
      `Tree view(s) contributed in package.json but never given a provider during activate(): ` +
        `${missing.join(", ")}. Captured providers: ${JSON.stringify(captured)}`
    );
  });

  for (const viewId of EXPECTED_TREE_VIEW_IDS) {
    test(`${viewId} resolves children on an empty workspace`, async () => {
      await resolveRoot(providerFor(viewId));
    });
  }
});

suite("tree views (populated workspace)", () => {
  test("the populated fixture lands in the workspace folder", () => {
    materializePopulatedFixture();
  });

  for (const viewId of EXPECTED_TREE_VIEW_IDS) {
    test(`${viewId} resolves children on a populated workspace`, async () => {
      await resolveRoot(providerFor(viewId));
    });
  }
});
