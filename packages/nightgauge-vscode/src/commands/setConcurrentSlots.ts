import * as vscode from "vscode";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { TierGate } from "../platform/TierGate";
import type { LicensePreflight } from "../platform/LicensePreflight";

export function registerSetConcurrentSlotsCommand(
  concurrentPipelineManager: ConcurrentPipelineManager | null,
  incrediRoot: string | null,
  tierGate?: TierGate | null,
  licensePreflight?: LicensePreflight | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.setConcurrentSlots", async () => {
    const current = concurrentPipelineManager?.maxConcurrentSlots ?? 1;

    const options: vscode.QuickPickItem[] = [1, 2, 3, 4, 5].map((n) => ({
      label: n === 1 ? "1 — sequential" : String(n),
      description: n === current ? "current" : undefined,
      picked: n === current,
    }));

    const picked = await vscode.window.showQuickPick(options, {
      title: "Set Concurrent Pipeline Slots",
      placeHolder: "How many issues should run at the same time?",
    });

    if (!picked) return;

    const n = parseInt(picked.label, 10);

    // Tier gate: running more than 1 concurrent pipeline is a pro+ feature
    // (Issue #4156). Sequential (n=1) is always allowed regardless of tier —
    // only the actual concurrency bypass needs gating.
    if (n > 1 && tierGate && licensePreflight) {
      const preflightResult = await licensePreflight.validate();
      const gate = tierGate.check("concurrent-pipelines", preflightResult.tier);
      if (!gate.allowed) {
        const action = await vscode.window.showInformationMessage(
          `Running more than 1 pipeline concurrently requires ${gate.requiredTier} tier. Upgrade to unlock concurrent pipelines.`,
          "View Plans"
        );
        if (action === "View Plans") {
          void vscode.env.openExternal(vscode.Uri.parse(gate.upgradeUrl));
        }
        return;
      }
    }

    // Update the TypeScript-side slot manager immediately.
    concurrentPipelineManager?.setMaxConcurrentSlots(n);

    // Persist to pipeline.max_concurrent — the unified source of truth for
    // both the TS slot manager and the Go autonomous scheduler. We deliberately
    // no longer write autonomous.max_concurrent, which is deprecated. Local
    // config is gitignored — safe for user-specific tuning. See Issue #3195.
    if (incrediRoot) {
      const yaml = new IncrediYamlService(incrediRoot);
      try {
        await yaml.writeLocal({
          pipeline: { max_concurrent: n },
        } as Parameters<typeof yaml.writeLocal>[0]);
      } finally {
        yaml.dispose();
      }
    }

    vscode.window.showInformationMessage(
      `Concurrent slots set to ${n}. Takes effect immediately for queued pipelines${n > 1 ? "; autonomous mode uses the new limit on its next scan" : ""}.`
    );
  });
}
