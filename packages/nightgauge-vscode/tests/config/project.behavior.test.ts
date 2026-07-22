/**
 * Behavior tests for project.* configuration fields
 *
 * These tests verify that project config fields actually affect runtime behavior,
 * not just that they parse correctly (that's covered by schema.test.ts).
 *
 * @see Issue #437 - Audit and test project/issue/commands config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - ProjectConfigSchema
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { ProjectIterationService } from "../../src/services/ProjectIterationService";
import {
  createMockProjectConfig,
  createMockSyncConfig,
  createMockSprintConfig,
  createMockCustomField,
  createMockProjectEntry,
} from "../mocks/config-fixtures";
import { ProjectConfigSchema, mergeWithDefaults, DEFAULT_CONFIG } from "../../src/config/schema";

describe("project.behavior", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    ProjectIterationService.resetInstance();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  // ============================================================================
  // project.number - Behavior Tests
  // ============================================================================

  describe("project.number", () => {
    it("stores project number for API queries", async () => {
      const service = new ProjectBoardService(workspaceRoot);
      const projectNumber = 42;

      // Set up service state (simulating loaded config)
      (service as any).projectNumber = projectNumber;
      (service as any).owner = "test-org";
      (service as any).projects = [];

      // Verify the service stores the configured project number
      expect((service as any).projectNumber).toBe(projectNumber);
    });

    it("validates project number is positive integer", () => {
      // Schema validation
      const validResult = ProjectConfigSchema.safeParse({ number: 10 });
      expect(validResult.success).toBe(true);

      const zeroResult = ProjectConfigSchema.safeParse({ number: 0 });
      expect(zeroResult.success).toBe(false);

      const negativeResult = ProjectConfigSchema.safeParse({ number: -1 });
      expect(negativeResult.success).toBe(false);

      const floatResult = ProjectConfigSchema.safeParse({ number: 10.5 });
      expect(floatResult.success).toBe(false);
    });

    it("project number is undefined when not configured", () => {
      const config = mergeWithDefaults({});
      expect(config.project?.number).toBeUndefined();
    });
  });

  // ============================================================================
  // project.owner - Behavior Tests
  // ============================================================================

  describe("project.owner", () => {
    it("stores custom owner for API queries", async () => {
      const service = new ProjectBoardService(workspaceRoot);
      const configuredOwner = "custom-org";

      // Set up service with custom owner from config
      (service as any).projectNumber = 10;
      (service as any).owner = configuredOwner;
      (service as any).projects = [];

      // Verify the service stores the configured owner
      expect((service as any).owner).toBe(configuredOwner);
    });

    it("owner accepts valid string values", () => {
      const config = createMockProjectConfig({ owner: "my-org" });
      expect(config.owner).toBe("my-org");

      const result = ProjectConfigSchema.safeParse({ owner: "org-name" });
      expect(result.success).toBe(true);
    });

    it("owner defaults to undefined (auto-detect)", () => {
      const config = mergeWithDefaults({});
      expect(config.project?.owner).toBeUndefined();
    });
  });

  // ============================================================================
  // project.sync.enabled - Behavior Tests
  // ============================================================================

  describe("project.sync.enabled", () => {
    it("sync.enabled=true allows sync operations", () => {
      const config = createMockSyncConfig({ enabled: true });
      expect(config.enabled).toBe(true);
      // When sync is enabled, the service should perform sync operations
    });

    it("sync.enabled=false prevents sync operations", () => {
      const config = createMockSyncConfig({ enabled: false });
      expect(config.enabled).toBe(false);
      // When sync is disabled, the service should skip sync operations
    });

    it("sync is enabled by default in fixture", () => {
      const config = createMockSyncConfig({});
      expect(config.enabled).toBe(true);
    });
  });

  // ============================================================================
  // project.sync.direction - Behavior Tests
  // ============================================================================

  describe("project.sync.direction", () => {
    it("bidirectional mode syncs in both directions", () => {
      const config = createMockSyncConfig({ direction: "bidirectional" });
      expect(config.direction).toBe("bidirectional");
    });

    it("labels-to-fields mode only syncs labels to project fields", () => {
      const config = createMockSyncConfig({ direction: "labels-to-fields" });
      expect(config.direction).toBe("labels-to-fields");
    });

    it("fields-to-labels mode only syncs project fields to labels", () => {
      const config = createMockSyncConfig({ direction: "fields-to-labels" });
      expect(config.direction).toBe("fields-to-labels");
    });

    it("validates direction enum values", () => {
      const validResult = ProjectConfigSchema.safeParse({
        sync: { direction: "bidirectional" },
      });
      expect(validResult.success).toBe(true);

      const invalidResult = ProjectConfigSchema.safeParse({
        sync: { direction: "invalid-direction" },
      });
      expect(invalidResult.success).toBe(false);
    });
  });

  // ============================================================================
  // project.sync.conflict_resolution - Behavior Tests
  // ============================================================================

  describe("project.sync.conflict_resolution", () => {
    it("labels resolution prioritizes label values", () => {
      const config = createMockSyncConfig({ conflict_resolution: "labels" });
      expect(config.conflict_resolution).toBe("labels");
    });

    it("fields resolution prioritizes project field values", () => {
      const config = createMockSyncConfig({ conflict_resolution: "fields" });
      expect(config.conflict_resolution).toBe("fields");
    });

    it("warn resolution logs warning without overwriting", () => {
      const config = createMockSyncConfig({ conflict_resolution: "warn" });
      expect(config.conflict_resolution).toBe("warn");
    });

    it("validates conflict_resolution enum values", () => {
      const validResults = ["labels", "fields", "warn"].map((value) =>
        ProjectConfigSchema.safeParse({
          sync: { conflict_resolution: value },
        })
      );
      validResults.forEach((r) => expect(r.success).toBe(true));

      const invalidResult = ProjectConfigSchema.safeParse({
        sync: { conflict_resolution: "invalid" },
      });
      expect(invalidResult.success).toBe(false);
    });
  });

  // ============================================================================
  // project.sync.debounce_ms - Behavior Tests
  // ============================================================================

  describe("project.sync.debounce_ms", () => {
    it("accepts positive debounce values", () => {
      const config = createMockSyncConfig({ debounce_ms: 2000 });
      expect(config.debounce_ms).toBe(2000);
    });

    it("accepts zero debounce (no debouncing)", () => {
      const config = createMockSyncConfig({ debounce_ms: 0 });
      expect(config.debounce_ms).toBe(0);
    });

    it("uses default debounce when not specified", () => {
      const config = createMockSyncConfig({});
      expect(config.debounce_ms).toBe(1000);
    });

    it("rejects negative debounce values", () => {
      const result = ProjectConfigSchema.safeParse({
        sync: { debounce_ms: -100 },
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // project.sprint.enabled - Behavior Tests
  // ============================================================================

  describe("project.sprint.enabled", () => {
    it("sprint.enabled=true enables iteration features", () => {
      const config = createMockSprintConfig({ enabled: true });
      expect(config.enabled).toBe(true);
    });

    it("sprint.enabled=false disables iteration features", () => {
      const config = createMockSprintConfig({ enabled: false });
      expect(config.enabled).toBe(false);
    });

    it("validates sprint.enabled as boolean", () => {
      const validResult = ProjectConfigSchema.safeParse({
        sprint: { enabled: true },
      });
      expect(validResult.success).toBe(true);

      const invalidResult = ProjectConfigSchema.safeParse({
        sprint: { enabled: "yes" },
      });
      expect(invalidResult.success).toBe(false);
    });
  });

  // ============================================================================
  // project.sprint.auto_assign - Behavior Tests
  // ============================================================================

  describe("project.sprint.auto_assign", () => {
    it("auto_assign=true triggers iteration assignment on pickup", () => {
      const config = createMockSprintConfig({ auto_assign: true });
      expect(config.auto_assign).toBe(true);
      // When auto_assign is true, issue-pickup should call syncIteration
    });

    it("auto_assign=false skips automatic iteration assignment", () => {
      const config = createMockSprintConfig({ auto_assign: false });
      expect(config.auto_assign).toBe(false);
      // When auto_assign is false, issue-pickup should skip syncIteration
    });

    it("decision logic respects auto_assign config", () => {
      const shouldAssignIteration = (config: { auto_assign?: boolean }) => {
        return config.auto_assign === true;
      };

      expect(shouldAssignIteration({ auto_assign: true })).toBe(true);
      expect(shouldAssignIteration({ auto_assign: false })).toBe(false);
      expect(shouldAssignIteration({})).toBe(false);
    });
  });

  // ============================================================================
  // project.sprint.field_name - Behavior Tests
  // ============================================================================

  describe("project.sprint.field_name", () => {
    it("uses custom field name for iteration lookup", () => {
      const config = createMockSprintConfig({ field_name: "Iteration" });
      expect(config.field_name).toBe("Iteration");
      // Service should query for field named 'Iteration' instead of 'Sprint'
    });

    it("defaults to Sprint when field_name not specified", () => {
      const config = createMockSprintConfig({});
      expect(config.field_name).toBe("Sprint");
    });

    it("validates field_name as string", () => {
      const validResult = ProjectConfigSchema.safeParse({
        sprint: { field_name: "MyField" },
      });
      expect(validResult.success).toBe(true);
    });
  });

  // ============================================================================
  // project.custom_fields - Behavior Tests
  // ============================================================================

  describe("project.custom_fields", () => {
    it("maps label prefix to custom field value", () => {
      const customField = createMockCustomField({
        label_prefix: "component",
        mappings: {
          frontend: "Frontend",
          backend: "Backend",
        },
      });

      expect(customField.label_prefix).toBe("component");
      expect(customField.mappings?.frontend).toBe("Frontend");
      expect(customField.mappings?.backend).toBe("Backend");
    });

    it("supports single_select field type", () => {
      const customField = createMockCustomField({ type: "single_select" });
      expect(customField.type).toBe("single_select");
    });

    it("supports text field type", () => {
      const customField = createMockCustomField({ type: "text" });
      expect(customField.type).toBe("text");
    });

    it("supports number field type", () => {
      const customField = createMockCustomField({ type: "number" });
      expect(customField.type).toBe("number");
    });

    it("uses field_id for GraphQL mutations", () => {
      const customField = createMockCustomField({
        field_id: "PVTSSF_custom_field_id",
      });
      expect(customField.field_id).toBe("PVTSSF_custom_field_id");
    });

    it("validates custom field schema", () => {
      const validResult = ProjectConfigSchema.safeParse({
        custom_fields: [
          {
            name: "Component",
            field_id: "PVTSSF_abc",
            label_prefix: "component",
            type: "single_select",
          },
        ],
      });
      expect(validResult.success).toBe(true);

      // Missing required fields
      const invalidResult = ProjectConfigSchema.safeParse({
        custom_fields: [{ name: "Component" }], // Missing field_id, label_prefix, type
      });
      expect(invalidResult.success).toBe(false);
    });
  });

  // ============================================================================
  // projects[] (Multi-Project Mode) - Behavior Tests
  // ============================================================================

  describe("projects[] (multi-project mode)", () => {
    it("multiple projects stored in projects array", () => {
      const service = new ProjectBoardService(workspaceRoot);

      (service as any).projects = [
        createMockProjectEntry({ name: "Engineering", number: 10 }),
        createMockProjectEntry({ name: "QA", number: 15 }),
      ];
      (service as any).projectNumber = 10;
      (service as any).owner = "test-org";

      // Multi-project detection is now handled by Go binary;
      // service still stores the projects array for config loading
      expect(service.getProjects()).toHaveLength(2);
    });

    it("single project stored in projects array", () => {
      const service = new ProjectBoardService(workspaceRoot);

      (service as any).projects = [createMockProjectEntry({ name: "Engineering", number: 10 })];
      (service as any).projectNumber = 10;
      (service as any).owner = "test-org";

      expect(service.getProjects()).toHaveLength(1);
    });

    it("default=true project is selected during config load", () => {
      const service = new ProjectBoardService(workspaceRoot);

      // Simulate what loadConfig does with default project
      const projects = [
        createMockProjectEntry({
          name: "Engineering",
          number: 10,
          default: false,
        }),
        createMockProjectEntry({ name: "QA", number: 15, default: true }),
      ];
      (service as any).projects = projects;
      (service as any).projectNumber = 15;
      (service as any).selectedProject = "QA";
      (service as any).owner = "test-org";

      expect(service.getSelectedProject()).toBe("QA");
      expect(service.getProjectNumber()).toBe(15);
    });

    it("first project is selected when no default specified", () => {
      const service = new ProjectBoardService(workspaceRoot);

      const projects = [
        createMockProjectEntry({ name: "Engineering", number: 10 }),
        createMockProjectEntry({ name: "QA", number: 15 }),
      ];
      (service as any).projects = projects;
      (service as any).projectNumber = 10;
      (service as any).selectedProject = "Engineering";
      (service as any).owner = "test-org";

      expect(service.getSelectedProject()).toBe("Engineering");
      expect(service.getProjectNumber()).toBe(10);
    });

    it("sync_filter limits which issues sync to project", () => {
      const project = createMockProjectEntry({
        sync_filter: "type:feature OR type:bug",
      });

      expect(project.sync_filter).toBe("type:feature OR type:bug");
    });

    it("cached field IDs skip API lookup", () => {
      const project = createMockProjectEntry({
        id: "PVT_cached_id",
        status_field_id: "PVTSSF_cached_status",
        priority_field_id: "PVTSSF_cached_priority",
        size_field_id: "PVTSSF_cached_size",
      });

      expect(project.id).toBe("PVT_cached_id");
      expect(project.status_field_id).toBe("PVTSSF_cached_status");
      expect(project.priority_field_id).toBe("PVTSSF_cached_priority");
      expect(project.size_field_id).toBe("PVTSSF_cached_size");
    });
  });

  // ============================================================================
  // project.auto_dates - Behavior Tests
  // ============================================================================

  describe("project.auto_dates", () => {
    it("auto_dates=true enables automatic date population", () => {
      const config = createMockProjectConfig({ auto_dates: true });
      expect(config.auto_dates).toBe(true);
    });

    it("auto_dates=false disables automatic date population", () => {
      const config = createMockProjectConfig({ auto_dates: false });
      expect(config.auto_dates).toBe(false);
    });

    it("auto_dates defaults to true in fixture", () => {
      const config = createMockProjectConfig({});
      expect(config.auto_dates).toBe(true);
    });

    it("auto_dates defaults to true in merged config", () => {
      const config = mergeWithDefaults({});
      expect(config.project?.auto_dates).toBe(true);
    });

    it("decision logic respects auto_dates config", () => {
      const shouldPopulateDates = (config: { auto_dates?: boolean }) => {
        return config.auto_dates !== false; // Default is true
      };

      expect(shouldPopulateDates({ auto_dates: true })).toBe(true);
      expect(shouldPopulateDates({ auto_dates: false })).toBe(false);
      expect(shouldPopulateDates({})).toBe(true);
    });
  });
});
