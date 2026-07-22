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
            current === "codex" ? "Current adapter" : "Use Codex adapter script execution path",
          value: "codex",
        },
        {
          label: "Gemini CLI",
          description:
            current === "gemini"
              ? "Current adapter"
              : "Use Google Gemini CLI binary execution path",
          value: "gemini",
        },
        {
          label: "Gemini SDK (Direct API)",
          detail: "Requires GEMINI_API_KEY or GOOGLE_API_KEY environment variable",
          description:
            current === "gemini-sdk"
              ? "Current adapter"
              : "Use Google Gemini SDK with direct API access",
          value: "gemini-sdk",
        },
        {
          label: "LM Studio",
          description:
            current === "lm-studio"
              ? "Current adapter"
              : "Use LM Studio local inference (HTTP to localhost:1234)",
          value: "lm-studio",
        },
        {
          label: "Ollama",
          description:
            current === "ollama"
              ? "Current adapter"
              : "Use Ollama local inference (HTTP to localhost:11434)",
          value: "ollama",
        },
        {
          label: "GitHub Copilot CLI",
          detail: "Use GitHub Copilot subscription (copilot binary)",
          description:
            current === "copilot" ? "Current adapter" : "Use GitHub Copilot CLI execution path",
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

    if (adapterSelection.value === "ollama") {
      const hasModel = process.env.NIGHTGAUGE_OLLAMA_MODEL;
      if (!hasModel) {
        // Non-blocking warning — still proceed with save
        vscode.window.showWarningMessage(
          "Ollama: No model configured. Set NIGHTGAUGE_OLLAMA_MODEL to a model " +
            "you have pulled (e.g., llama3.1, codellama). Run `ollama pull <model>` first."
        );
      }
    }

    if (adapterSelection.value === "gemini" || adapterSelection.value === "gemini-sdk") {
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
