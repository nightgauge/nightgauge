/**
 * TelemetryConsentService — owns user-facing telemetry consent UX and state.
 *
 * Responsibilities (originally issue 3327, revised by #738):
 * - First-run **disclosure notice** with two actions (Turn off / Keep on).
 * - VSCode configuration is the single source of truth for `enabled`,
 *   `streams`, `uploadIntervalMinutes` (ADR-001); always written to User scope
 *   so the preference follows the user across projects.
 * - Notice bookkeeping and `lastUploadAt` in `globalState` (per-machine).
 * - Per-stream gating via {@link isStreamEnabled}.
 *
 * ## Telemetry is opt-out, so the modal states rather than asks (#738)
 *
 * This used to be a permission dialog — "Help improve Nightgauge by sharing
 * anonymous usage data?" with Decline / Decide later / Enable. Once the default
 * is on, that wording is simply false: it presents an accomplished fact as a
 * pending request, and "Decide later" tells the operator nothing is flowing
 * when something is.
 *
 * So it became a notice. It reports what is already happening and offers the
 * off switch, which is the pattern every credible opt-out product uses. The
 * flip is only defensible *because* of this: a default nobody is told about is
 * not opt-out, it is undisclosed collection. The notice is the consideration
 * paid for the default, not an optional nicety attached to it.
 *
 * There is deliberately no "decide later" any more. Deferral was an answer to a
 * question, and there is no longer a question outstanding.
 *
 * VSCode global telemetry (`vscode.env.isTelemetryEnabled`) remains the hard
 * kill-switch and is honored before any consent logic.
 */

import * as vscode from "vscode";
import type { Logger } from "../utils/logger.js";
import { ALL_STREAMS, isTelemetryStream, type TelemetryStream } from "./telemetry/types.js";
import { ConfigBridge } from "./ConfigBridge.js";

const CONFIG_NAMESPACE = "nightgauge";
const SETTING_ENABLED = "telemetry.enabled";
const SETTING_STREAMS = "telemetry.streams";
const SETTING_UPLOAD_INTERVAL = "telemetry.uploadIntervalMinutes";

/**
 * Bookkeeping for the disclosure notice. Each material change to what is sent
 * gets a **new** key rather than a reuse of the last one, and that is the
 * point: an operator who saw an earlier notice has not seen this disclosure,
 * and suppressing it because they once saw a different message would silently
 * keep the one population most entitled to be told on the old description.
 * v2 (the #738 key was `optOutNoticeSeen`) says what a run sends — the issue
 * title, labels and body excerpt, the branch, error messages — where the first
 * notice called it anonymous usage data that never included branch names.
 */
const GLOBAL_KEY_NOTICE_SEEN = "nightgauge.telemetry.disclosureSeen.v2";
const GLOBAL_KEY_LAST_UPLOAD_AT = "nightgauge.telemetry.lastUploadAt";

const DEFAULT_UPLOAD_INTERVAL_MIN = 15;
const MIN_UPLOAD_INTERVAL_MIN = 1;
const MAX_UPLOAD_INTERVAL_MIN = 1440;

const NOTICE_MESSAGE = "With cloud features on, Nightgauge sends run telemetry to your account.";
const NOTICE_DETAIL =
  "This is on by default. Nothing is sent unless cloud features are on " +
  "(platform.enabled in your machine-tier config.yaml, or a license or API key " +
  "in the environment VS Code was started from) and you are signed in or have " +
  "a license key. Each run then sends its repository and issue number, the " +
  "issue title, labels and the first 8,192 characters of its body, the branch, " +
  "stage timings, token counts, cost and outcome, and a failed stage's error " +
  "message (which can quote command output) — never your source code, file " +
  "contents or secrets. " +
  "Adapter usage (how much of your AI plan is left) is reported to your own " +
  "account dashboard so you can see it across machines. You can turn any of it " +
  "off now or later in Nightgauge: Telemetry Settings, and read what is sent, " +
  "when and why in docs/TELEMETRY_PRIVACY.md.";

const ACTION_TURN_OFF = "Turn off";
const ACTION_KEEP_ON = "Keep on";

/** The setting `editorTelemetryConsent` reads, for change subscriptions. */
export const TELEMETRY_ENABLED_SETTING = `${CONFIG_NAMESPACE}.${SETTING_ENABLED}`;

/**
 * The editor's telemetry consent: VS Code's own telemetry level (the hard
 * kill switch) and `nightgauge.telemetry.enabled` (opt-out, so only an
 * explicit `false` withdraws it). The extension hands this to the daemon it
 * starts, which sends run telemetry only while it holds (and only with the
 * cloud enabled); see docs/TELEMETRY_PRIVACY.md.
 */
export function editorTelemetryConsent(): boolean {
  if (!vscode.env?.isTelemetryEnabled) {
    return false;
  }
  const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
  return cfg.get<boolean>(SETTING_ENABLED) !== false;
}

export class TelemetryConsentService {
  private readonly context: vscode.ExtensionContext;
  private readonly logger: Logger | null;
  private inFlightPrompt: Promise<void> | null = null;

  constructor(context: vscode.ExtensionContext, logger?: Logger | null) {
    this.context = context;
    this.logger = logger ?? null;
  }

  // ─── Read state ─────────────────────────────────────────────────────────

  /**
   * Whether telemetry may send.
   *
   * Opt-out (#738): only an explicit `false` disables. The editor's own kill
   * switch is checked first and cannot be overridden by any Nightgauge setting.
   *
   * The `!== false` rather than `=== true` is the entire semantic change, and
   * it is load-bearing in a way `??` would not be: `cfg.get` returns the
   * manifest default (now `true`) when unset, so this reads as on for a fresh
   * install and stays off for anyone who ever declined.
   */
  isEnabled(): boolean {
    return editorTelemetryConsent();
  }

  /**
   * `platform.telemetry.enabled`, the machine-tier switch the daemon and the
   * CLI read. Opt-out like the setting: only an explicit `false` withdraws
   * it. A repository's config cannot set it (the merge never takes the
   * `platform` block from a repository tier, #1049). Fails closed until the
   * configuration has been read, since an explicit `false` cannot be seen
   * before then.
   *
   * Kept apart from {@link isEnabled}, which is the editor's own consent: the
   * extension hands that one to the daemon, which reads this one itself, and
   * the Telemetry Settings panel's switch shows the setting it writes.
   */
  isPlatformTelemetryEnabled(): boolean {
    const bridge = ConfigBridge.getInstance();
    if (!bridge.isInitialized()) {
      return false;
    }
    return bridge.getPlatform()?.telemetry?.enabled !== false;
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

  // ─── First-run disclosure notice ────────────────────────────────────────

  /**
   * Show the disclosure notice exactly once per machine.
   *
   * No-op when:
   *   - VSCode global telemetry is off (nothing is being sent, so there is
   *     nothing to disclose), or
   *   - `nightgauge.telemetry.enabled` is `false` — the operator turned it
   *     off, so nothing is being sent and there is nothing to disclose, or
   *   - this notice has already been shown on this machine.
   *
   * An explicit `true` does not skip it: that choice was made on an earlier,
   * different description of what is sent, and this one is owed once.
   *
   * Concurrent invocations during activation collapse to a single notice.
   */
  async maybeShowFirstRunPrompt(): Promise<void> {
    if (this.inFlightPrompt) {
      return this.inFlightPrompt;
    }
    this.inFlightPrompt = this.runFirstRunNotice().finally(() => {
      this.inFlightPrompt = null;
    });
    return this.inFlightPrompt;
  }

  private async runFirstRunNotice(): Promise<void> {
    if (!vscode.env.isTelemetryEnabled) {
      return;
    }
    if (this.context.globalState.get<boolean>(GLOBAL_KEY_NOTICE_SEEN, false)) {
      return;
    }
    if (!this.isEnabled()) {
      await this.context.globalState.update(GLOBAL_KEY_NOTICE_SEEN, true);
      return;
    }

    // Mark seen before awaiting the modal, not after. A window closed mid-modal
    // would otherwise re-show it on every activation; the operator has been
    // told either way, and the settings panel remains the durable off switch.
    await this.context.globalState.update(GLOBAL_KEY_NOTICE_SEEN, true);
    this.logger?.info("Showing telemetry disclosure notice");

    // Order matters: the FIRST action takes modal default-focus. "Turn off"
    // leads for the same reason "Decline" used to — the default-focused action
    // should be the one that collects less, so a reflexive Enter never enables
    // something the operator did not read.
    const choice = await vscode.window.showInformationMessage(
      NOTICE_MESSAGE,
      { modal: true, detail: NOTICE_DETAIL },
      ACTION_TURN_OFF,
      ACTION_KEEP_ON
    );

    if (choice === ACTION_TURN_OFF) {
      await this.setEnabled(false);
      void vscode.window.showInformationMessage(
        "Telemetry turned off. Nothing further will be sent."
      );
      return;
    }

    // "Keep on" writes the value explicitly rather than leaving it to the
    // default. The operator has now actually chosen, and recording that keeps
    // them out of any future disclosure aimed at people who never decided.
    if (choice === ACTION_KEEP_ON) {
      await this.setEnabled(true);
    }
    // Esc-dismissed: the notice was read, the default stands, nothing is
    // written. There is no question left outstanding to reschedule.
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

  private async updateSetting(key: string, value: unknown): Promise<void> {
    const cfg = vscode.workspace.getConfiguration(CONFIG_NAMESPACE);
    await cfg.update(key, value, vscode.ConfigurationTarget.Global);
  }
}

function clamp(value: number, min: number, max: number): number {
  if (Number.isNaN(value)) return min;
  return Math.min(Math.max(value, min), max);
}
