/**
 * Set Repository Max Concurrent command (Issue #2987)
 *
 * Opens a quick-pick to choose a per-repo concurrency cap and writes the
 * result to `concurrency.repository_overrides.<repo>` (machine tier) via
 * `RepositoriesTreeProvider.setRepoMaxConcurrent()`.
 *
 * Cap semantics mirror `internal/config/config.go` CapForRepo():
 *   - Workspace max  → cap at `concurrency.workspace_max` (most a repo can run)
 *   - 1 (sequential) → one pipeline at a time for this repo
 *   - N≥2            → up to N concurrent pipelines for this repo
 */

import * as vscode from "vscode";
import type { RepositoriesTreeProvider } from "../views/RepositoriesTreeProvider";
import { RepositoryTreeItem } from "../views/items/RepositoryTreeItem";
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";

const PRESET_OPTIONS: Array<{
  label: string;
  description?: string;
  value: number | undefined;
  isCustom?: boolean;
}> = [
  {
    label: "$(circle-large-outline) Workspace max",
    description: "Cap at concurrency.workspace_max — the most a single repo can run",
    value: undefined,
  },
  {
    label: "$(symbol-number) 1 (sequential)",
    description: "At most one pipeline at a time for this repo",
    value: 1,
  },
  { label: "$(symbol-number) 2", value: 2 },
  { label: "$(symbol-number) 3", value: 3 },
  { label: "$(symbol-number) 4", value: 4 },
  { label: "$(symbol-number) 5", value: 5 },
  {
    label: "$(edit) Custom…",
    description: "Enter a specific cap (≥1)",
    value: undefined,
    isCustom: true,
  },
];

interface CapPickItem extends vscode.QuickPickItem {
  value: number | undefined;
  isCustom?: boolean;
}

/**
 * Register the Set Max Concurrent command for the Repositories tree view.
 */
export function registerSetRepoMaxConcurrentCommand(
  provider: RepositoriesTreeProvider,
  tierGate?: TierGate | null,
  licensePreflight?: LicensePreflight | null
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.repo.setMaxConcurrent",
    async (item?: RepositoryTreeItem) => {
      if (!item || !(item instanceof RepositoryTreeItem)) {
        void vscode.window.showWarningMessage(
          "Right-click a repository in the Repositories view to set its concurrency cap."
        );
        return;
      }

      const currentLabel = describeCurrent(item);
      const items: CapPickItem[] = PRESET_OPTIONS.map((opt) => ({
        label: opt.label,
        description: opt.description,
        value: opt.value,
        isCustom: opt.isCustom,
      }));

      const picked = await vscode.window.showQuickPick(items, {
        title: `${item.repository.name} — concurrency cap`,
        placeHolder: `Current: ${currentLabel}`,
        matchOnDescription: true,
        ignoreFocusOut: false,
      });
      if (!picked) return;

      let value: number | undefined;
      if (picked.isCustom) {
        const input = await vscode.window.showInputBox({
          title: `${item.repository.name} — custom concurrency cap`,
          prompt: "Enter the maximum concurrent pipelines for this repo (integer ≥1)",
          value: typeof item.maxConcurrent === "number" ? String(item.maxConcurrent) : "",
          validateInput: (raw) => {
            const trimmed = raw.trim();
            if (!trimmed) return "Enter a positive integer.";
            const n = Number(trimmed);
            if (!Number.isFinite(n) || !Number.isInteger(n)) {
              return "Must be an integer.";
            }
            if (n < 1) return "Must be ≥ 1.";
            if (n > 1000) return "Must be ≤ 1000 (sanity check).";
            return null;
          },
        });
        if (input === undefined) return;
        value = Math.floor(Number(input.trim()));
      } else {
        value = picked.value;
      }

      // Tier gate: an explicit per-repo cap above 1 is the same
      // "concurrent-pipelines" entitlement setConcurrentSlots.ts gates
      // (Issue #4156) — this command is a second bypass path to the same
      // capability. "Workspace max" (value === undefined) defers to whatever
      // the workspace already allows rather than requesting a NEW escalation
      // here, so it isn't gated.
      if (value !== undefined && value > 1 && tierGate && licensePreflight) {
        const preflightResult = await licensePreflight.validate();
        const gate = tierGate.check("concurrent-pipelines", preflightResult.tier);
        if (!gate.allowed) {
          const action = await vscode.window.showInformationMessage(
            `A per-repo concurrency cap above 1 requires ${gate.requiredTier} tier. Upgrade to unlock concurrent pipelines.`,
            "View Plans"
          );
          if (action === "View Plans") {
            void vscode.env.openExternal(vscode.Uri.parse(gate.upgradeUrl));
          }
          return;
        }
      }

      await provider.setRepoMaxConcurrent(item, value);
    }
  );
}

/**
 * Format the current cap for the quick-pick placeholder text.
 */
function describeCurrent(item: RepositoryTreeItem): string {
  if (typeof item.maxConcurrent === "number" && item.maxConcurrent >= 2) {
    return `max ${item.maxConcurrent}`;
  }
  if (item.isSequential) return "sequential (1)";
  return "default";
}
