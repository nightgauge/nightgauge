/**
 * configureForgeInstance - Multi-step wizard for adding or editing a forge instance.
 *
 * Stores credentials in VSCode SecretStorage (OS keychain). Writes forge metadata
 * (kind, base_url, auth_method, ca_bundle) to .nightgauge/config.yaml via
 * IncrediYamlService. Supports GitHub and GitLab forge kinds with PAT, OAuth2,
 * CI job token, and deploy token auth methods.
 *
 * @see Issue #3364 - VSCode extension settings UI for managing forge instances
 */

import * as vscode from "vscode";
import * as path from "path";
import { SecretStorageService } from "../services/SecretStorageService";
import { IpcClient } from "../services/IpcClient";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import { validatePemFile } from "../utils/pemValidator";

const INSTANCE_ID_PATTERN = /^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$/;
const HTTPS_URL_PATTERN = /^https?:\/\/.+/;

function getWorkspaceRoot(): string | undefined {
  return vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
}

/** Register the configureForgeInstance command.
 *
 * Accepts an optional `instanceId` argument for pre-filling the wizard when
 * editing an existing instance (called from SettingsPanel forge-action:edit).
 */
export function registerConfigureForgeInstanceCommand(): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.configureForgeInstance",
    async (instanceIdArg?: string) => {
      const secretSvc = SecretStorageService.getInstance();
      if (!secretSvc) {
        vscode.window.showErrorMessage("Nightgauge: SecretStorage is not available.");
        return;
      }

      const workspaceRoot = getWorkspaceRoot();
      if (!workspaceRoot) {
        vscode.window.showErrorMessage(
          "Nightgauge: No workspace folder open. Open a project folder first."
        );
        return;
      }

      const isEdit = typeof instanceIdArg === "string" && instanceIdArg.length > 0;

      // ── Step 1: Instance ID ──────────────────────────────────────────────
      let instanceId: string;
      if (isEdit) {
        instanceId = instanceIdArg;
      } else {
        const idInput = await vscode.window.showInputBox({
          title: "Configure Forge Instance (1/6) — Instance ID",
          prompt:
            "Enter a unique ID for this forge (e.g. github, corp-gitlab). Used as the key in config.yaml.",
          placeHolder: "github",
          ignoreFocusOut: true,
          validateInput: (v) => {
            if (!v.trim()) return "Instance ID cannot be empty";
            if (!INSTANCE_ID_PATTERN.test(v.trim())) {
              return "Use lowercase letters, digits, and hyphens only (e.g. corp-gitlab)";
            }
            return null;
          },
        });
        if (!idInput) return;
        instanceId = idInput.trim();
      }

      // ── Step 2: URL ──────────────────────────────────────────────────────
      const urlInput = await vscode.window.showInputBox({
        title: `Configure Forge Instance (${isEdit ? "1" : "2"}/6) — Forge URL`,
        prompt:
          "Base URL of the forge. Leave blank for GitHub.com, or enter a GitLab instance URL.",
        placeHolder: "https://gitlab.example.com",
        value: "",
        ignoreFocusOut: true,
        validateInput: (v) => {
          if (!v.trim()) return null; // blank = default (github.com)
          if (!HTTPS_URL_PATTERN.test(v.trim())) {
            return "Must be an https:// URL";
          }
          return null;
        },
      });
      if (urlInput === undefined) return; // dismissed
      const baseUrl = urlInput.trim();

      // ── Step 3: Kind ─────────────────────────────────────────────────────
      const kindPick = await vscode.window.showQuickPick(
        [
          { label: "GitHub", description: "github.com or GitHub Enterprise", value: "github" },
          { label: "GitLab", description: "gitlab.com or self-hosted GitLab", value: "gitlab" },
        ],
        {
          title: `Configure Forge Instance (${isEdit ? "2" : "3"}/6) — Forge Kind`,
          placeHolder: "Select forge kind",
          ignoreFocusOut: true,
        }
      );
      if (!kindPick) return;
      const kind = kindPick.value;

      // ── Step 4: Auth method ───────────────────────────────────────────────
      const authItems: Array<{ label: string; description: string; value: string }> = [
        { label: "PAT", description: "Personal Access Token", value: "pat" },
        {
          label: "OAuth2",
          description: "Device-code OAuth2 flow (GitLab — preview)",
          value: "oauth2",
        },
        {
          label: "CI Job Token",
          description: "Use CI_JOB_TOKEN from the environment",
          value: "ci_job_token",
        },
        {
          label: "Deploy Token",
          description: "Deploy token with username + token",
          value: "deploy_token",
        },
      ];
      if (kind === "github") {
        // OAuth2 device-code is GitLab-specific; remove it for GitHub
        authItems.splice(1, 1);
      }

      const authPick = await vscode.window.showQuickPick(authItems, {
        title: `Configure Forge Instance (${isEdit ? "3" : "4"}/6) — Auth Method`,
        placeHolder: "Select authentication method",
        ignoreFocusOut: true,
      });
      if (!authPick) return;
      const authMethod = authPick.value;

      // ── Step 5: Credential input ──────────────────────────────────────────
      let credential: string | undefined;
      let deployUser: string | undefined;

      if (authMethod === "pat") {
        const tokenInput = await vscode.window.showInputBox({
          title: `Configure Forge Instance (${isEdit ? "4" : "5"}/6) — Personal Access Token`,
          prompt: "Enter your Personal Access Token. Stored securely in the OS keychain.",
          password: true,
          ignoreFocusOut: true,
          validateInput: (v) => (!v.trim() ? "Token cannot be empty" : null),
        });
        if (!tokenInput) return;
        credential = tokenInput.trim();
      } else if (authMethod === "oauth2") {
        // OAuth2 device-code flow — W4-3 stub; show informational message
        const proceed = await vscode.window.showInformationMessage(
          "OAuth2 device-code flow for GitLab is currently in preview. " +
            "The flow requires the nightgauge binary to support W4-3 (auth device flow). " +
            "Proceed anyway to configure the auth method and trigger the flow when ready?",
          "Proceed",
          "Cancel"
        );
        if (proceed !== "Proceed") return;

        try {
          const flowResult = await IpcClient.getInstance().authDeviceFlowStart();
          if (flowResult.verification_uri) {
            await vscode.env.openExternal(vscode.Uri.parse(flowResult.verification_uri));
            vscode.window.showInformationMessage(
              `Complete the OAuth2 flow in your browser. User code: ${flowResult.user_code ?? "(see browser)"}`
            );
          }
        } catch (err) {
          const msg = err instanceof Error ? err.message : String(err);
          if (msg.includes("not yet wired") || msg.includes("W4-3")) {
            vscode.window.showWarningMessage(
              "OAuth2 device-code flow is not yet available in this build (W4-3). " +
                "The forge will be saved with oauth2 auth method — run the flow manually later."
            );
          } else {
            vscode.window.showErrorMessage(`OAuth2 flow error: ${msg}`);
            return;
          }
        }
        // No credential stored for OAuth2 — tokens are managed by the binary
      } else if (authMethod === "ci_job_token") {
        // No credential needed — CI_JOB_TOKEN comes from the environment
        vscode.window.showInformationMessage(
          "CI job token auth uses the CI_JOB_TOKEN environment variable automatically."
        );
      } else if (authMethod === "deploy_token") {
        deployUser = await vscode.window.showInputBox({
          title: `Configure Forge Instance (${isEdit ? "4" : "5"}/6) — Deploy Token Username`,
          prompt: "Enter the deploy token username.",
          ignoreFocusOut: true,
          validateInput: (v) => (!v.trim() ? "Username cannot be empty" : null),
        });
        if (!deployUser) return;

        const tokenInput = await vscode.window.showInputBox({
          title: `Configure Forge Instance (${isEdit ? "4" : "5"}/6) — Deploy Token`,
          prompt: "Enter the deploy token. Stored securely in the OS keychain.",
          password: true,
          ignoreFocusOut: true,
          validateInput: (v) => (!v.trim() ? "Token cannot be empty" : null),
        });
        if (!tokenInput) return;
        // Store as "user:token" so it can be split on retrieval
        credential = `${deployUser.trim()}:${tokenInput.trim()}`;
      }

      // ── Step 6: CA bundle (optional) ─────────────────────────────────────
      let caBundle: string | undefined;
      const caPick = await vscode.window.showQuickPick(
        [
          { label: "$(check) No CA bundle needed", value: "skip" },
          { label: "$(file) Select CA bundle file…", value: "pick" },
        ],
        {
          title: `Configure Forge Instance (${isEdit ? "5" : "6"}/6) — CA Bundle (optional)`,
          placeHolder: "Needed only for self-signed TLS certificates",
          ignoreFocusOut: true,
        }
      );
      if (!caPick) return;

      if (caPick.value === "pick") {
        const uris = await vscode.window.showOpenDialog({
          title: "Select PEM CA Bundle",
          filters: { "PEM Certificate Bundle": ["pem", "crt", "cer"] },
          canSelectMany: false,
          canSelectFolders: false,
        });
        if (!uris || uris.length === 0) return;
        const selectedPath = uris[0].fsPath;
        const pemError = await validatePemFile(selectedPath);
        if (pemError) {
          vscode.window.showErrorMessage(`Invalid CA bundle: ${pemError}`);
          return;
        }
        // Store absolute path (Go resolves relative to config dir)
        caBundle = selectedPath;
      }

      // ── Step 7: Connection test ───────────────────────────────────────────
      const testResult = await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Testing forge connection…",
          cancellable: false,
        },
        async () => {
          try {
            // Build temp config so the test can run before saving
            const yamlService = new IncrediYamlService(workspaceRoot);
            const { config: currentCfg } = await yamlService.read();
            const tempCfg = currentCfg ?? {};
            if (!tempCfg.forges) tempCfg.forges = {};
            (tempCfg.forges as Record<string, unknown>)[instanceId] = {
              kind,
              base_url: baseUrl || undefined,
              auth_method: authMethod,
              ca_bundle: caBundle,
            };
            await yamlService.write(tempCfg, "project");

            const result = await IpcClient.getInstance().forgeConnectionTest(
              instanceId,
              credential ?? ""
            );

            // Revert temp config write if connection failed to avoid leaving partial state
            if (!result.ok) {
              const { config: origCfg } = await yamlService.read();
              if (origCfg?.forges) {
                delete (origCfg.forges as Record<string, unknown>)[instanceId];
                await yamlService.write(origCfg, "project");
              }
            }
            return result;
          } catch (err) {
            return {
              ok: false,
              error: err instanceof Error ? err.message : String(err),
              latency_ms: 0,
            };
          }
        }
      );

      if (!testResult.ok) {
        const continueAnyway = await vscode.window.showWarningMessage(
          `Connection test failed: ${testResult.error ?? "unknown error"}. Save anyway?`,
          "Save Anyway",
          "Cancel"
        );
        if (continueAnyway !== "Save Anyway") return;
      } else {
        vscode.window.showInformationMessage(
          `Forge connection successful ✓ (${testResult.latency_ms}ms)`
        );
      }

      // ── Step 8: Save ──────────────────────────────────────────────────────
      try {
        // Save credential to SecretStorage
        if (credential !== undefined) {
          await secretSvc.setForgeSecret(instanceId, credential);
        }
        if (testResult.ok) {
          await secretSvc.setForgeLastTested(instanceId, new Date().toISOString());
        }

        // Write forge metadata to config.yaml
        const yamlService = new IncrediYamlService(workspaceRoot);
        const { config: currentCfg } = await yamlService.read();
        const cfg = currentCfg ?? {};
        if (!cfg.forges) cfg.forges = {};

        const forgeEntry: Record<string, unknown> = {
          kind,
          auth_method: authMethod,
        };
        if (baseUrl) forgeEntry.base_url = baseUrl;
        if (caBundle)
          forgeEntry.ca_bundle = path.relative(path.join(workspaceRoot, ".nightgauge"), caBundle);

        (cfg.forges as Record<string, unknown>)[instanceId] = forgeEntry;
        await yamlService.write(cfg, "project");

        vscode.window.showInformationMessage(
          `Forge instance "${instanceId}" ${isEdit ? "updated" : "added"} successfully.`
        );
      } catch (err) {
        vscode.window.showErrorMessage(
          `Failed to save forge instance: ${err instanceof Error ? err.message : String(err)}`
        );
      }
    }
  );
}
