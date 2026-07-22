import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { IncrediConfig } from "../views/settings/types";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import { getExecutionAdapter, type ExecutionAdapter } from "../utils/incrediConfig";
import { ConfigBridge } from "../services/ConfigBridge";

interface AdapterOption extends vscode.QuickPickItem {
  value: ExecutionAdapter;
}

/**
 * Build a minimal config delta containing only the adapter field.
 * writeLocal() merges this onto the existing local config file, so all
 * other keys are preserved as-is.
 */
function adapterDelta(adapter: ExecutionAdapter): IncrediConfig {
  return { ui: { core: { adapter } } } as IncrediConfig;
}

export function registerSwitchAdapterCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.switchAdapter", async () => {
    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!workspaceRoot) {
      vscode.window.showErrorMessage("No workspace folder open.");
      return;
    }

    const current = getExecutionAdapter(workspaceRoot);
    const adapterSelection = await vscode.window.showQuickPick<AdapterOption>(
      [
        {
          label: "Claude (default)",
          description: current === "claude" ? "Current adapter" : "Use Claude CLI execution path",
          value: "claude",
        },
        {
          label: "Codex",
          description:
            current === "codex" ? "Current adapter (beta)" : "Beta agentic pipeline adapter",
          value: "codex",
        },
        {
          label: "Gemini CLI",
          description:
            current === "gemini"
              ? "Current adapter (experimental)"
              : "Experimental agentic pipeline adapter",
          value: "gemini",
        },
        {
          label: "GitHub Copilot CLI",
          detail: "Experimental; requires the Copilot CLI and subscription",
          description:
            current === "copilot"
              ? "Current adapter (experimental)"
              : "Experimental agentic pipeline adapter",
          value: "copilot",
        },
      ],
      {
        title: "Nightgauge: Switch Execution Adapter",
        placeHolder: `Current adapter: ${current}`,
      }
    );

    if (!adapterSelection) {
      return;
    }

    if (adapterSelection.value === "copilot") {
      const hasToken =
        process.env.GH_TOKEN || process.env.GITHUB_TOKEN || process.env.COPILOT_GITHUB_TOKEN;
      if (!hasToken) {
        // Non-blocking warning — still proceed with save
        vscode.window.showWarningMessage(
          "GitHub Copilot: No GH_TOKEN, GITHUB_TOKEN, or COPILOT_GITHUB_TOKEN " +
            "found. The pipeline will attempt `copilot auth status` at runtime. " +
            "Run `gh auth login` if authentication fails."
        );
      }
    }

    if (adapterSelection.value === "gemini") {
      const hasApiKey = process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY;
      if (!hasApiKey) {
        // Non-blocking warning — still proceed with save
        vscode.window.showWarningMessage(
          "Gemini: No GEMINI_API_KEY or GOOGLE_API_KEY found. " +
            "Auth cascade at runtime: GEMINI_API_KEY → GOOGLE_API_KEY → gcloud auth. " +
            "Run `gcloud auth application-default login` if authentication fails."
        );
      }
    }

    // Always write to local config (config.local.yaml, gitignored).
    // The adapter is a personal developer preference — different developers
    // on the same project may use different providers. It should never be
    // committed to the project config.
    const yamlService = new IncrediYamlService(workspaceRoot);

    try {
      const writeResult = await yamlService.writeLocal(adapterDelta(adapterSelection.value));

      if (!writeResult.success) {
        vscode.window.showErrorMessage(
          `Failed to save adapter: ${writeResult.error ?? "unknown error"}`
        );
        return;
      }

      await ConfigBridge.getInstance().reload();
      logger.info("Execution adapter switched", {
        adapter: adapterSelection.value,
      });

      vscode.window.showInformationMessage(`Nightgauge adapter set to ${adapterSelection.value}.`);
    } finally {
      yamlService.dispose();
    }
  });
}
