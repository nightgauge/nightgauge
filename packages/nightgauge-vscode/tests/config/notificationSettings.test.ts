/**
 * Unit tests for notificationSettings.ts
 *
 * Tests ConfigBridge integration, fallback behavior, and type mappings.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  getNotificationSettings,
  getSoundForType,
  DEFAULT_NOTIFICATION_SETTINGS,
  type NotificationSettings,
} from "../../src/config/notificationSettings";
import { ConfigBridge } from "../../src/services/ConfigBridge";
import { DEFAULT_CONFIG } from "../../src/config/schema";

// Mock ConfigBridge
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("notificationSettings", () => {
  let mockConfigBridge: {
    isInitialized: ReturnType<typeof vi.fn>;
    getUI: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    mockConfigBridge = {
      isInitialized: vi.fn(),
      getUI: vi.fn(),
    };
    vi.mocked(ConfigBridge.getInstance).mockReturnValue(
      mockConfigBridge as unknown as ConfigBridge
    );
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(console, "debug").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("DEFAULT_NOTIFICATION_SETTINGS", () => {
    it("has expected default values from schema", () => {
      expect(DEFAULT_NOTIFICATION_SETTINGS.enabled).toBe(true);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.enabled).toBe(true);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.alert).toBe("Glass");
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.success).toBe("Hero");
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.error).toBe("Basso");
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.volume).toBe(0.5);
      expect(DEFAULT_NOTIFICATION_SETTINGS.banner.enabled).toBe(true);
      expect(DEFAULT_NOTIFICATION_SETTINGS.dockBounce.enabled).toBe(true);
      expect(DEFAULT_NOTIFICATION_SETTINGS.respectDoNotDisturb).toBe(true);
    });

    it("matches DEFAULT_CONFIG.ui.notifications", () => {
      const schemaDefaults = DEFAULT_CONFIG.ui!.notifications!;
      expect(DEFAULT_NOTIFICATION_SETTINGS.enabled).toBe(schemaDefaults.enabled);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.enabled).toBe(schemaDefaults.sounds!.enabled);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.alert).toBe(schemaDefaults.sounds!.alert);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.success).toBe(schemaDefaults.sounds!.success);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.error).toBe(schemaDefaults.sounds!.error);
      expect(DEFAULT_NOTIFICATION_SETTINGS.sounds.volume).toBe(schemaDefaults.sounds!.volume);
      expect(DEFAULT_NOTIFICATION_SETTINGS.banner.enabled).toBe(schemaDefaults.banner_enabled);
      expect(DEFAULT_NOTIFICATION_SETTINGS.dockBounce.enabled).toBe(
        schemaDefaults.dock_bounce_enabled
      );
      expect(DEFAULT_NOTIFICATION_SETTINGS.respectDoNotDisturb).toBe(
        schemaDefaults.respect_do_not_disturb
      );
    });
  });

  describe("getNotificationSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getNotificationSettings();

      expect(settings).toEqual(DEFAULT_NOTIFICATION_SETTINGS);
      expect(console.debug).toHaveBeenCalledWith(
        "[Nightgauge] ConfigBridge not initialized, using defaults for notifications"
      );
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        notifications: {
          enabled: false,
          sounds: {
            enabled: false,
            alert: "Ping",
            success: "Pop",
            error: "Sosumi",
            volume: 0.8,
          },
          banner_enabled: false,
          dock_bounce_enabled: false,
          respect_do_not_disturb: false,
        },
      });

      const settings = getNotificationSettings();

      expect(settings.enabled).toBe(false);
      expect(settings.sounds.enabled).toBe(false);
      expect(settings.sounds.alert).toBe("Ping");
      expect(settings.sounds.success).toBe("Pop");
      expect(settings.sounds.error).toBe("Sosumi");
      expect(settings.sounds.volume).toBe(0.8);
      expect(settings.banner.enabled).toBe(false);
      expect(settings.dockBounce.enabled).toBe(false);
      expect(settings.respectDoNotDisturb).toBe(false);
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        notifications: {
          enabled: false,
          // sounds is missing
        },
      });

      const settings = getNotificationSettings();

      expect(settings.enabled).toBe(false);
      // Missing values should fall back to defaults
      expect(settings.sounds.enabled).toBe(DEFAULT_CONFIG.ui!.notifications!.sounds!.enabled);
      expect(settings.sounds.alert).toBe(DEFAULT_CONFIG.ui!.notifications!.sounds!.alert);
    });

    it("handles undefined UI config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getNotificationSettings();

      expect(settings).toEqual(DEFAULT_NOTIFICATION_SETTINGS);
    });

    it("handles undefined notifications config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({});

      const settings = getNotificationSettings();

      expect(settings).toEqual(DEFAULT_NOTIFICATION_SETTINGS);
    });
  });

  describe("getSoundForType", () => {
    it("returns null when notifications are disabled", () => {
      const settings: NotificationSettings = {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        enabled: false,
      };

      expect(getSoundForType(settings, "alert")).toBeNull();
      expect(getSoundForType(settings, "success")).toBeNull();
      expect(getSoundForType(settings, "error")).toBeNull();
    });

    it("returns null when sounds are disabled", () => {
      const settings: NotificationSettings = {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        sounds: {
          ...DEFAULT_NOTIFICATION_SETTINGS.sounds,
          enabled: false,
        },
      };

      expect(getSoundForType(settings, "alert")).toBeNull();
      expect(getSoundForType(settings, "success")).toBeNull();
      expect(getSoundForType(settings, "error")).toBeNull();
    });

    it('returns null for "none" sound', () => {
      const settings: NotificationSettings = {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        sounds: {
          ...DEFAULT_NOTIFICATION_SETTINGS.sounds,
          alert: "none",
          success: "none",
          error: "none",
        },
      };

      expect(getSoundForType(settings, "alert")).toBeNull();
      expect(getSoundForType(settings, "success")).toBeNull();
      expect(getSoundForType(settings, "error")).toBeNull();
    });

    it("returns correct sound names for each type", () => {
      const settings: NotificationSettings = {
        ...DEFAULT_NOTIFICATION_SETTINGS,
        sounds: {
          enabled: true,
          alert: "Ping",
          success: "Pop",
          error: "Sosumi",
          volume: 0.5,
        },
      };

      expect(getSoundForType(settings, "alert")).toBe("Ping");
      expect(getSoundForType(settings, "success")).toBe("Pop");
      expect(getSoundForType(settings, "error")).toBe("Sosumi");
    });
  });

  describe("type mappings", () => {
    it("maps snake_case schema fields to camelCase interface", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        notifications: {
          banner_enabled: false, // snake_case in schema
          dock_bounce_enabled: false, // snake_case in schema
          respect_do_not_disturb: false, // snake_case in schema
        },
      });

      const settings = getNotificationSettings();

      // Verify mapping to camelCase/nested structure
      expect(settings.banner.enabled).toBe(false);
      expect(settings.dockBounce.enabled).toBe(false);
      expect(settings.respectDoNotDisturb).toBe(false);
    });
  });
});
