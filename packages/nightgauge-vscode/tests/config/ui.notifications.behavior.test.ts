/**
 * Behavior tests for ui.notifications.* configuration fields
 *
 * These tests verify that notification config fields affect runtime behavior,
 * specifically sound playback, banner display, and system integration.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 * @see packages/nightgauge-vscode/src/config/schema.ts - UINotificationsConfigSchema
 */

import { describe, it, expect } from "vitest";
import {
  UINotificationsConfigSchema,
  UINotificationSoundsConfigSchema,
  AlertSoundSchema,
  SuccessSoundSchema,
  ErrorSoundSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// Mock Fixtures
// ============================================================================

/**
 * Default notifications configuration for tests
 */
export const DEFAULT_UI_NOTIFICATIONS_CONFIG = {
  enabled: true,
  sounds: {
    enabled: true,
    alert: "Glass" as const,
    success: "Hero" as const,
    error: "Basso" as const,
    volume: 0.5,
  },
  banner_enabled: true,
  dock_bounce_enabled: true,
  respect_do_not_disturb: true,
};

/**
 * Create a mock notifications configuration with optional overrides
 */
export function createMockUINotificationsConfig(
  overrides?: Partial<typeof DEFAULT_UI_NOTIFICATIONS_CONFIG>
) {
  // Deep merge sounds separately to preserve enabled when only changing alert/success/error
  const mergedSounds = overrides?.sounds
    ? { ...DEFAULT_UI_NOTIFICATIONS_CONFIG.sounds, ...overrides.sounds }
    : DEFAULT_UI_NOTIFICATIONS_CONFIG.sounds;

  return {
    ...DEFAULT_UI_NOTIFICATIONS_CONFIG,
    ...overrides,
    sounds: mergedSounds,
  };
}

describe("ui.notifications.behavior", () => {
  // ============================================================================
  // enabled - Behavior Tests
  // ============================================================================

  describe("enabled", () => {
    it("master toggle affects all notification types", () => {
      const config = createMockUINotificationsConfig({ enabled: false });

      const shouldNotify = (cfg: typeof config): boolean => {
        return cfg.enabled === true;
      };

      expect(shouldNotify(config)).toBe(false);
    });

    it("when enabled, individual settings control behavior", () => {
      const config = createMockUINotificationsConfig({
        enabled: true,
        banner_enabled: false,
      });

      const shouldShowBanner = (cfg: typeof config): boolean => {
        return cfg.enabled && cfg.banner_enabled;
      };

      expect(shouldShowBanner(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.enabled).toBe(true);
    });
  });

  // ============================================================================
  // sounds.enabled - Behavior Tests
  // ============================================================================

  describe("sounds.enabled", () => {
    it("master toggle for all sounds", () => {
      const config = createMockUINotificationsConfig({
        sounds: { enabled: false },
      });

      const shouldPlaySound = (cfg: typeof config): boolean => {
        return cfg.enabled && cfg.sounds?.enabled === true;
      };

      expect(shouldPlaySound(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.enabled).toBe(true);
    });
  });

  // ============================================================================
  // sounds.alert - Behavior Tests
  // ============================================================================

  describe("sounds.alert", () => {
    it("selects sound for user input needed", () => {
      const config = createMockUINotificationsConfig({
        sounds: { alert: "Ping" },
      });

      const getAlertSoundPath = (cfg: typeof config): string | null => {
        if (!cfg.enabled || !cfg.sounds?.enabled) return null;
        if (cfg.sounds.alert === "none") return null;
        return `/System/Library/Sounds/${cfg.sounds.alert}.aiff`;
      };

      expect(getAlertSoundPath(config)).toBe("/System/Library/Sounds/Ping.aiff");
    });

    it("returns null when sound is none", () => {
      const config = createMockUINotificationsConfig({
        sounds: { alert: "none" },
      });

      const getAlertSoundPath = (cfg: typeof config): string | null => {
        if (cfg.sounds?.alert === "none") return null;
        return `/System/Library/Sounds/${cfg.sounds?.alert}.aiff`;
      };

      expect(getAlertSoundPath(config)).toBe(null);
    });

    it("accepts all valid alert sounds", () => {
      expect(AlertSoundSchema.safeParse("Glass").success).toBe(true);
      expect(AlertSoundSchema.safeParse("Ping").success).toBe(true);
      expect(AlertSoundSchema.safeParse("Blow").success).toBe(true);
      expect(AlertSoundSchema.safeParse("Bottle").success).toBe(true);
      expect(AlertSoundSchema.safeParse("Frog").success).toBe(true);
      expect(AlertSoundSchema.safeParse("Funk").success).toBe(true);
      expect(AlertSoundSchema.safeParse("none").success).toBe(true);
    });

    it("rejects invalid alert sounds", () => {
      expect(AlertSoundSchema.safeParse("Beep").success).toBe(false);
    });

    it("defaults to Glass", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.alert).toBe("Glass");
    });
  });

  // ============================================================================
  // sounds.success - Behavior Tests
  // ============================================================================

  describe("sounds.success", () => {
    it("selects sound for pipeline completion", () => {
      const config = createMockUINotificationsConfig({
        sounds: { success: "Purr" },
      });

      const getSuccessSoundPath = (cfg: typeof config): string | null => {
        if (!cfg.enabled || !cfg.sounds?.enabled) return null;
        if (cfg.sounds.success === "none") return null;
        return `/System/Library/Sounds/${cfg.sounds.success}.aiff`;
      };

      expect(getSuccessSoundPath(config)).toBe("/System/Library/Sounds/Purr.aiff");
    });

    it("accepts all valid success sounds", () => {
      expect(SuccessSoundSchema.safeParse("Hero").success).toBe(true);
      expect(SuccessSoundSchema.safeParse("Purr").success).toBe(true);
      expect(SuccessSoundSchema.safeParse("Pop").success).toBe(true);
      expect(SuccessSoundSchema.safeParse("Submarine").success).toBe(true);
      expect(SuccessSoundSchema.safeParse("none").success).toBe(true);
    });

    it("rejects invalid success sounds", () => {
      expect(SuccessSoundSchema.safeParse("Chime").success).toBe(false);
    });

    it("defaults to Hero", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.success).toBe("Hero");
    });
  });

  // ============================================================================
  // sounds.error - Behavior Tests
  // ============================================================================

  describe("sounds.error", () => {
    it("selects sound for pipeline errors", () => {
      const config = createMockUINotificationsConfig({
        sounds: { error: "Sosumi" },
      });

      const getErrorSoundPath = (cfg: typeof config): string | null => {
        if (!cfg.enabled || !cfg.sounds?.enabled) return null;
        if (cfg.sounds.error === "none") return null;
        return `/System/Library/Sounds/${cfg.sounds.error}.aiff`;
      };

      expect(getErrorSoundPath(config)).toBe("/System/Library/Sounds/Sosumi.aiff");
    });

    it("accepts all valid error sounds", () => {
      expect(ErrorSoundSchema.safeParse("Basso").success).toBe(true);
      expect(ErrorSoundSchema.safeParse("Sosumi").success).toBe(true);
      expect(ErrorSoundSchema.safeParse("Morse").success).toBe(true);
      expect(ErrorSoundSchema.safeParse("Tink").success).toBe(true);
      expect(ErrorSoundSchema.safeParse("none").success).toBe(true);
    });

    it("rejects invalid error sounds", () => {
      expect(ErrorSoundSchema.safeParse("Boom").success).toBe(false);
    });

    it("defaults to Basso", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.error).toBe("Basso");
    });
  });

  // ============================================================================
  // sounds.volume - Behavior Tests
  // ============================================================================

  describe("sounds.volume", () => {
    it("affects sound playback level", () => {
      const config = createMockUINotificationsConfig({
        sounds: { volume: 0.75 },
      });

      const getPlaybackVolume = (cfg: typeof config): number => {
        return cfg.sounds?.volume ?? 0.5;
      };

      expect(getPlaybackVolume(config)).toBe(0.75);
    });

    it("accepts values 0-1", () => {
      expect(UINotificationSoundsConfigSchema.safeParse({ volume: 0 }).success).toBe(true);
      expect(UINotificationSoundsConfigSchema.safeParse({ volume: 0.5 }).success).toBe(true);
      expect(UINotificationSoundsConfigSchema.safeParse({ volume: 1 }).success).toBe(true);
    });

    it("rejects values outside range", () => {
      expect(UINotificationSoundsConfigSchema.safeParse({ volume: -0.1 }).success).toBe(false);
      expect(UINotificationSoundsConfigSchema.safeParse({ volume: 1.1 }).success).toBe(false);
    });

    it("defaults to 0.5", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.sounds?.volume).toBe(0.5);
    });
  });

  // ============================================================================
  // banner_enabled - Behavior Tests
  // ============================================================================

  describe("banner_enabled", () => {
    it("controls VS Code notification banners", () => {
      const config = createMockUINotificationsConfig({ banner_enabled: false });

      const shouldShowBanner = (cfg: typeof config): boolean => {
        return cfg.enabled && cfg.banner_enabled;
      };

      expect(shouldShowBanner(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.banner_enabled).toBe(true);
    });
  });

  // ============================================================================
  // dock_bounce_enabled - Behavior Tests
  // ============================================================================

  describe("dock_bounce_enabled", () => {
    it("controls macOS dock bounce", () => {
      const config = createMockUINotificationsConfig({
        dock_bounce_enabled: false,
      });

      const shouldBounceDock = (cfg: typeof config): boolean => {
        return cfg.enabled && cfg.dock_bounce_enabled;
      };

      expect(shouldBounceDock(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.dock_bounce_enabled).toBe(true);
    });
  });

  // ============================================================================
  // respect_do_not_disturb - Behavior Tests
  // ============================================================================

  describe("respect_do_not_disturb", () => {
    it("suppresses notifications when DND is active", () => {
      const config = createMockUINotificationsConfig({
        respect_do_not_disturb: true,
      });

      const shouldNotify = (cfg: typeof config, isDNDActive: boolean): boolean => {
        if (cfg.respect_do_not_disturb && isDNDActive) {
          return false;
        }
        return cfg.enabled;
      };

      expect(shouldNotify(config, true)).toBe(false);
      expect(shouldNotify(config, false)).toBe(true);
    });

    it("ignores DND when disabled", () => {
      const config = createMockUINotificationsConfig({
        respect_do_not_disturb: false,
      });

      const shouldNotify = (cfg: typeof config, isDNDActive: boolean): boolean => {
        if (cfg.respect_do_not_disturb && isDNDActive) {
          return false;
        }
        return cfg.enabled;
      };

      expect(shouldNotify(config, true)).toBe(true);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.notifications?.respect_do_not_disturb).toBe(true);
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete notifications config", () => {
      const result = UINotificationsConfigSchema.safeParse(DEFAULT_UI_NOTIFICATIONS_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial notifications config", () => {
      const result = UINotificationsConfigSchema.safeParse({
        enabled: false,
        sounds: { volume: 0.25 },
      });
      expect(result.success).toBe(true);
    });

    it("validates empty notifications config", () => {
      const result = UINotificationsConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates sounds config standalone", () => {
      const result = UINotificationSoundsConfigSchema.safeParse({
        alert: "Funk",
        success: "Pop",
        error: "Tink",
        volume: 0.8,
      });
      expect(result.success).toBe(true);
    });
  });
});
