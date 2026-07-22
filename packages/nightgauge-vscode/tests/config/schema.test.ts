/**
 * Tests for Nightgauge configuration Zod schema
 *
 * @see Issue #432 - Comprehensive Zod Schema for Config Fields
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import {
  resolvePlatformBaseUrl,
  PLATFORM_ENV_PRESETS,
  IncrediConfigSchema,
  validateConfig,
  parseConfig,
  mergeWithDefaults,
  getDefaultConfig,
  // Enum schemas
  MergeStrategySchema,
  SyncDirectionSchema,
  ConflictResolutionSchema,
  EnforcementModeSchema,
  CustomFieldTypeSchema,
  TrustedStageSchema,
  // Section schemas
  ProjectConfigSchema,
  PullRequestConfigSchema,
  BranchConfigSchema,
  IssueConfigSchema,
  PipelineConfigSchema,
  RoutingConfigSchema,
  ChangeRuleSchema,
  ValidationConfigSchema,
  SanitizationConfigSchema,
  SanitizationModeSchema,
  HumanInTheLoopConfigSchema,
  RalphLoopConfigSchema,
  AutomationsConfigSchema,
  AutonomousConfigSchema,
  SafetyRailsConfigSchema,
  GitHubAuthConfigSchema,
  BaselineCIGateConfigSchema,
} from "../../src/config/schema";

describe("IncrediConfigSchema", () => {
  // ============================================================================
  // Valid Config Tests
  // ============================================================================

  describe("valid configurations", () => {
    it("accepts empty config and returns defaults", () => {
      const result = validateConfig({});
      expect(result.valid).toBe(true);
      expect(result.config).toBeDefined();
    });

    it("accepts partial config and merges with defaults", () => {
      const config = {
        project: { number: 10 },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(true);
      expect(result.config?.project?.number).toBe(10);
    });

    it("accepts structured project field mappings for board metadata", () => {
      const config = {
        project: {
          fields: {
            status: {
              id: "PVTSSF_status",
              options: {
                backlog: "option_backlog",
                ready: "option_ready",
              },
            },
            priority: {
              id: "PVTSSF_priority",
              options: {
                p0: "option_p0",
              },
            },
            size: {
              id: "PVTSSF_size",
              options: {
                m: "option_m",
              },
            },
          },
        },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(true);
      expect(result.config?.project?.fields?.status).toEqual(config.project.fields.status);
      expect(result.config?.project?.fields?.priority).toEqual(config.project.fields.priority);
      expect(result.config?.project?.fields?.size).toEqual(config.project.fields.size);
    });

    it("accepts full config with all sections", () => {
      const config = {
        project: {
          number: 10,
          owner: "nightgauge",
          auto_dates: true,
          sprint: {
            enabled: true,
            auto_assign: true,
            field_name: "Sprint",
          },
          sync: {
            enabled: true,
            direction: "bidirectional",
            conflict_resolution: "warn",
            debounce_ms: 1000,
          },
        },
        pull_request: {
          merge_strategy: "squash",
          delete_branch: true,
          draft_by_default: false,
          reviewers: ["alice", "bob"],
          auto_merge: false,
        },
        branch: {
          base: "main",
          protected: ["main", "develop"],
          suggestions: true,
          prefixes: {
            feature: "feat/",
            bugfix: "fix/",
          },
        },
        issue: {
          auto_assign: true,
          default_labels: ["status:ready"],
        },
        pipeline: {
          ci_timeout: 600,
          auto_fix: true,
          skip: {
            tests: false,
            lint: false,
          },
          logs: {
            retain: true,
            dir: ".nightgauge/logs",
          },
          retry: {
            max_auto_attempts: 3,
            backoff_multiplier: 2,
          },
        },
        routing: {
          trivial_max_complexity: 2,
          extensive_min_complexity: 5,
          force_full_pipeline: false,
        },
        commands: {
          test: "pnpm test",
          lint: "pnpm lint",
        },
        validation: {
          require_tests: true,
          require_changelog: false,
          max_files_changed: 50,
          max_lines_changed: 2000,
        },
        sanitization: {
          enabled: true,
          sanitize_input: false,
          logging: true,
          warn_only: false,
          allowlist: [],
          blocklist: [],
          safe_directories: ["./dist", "./build"],
        },
        human_in_the_loop: {
          auto_accept_stages: false,
          auto_accept_permissions: false,
          trusted_stages: ["feature-planning", "feature-validate"],
        },
        ralph_loop: {
          enabled: true,
          build: true,
          tests: true,
          lint: false,
          limits: {
            max_iterations: 3,
            total_token_budget: 10000,
          },
        },
        automations: {
          enabled: true,
          dry_run: false,
          triggers: [
            {
              trigger: "status:in-review",
              actions: [{ type: "add_label", label: "needs-review" }],
            },
          ],
        },
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("accepts multi-project configuration", () => {
      const config = {
        projects: [
          {
            name: "Engineering Board",
            number: 10,
            sync_filter: "type:feature OR type:bug",
            default: true,
          },
          {
            name: "QA Board",
            number: 15,
            sync_filter: "type:bug",
          },
        ],
      };

      const result = validateConfig(config);
      expect(result.valid).toBe(true);
      expect(result.config?.projects).toHaveLength(2);
    });
  });

  // ============================================================================
  // Invalid Type Tests
  // ============================================================================

  describe("invalid type validation", () => {
    it("rejects string where number expected (project.number)", () => {
      const config = {
        project: { number: "abc" },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({
          field: expect.stringContaining("project.number"),
        })
      );
    });

    it("rejects number where string expected (branch.base)", () => {
      const config = {
        branch: { base: 123 },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({
          field: expect.stringContaining("branch.base"),
        })
      );
    });

    it("rejects invalid enum value (merge_strategy)", () => {
      const config = {
        pull_request: { merge_strategy: "invalid" },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors).toContainEqual(
        expect.objectContaining({
          field: expect.stringContaining("merge_strategy"),
        })
      );
    });

    it("rejects array where object expected", () => {
      const config = {
        project: ["invalid"],
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });

    it("rejects object where array expected (reviewers)", () => {
      const config = {
        pull_request: { reviewers: { name: "alice" } },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });
  });

  // ============================================================================
  // Boundary Value Tests
  // ============================================================================

  describe("boundary value validation", () => {
    it("rejects zero for project.number (must be positive)", () => {
      const config = {
        project: { number: 0 },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });

    it("rejects negative numbers (ci_timeout)", () => {
      const config = {
        pipeline: { ci_timeout: -1 },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });

    it("accepts empty arrays (reviewers, trusted_stages)", () => {
      const config = {
        pull_request: { reviewers: [] },
        human_in_the_loop: { trusted_stages: [] },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("accepts minimum valid values", () => {
      const config = {
        project: { number: 1 },
        pipeline: { ci_timeout: 0 },
        validation: { max_files_changed: 1, max_lines_changed: 1 },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("rejects backoff_multiplier less than 1", () => {
      const config = {
        pipeline: {
          retry: { backoff_multiplier: 0.5 },
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });
  });

  // ============================================================================
  // Enum Validation Tests
  // ============================================================================

  describe("enum validation", () => {
    it("accepts valid merge strategies", () => {
      expect(MergeStrategySchema.safeParse("squash").success).toBe(true);
      expect(MergeStrategySchema.safeParse("merge").success).toBe(true);
      expect(MergeStrategySchema.safeParse("rebase").success).toBe(true);
      expect(MergeStrategySchema.safeParse("invalid").success).toBe(false);
    });

    it("accepts valid sync directions", () => {
      expect(SyncDirectionSchema.safeParse("bidirectional").success).toBe(true);
      expect(SyncDirectionSchema.safeParse("labels-to-fields").success).toBe(true);
      expect(SyncDirectionSchema.safeParse("fields-to-labels").success).toBe(true);
      expect(SyncDirectionSchema.safeParse("invalid").success).toBe(false);
    });

    it("accepts valid conflict resolution values", () => {
      expect(ConflictResolutionSchema.safeParse("labels").success).toBe(true);
      expect(ConflictResolutionSchema.safeParse("fields").success).toBe(true);
      expect(ConflictResolutionSchema.safeParse("warn").success).toBe(true);
      expect(ConflictResolutionSchema.safeParse("invalid").success).toBe(false);
    });

    it("accepts valid enforcement modes", () => {
      expect(EnforcementModeSchema.safeParse("warn").success).toBe(true);
      expect(EnforcementModeSchema.safeParse("block").success).toBe(true);
      expect(EnforcementModeSchema.safeParse("ignore").success).toBe(true);
      expect(EnforcementModeSchema.safeParse("invalid").success).toBe(false);
    });

    it("accepts valid custom field types", () => {
      expect(CustomFieldTypeSchema.safeParse("single_select").success).toBe(true);
      expect(CustomFieldTypeSchema.safeParse("text").success).toBe(true);
      expect(CustomFieldTypeSchema.safeParse("number").success).toBe(true);
      expect(CustomFieldTypeSchema.safeParse("invalid").success).toBe(false);
    });

    it("accepts valid sanitization modes", () => {
      expect(SanitizationModeSchema.safeParse("warn").success).toBe(true);
      expect(SanitizationModeSchema.safeParse("block").success).toBe(true);
      expect(SanitizationModeSchema.safeParse("disabled").success).toBe(true);
      expect(SanitizationModeSchema.safeParse("invalid").success).toBe(false);
    });

    it("accepts valid trusted stages", () => {
      expect(TrustedStageSchema.safeParse("issue-pickup").success).toBe(true);
      expect(TrustedStageSchema.safeParse("feature-planning").success).toBe(true);
      expect(TrustedStageSchema.safeParse("feature-dev").success).toBe(true);
      expect(TrustedStageSchema.safeParse("feature-validate").success).toBe(true);
      expect(TrustedStageSchema.safeParse("pr-create").success).toBe(true);
      expect(TrustedStageSchema.safeParse("pr-merge").success).toBe(true);
      expect(TrustedStageSchema.safeParse("invalid-stage").success).toBe(false);
    });
  });

  // ============================================================================
  // Error Message Tests
  // ============================================================================

  describe("error messages", () => {
    it("includes field path in error", () => {
      const config = {
        project: { number: "abc" },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors[0].field).toContain("project");
      expect(result.errors[0].field).toContain("number");
    });

    it("includes expected type in error message", () => {
      const config = {
        pull_request: { merge_strategy: "invalid" },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors[0].message).toBeDefined();
    });

    it("collects multiple errors", () => {
      const config = {
        project: { number: "abc" },
        branch: { base: 123 },
        pipeline: { ci_timeout: -1 },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors.length).toBeGreaterThan(1);
    });

    it("provides clear error for missing required fields in nested objects", () => {
      const config = {
        project: {
          custom_fields: [
            {
              // Missing required fields: name, field_id, label_prefix, type
            },
          ],
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
      expect(result.errors.length).toBeGreaterThan(0);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("returns defaults for empty config via mergeWithDefaults", () => {
      const result = mergeWithDefaults({});
      expect(result).toBeDefined();
    });

    it("applies pull_request defaults", () => {
      const config = mergeWithDefaults({ pull_request: {} });
      expect(config.pull_request?.merge_strategy).toBe("squash");
      expect(config.pull_request?.delete_branch).toBe(true);
      expect(config.pull_request?.draft_by_default).toBe(false);
      expect(config.pull_request?.reviewers).toEqual([]);
    });

    it("applies branch defaults", () => {
      const config = mergeWithDefaults({ branch: {} });
      expect(config.branch?.base).toBe("main");
      expect(config.branch?.protected).toContain("main");
    });

    it("applies pipeline defaults", () => {
      const config = mergeWithDefaults({ pipeline: {} });
      expect(config.pipeline?.ci_timeout).toBe(10);
      expect(config.pipeline?.auto_fix).toBe(true);
    });

    it("applies validation defaults", () => {
      const config = mergeWithDefaults({ validation: {} });
      expect(config.validation?.require_tests).toBe(true);
      expect(config.validation?.max_files_changed).toBe(50);
    });

    it("applies sanitization defaults", () => {
      const config = mergeWithDefaults({ sanitization: {} });
      expect(config.sanitization?.enabled).toBe(true);
      expect(config.sanitization?.mode).toBe("warn");
      expect(config.sanitization?.warn_only).toBe(false);
      expect(config.sanitization?.safe_directories).toEqual([
        "./dist",
        "./build",
        "./node_modules",
        "./.next",
        "./coverage",
        "./out",
        "./.cache",
      ]);
    });

    it("applies human_in_the_loop defaults", () => {
      const config = mergeWithDefaults({ human_in_the_loop: {} });
      expect(config.human_in_the_loop?.auto_accept_stages).toBe(true);
      expect(config.human_in_the_loop?.auto_accept_permissions).toBe(false);
      expect(config.human_in_the_loop?.trusted_stages).toEqual([]);
    });

    it("getDefaultConfig returns complete defaults", () => {
      const defaults = getDefaultConfig();
      expect(defaults).toBeDefined();
      // Check some expected defaults exist
      expect(defaults.pull_request).toBeDefined();
      expect(defaults.branch).toBeDefined();
      expect(defaults.pipeline).toBeDefined();
    });
  });

  // ============================================================================
  // Section Schema Tests
  // ============================================================================

  describe("section schemas", () => {
    it("ProjectConfigSchema accepts valid project config", () => {
      const result = ProjectConfigSchema.safeParse({
        number: 10,
        owner: "org",
        auto_dates: true,
      });
      expect(result.success).toBe(true);
    });

    it("PullRequestConfigSchema accepts valid PR config", () => {
      const result = PullRequestConfigSchema.safeParse({
        merge_strategy: "squash",
        reviewers: ["alice"],
      });
      expect(result.success).toBe(true);
    });

    it("BranchConfigSchema accepts valid branch config", () => {
      const result = BranchConfigSchema.safeParse({
        base: "main",
        protected: ["main"],
        prefixes: { feature: "feat/" },
      });
      expect(result.success).toBe(true);
    });

    it("IssueConfigSchema accepts valid issue config", () => {
      const result = IssueConfigSchema.safeParse({
        auto_assign: true,
        default_labels: ["needs-triage"],
      });
      expect(result.success).toBe(true);
    });

    it("PipelineConfigSchema accepts valid pipeline config", () => {
      const result = PipelineConfigSchema.safeParse({
        ci_timeout: 300,
        auto_fix: true,
        skip: { tests: false },
        logs: { retain: true },
        retry: { max_auto_attempts: 3 },
      });
      expect(result.success).toBe(true);
    });

    it("RoutingConfigSchema accepts valid routing config", () => {
      const result = RoutingConfigSchema.safeParse({
        trivial_max_complexity: 2,
        extensive_min_complexity: 5,
      });
      expect(result.success).toBe(true);
    });

    it("RoutingConfigSchema accepts a full change_rules block (#4125)", () => {
      const result = RoutingConfigSchema.safeParse({
        force_full_pipeline: false,
        change_rules: [
          {
            name: "docs-only",
            description: "Docs skip planning and validate.",
            globs: ["docs/**", "**/*.md"],
            change_types: ["docs"],
            skip_stages: ["feature-planning", "feature-validate"],
            override_route: "trivial",
          },
          {
            name: "generated",
            globs: ["**/*.gen.go"],
            change_types: ["code"],
            ci_jobs: ["build-and-test"],
          },
        ],
      });
      expect(result.success).toBe(true);
    });

    it("RoutingConfigSchema is valid with no change_rules (defaults apply)", () => {
      const result = RoutingConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("ChangeRuleSchema requires a non-empty name", () => {
      const result = ChangeRuleSchema.safeParse({ name: "", globs: ["docs/**"] });
      expect(result.success).toBe(false);
    });

    it("ChangeRuleSchema rejects an unknown change_type", () => {
      const result = ChangeRuleSchema.safeParse({
        name: "bad",
        change_types: ["binary"],
      });
      expect(result.success).toBe(false);
    });

    it("ChangeRuleSchema rejects an invalid override_route", () => {
      const result = ChangeRuleSchema.safeParse({
        name: "bad",
        override_route: "fast",
      });
      expect(result.success).toBe(false);
    });

    it("ValidationConfigSchema accepts valid validation config", () => {
      const result = ValidationConfigSchema.safeParse({
        require_tests: true,
        max_files_changed: 50,
      });
      expect(result.success).toBe(true);
    });

    it("SanitizationConfigSchema accepts valid sanitization config", () => {
      const result = SanitizationConfigSchema.safeParse({
        enabled: true,
        allowlist: ["rm -rf ./node_modules"],
      });
      expect(result.success).toBe(true);
    });

    it("SanitizationConfigSchema accepts safe_directories", () => {
      const result = SanitizationConfigSchema.safeParse({
        enabled: true,
        safe_directories: ["./dist", "./build", "./node_modules"],
      });
      expect(result.success).toBe(true);
    });

    it("HumanInTheLoopConfigSchema accepts valid HITL config", () => {
      const result = HumanInTheLoopConfigSchema.safeParse({
        auto_accept_stages: true,
        trusted_stages: ["feature-planning"],
      });
      expect(result.success).toBe(true);
    });

    it("RalphLoopConfigSchema accepts valid ralph loop config", () => {
      const result = RalphLoopConfigSchema.safeParse({
        enabled: true,
        limits: { max_iterations: 5 },
      });
      expect(result.success).toBe(true);
    });

    it("BaselineCIGateConfigSchema accepts valid config", () => {
      const result = BaselineCIGateConfigSchema.safeParse({
        enabled: true,
        lookback_runs: 10,
        red_threshold: 3,
        green_threshold: 2,
      });
      expect(result.success).toBe(true);
    });

    it("BaselineCIGateConfigSchema accepts empty object (all optional)", () => {
      const result = BaselineCIGateConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("BaselineCIGateConfigSchema rejects lookback_runs > 20", () => {
      const result = BaselineCIGateConfigSchema.safeParse({ lookback_runs: 50 });
      expect(result.success).toBe(false);
    });

    it("BaselineCIGateConfigSchema rejects negative thresholds", () => {
      const result = BaselineCIGateConfigSchema.safeParse({ red_threshold: 0 });
      expect(result.success).toBe(false);
    });

    it("PipelineConfigSchema accepts baseline_ci_gate section", () => {
      const result = PipelineConfigSchema.safeParse({
        baseline_ci_gate: { enabled: true, lookback_runs: 5 },
      });
      expect(result.success).toBe(true);
    });

    it("AutomationsConfigSchema accepts valid automations config", () => {
      const result = AutomationsConfigSchema.safeParse({
        enabled: true,
        triggers: [
          {
            trigger: "status:done",
            actions: [{ type: "notify", users: ["@team"], message: "Done!" }],
          },
        ],
      });
      expect(result.success).toBe(true);
    });
  });

  // ============================================================================
  // Complex Validation Tests
  // ============================================================================

  describe("complex validation scenarios", () => {
    it("accepts custom_fields with mappings", () => {
      const config = {
        project: {
          number: 10,
          custom_fields: [
            {
              name: "Component",
              field_id: "PVTSSF_abc123",
              label_prefix: "component",
              type: "single_select",
              mappings: {
                frontend: "Frontend",
                backend: "Backend",
              },
            },
          ],
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("accepts automation triggers with multiple actions", () => {
      const config = {
        automations: {
          enabled: true,
          triggers: [
            {
              name: "on-review",
              trigger: "status:in-review",
              from: "status:in-progress",
              actions: [
                { type: "assign_reviewers", reviewers: ["@team/reviewers"] },
                {
                  type: "post_slack",
                  webhook_env: "SLACK_WEBHOOK",
                  message: "Ready for review",
                },
                { type: "add_label", label: "needs-review" },
              ],
            },
          ],
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("accepts both pr and pull_request (alias support)", () => {
      const config1 = {
        pr: { merge_strategy: "squash" },
      };
      const config2 = {
        pull_request: { merge_strategy: "squash" },
      };
      expect(validateConfig(config1).valid).toBe(true);
      expect(validateConfig(config2).valid).toBe(true);
    });

    it("accepts ralph loop abort patterns", () => {
      const config = {
        ralph_loop: {
          enabled: true,
          abort_patterns: ["Custom error pattern", "FATAL: database connection failed"],
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(true);
    });

    it("rejects automation trigger without actions", () => {
      const config = {
        automations: {
          triggers: [
            {
              trigger: "status:done",
              actions: [], // Empty actions array should fail
            },
          ],
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });

    it("rejects invalid trusted_stages values", () => {
      const config = {
        human_in_the_loop: {
          trusted_stages: ["invalid-stage"],
        },
      };
      const result = validateConfig(config);
      expect(result.valid).toBe(false);
    });
  });

  // ============================================================================
  // parseConfig Tests
  // ============================================================================

  describe("parseConfig", () => {
    it("returns validated config for valid input", () => {
      const config = parseConfig({ project: { number: 10 } });
      expect(config.project?.number).toBe(10);
    });

    it("throws ZodError for invalid input", () => {
      expect(() => {
        parseConfig({ project: { number: "invalid" } });
      }).toThrow();
    });

    it("applies defaults during parse", () => {
      const config = parseConfig({});
      expect(config).toBeDefined();
    });
  });
});

// ============================================================================
// AutonomousConfigSchema Tests (Issue #2536)
// ============================================================================

describe("AutonomousConfigSchema", () => {
  describe("valid configurations", () => {
    it("accepts all four new fields with valid values", () => {
      const result = AutonomousConfigSchema.safeParse({
        auto_actionable: true,
        refinement_enabled: false,
        refinement_interval: "60s",
        refinement_max_concurrent: 2,
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.auto_actionable).toBe(true);
        expect(result.data.refinement_enabled).toBe(false);
        expect(result.data.refinement_interval).toBe("60s");
        expect(result.data.refinement_max_concurrent).toBe(2);
      }
    });

    it("accepts empty object (all fields optional)", () => {
      const result = AutonomousConfigSchema.safeParse({});
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.auto_actionable).toBeUndefined();
        expect(result.data.refinement_enabled).toBeUndefined();
        expect(result.data.refinement_interval).toBeUndefined();
        expect(result.data.refinement_max_concurrent).toBeUndefined();
      }
    });

    it("accepts partial config with only new fields", () => {
      const result = AutonomousConfigSchema.safeParse({
        auto_actionable: false,
        refinement_max_concurrent: 3,
      });
      expect(result.success).toBe(true);
    });

    it("accepts refinement_max_concurrent at minimum (1)", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_max_concurrent: 1,
      });
      expect(result.success).toBe(true);
    });

    it("accepts refinement_max_concurrent at maximum (3)", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_max_concurrent: 3,
      });
      expect(result.success).toBe(true);
    });

    it("accepts refinement_interval as a duration string", () => {
      for (const interval of ["30s", "1m", "5m", "120s"]) {
        const result = AutonomousConfigSchema.safeParse({
          refinement_interval: interval,
        });
        expect(result.success).toBe(true);
      }
    });

    it("accepts existing fields alongside new fields", () => {
      const result = AutonomousConfigSchema.safeParse({
        scan_interval: "30s",
        max_concurrent: 3,
        pickup_backlog: false,
        auto_actionable: false,
        refinement_enabled: true,
        refinement_interval: "60s",
        refinement_max_concurrent: 1,
      });
      expect(result.success).toBe(true);
    });

    it("accepts safety_rails nested config", () => {
      const result = AutonomousConfigSchema.safeParse({
        auto_actionable: false,
        safety_rails: {
          circuit_breaker_max: 3,
          rate_limit_per_hour: 20,
        },
      });
      expect(result.success).toBe(true);
    });
  });

  describe("invalid configurations", () => {
    it("rejects refinement_max_concurrent below minimum (0)", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_max_concurrent: 0,
      });
      expect(result.success).toBe(false);
    });

    it("rejects refinement_max_concurrent above maximum (4)", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_max_concurrent: 4,
      });
      expect(result.success).toBe(false);
    });

    it("rejects auto_actionable as string", () => {
      const result = AutonomousConfigSchema.safeParse({
        auto_actionable: "true",
      });
      expect(result.success).toBe(false);
    });

    it("rejects refinement_enabled as number", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_enabled: 1,
      });
      expect(result.success).toBe(false);
    });

    it("rejects refinement_max_concurrent as string", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_max_concurrent: "2",
      });
      expect(result.success).toBe(false);
    });

    it("rejects refinement_max_concurrent as float", () => {
      const result = AutonomousConfigSchema.safeParse({
        refinement_max_concurrent: 1.5,
      });
      expect(result.success).toBe(false);
    });
  });

  // Issue #3437 — `enabled_repos` and `repositories` were missing from the
  // schema, so Zod silently stripped them during parse. The merged config
  // returned `null` for `autonomous.enabled_repos`, which made
  // `enabledRepos.length === 0` trigger the "scan all" branch and made
  // every checkbox in the Repositories tree read as Checked regardless
  // of what the user toggled. The Go side worked because it has its own
  // YAML loader. These regression tests pin the schema fields in place.
  describe("enabled_repos and repositories — Issue #3437", () => {
    it("preserves enabled_repos array of repo short names", () => {
      const result = AutonomousConfigSchema.safeParse({
        enabled_repos: ["nightgauge", "acme-platform"],
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.enabled_repos).toEqual(["nightgauge", "acme-platform"]);
      }
    });

    it("preserves enabled_repos with fully-qualified slugs", () => {
      const result = AutonomousConfigSchema.safeParse({
        enabled_repos: ["nightgauge/nightgauge", "acme/mobile"],
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.enabled_repos).toEqual(["nightgauge/nightgauge", "acme/mobile"]);
      }
    });

    it("preserves empty enabled_repos array (means scan-all by Go convention)", () => {
      const result = AutonomousConfigSchema.safeParse({ enabled_repos: [] });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.enabled_repos).toEqual([]);
      }
    });

    it("preserves repositories map with sequential and max_concurrent", () => {
      const result = AutonomousConfigSchema.safeParse({
        repositories: {
          nightgauge: { sequential: true },
          "acme-platform": { max_concurrent: 2 },
        },
      });
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.repositories?.nightgauge?.sequential).toBe(true);
        expect(result.data.repositories?.["acme-platform"]?.max_concurrent).toBe(2);
      }
    });

    it("rejects max_concurrent < 1 in repositories override", () => {
      const result = AutonomousConfigSchema.safeParse({
        repositories: { foo: { max_concurrent: 0 } },
      });
      expect(result.success).toBe(false);
    });

    it("rejects non-string entries in enabled_repos", () => {
      const result = AutonomousConfigSchema.safeParse({
        enabled_repos: ["nightgauge", 42],
      });
      expect(result.success).toBe(false);
    });

    it("regression — full autonomous block from a real .nightgauge/config.yaml round-trips intact", () => {
      // Mirrors the user's actual config that surfaced #3437. The whole
      // point: this exact block must come out of safeParse identical to
      // what went in (modulo Zod-introduced defaults). If a future
      // refactor strips `enabled_repos` or `repositories` again, this
      // test fails before any user notices the regression.
      const input = {
        scan_interval: "30s",
        max_concurrent: 1,
        budget_ceiling: 0,
        refinement_enabled: true,
        refinement_interval: "60s",
        refinement_max_concurrent: 1,
        safety_rails: {
          circuit_breaker_max: 3,
          rate_limit_per_hour: 20,
          epic_checkpoint: true,
          health_gate_min: 30,
        },
        repositories: {
          nightgauge: { sequential: true },
          "acme-platform": { sequential: true },
          "acme-dashboard": { sequential: true },
          "acme-mobile": { sequential: true },
        },
        enabled_repos: ["nightgauge", "acme-platform", "acme-dashboard"],
      };
      const result = AutonomousConfigSchema.safeParse(input);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data).toEqual(input);
      }
    });
  });
});

describe("SafetyRailsConfigSchema", () => {
  it("accepts valid safety rails config", () => {
    const result = SafetyRailsConfigSchema.safeParse({
      budget_ceiling: 500000,
      circuit_breaker_max: 3,
      rate_limit_per_hour: 20,
      epic_checkpoint: true,
      health_gate_min: 30,
    });
    expect(result.success).toBe(true);
  });

  it("accepts empty object (all fields optional)", () => {
    const result = SafetyRailsConfigSchema.safeParse({});
    expect(result.success).toBe(true);
  });

  it("rejects health_gate_min outside 0-100", () => {
    const result = SafetyRailsConfigSchema.safeParse({ health_gate_min: 101 });
    expect(result.success).toBe(false);
  });
});

describe("IncrediConfigSchema autonomous section (Issue #2536)", () => {
  it("accepts autonomous section in top-level config", () => {
    const result = IncrediConfigSchema.safeParse({
      autonomous: {
        auto_actionable: false,
        refinement_enabled: true,
        refinement_interval: "60s",
        refinement_max_concurrent: 1,
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.autonomous?.auto_actionable).toBe(false);
      expect(result.data.autonomous?.refinement_enabled).toBe(true);
      expect(result.data.autonomous?.refinement_max_concurrent).toBe(1);
    }
  });

  it("autonomous section is optional in top-level config", () => {
    const result = IncrediConfigSchema.safeParse({ project: { number: 1 } });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.autonomous).toBeUndefined();
    }
  });

  it("rejects invalid refinement_max_concurrent in top-level config", () => {
    const result = IncrediConfigSchema.safeParse({
      autonomous: { refinement_max_concurrent: 5 },
    });
    expect(result.success).toBe(false);
  });
});

// ============================================================================
// GitHubAuthConfigSchema tests (#2663 — per-project GitHub token config)
// ============================================================================

describe("GitHubAuthConfigSchema", () => {
  it("accepts empty object", () => {
    const result = GitHubAuthConfigSchema.safeParse({});
    expect(result.success).toBe(true);
  });

  it("accepts users-only config (backward compat)", () => {
    const result = GitHubAuthConfigSchema.safeParse({
      users: { nightgauge: "octocat" },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.users?.["nightgauge"]).toBe("octocat");
    }
  });

  it("accepts per-project token field", () => {
    const result = GitHubAuthConfigSchema.safeParse({
      token: "env:GITHUB_TOKEN_NIGHTGAUGE",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.token).toBe("env:GITHUB_TOKEN_NIGHTGAUGE");
    }
  });

  it("accepts direct PAT string in token field", () => {
    const result = GitHubAuthConfigSchema.safeParse({
      token: "ghp_directtoken123",
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.token).toBe("ghp_directtoken123");
    }
  });

  it("accepts per-org tokens map", () => {
    const result = GitHubAuthConfigSchema.safeParse({
      tokens: {
        nightgauge: "env:GITHUB_TOKEN_NIGHTGAUGE",
        "Acme-Community": "env:GITHUB_TOKEN_ACME",
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.tokens?.["nightgauge"]).toBe("env:GITHUB_TOKEN_NIGHTGAUGE");
      expect(result.data.tokens?.["Acme-Community"]).toBe("env:GITHUB_TOKEN_ACME");
    }
  });

  it("accepts all fields together", () => {
    const result = GitHubAuthConfigSchema.safeParse({
      users: { nightgauge: "octocat" },
      token: "env:GITHUB_TOKEN_NIGHTGAUGE",
      tokens: { nightgauge: "env:GITHUB_TOKEN_NIGHTGAUGE" },
    });
    expect(result.success).toBe(true);
  });

  it("accepts github_auth embedded in top-level config", () => {
    const result = IncrediConfigSchema.safeParse({
      github_auth: {
        token: "env:GITHUB_TOKEN_NIGHTGAUGE",
        tokens: { nightgauge: "env:GITHUB_TOKEN_NIGHTGAUGE" },
      },
    });
    expect(result.success).toBe(true);
    if (result.success) {
      expect(result.data.github_auth?.token).toBe("env:GITHUB_TOKEN_NIGHTGAUGE");
    }
  });
});

// ============================================================================
// resolvePlatformBaseUrl — Issue #3718
// ============================================================================

describe("resolvePlatformBaseUrl", () => {
  it("returns production URL when config is undefined", () => {
    expect(resolvePlatformBaseUrl(undefined)).toBe(PLATFORM_ENV_PRESETS.production);
  });

  it("returns production URL when environment is 'production'", () => {
    expect(resolvePlatformBaseUrl({ environment: "production" })).toBe(
      PLATFORM_ENV_PRESETS.production
    );
  });

  it("returns canary URL when environment is 'canary'", () => {
    expect(resolvePlatformBaseUrl({ environment: "canary" })).toBe(PLATFORM_ENV_PRESETS.canary);
  });

  it("returns local URL when environment is 'local'", () => {
    expect(resolvePlatformBaseUrl({ environment: "local" })).toBe(PLATFORM_ENV_PRESETS.local);
  });

  it("returns api_url when environment is 'custom' with HTTPS URL", () => {
    const url = "https://staging.api.nightgauge.dev";
    expect(resolvePlatformBaseUrl({ environment: "custom", api_url: url })).toBe(url);
  });

  it("allows localhost HTTP URL when environment is 'custom'", () => {
    expect(
      resolvePlatformBaseUrl({ environment: "custom", api_url: "http://localhost:8787" })
    ).toBe("http://localhost:8787");
  });

  it("allows 127.0.0.1 HTTP URL when environment is 'custom'", () => {
    expect(
      resolvePlatformBaseUrl({ environment: "custom", api_url: "http://127.0.0.1:8787" })
    ).toBe("http://127.0.0.1:8787");
  });

  it("throws for non-HTTPS custom URL on a non-localhost host", () => {
    expect(() =>
      resolvePlatformBaseUrl({ environment: "custom", api_url: "http://staging.example.com" })
    ).toThrow(/HTTPS/);
  });

  it("falls back to production when environment is 'custom' but api_url is missing", () => {
    expect(resolvePlatformBaseUrl({ environment: "custom" })).toBe(PLATFORM_ENV_PRESETS.production);
  });

  it("falls back to production for an invalid custom URL", () => {
    expect(resolvePlatformBaseUrl({ environment: "custom", api_url: "not-a-url" })).toBe(
      PLATFORM_ENV_PRESETS.production
    );
  });

  it("backward-compat: treats non-production api_url without environment as custom", () => {
    const url = "https://dev.api.nightgauge.dev";
    expect(resolvePlatformBaseUrl({ api_url: url })).toBe(url);
  });

  it("backward-compat: production api_url without environment returns production preset", () => {
    expect(resolvePlatformBaseUrl({ api_url: PLATFORM_ENV_PRESETS.production })).toBe(
      PLATFORM_ENV_PRESETS.production
    );
  });
});
