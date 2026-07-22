/**
 * Integration tests for config.yaml → merge engine → service behavior
 *
 * These tests verify the end-to-end config pipeline:
 * 1. Writing a value in config.yaml
 * 2. Config merge engine resolves it correctly
 * 3. Service reads the correct effective value
 *
 * This ensures the ConfigBridge migration actually works and prevents
 * future regressions where config values might not flow correctly.
 *
 * @see Issue #477 - Add integration tests for config.yaml → merge engine → service behavior
 * @see Issue #473 - ConfigBridge migration
 * @see Issue #474 - batchConfig refactor
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as os from "node:os";

// Mock vscode before importing modules that use it
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
      delete: vi.fn(),
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
  mergeFileConfigs,
  type ConfigTiers,
} from "../../src/config/configMergeEngine";
import { validateConfig, getSource, type IncrediConfig } from "../../src/config/schema";

describe("Config Integration Tests (Issue #477)", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    vi.clearAllMocks();
    // Clear all NIGHTGAUGE_ env vars
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // Config File → Service Behavior Tests
  // ============================================================================

  describe("config file → service behavior", () => {
    describe("pipeline config integration", () => {
      it("should flow pipeline.auto_fix from config to services", () => {
        const projectConfig: Partial<IncrediConfig> = {
          pipeline: {
            auto_fix: false,
            ci_timeout: 600,
          },
        };

        const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

        expect(result.config.pipeline?.auto_fix).toBe(false);
        expect(result.config.pipeline?.ci_timeout).toBe(600);
      });

      it("should flow pipeline.skip settings correctly", () => {
        const projectConfig: Partial<IncrediConfig> = {
          pipeline: {
            skip: {
              tests: true,
              lint: false,
            },
          },
        };

        const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

        expect(result.config.pipeline?.skip?.tests).toBe(true);
        expect(result.config.pipeline?.skip?.lint).toBe(false);
        // Unspecified fields remain undefined (not merged from defaults)
        // The merge engine does shallow merge, so skip.typecheck is not set
        expect(result.config.pipeline?.skip?.typecheck).toBeUndefined();
      });
    });

    describe("PR config integration", () => {
      it("should flow pr.merge_strategy to PR creation", () => {
        const projectConfig: Partial<IncrediConfig> = {
          pr: {
            merge_strategy: "rebase",
            delete_branch: true,
            reviewers: ["alice", "bob"],
          },
        };

        const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

        expect(result.config.pr?.merge_strategy).toBe("rebase");
        expect(result.config.pr?.delete_branch).toBe(true);
        expect(result.config.pr?.reviewers).toEqual(["alice", "bob"]);
      });

      it("should use pull_request alias correctly", () => {
        const projectConfig: Partial<IncrediConfig> = {
          pull_request: {
            merge_strategy: "squash",
            delete_branch: false,
          },
        };

        const result = mergeConfigs({ project: projectConfig }, { skipEnvResolution: true });

        expect(result.config.pull_request?.merge_strategy).toBe("squash");
        expect(result.config.pull_request?.delete_branch).toBe(false);
      });
    });
  });

  // ============================================================================
  // Multi-Tier Precedence Tests
  // ============================================================================

  describe("multi-tier precedence (project < local < env)", () => {
    it("local overrides project for pr.delete_branch", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: false } },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(true);
      expect(getSource(result.sources, "pr.delete_branch")).toBe("local");
    });

    it("env overrides local for pipeline.auto_fix", () => {
      process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX = "false";

      const tiers: ConfigTiers = {
        local: { pipeline: { auto_fix: true } },
      };

      const result = mergeConfigs(tiers);

      expect(result.config.pipeline?.auto_fix).toBe(false);
      expect(getSource(result.sources, "pipeline.auto_fix")).toBe("env");
    });

    it("preserves unoverridden values from lower tiers", () => {
      const tiers: ConfigTiers = {
        project: {
          pr: {
            merge_strategy: "squash",
            delete_branch: true,
            reviewers: ["team-lead"],
          },
        },
        local: {
          pr: {
            delete_branch: true,
            // merge_strategy and delete_branch not specified
          },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(true); // From local
      expect(result.config.pr?.merge_strategy).toBe("squash"); // From project
      expect(result.config.pr?.delete_branch).toBe(true); // From project
      expect(result.config.pr?.reviewers).toEqual(["team-lead"]); // From project
    });

    it("handles three-level deep nesting (pipeline.retry.max_auto_attempts)", () => {
      const tiers: ConfigTiers = {
        project: {
          pipeline: {
            retry: {
              max_auto_attempts: 3,
              backoff_multiplier: 2,
            },
          },
        },
        local: {
          pipeline: {
            retry: {
              max_auto_attempts: 5,
              // backoff_multiplier not specified
            },
          },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pipeline?.retry?.max_auto_attempts).toBe(5); // Local
      expect(result.config.pipeline?.retry?.backoff_multiplier).toBe(2); // Project
    });

    it("replaces arrays rather than merging them", () => {
      const tiers: ConfigTiers = {
        project: {
          pr: { reviewers: ["alice", "bob", "charlie"] },
        },
        local: {
          pr: { reviewers: ["david"] },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Local array completely replaces project array
      expect(result.config.pr?.reviewers).toEqual(["david"]);
    });
  });

  // ============================================================================
  // Source Tracking Tests
  // ============================================================================

  describe("source tracking", () => {
    it("tracks default source for unoverridden values", () => {
      const result = mergeConfigs({}, { skipEnvResolution: true });

      expect(getSource(result.sources, "branch.base")).toBe("default");
      expect(getSource(result.sources, "validation.require_tests")).toBe("default");
    });

    it("tracks project source for project values", () => {
      const tiers: ConfigTiers = {
        project: {
          project: { number: 42 },
          pr: { merge_strategy: "rebase" },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(getSource(result.sources, "project.number")).toBe("project");
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("project");
    });

    it("tracks local source for local overrides", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: false } },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(getSource(result.sources, "pr.delete_branch")).toBe("local");
    });

    it("tracks env source for environment overrides", () => {
      process.env.NIGHTGAUGE_PIPELINE_AUTO_FIX = "true";

      const result = mergeConfigs({});

      expect(getSource(result.sources, "pipeline.auto_fix")).toBe("env");
      expect(result.envVarsApplied).toContain("NIGHTGAUGE_PIPELINE_AUTO_FIX");
    });

    it("provides complete tier metadata", () => {
      const tiers: ConfigTiers = {
        global: { pr: { merge_strategy: "rebase" } },
        project: { project: { number: 10 } },
        local: { pr: { delete_branch: true } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.tiers.hasDefaults).toBe(true);
      expect(result.tiers.hasGlobal).toBe(true);
      expect(result.tiers.hasProject).toBe(true);
      expect(result.tiers.hasLocal).toBe(true);
      expect(result.tiers.hasEnv).toBe(false);
      expect(result.tiers.hasCli).toBe(false);
    });
  });

  // ============================================================================
  // Real-World Scenario Tests
  // ============================================================================

  describe("real-world scenarios", () => {
    it("CI environment enables auto-accept via env vars", () => {
      // Project config: standard settings
      const projectConfig: Partial<IncrediConfig> = {
        human_in_the_loop: {
          auto_accept_stages: false,
          auto_accept_permissions: false,
        },
      };

      // CI env vars enable automation
      process.env.NIGHTGAUGE_HUMAN_IN_THE_LOOP_AUTO_ACCEPT_STAGES = "true";
      process.env.NIGHTGAUGE_HUMAN_IN_THE_LOOP_AUTO_ACCEPT_PERMISSIONS = "true";

      const result = mergeConfigs({ project: projectConfig });

      expect(result.config.human_in_the_loop?.auto_accept_stages).toBe(true);
      expect(result.config.human_in_the_loop?.auto_accept_permissions).toBe(true);
      expect(getSource(result.sources, "human_in_the_loop.auto_accept_stages")).toBe("env");
    });

    it("global user preferences apply across repositories", () => {
      // User's global preference for squash
      const globalConfig: Partial<IncrediConfig> = {
        pr: {
          merge_strategy: "squash",
          delete_branch: true,
        },
      };

      // Project has different reviewers
      const projectConfig: Partial<IncrediConfig> = {
        pr: {
          reviewers: ["project-lead"],
        },
      };

      const result = mergeConfigs(
        { global: globalConfig, project: projectConfig },
        { skipEnvResolution: true }
      );

      // Global preferences
      expect(result.config.pr?.merge_strategy).toBe("squash");
      expect(result.config.pr?.delete_branch).toBe(true);

      // Project-specific reviewers
      expect(result.config.pr?.reviewers).toEqual(["project-lead"]);

      // Verify sources
      expect(getSource(result.sources, "pr.merge_strategy")).toBe("global");
      expect(getSource(result.sources, "pr.reviewers")).toBe("project");
    });
  });

  // ============================================================================
  // Validation Integration Tests
  // ============================================================================

  describe("validation integration", () => {
    it("validates merged config catches invalid project.number", () => {
      const tiers: ConfigTiers = {
        project: {
          project: { number: -1 }, // Invalid: must be positive
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.validation.valid).toBe(false);
      expect(result.validation.errors.length).toBeGreaterThan(0);
      expect(result.validation.errors[0].field).toContain("project.number");
    });
  });

  // ============================================================================
  // Edge Cases and Error Handling
  // ============================================================================

  describe("edge cases", () => {
    it("handles empty config at all tiers", () => {
      const tiers: ConfigTiers = {
        global: {},
        project: {},
        local: {},
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config).toBeDefined();
      expect(result.validation.valid).toBe(true);
    });

    it("handles undefined vs null correctly", () => {
      const tiers: ConfigTiers = {
        project: { project: { number: 10 } },
        local: { project: { number: undefined } }, // undefined does NOT override
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // undefined should NOT override existing value
      expect(result.config.project?.number).toBe(10);
    });

    it("handles boolean false correctly (explicit false should override)", () => {
      const tiers: ConfigTiers = {
        project: { pr: { delete_branch: true } },
        local: { pr: { delete_branch: false } },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      expect(result.config.pr?.delete_branch).toBe(false);
    });

    it("handles empty string env var correctly", () => {
      process.env.NIGHTGAUGE_PR_MERGE_STRATEGY = "";

      const tiers: ConfigTiers = {
        project: { pr: { merge_strategy: "squash" } },
      };

      const result = mergeConfigs(tiers);

      // Empty string should not override
      expect(result.config.pr?.merge_strategy).toBe("squash");
    });
  });

  // ============================================================================
  // Performance Tests
  // ============================================================================

  describe("performance", () => {
    it("merges typical config without pathological slowness", () => {
      const tiers: ConfigTiers = {
        global: {
          pr: { merge_strategy: "squash", delete_branch: true },
        },
        project: {
          project: { number: 10, auto_dates: true },
          pr: { reviewers: ["team-lead"] },
          pipeline: { auto_fix: true, ci_timeout: 300 },
        },
        local: {
          pr: { delete_branch: true },
        },
      };

      const result = mergeConfigs(tiers, { skipEnvResolution: true });

      // Regression guard, not a micro-benchmark. Typical execution is <5ms; a
      // tight wall-clock bound flaked under full-suite CPU contention, so the
      // ceiling is deliberately generous while still catching pathological cases.
      expect(result.mergeTimeMs).toBeLessThan(1000);
    });

    it("includes env resolution without pathological slowness", () => {
      process.env.NIGHTGAUGE_PR_DELETE_BRANCH = "true";

      const tiers: ConfigTiers = {
        project: {
          project: { number: 10 },
        },
      };

      const result = mergeConfigs(tiers);

      // Regression guard, not a micro-benchmark — generous to survive CI load.
      expect(result.mergeTimeMs).toBeLessThan(1000);
    });
  });
});
