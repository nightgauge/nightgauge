/**
 * Show Diagnostics command (#749).
 *
 * The extension used to split diagnostics across six output channels —
 * Nightgauge, Autonomous, Codex Setup, Go Backend, Pipeline, Plugin Setup —
 * with no single place an operator could look. This command reveals the one
 * channel everything now folds into (see ../utils/logger.ts) and prints a
 * one-shot snapshot: platform connectivity, which credential kind is in use
 * (never the value — #742 made that distinction the whole point), the Go
 * binary's path and version, and the last transport error observed per
 * platform surface.
 *
 * Every line here goes through `Logger`, which redacts before it reaches the
 * channel (see ../utils/redaction.ts) — the same guarantee every other
 * consolidated line gets.
 *
 * @see Issue #749 — consolidate six output channels, add Show Diagnostics
 * @see Issue #742 — the Go daemon holds a session JWT or a license key
 */

import * as vscode from "vscode";
import { execFile } from "child_process";
import { promisify } from "util";
import type { Logger } from "../utils/logger";
import type { PlatformStatusBarItem } from "../platform/PlatformStatusBarItem";
import { BinaryResolver } from "../services/BinaryResolver";
import { IpcClientBase } from "../services/IpcClientBase";
import { TokenStorage } from "../platform/TokenStorage";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";
import { redactSecrets } from "../utils/redaction";

const execFileAsync = promisify(execFile);
const VERSION_PROBE_TIMEOUT_MS = 5000;

/** The platform.* IPC methods behind each dashboard tab, labeled for the snapshot. */
const PLATFORM_SURFACES: ReadonlyArray<{ label: string; endpoint: string }> = [
  { label: "Health", endpoint: "platform.getAnalyticsHealth" },
  { label: "Trends", endpoint: "platform.getAnalyticsTrends" },
  { label: "Cost", endpoint: "platform.getCostAnalytics" },
  { label: "Runs", endpoint: "platform.getAnalyticsRuns" },
  { label: "Compliance", endpoint: "platform.auditListReports" },
  { label: "Quota / Usage", endpoint: "platform.getUsageSummary" },
];

export interface ShowDiagnosticsDeps {
  /** The shared main-channel Logger — every snapshot line and the reveal go through it. */
  logger: Logger;
  /** For the one overall platform connectivity line; null when the platform surface never wired up. */
  platformStatusBarItem: PlatformStatusBarItem | null;
}

/**
 * Never the value — only which kind of credential the daemon is currently
 * handed, mirroring internal/platform/client.go's bearer() precedence
 * (session JWT, then license key) from the extension's own view of it.
 */
async function resolveCredentialKind(): Promise<string> {
  try {
    const token = await TokenStorage.getInstance()?.retrieve("accessToken");
    if (token) return "session (signed in)";
  } catch {
    // Fall through to the license-key check below.
  }

  try {
    const licenseKey = await SecretStorageService.getInstance()?.getSecret(
      SECRET_KEYS.platformLicenseKey
    );
    if (licenseKey) return "license key";
  } catch {
    // Fall through to "none configured".
  }

  return "none configured";
}

async function resolveGoBinary(): Promise<{ path: string | null; version: string }> {
  const path = await BinaryResolver.fromVSCode().resolve();
  if (!path) {
    return { path: null, version: "unknown (binary not found)" };
  }

  try {
    const { stdout } = await execFileAsync(path, ["version"], {
      timeout: VERSION_PROBE_TIMEOUT_MS,
    });
    const version = stdout.trim();
    return { path, version: version || "unknown (empty output)" };
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    return { path, version: `unknown (version probe failed: ${message})` };
  }
}

function formatLastError(endpoint: string): string {
  const entry = IpcClientBase.getLastTransportErrors().get(endpoint);
  if (!entry) return "no error recorded this session";
  const statusPart = entry.status !== undefined ? ` (status ${entry.status})` : "";
  // Redact here too, not only at print time — the acceptance criterion is
  // that no secret reaches the snapshot itself, not just the rendered
  // channel (#749). The raw transport message is Go/IPC error text and
  // should never legitimately carry a credential, but it is untrusted
  // enough (proxied error strings, occasionally echoing request context)
  // that this is defense-in-depth, not decoration.
  return redactSecrets(`${entry.timestamp}${statusPart} — ${entry.message}`);
}

/**
 * Build the snapshot as an array of lines rather than printing directly, so
 * tests can assert on the content without a real OutputChannel (#749).
 */
export async function buildDiagnosticsSnapshot(
  deps: Pick<ShowDiagnosticsDeps, "platformStatusBarItem">
): Promise<string[]> {
  const [credentialKind, binary] = await Promise.all([resolveCredentialKind(), resolveGoBinary()]);

  const connectivity = deps.platformStatusBarItem
    ? `${deps.platformStatusBarItem.getDisplayState()} — ${deps.platformStatusBarItem
        .getConnectionDetails()
        .replace(/\n/g, " | ")}`
    : "platform surface not initialized";

  const lines: string[] = [
    "=== Nightgauge diagnostics snapshot ===",
    `Platform connectivity: ${connectivity}`,
    `Credential in use: ${credentialKind}`,
    `Go binary path: ${binary.path ?? "not found"}`,
    `Go binary version: ${binary.version}`,
    "Last error per platform surface:",
  ];

  for (const surface of PLATFORM_SURFACES) {
    lines.push(`  ${surface.label} (${surface.endpoint}): ${formatLastError(surface.endpoint)}`);
  }

  lines.push("=== end diagnostics snapshot ===");

  // Belt-and-suspenders: redact the whole snapshot, not just the one line
  // (formatLastError) known to carry untrusted text. No line here should
  // legitimately contain a secret, but the acceptance criterion is that none
  // *can* reach the snapshot — a future field added to this array without
  // remembering to redact it is exactly the failure mode this guards (#749).
  return lines.map((line) => redactSecrets(line));
}

export function registerShowDiagnosticsCommand(deps: ShowDiagnosticsDeps): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showDiagnostics", async () => {
    deps.logger.show();
    deps.logger.info("Diagnostics requested — gathering snapshot...");

    try {
      const lines = await buildDiagnosticsSnapshot(deps);
      for (const line of lines) {
        deps.logger.info(line);
      }
    } catch (err) {
      deps.logger.error(
        "Failed to build diagnostics snapshot",
        err instanceof Error ? err : undefined
      );
      vscode.window.showErrorMessage(
        `Nightgauge: could not gather diagnostics — ${err instanceof Error ? err.message : String(err)}`
      );
    }
  });
}
