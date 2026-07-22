/**
 * SettingsMessageHandler - Message protocol handler for settings WebView
 *
 * Handles bidirectional message passing between the WebView and extension.
 * Validates incoming messages and routes them to appropriate handlers.
 *
 * @see docs/ARCHITECTURE.md for WebView patterns
 * @see Issue #440 - Multi-tier config GUI support
 */

import type {
  IncrediConfig,
  ViewTier,
  EditableTier,
  TierViewState,
  ConfigSourceMap,
  TierMetadata,
} from "./types";

/**
 * Messages sent from extension to WebView
 */
export type ExtensionToWebViewMessage =
  | { type: "update"; config: IncrediConfig }
  | {
      type: "update-tiered";
      config: IncrediConfig;
      sources: ConfigSourceMap;
      tierState: TierViewState;
      tiers: TierMetadata;
    }
  | { type: "tier-changed"; tier: ViewTier }
  | { type: "saved"; tier?: EditableTier }
  | { type: "error"; message: string }
  | { type: "patch-values"; values: Record<string, unknown> }
  | { type: "locked"; lockedSections: string[] };

/**
 * Messages sent from WebView to extension
 */
export type WebViewToExtensionMessage =
  | { type: "change"; path: string; value: unknown; targetTier?: EditableTier }
  | { type: "list-add"; path: string; value: string; targetTier?: EditableTier }
  | {
      type: "list-remove";
      path: string;
      index: number;
      targetTier?: EditableTier;
    }
  | { type: "save"; targetTier?: EditableTier }
  | { type: "reset" }
  | { type: "reset-setting"; path: string; toTier: ViewTier }
  | { type: "switch-tier"; tier: ViewTier }
  | { type: "open-tier-file"; tier: ViewTier }
  | { type: "open-doc"; path: string }
  | { type: "action"; action: string; payload?: Record<string, unknown> }
  | { type: "forge-action"; action: "edit" | "delete" | "test" | "set-default"; instanceId: string }
  | { type: "forge-add" }
  | { type: "dismissDriftBanner" }
  | { type: "showDriftedKeysOnly" }
  | { type: "moveTierKey"; key: string; targetTier: string };

/**
 * Callbacks for handling WebView messages
 */
export interface SettingsMessageCallbacks {
  onChange?: (path: string, value: unknown, targetTier?: EditableTier) => void;
  onListAdd?: (path: string, value: string, targetTier?: EditableTier) => void;
  onListRemove?: (path: string, index: number, targetTier?: EditableTier) => void;
  onSave?: (targetTier?: EditableTier) => void;
  onReset?: () => void;
  onResetSetting?: (path: string, toTier: ViewTier) => void;
  onSwitchTier?: (tier: ViewTier) => void;
  onOpenTierFile?: (tier: ViewTier) => void;
  onOpenDoc?: (path: string) => void;
  onAction?: (action: string, payload?: Record<string, unknown>) => void | Promise<void>;
  onForgeAdd?: () => void | Promise<void>;
  onForgeAction?: (
    action: "edit" | "delete" | "test" | "set-default",
    instanceId: string
  ) => void | Promise<void>;
  onDismissDriftBanner?: () => void;
  onShowDriftedKeysOnly?: () => void;
  onMoveTierKey?: (key: string, targetTier: string) => void | Promise<void>;
}

/**
 * SettingsMessageHandler class for handling WebView messages
 *
 * @example
 * ```typescript
 * const handler = new SettingsMessageHandler({
 *   onChange: (path, value) => {
 *     console.log('Setting changed:', path, value);
 *   },
 *   onSave: () => saveConfig(),
 * });
 *
 * panel.webview.onDidReceiveMessage(handler.handleMessage);
 * ```
 */
export class SettingsMessageHandler {
  private callbacks: SettingsMessageCallbacks;

  constructor(callbacks: SettingsMessageCallbacks = {}) {
    this.callbacks = callbacks;
  }

  /**
   * Handle incoming message from WebView
   */
  handleMessage = (message: unknown): void => {
    if (!this.isValidMessage(message)) {
      console.warn("Invalid message received from settings WebView:", message);
      return;
    }

    const msg = message as WebViewToExtensionMessage;

    switch (msg.type) {
      case "change":
        if (this.isValidChangeMessage(msg)) {
          this.callbacks.onChange?.(msg.path, msg.value, msg.targetTier);
        }
        break;

      case "list-add":
        if (this.isValidListAddMessage(msg)) {
          this.callbacks.onListAdd?.(msg.path, msg.value, msg.targetTier);
        }
        break;

      case "list-remove":
        if (this.isValidListRemoveMessage(msg)) {
          this.callbacks.onListRemove?.(msg.path, msg.index, msg.targetTier);
        }
        break;

      case "save":
        if (this.isValidSaveMessage(msg)) {
          this.callbacks.onSave?.(msg.targetTier);
        }
        break;

      case "reset":
        this.callbacks.onReset?.();
        break;

      case "reset-setting":
        if (this.isValidResetSettingMessage(msg)) {
          this.callbacks.onResetSetting?.(msg.path, msg.toTier);
        }
        break;

      case "switch-tier":
        if (this.isValidSwitchTierMessage(msg)) {
          this.callbacks.onSwitchTier?.(msg.tier);
        }
        break;

      case "open-tier-file":
        if (this.isValidOpenTierFileMessage(msg)) {
          this.callbacks.onOpenTierFile?.(msg.tier);
        }
        break;

      case "open-doc":
        if (this.isValidOpenDocMessage(msg)) {
          this.callbacks.onOpenDoc?.(msg.path);
        }
        break;

      case "action":
        if (this.isValidActionMessage(msg)) {
          const result = this.callbacks.onAction?.(msg.action, msg.payload);
          if (result && typeof (result as Promise<void>).catch === "function") {
            (result as Promise<void>).catch((error) => {
              console.error("Settings action callback failed:", error);
            });
          }
        }
        break;

      case "forge-add": {
        const r = this.callbacks.onForgeAdd?.();
        if (r && typeof (r as Promise<void>).catch === "function") {
          (r as Promise<void>).catch((err) => console.error("forge-add callback failed:", err));
        }
        break;
      }

      case "forge-action":
        if (this.isValidForgeActionMessage(msg)) {
          const r = this.callbacks.onForgeAction?.(msg.action, msg.instanceId);
          if (r && typeof (r as Promise<void>).catch === "function") {
            (r as Promise<void>).catch((err) =>
              console.error("forge-action callback failed:", err)
            );
          }
        }
        break;

      case "dismissDriftBanner":
        this.callbacks.onDismissDriftBanner?.();
        break;

      case "showDriftedKeysOnly":
        this.callbacks.onShowDriftedKeysOnly?.();
        break;

      case "moveTierKey": {
        const r = this.callbacks.onMoveTierKey?.(msg.key, msg.targetTier);
        if (r && typeof (r as Promise<void>).catch === "function") {
          (r as Promise<void>).catch((err) => console.error("moveTierKey callback failed:", err));
        }
        break;
      }

      default:
        console.warn("Unknown message type from settings WebView:", msg);
    }
  };

  /**
   * Type guard for valid message structure
   */
  private isValidMessage(message: unknown): message is { type: string } {
    return (
      typeof message === "object" &&
      message !== null &&
      "type" in message &&
      typeof (message as { type: unknown }).type === "string"
    );
  }

  /**
   * Type guard for change message
   */
  private isValidChangeMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "change"; path: string; value: unknown } {
    return (
      msg.type === "change" &&
      "path" in msg &&
      typeof msg.path === "string" &&
      msg.path.length > 0 &&
      "value" in msg
    );
  }

  /**
   * Type guard for list-add message
   */
  private isValidListAddMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "list-add"; path: string; value: string } {
    return (
      msg.type === "list-add" &&
      "path" in msg &&
      typeof msg.path === "string" &&
      "value" in msg &&
      typeof msg.value === "string"
    );
  }

  /**
   * Type guard for list-remove message
   */
  private isValidListRemoveMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "list-remove"; path: string; index: number } {
    return (
      msg.type === "list-remove" &&
      "path" in msg &&
      typeof msg.path === "string" &&
      "index" in msg &&
      typeof msg.index === "number"
    );
  }

  /**
   * Type guard for open-doc message
   */
  private isValidOpenDocMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "open-doc"; path: string } {
    return msg.type === "open-doc" && "path" in msg && typeof msg.path === "string";
  }

  private isValidActionMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "action"; action: string; payload?: Record<string, unknown> } {
    return (
      msg.type === "action" &&
      "action" in msg &&
      typeof msg.action === "string" &&
      (!("payload" in msg) ||
        msg.payload === undefined ||
        (typeof msg.payload === "object" && msg.payload !== null))
    );
  }

  /**
   * Type guard for save message with optional tier
   */
  private isValidSaveMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "save"; targetTier?: EditableTier } {
    if (msg.type !== "save") return false;
    if ("targetTier" in msg && msg.targetTier !== undefined) {
      return msg.targetTier === "project" || msg.targetTier === "local";
    }
    return true;
  }

  /**
   * Type guard for reset-setting message
   */
  private isValidResetSettingMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "reset-setting"; path: string; toTier: ViewTier } {
    return (
      msg.type === "reset-setting" &&
      "path" in msg &&
      typeof msg.path === "string" &&
      "toTier" in msg &&
      typeof msg.toTier === "string"
    );
  }

  /**
   * Type guard for switch-tier message
   */
  private isValidSwitchTierMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "switch-tier"; tier: ViewTier } {
    return msg.type === "switch-tier" && "tier" in msg && typeof msg.tier === "string";
  }

  /**
   * Type guard for open-tier-file message
   */
  private isValidOpenTierFileMessage(
    msg: WebViewToExtensionMessage
  ): msg is { type: "open-tier-file"; tier: ViewTier } {
    return msg.type === "open-tier-file" && "tier" in msg && typeof msg.tier === "string";
  }

  private isValidForgeActionMessage(msg: WebViewToExtensionMessage): msg is {
    type: "forge-action";
    action: "edit" | "delete" | "test" | "set-default";
    instanceId: string;
  } {
    if (msg.type !== "forge-action") return false;
    const validActions = ["edit", "delete", "test", "set-default"];
    return (
      "action" in msg &&
      typeof (msg as { action: unknown }).action === "string" &&
      validActions.includes((msg as { action: string }).action) &&
      "instanceId" in msg &&
      typeof (msg as { instanceId: unknown }).instanceId === "string"
    );
  }

  /**
   * Update callbacks
   */
  setCallbacks(callbacks: Partial<SettingsMessageCallbacks>): void {
    this.callbacks = { ...this.callbacks, ...callbacks };
  }
}

/**
 * Create an update message for the WebView
 */
export function createUpdateMessage(config: IncrediConfig): ExtensionToWebViewMessage {
  return { type: "update", config };
}

/**
 * Create a saved message for the WebView
 */
export function createSavedMessage(): ExtensionToWebViewMessage {
  return { type: "saved" };
}

/**
 * Create an error message for the WebView
 */
export function createErrorMessage(message: string): ExtensionToWebViewMessage {
  return { type: "error", message };
}

/**
 * Create a locked message for the WebView
 */
export function createLockedMessage(lockedSections: string[]): ExtensionToWebViewMessage {
  return { type: "locked", lockedSections };
}

/**
 * Create a tiered update message for the WebView
 *
 * @param config - Effective merged configuration
 * @param sources - Source map for each config path
 * @param tierState - Current tier view state
 * @param tiers - Metadata about which tiers are present
 */
export function createTieredUpdateMessage(
  config: IncrediConfig,
  sources: ConfigSourceMap,
  tierState: TierViewState,
  tiers: TierMetadata
): ExtensionToWebViewMessage {
  return { type: "update-tiered", config, sources, tierState, tiers };
}

/**
 * Create a tier changed message for the WebView
 */
export function createTierChangedMessage(tier: ViewTier): ExtensionToWebViewMessage {
  return { type: "tier-changed", tier };
}

/**
 * Create a saved message with optional tier info
 */
export function createTieredSavedMessage(tier?: EditableTier): ExtensionToWebViewMessage {
  return { type: "saved", tier };
}
