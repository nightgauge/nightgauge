/**
 * Message protocol for the Notifier Settings webview (#3379).
 */

import { NOTIFIER_PROVIDERS } from "./NotifierInstancesSection";
import type { NotifierInstanceRow, NotifierProvider } from "./NotifierInstancesSection";

export type NotifierWebViewToExtensionMessage =
  | { type: "getState" }
  | { type: "notifier-add"; notifierType: NotifierProvider }
  | { type: "notifier-action"; action: "test" | "remove"; id: string };

export type NotifierExtensionToWebViewMessage =
  | { type: "update"; notifiers: NotifierInstanceRow[] }
  | { type: "test-result"; id: string; ok: boolean; error?: string }
  | { type: "error"; message: string };

export interface NotifierMessageCallbacks {
  onGetState: () => Promise<void> | void;
  onNotifierAdd: (notifierType: NotifierProvider) => Promise<void> | void;
  onNotifierAction: (action: "test" | "remove", id: string) => Promise<void> | void;
}

export class NotifierSettingsMessageHandler {
  constructor(private readonly callbacks: NotifierMessageCallbacks) {}

  handleMessage = async (message: unknown): Promise<void> => {
    if (!isObject(message) || typeof (message as { type?: unknown }).type !== "string") {
      return;
    }
    const msg = message as NotifierWebViewToExtensionMessage;
    switch (msg.type) {
      case "getState":
        await this.callbacks.onGetState();
        return;
      case "notifier-add":
        if (NOTIFIER_PROVIDERS.includes(msg.notifierType)) {
          await this.callbacks.onNotifierAdd(msg.notifierType);
        }
        return;
      case "notifier-action":
        if (
          (msg.action === "test" || msg.action === "remove") &&
          typeof msg.id === "string" &&
          msg.id.length > 0
        ) {
          await this.callbacks.onNotifierAction(msg.action, msg.id);
        }
        return;
      default:
        return;
    }
  };
}

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}
