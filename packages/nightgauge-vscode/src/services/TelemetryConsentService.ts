/**
 * TelemetryConsentService — owns user-facing telemetry consent UX and state.
 *
 * Responsibilities (#3327):
 * - First-run modal prompt with three actions (Decline / Decide later / Enable).
 * - 7-day reschedule on "Decide later" (or modal Esc-dismiss).
 * - VSCode configuration is the single source of truth for `enabled`,
 *   `streams`, `uploadIntervalMinutes` (ADR-001); always written to User scope
 *   so the preference follows the user across projects.
 * - Prompt bookkeeping and `lastUploadAt` in `globalState` (per-machine).
 * - Per-stream gating via {@link isStreamEnabled}.
 *
 * VSCode global telemetry (`vscode.env.isTelemetryEnabled`) is the hard
 * kill-switch and is honored before any consent logic.
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger.js";
import { ALL_STREAMS, isTelemetryStream, type TelemetryStream } from "./telemetry/types.js";

const CONFIG_NAMESPACE = "nightgauge";
const SETTING_ENABLED = "telemetry.enabled";
const SETTING_STREAMS = "telemetry.streams";
const SETTING_UPLOAD_INTERVAL = "telemetry.uploadIntervalMinutes";

const GLOBAL_KEY_PROMPT_SEEN = "nightgauge.telemetry.firstRunPromptSeen";
const GLOBAL_KEY_NEXT_PROMPT_AT = "nightgauge.telemetry.nextPromptAtMs";
const GLOBAL_KEY_LAST_UPLOAD_AT = "nightgauge.telemetry.lastUploadAt";

const SEVEN_DAYS_MS = 7 * 24 * 60 * 60 * 1000;
const DEFAULT_UPLOAD_INTERVAL_MIN = 15;
const MIN_UPLOAD_INTERVAL_MIN = 1;
const MAX_UPLOAD_INTERVAL_MIN = 1440;

const PROMPT_MESSAGE = "Help improve Nightgauge by sharing anonymous usage data?";
const PROMPT_DETAIL =
  "We collect aggregate counts and outcome categories — never source code, file " +
  "contents, secrets, branch names, paths, or repository identifiers. You can " +
  "change this anytime in Nightgauge: Telemetry Settings, and view the full " +
  "list in docs/TELEMETRY_PRIVACY.md.";

const ACTION_DECLINE = "Decline";
const ACTION_DECIDE_LATER = "Decide later";
const ACTION_ENABLE = "Enable";

export class TelemetryConsentService {
  private readonly context: vscode.ExtensionContext;
  private readonly logger: Logger | null;
  private inFlightPrompt: Promise<void> | null = null;

  constructor(context: vscode.ExtensionContext, logger?: Logger | null) {
    this.context = context;
    this.logger = logger ?? null;
  }

  // ─── Read state ─────────────────────────────────────────────────────────

  isEnabled(): boolean {
    if (!vscode.env.isTelemetryEnabled) {
      return false;
    }
    const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
    return cfg.get<boolean>(SETTING_ENABLED) === true;
  }

  isStreamEnabled(stream: TelemetryStream): boolean {
    if (!this.isEnabled()) {
      return false;
    }
    return this.getStreams().includes(stream);
  }

  getStreams(): TelemetryStream[] {
    const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
    const raw = cfg.get<unknown>(SETTING_STREAMS);
    if (!Array.isArray(raw)) {
      return [...ALL_STREAMS];
    }
    const filtered = raw.filter(isTelemetryStream);
    return Array.from(new Set(filtered));
  }

  getUploadIntervalMinutes(): number {
    const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
    const raw = cfg.get<number>(SETTING_UPLOAD_INTERVAL);
    if (typeof raw !== "number" || !Number.isFinite(raw)) {
      return DEFAULT_UPLOAD_INTERVAL_MIN;
    }
    return clamp(raw, MIN_UPLOAD_INTERVAL_MIN, MAX_UPLOAD_INTERVAL_MIN);
  }

  getLastUploadAt(): number | null {
    const raw = this.context.globalState.get<number | undefined>(GLOBAL_KEY_LAST_UPLOAD_AT);
    return typeof raw === "number" && Number.isFinite(raw) ? raw : null;
  }

  // ─── Mutate state ───────────────────────────────────────────────────────

  async setEnabled(value: boolean): Promise<void> {
    await this.updateSetting(SETTING_ENABLED, value);
    this.logger?.info("Telemetry consent updated", { enabled: value });
  }

  async setStreams(streams: TelemetryStream[]): Promise<void> {
    const normalized = Array.from(new Set(streams.filter(isTelemetryStream)));
    await this.updateSetting(SETTING_STREAMS, normalized);
  }

  async setUploadIntervalMinutes(minutes: number): Promise<void> {
    const clamped = clamp(Math.round(minutes), MIN_UPLOAD_INTERVAL_MIN, MAX_UPLOAD_INTERVAL_MIN);
    await this.updateSetting(SETTING_UPLOAD_INTERVAL, clamped);
  }

  async recordUploadAt(timestampMs: number): Promise<void> {
    if (!Number.isFinite(timestampMs)) return;
    await this.context.globalState.update(GLOBAL_KEY_LAST_UPLOAD_AT, timestampMs);
  }

  // ─── First-run modal ────────────────────────────────────────────────────

  /**
   * Show the first-run consent modal exactly once per workspace, unless the
   * user picked "Decide later" — in which case re-prompt after 7 days.
   *
   * No-op when:
   *   - VSCode global telemetry is off,
   *   - user has already explicitly set `nightgauge.telemetry.enabled`
   *     in any scope (User or Workspace), or
   *   - workspace is within the 7-day reschedule window.
   *
   * Concurrent invocations during activation collapse to a single prompt.
   */
  async maybeShowFirstRunPrompt(): Promise<void> {
    if (this.inFlightPrompt) {
      return this.inFlightPrompt;
    }
    this.inFlightPrompt = this.runFirstRunPrompt().finally(() => {
      this.inFlightPrompt = null;
    });
    return this.inFlightPrompt;
  }

  private async runFirstRunPrompt(): Promise<void> {
    if (!vscode.env.isTelemetryEnabled) {
      return;
    }

    // If consent has been explicitly decided in any non-default scope,
    // mark prompt seen and exit.
    if (this.consentExplicitlySet()) {
      await this.context.globalState.update(GLOBAL_KEY_PROMPT_SEEN, true);
      return;
    }

    const promptSeen = this.context.globalState.get<boolean>(GLOBAL_KEY_PROMPT_SEEN, false);
    const nextPromptAt = this.context.globalState.get<number | undefined>(
      GLOBAL_KEY_NEXT_PROMPT_AT
    );
    const now = Date.now();

    if (promptSeen) {
      // If we've shown before and there's no scheduled re-prompt, never re-ask.
      if (typeof nextPromptAt !== "number") {
        return;
      }
      // If the rescheduled time hasn't elapsed, exit.
      if (nextPromptAt > now) {
        return;
      }
    }

    await this.context.globalState.update(GLOBAL_KEY_PROMPT_SEEN, true);
    this.logger?.info("Showing telemetry first-run prompt");

    // Order matters: the FIRST action becomes the modal default-focus.
    // Decline first → safe default per ADR-004.
    const choice = await vscode.window.showInformationMessage(
      PROMPT_MESSAGE,
      { modal: true, detail: PROMPT_DETAIL },
      ACTION_DECLINE,
      ACTION_DECIDE_LATER,
      ACTION_ENABLE
    );

    if (choice === ACTION_ENABLE) {
      await this.setEnabled(true);
      await this.context.globalState.update(GLOBAL_KEY_NEXT_PROMPT_AT, undefined);
      void vscode.window.showInformationMessage(
        "Telemetry enabled. Open Nightgauge: Telemetry Settings to fine-tune which streams send data."
      );
    } else if (choice === ACTION_DECLINE) {
      await this.setEnabled(false);
      await this.context.globalState.update(GLOBAL_KEY_NEXT_PROMPT_AT, undefined);
    } else {
      // "Decide later" or modal dismissed via Esc → reschedule for 7 days.
      await this.context.globalState.update(GLOBAL_KEY_NEXT_PROMPT_AT, now + SEVEN_DAYS_MS);
      this.logger?.info("Telemetry decision deferred", {
        nextPromptAtMs: now + SEVEN_DAYS_MS,
      });
    }
  }

  // ─── Settings panel entrypoint ──────────────────────────────────────────

  /**
   * Open the Telemetry Settings webview panel. The panel module is loaded
   * lazily to avoid a hard dependency cycle between the consent service and
   * the view layer.
   */
  async openSettingsPanel(): Promise<void> {
    const { TelemetrySettingsPanel } = await import("../views/telemetry/TelemetrySettingsPanel.js");
    TelemetrySettingsPanel.show(this.context, this);
  }

  // ─── Internals ──────────────────────────────────────────────────────────

  /**
   * `inspect()` distinguishes a default value from a real user/workspace
   * setting. We treat `enabled` as decided when ANY non-default scope has
   * a value.
   */
  private consentExplicitlySet(): boolean {
    const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
    const inspect = cfg.inspect<boolean>(SETTING_ENABLED);
    if (!inspect) return false;
    return (
      inspect.globalValue !== undefined ||
      inspect.workspaceValue !== undefined ||
      inspect.workspaceFolderValue !== undefined
    );
  }

  private async updateSetting(key: string, value: unknown): Promise<void> {
    const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
    await cfg.update(key, value, vscode.ConfigurationTarget.Global);
  }
}

function clamp(value: number, min: number, max: number): number {
  if (Number.isNaN(value)) return min;
  return Math.min(Math.max(value, min), max);
}
