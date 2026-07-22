/**
 * Notification Settings for Nightgauge
 *
 * Provides typed access to notification configuration via ConfigBridge.
 * Reads from the 6-tier merged config instead of direct VSCode settings.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import { ConfigBridge } from "../services/ConfigBridge";
import {
  type UINotificationsConfig,
  type UINotificationSoundsConfig,
  DEFAULT_CONFIG,
  type AlertSound as SchemaAlertSound,
  type SuccessSound as SchemaSuccessSound,
  type ErrorSound as SchemaErrorSound,
} from "./schema";

/**
 * Sound names available on macOS
 * Located at /System/Library/Sounds/
 *
 * Re-exported from schema for backward compatibility.
 */
export type AlertSound = SchemaAlertSound;
export type SuccessSound = SchemaSuccessSound;
export type ErrorSound = SchemaErrorSound;

/**
 * Notification type for the NotificationService
 */
export type NotificationType =
  "alert" | "success" | "error" | "issue-assigned" | "pr-review-requested";

/**
 * Notification configuration interface
 *
 * This interface maintains backward compatibility with existing code.
 * Values are sourced from ConfigBridge (UINotificationsConfig).
 */
export interface NotificationSettings {
  /** Master enable/disable for all notifications */
  enabled: boolean;

  /** Sound settings */
  sounds: {
    /** Enable/disable all sounds */
    enabled: boolean;
    /** Sound for user input needed events */
    alert: AlertSound;
    /** Sound for pipeline completion events */
    success: SuccessSound;
    /** Sound for pipeline error events */
    error: ErrorSound;
    /** Volume for sounds (0.0 - 1.0) */
    volume: number;
  };

  /** Banner notification settings */
  banner: {
    /** Enable/disable VS Code notification banners */
    enabled: boolean;
  };

  /** Dock bounce settings (macOS only) */
  dockBounce: {
    /** Enable/disable Dock icon bounce */
    enabled: boolean;
  };

  /** Respect system Do Not Disturb mode */
  respectDoNotDisturb: boolean;

  /** Opt-in list of GitHub event types that trigger toast notifications */
  events: string[];
}

/**
 * Default notification settings
 *
 * @deprecated Use DEFAULT_CONFIG.ui.notifications from schema.ts instead.
 * Kept for backward compatibility.
 */
export const DEFAULT_NOTIFICATION_SETTINGS: NotificationSettings = mapToLegacyShape(
  DEFAULT_CONFIG.ui?.notifications
);

/**
 * Map ConfigBridge UINotificationsConfig to legacy NotificationSettings shape
 *
 * Handles the snake_case → camelCase and nested object transformations.
 */
function mapToLegacyShape(config?: UINotificationsConfig): NotificationSettings {
  const defaults = DEFAULT_CONFIG.ui!.notifications!;
  const defaultSounds = defaults.sounds!;

  return {
    enabled: config?.enabled ?? defaults.enabled!,
    sounds: {
      enabled: config?.sounds?.enabled ?? defaultSounds.enabled!,
      alert: config?.sounds?.alert ?? defaultSounds.alert!,
      success: config?.sounds?.success ?? defaultSounds.success!,
      error: config?.sounds?.error ?? defaultSounds.error!,
      volume: config?.sounds?.volume ?? defaultSounds.volume!,
    },
    banner: {
      enabled: config?.banner_enabled ?? defaults.banner_enabled!,
    },
    dockBounce: {
      enabled: config?.dock_bounce_enabled ?? defaults.dock_bounce_enabled!,
    },
    respectDoNotDisturb: config?.respect_do_not_disturb ?? defaults.respect_do_not_disturb!,
    events: config?.events ?? defaults.events ?? [],
  };
}

/**
 * Get current notification settings from ConfigBridge
 *
 * Reads from the 6-tier merged configuration instead of directly
 * from VSCode settings. If ConfigBridge is not initialized,
 * returns defaults and logs a warning.
 */
export function getNotificationSettings(): NotificationSettings {
  const configBridge = ConfigBridge.getInstance();

  if (!configBridge.isInitialized()) {
    console.debug("[Nightgauge] ConfigBridge not initialized, using defaults for notifications");
    return mapToLegacyShape(DEFAULT_CONFIG.ui?.notifications);
  }

  const ui = configBridge.getUI();
  return mapToLegacyShape(ui?.notifications);
}

type SoundKey = "alert" | "success" | "error";

const NOTIFICATION_TYPE_TO_SOUND: Record<NotificationType, SoundKey> = {
  alert: "alert",
  success: "success",
  error: "error",
  "issue-assigned": "alert",
  "pr-review-requested": "alert",
};

/**
 * Get the sound file name for a notification type
 */
export function getSoundForType(
  settings: NotificationSettings,
  type: NotificationType
): string | null {
  if (!settings.enabled || !settings.sounds.enabled) {
    return null;
  }

  const soundKey = NOTIFICATION_TYPE_TO_SOUND[type];
  const soundName = settings.sounds[soundKey];
  if (soundName === "none") {
    return null;
  }

  return soundName;
}

/**
 * Re-export UINotificationSoundsConfig for consumers that need the raw type
 */
export type { UINotificationsConfig, UINotificationSoundsConfig };
