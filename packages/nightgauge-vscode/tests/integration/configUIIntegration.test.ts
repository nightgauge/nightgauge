/**
 * UI config section integration tests
 *
 * Tests that UI components correctly consume config values from the 6-tier system.
 * Verifies that configuration changes propagate to UI components correctly.
 *
 * @see Issue #477 - Add integration tests for config.yaml → merge engine → service behavior
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// Mock vscode
vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter {
    private listeners: Array<(data: unknown) => void> = [];
    get event() {
      return (listener: (data: unknown) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }
    fire(data: unknown) {
      this.listeners.forEach((l) => l(data));
    }
    dispose = vi.fn();
  },
  RelativePattern: class RelativePattern {
    constructor(
      public base: string,
      public pattern: string
    ) {}
  },
  Uri: {
    file: (p: string) => ({ fsPath: p, path: p }),
  },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
    fs: {
      readFile: vi.fn(),
      writeFile: vi.fn(),
      createDirectory: vi.fn(),
    },
  },
  FileSystemError: class FileSystemError extends Error {
    code: string;
    constructor(message: string) {
      super(message);
      this.code = "FileNotFound";
    }
  },
}));

import {
  mergeConfigs,
  type ConfigTiers,
  getFormattedEntries,
  formatConfigDisplay,
} from "../../src/config/configMergeEngine";
import { DEFAULT_CONFIG, type IncrediConfig } from "../../src/config/schema";

describe("UI Config Section Integration (Issue #477)", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    vi.clearAllMocks();
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // Pipeline UI Integration
  // ============================================================================

  describe("pipeline UI integration", () => {
    it("should provide pipeline settings for stage display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        pipeline: {
          ci_timeout: 600,
          auto_fix: true,
          skip: {
            tests: false,
            lint: false,
            typecheck: true,
          },
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      // Pipeline UI would show these settings
      expect(result.config.pipeline?.ci_timeout).toBe(600);
      expect(result.config.pipeline?.auto_fix).toBe(true);
      expect(result.config.pipeline?.skip?.typecheck).toBe(true);
    });

    it("should provide retry settings for error display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        pipeline: {
          retry: {
            max_auto_attempts: 5,
            backoff_multiplier: 2,
            initial_delay_ms: 200,
          },
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.pipeline?.retry?.max_auto_attempts).toBe(5);
      expect(result.config.pipeline?.retry?.backoff_multiplier).toBe(2);
    });

    it("should provide log settings for log display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        pipeline: {
          logs: {
            retain: true,
            dir: ".nightgauge/logs",
            max_age_days: 30,
          },
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.pipeline?.logs?.retain).toBe(true);
      expect(result.config.pipeline?.logs?.dir).toBe(".nightgauge/logs");
    });
  });

  // ============================================================================
  // PR UI Integration
  // ============================================================================

  describe("PR UI integration", () => {
    it("should provide PR settings for PR creation display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        pr: {
          merge_strategy: "squash",
          delete_branch: true,
          draft_by_default: false,
          reviewers: ["alice", "bob"],
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.pr?.merge_strategy).toBe("squash");
      expect(result.config.pr?.delete_branch).toBe(true);
      expect(result.config.pr?.reviewers).toEqual(["alice", "bob"]);
    });

    it("should provide admin merge setting for bypass display", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: false } },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // UI would show admin bypass is enabled
      expect(result.config.pr?.delete_branch).toBe(true);
    });
  });

  // ============================================================================
  // Project Board UI Integration
  // ============================================================================

  describe("project board UI integration", () => {
    it("should provide project settings for board display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        project: {
          number: 42,
          owner: "nightgauge",
          auto_dates: true,
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.project?.number).toBe(42);
      expect(result.config.project?.owner).toBe("nightgauge");
      expect(result.config.project?.auto_dates).toBe(true);
    });

    it("should provide sync settings for sync status display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        project: {
          number: 10,
          sync: {
            enabled: true,
            direction: "bidirectional",
            conflict_resolution: "labels",
          },
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.project?.sync?.enabled).toBe(true);
      expect(result.config.project?.sync?.direction).toBe("bidirectional");
    });

    it("should provide sprint settings for sprint display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        project: {
          number: 10,
          sprint: {
            enabled: true,
            auto_assign: true,
            field_name: "Sprint",
          },
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.project?.sprint?.enabled).toBe(true);
      expect(result.config.project?.sprint?.auto_assign).toBe(true);
    });
  });

  // ============================================================================
  // Settings Panel UI Integration
  // ============================================================================

  describe("settings panel UI integration", () => {
    it("should provide formatted entries for settings display", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const entries = getFormattedEntries(result);

      // Entries should have path, value, source, and sourceLabel
      expect(entries.length).toBeGreaterThan(0);

      const prMergeStrategy = entries.find((e) => e.path === "pr.merge_strategy");
      expect(prMergeStrategy).toBeDefined();
      expect(prMergeStrategy?.value).toBe("rebase");
      expect(prMergeStrategy?.source).toBe("global");

      const projectNumber = entries.find((e) => e.path === "project.number");
      expect(projectNumber).toBeDefined();
      expect(projectNumber?.value).toBe(10);
      expect(projectNumber?.source).toBe("project");

      const deleteBranch = entries.find((e) => e.path === "pr.delete_branch");
      expect(deleteBranch).toBeDefined();
      expect(deleteBranch?.value).toBe(true);
      expect(deleteBranch?.source).toBe("local");
    });

    it("should provide formatted display for config overview", () => {
      const tiers: ConfigTiers = {
        project: { project: { number: 10 } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const display = formatConfigDisplay(result);

      expect(display).toContain("Effective Configuration");
      expect(display).toContain("project.number");
      expect(display).toContain("10");
      expect(display).toContain("Merge time:");
    });

    it("should provide JSON format for export", () => {
      const tiers: ConfigTiers = {
        project: { project: { number: 10 } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });
      const json = formatConfigDisplay(result, { json: true });

      const parsed = JSON.parse(json);
      expect(parsed.config.project.number).toBe(10);
      expect(parsed.sources).toBeDefined();
      expect(parsed.tiers).toBeDefined();
    });
  });

  // ============================================================================
  // Human-in-the-Loop UI Integration
  // ============================================================================

  describe("human-in-the-loop UI integration", () => {
    it("should provide auto-accept settings for confirmation dialogs", () => {
      const projectConfig: Partial<IncrediConfig> = {
        human_in_the_loop: {
          auto_accept_stages: false,
          auto_accept_permissions: false,
          trusted_stages: ["feature-planning", "feature-validate"],
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.human_in_the_loop?.auto_accept_stages).toBe(false);
      expect(result.config.human_in_the_loop?.auto_accept_permissions).toBe(false);
      expect(result.config.human_in_the_loop?.trusted_stages).toContain("feature-planning");
    });

    it("should allow env override for CI automation", () => {
      process.env.NIGHTGAUGE_HUMAN_IN_THE_LOOP_AUTO_ACCEPT_STAGES = "true";

      const projectConfig: Partial<IncrediConfig> = {
        human_in_the_loop: {
          auto_accept_stages: false,
        },
      };

      const result = mergeConfigs({ project: projectConfig });

      // Env should enable auto-accept for CI
      expect(result.config.human_in_the_loop?.auto_accept_stages).toBe(true);
    });
  });

  // ============================================================================
  // Routing UI Integration
  // ============================================================================

  describe("routing UI integration", () => {
    it("should provide routing settings for stage skip display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        routing: {
          trivial_max_complexity: 2,
          extensive_min_complexity: 5,
          force_full_pipeline: false,
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.routing?.trivial_max_complexity).toBe(2);
      expect(result.config.routing?.extensive_min_complexity).toBe(5);
      expect(result.config.routing?.force_full_pipeline).toBe(false);
    });
  });

  // ============================================================================
  // Validation UI Integration
  // ============================================================================

  describe("validation UI integration", () => {
    it("should provide validation settings for PR checks display", () => {
      const projectConfig: Partial<IncrediConfig> = {
        validation: {
          require_tests: true,
          require_changelog: true,
          max_files_changed: 20,
          max_lines_changed: 500,
        },
      };

      const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

      expect(result.config.validation?.require_tests).toBe(true);
      expect(result.config.validation?.require_changelog).toBe(true);
      expect(result.config.validation?.max_files_changed).toBe(20);
    });
  });

  // ============================================================================
  // Tier Metadata UI Integration
  // ============================================================================

  describe("tier metadata UI integration", () => {
    it("should provide tier info for config source indicator", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // UI would display which tiers are active
      expect(result.tiers.hasGlobal).toBe(true);
      expect(result.tiers.hasProject).toBe(true);
      expect(result.tiers.hasLocal).toBe(true);
      expect(result.tiers.hasEnv).toBe(false);
      expect(result.tiers.hasCli).toBe(false);
    });

    it("should provide env vars applied list for debug display", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      const result = mergeConfigs({});

      expect(result.envVarsApplied).toContain("NIGHTGAUGE_PR_DELETE_BRANCH");
    });

    it("should provide CLI overrides list for debug display", () => {
      const tiers: ConfigTiers = {
        cli: {
          pr: { delete_branch: true },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.cliOverrides).toContain("pr.delete_branch");
    });
  });
});
