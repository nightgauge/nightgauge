import * as vscode from "vscode";
import type { Logger } from "../utils/logger";
import type { SessionManager } from "../platform/SessionManager";
import type { IncrediConfig } from "../views/settings/types";
import {
  type PlatformEnvironment,
  PLATFORM_ENV_PRESETS,
  resolvePlatformBaseUrl,
} from "../config/schema";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import { ConfigBridge } from "../services/ConfigBridge";
import { ProjectEventSubscriber } from "../services/ProjectEventSubscriber";
import { TokenStorage } from "../platform/TokenStorage";

interface EnvironmentOption extends vscode.QuickPickItem {
  value: PlatformEnvironment;
}

export function registerSwitchPlatformEnvironmentCommand(
  logger: Logger,
  _sessionManager: SessionManager | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.platform.switchEnvironment", async () => {
    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!workspaceRoot) {
      vscode.window.showErrorMessage("No workspace folder open.");
      return;
    }

    const currentEnv = ConfigBridge.getInstance().getPlatform()?.environment ?? "production";

    const items: EnvironmentOption[] = [
      {
        label: "Production",
        description: currentEnv === "production" ? "$(check) Current" : undefined,
        detail: PLATFORM_ENV_PRESETS.production,
        value: "production",
      },
      {
        label: "Canary",
        description: currentEnv === "canary" ? "$(check) Current" : undefined,
        detail: PLATFORM_ENV_PRESETS.canary,
        value: "canary",
      },
      {
        label: "Local",
        description: currentEnv === "local" ? "$(check) Current" : undefined,
        detail: PLATFORM_ENV_PRESETS.local,
        value: "local",
      },
      {
        label: "Custom",
        description: currentEnv === "custom" ? "$(check) Current" : undefined,
        detail: "Enter a custom HTTPS URL (localhost exempt)",
        value: "custom",
      },
    ];

    const selected = await vscode.window.showQuickPick<EnvironmentOption>(items, {
      title: "Nightgauge: Switch Platform Environment",
      placeHolder: "Select environment",
    });

    if (!selected) {
      return;
    }

    const selectedEnv = selected.value;
    let customUrl: string | undefined;

    if (selectedEnv === "custom") {
      customUrl = await vscode.window.showInputBox({
        title: "Custom Platform URL",
        prompt: "Enter HTTPS URL (localhost allowed)",
        placeHolder: "https://my.platform.example.com",
        validateInput: (val) => {
          try {
            resolvePlatformBaseUrl({ environment: "custom", api_url: val });
            return undefined;
          } catch (e) {
            return e instanceof Error ? e.message : "Invalid URL";
          }
        },
      });

      if (customUrl === undefined) {
        return;
      }
    }

    const delta: IncrediConfig = {
      platform: {
        environment: selectedEnv,
        ...(selectedEnv === "custom" ? { api_url: customUrl } : {}),
      },
    } as IncrediConfig;

    const yamlService = new IncrediYamlService(workspaceRoot);
    try {
      const writeResult = await yamlService.writeLocal(delta);
      if (!writeResult.success) {
        vscode.window.showErrorMessage(
          `Failed to save platform environment: ${writeResult.error ?? "unknown error"}`
        );
        return;
      }
    } finally {
      yamlService.dispose();
    }

    await ConfigBridge.getInstance().reload();

    const newBaseUrl = resolvePlatformBaseUrl(ConfigBridge.getInstance().getPlatform());

    // Re-target SSE stream if a subscriber is active
    try {
      // #3925 — non-throwing accessor: when no subscriber is initialized
      // (event_stream_enabled false) this is simply a no-op, not an exception.
      const subscriber = ProjectEventSubscriber.getInstanceOrNull();
      if (subscriber?.isConnected()) {
        subscriber.disconnect();
        const accessToken = (await TokenStorage.getInstance()?.retrieve("accessToken")) ?? null;
        if (accessToken) {
          subscriber.connect(newBaseUrl, accessToken);
        } else {
          logger.warn(
            "switchPlatformEnvironment: no access token — SSE will reconnect on next auth cycle"
          );
        }
      }
    } catch {
      // Subscriber not initialized — no-op
    }

    logger.info("Platform environment switched", {
      environment: selectedEnv,
      baseUrl: newBaseUrl,
    });

    vscode.window.showInformationMessage(
      `Nightgauge: Platform environment set to ${selectedEnv}${selectedEnv === "custom" ? ` (${customUrl})` : ""}.`
    );
  });
}
