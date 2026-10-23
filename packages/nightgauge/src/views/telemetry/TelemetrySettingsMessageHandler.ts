/**
 * Message protocol for the Telemetry Settings webview (#3327).
 */

import type { TelemetryStream } from "../../services/telemetry/types.js";

export interface TelemetryPanelState {
  enabled: boolean;
  streams: TelemetryStream[];
  uploadIntervalMinutes: number;
  lastUploadAtMs: number | null;
  lastUploadDisplay: string;
  privacyDocPath: string;
}

export type TelemetryWebViewToExtensionMessage =
  | { type: "getState" }
  | { type: "setEnabled"; value: boolean }
  | { type: "toggleStream"; stream: TelemetryStream; enabled: boolean }
  | { type: "setUploadInterval"; minutes: number }
  | { type: "openPrivacyDoc" };

export type TelemetryExtensionToWebViewMessage = {
  type: "state";
  state: TelemetryPanelState;
};

export interface TelemetryMessageCallbacks {
  onGetState: () => Promise<void> | void;
  onSetEnabled: (value: boolean) => Promise<void> | void;
  onToggleStream: (stream: TelemetryStream, enabled: boolean) => Promise<void> | void;
  onSetUploadInterval: (minutes: number) => Promise<void> | void;
  onOpenPrivacyDoc: () => Promise<void> | void;
}

export class TelemetrySettingsMessageHandler {
  constructor(private readonly callbacks: TelemetryMessageCallbacks) {}

  handleMessage = async (message: unknown): Promise<void> => {
    if (!isObject(message) || typeof (message as { type?: unknown }).type !== "string") {
      return;
    }
    const msg = message as TelemetryWebViewToExtensionMessage;
    switch (msg.type) {
      case "getState":
        await this.callbacks.onGetState();
        return;
      case "setEnabled":
        if (typeof msg.value === "boolean") {
          await this.callbacks.onSetEnabled(msg.value);
        }
        return;
      case "toggleStream":
        if (typeof msg.stream === "string" && typeof msg.enabled === "boolean") {
          await this.callbacks.onToggleStream(msg.stream, msg.enabled);
        }
        return;
      case "setUploadInterval":
        if (typeof msg.minutes === "number") {
          await this.callbacks.onSetUploadInterval(msg.minutes);
        }
        return;
      case "openPrivacyDoc":
        await this.callbacks.onOpenPrivacyDoc();
        return;
      default:
        return;
    }
  };
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}
