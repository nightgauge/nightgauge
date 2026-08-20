/**
 * Contributed commands and registered commands, reconciled in both
 * directions.
 *
 * The two failures are not symmetric:
 *
 *   contributed with no registration — a palette/menu entry that throws
 *     "command 'x' not found" the moment a user clicks it. User-visible.
 *   registered with no contribution — dead code, or a command reachable only
 *     from another command. Invisible to users, but also invisible to the
 *     `--check` drift gate on `src/manifest/index.ts`, so it accumulates.
 *
 * Nothing here hard-codes a command count. The contributed set is read from
 * the extension's own `packageJSON` at runtime (#749 is adding commands right
 * now), and the id prefixes that mark a command as "ours" are derived from
 * that same set rather than listed.
 */

import * as assert from "node:assert/strict";
import * as vscode from "vscode";
import { suite, test } from "../harness.js";
import { extension } from "./activation.suite.js";
import {
  CONTRIBUTED_WITHOUT_REGISTRATION,
  REGISTERED_WITHOUT_CONTRIBUTION,
} from "../known-issues.js";

/**
 * Commands VSCode synthesizes for every contributed view. They belong to the
 * platform, appear in `getCommands()`, and will never be in any extension's
 * `contributes.commands` — counting them as drift would bury twenty real
 * entries under thirty-five platform ones.
 */
const SYNTHETIC_VIEW_COMMAND_SUFFIXES = [
  ".focus",
  ".open",
  ".removeView",
  ".resetViewLocation",
  ".toggleVisibility",
];

interface ContributedCommand {
  command: string;
  title?: string;
}

export function contributedCommandIds(): string[] {
  const contributes = (extension().packageJSON as { contributes?: { commands?: unknown } })
    .contributes;
  const commands = contributes?.commands;
  if (!Array.isArray(commands) || commands.length === 0) {
    throw new Error(
      "package.json contributes no commands — either the manifest generator regressed or " +
        "this tier is reading the wrong extension's packageJSON. Refusing to reconcile an " +
        "empty set, which would pass vacuously."
    );
  }
  return (commands as ContributedCommand[]).map((entry) => entry.command).sort();
}

/**
 * The namespaces this extension owns, derived from what it contributes.
 *
 * Currently `nightgauge` and `nightgauge-pipeline`; deriving rather than
 * listing means a new namespace is covered the day it is contributed.
 */
function ownedPrefixes(contributed: string[]): string[] {
  return [...new Set(contributed.map((id) => `${id.split(".")[0]}.`))];
}

/**
 * The `<viewId>.focus`-style ids VSCode creates for each contributed view,
 * derived from `contributes.views` rather than listed — a new view brings its
 * own five along the day it is contributed.
 */
function syntheticViewCommandIds(): Set<string> {
  const views = (extension().packageJSON as { contributes?: { views?: Record<string, unknown> } })
    .contributes?.views;
  const ids = new Set<string>();
  for (const group of Object.values(views ?? {})) {
    for (const view of (group as { id?: string }[]) ?? []) {
      if (!view?.id) {
        continue;
      }
      for (const suffix of SYNTHETIC_VIEW_COMMAND_SUFFIXES) {
        ids.add(`${view.id}${suffix}`);
      }
    }
  }
  return ids;
}

async function registeredOwnCommandIds(contributed: string[]): Promise<string[]> {
  const prefixes = ownedPrefixes(contributed);
  const synthetic = syntheticViewCommandIds();
  const all = await vscode.commands.getCommands(true);
  return all
    .filter((id) => prefixes.some((prefix) => id.startsWith(prefix)))
    .filter((id) => !synthetic.has(id))
    .sort();
}

suite("commands", () => {
  test("the contributed command list is non-trivial and unique", () => {
    const contributed = contributedCommandIds();
    assert.ok(
      contributed.length >= 100,
      `Only ${contributed.length} contributed commands — suspiciously few; the reconciliation ` +
        `below would be near-vacuous.`
    );
    assert.deepEqual(
      contributed.filter((id, index) => contributed.indexOf(id) !== index),
      [],
      "Duplicate command ids in contributes.commands"
    );
  });

  test("contributed commands with no registration match the recorded baseline", async () => {
    const contributed = contributedCommandIds();
    const registered = new Set(await vscode.commands.getCommands(true));
    const missing = contributed.filter((id) => !registered.has(id));
    assert.deepEqual(
      missing,
      [...CONTRIBUTED_WITHOUT_REGISTRATION],
      `Contributed commands with no registration changed. Each such command is a palette or ` +
        `menu entry that throws "command not found" when a user clicks it. If the list GREW, ` +
        `a new one was just shipped — fix it. If it SHRANK, delete the fixed id from ` +
        `CONTRIBUTED_WITHOUT_REGISTRATION in tests/vscode-host/known-issues.ts.`
    );
  });

  test("registered commands with no contribution match the recorded, classified baseline", async () => {
    const contributed = contributedCommandIds();
    const contributedSet = new Set(contributed);
    const registered = await registeredOwnCommandIds(contributed);
    const uncontributed = registered.filter((id) => !contributedSet.has(id));
    assert.deepEqual(
      uncontributed,
      REGISTERED_WITHOUT_CONTRIBUTION.map((entry) => entry.id),
      `Registered-but-uncontributed commands changed. These are invisible to the palette and ` +
        `to the manifest drift gate. If the list GREW, contribute the new command in ` +
        `src/manifest/index.ts or add a classified entry to REGISTERED_WITHOUT_CONTRIBUTION; ` +
        `if it SHRANK, delete the entry from tests/vscode-host/known-issues.ts.`
    );
  });

  test("every uncontributed command carries a real classification, not a bare id", () => {
    for (const entry of REGISTERED_WITHOUT_CONTRIBUTION) {
      assert.ok(
        entry.reason && entry.reason.length > 0,
        `${entry.id} in REGISTERED_WITHOUT_CONTRIBUTION has no \`reason\` — a bare id is a ` +
          `count, not a classification (#766). Every entry must say WHY it has no ` +
          `contribution: bound to a tree item, a status bar item, a webview, a ` +
          `notification — or, when none of those apply, "unverified-external" with a note ` +
          `explaining what would need confirming.`
      );
      assert.ok(
        entry.note && entry.note.trim().length > 10,
        `${entry.id} in REGISTERED_WITHOUT_CONTRIBUTION has no substantive \`note\` — the ` +
          `classification must point at the actual binding (file/class), not just assert a ` +
          `category.`
      );
    }
  });
});
