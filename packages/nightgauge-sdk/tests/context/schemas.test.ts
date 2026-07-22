import { describe, it, expect } from "vitest";
import {
  IssueContextSchema,
  PlanningContextSchema,
  DevContextSchema,
  ValidateContextSchema,
  PRContextSchema,
  SCHEMA_VERSION,
  FeedbackContextSchema,
} from "../../src/context/schemas/index.js";

describe("Context Schemas", () => {
  describe("IssueContextSchema", () => {
    it("should validate a complete issue context", () => {
      const validIssue = {
        schema_version: "1.0" as const,
        issue_number: 42,
        title: "Add user photo upload",
        type: "feature" as const,
        branch: "feat/42-user-photo-upload",
        base_branch: "main",
        requirements: {
          summary: "Allow users to upload profile photos",
          user_story: "As a user, I want to upload a profile photo...",
          acceptance_criteria: ["Users can upload JPG/PNG images", "Images are resized to 200x200"],
          technical_notes: ["Integrate with FileService", "Use S3 storage"],
        },
        labels: ["feature", "priority:high"],
        milestone: "v2.0",
        created_at: "2026-02-01T12:00:00Z",
      };

      const result = IssueContextSchema.safeParse(validIssue);
      expect(result.success).toBe(true);
    });

    it("should validate issue context with milestone object", () => {
      const issueWithMilestoneObject = {
        schema_version: "1.0" as const,
        issue_number: 89,
        title: "SDK Core - PipelineOrchestrator",
        type: "feature" as const,
        branch: "feat/89-sdk-core-pipeline-orchestrator",
        base_branch: "main",
        requirements: {
          summary: "Implement the core PipelineOrchestrator class",
        },
        labels: ["type:feature", "priority:critical"],
        milestone: {
          number: 2,
          title: "Milestone 2: Quality Gates",
          due_on: "2026-02-28T00:00:00Z",
        },
        created_at: "2026-02-01T19:47:00Z",
      };

      const result = IssueContextSchema.safeParse(issueWithMilestoneObject);
      expect(result.success).toBe(true);
    });

    it('should coerce unknown issue type to default "feature"', () => {
      const unknownType = {
        schema_version: "1.0",
        issue_number: 42,
        title: "Test",
        type: "invalid-type",
        branch: "feat/42-test",
        base_branch: "main",
        requirements: { summary: "Test" },
        labels: [],
        created_at: "2026-02-01T12:00:00Z",
      };

      const result = IssueContextSchema.safeParse(unknownType);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.type).toBe("feature");
      }
    });

    it("should coerce common type aliases", () => {
      const aliasedType = {
        schema_version: "1.0",
        issue_number: 42,
        title: "Fix login",
        type: "bugfix",
        branch: "fix/42-login",
        base_branch: "main",
        requirements: { summary: "Fix login" },
        labels: [],
      };

      const result = IssueContextSchema.safeParse(aliasedType);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.type).toBe("bug");
      }
    });

    it("should provide defaults for missing optional fields", () => {
      const missingFields = {
        schema_version: "1.0",
        issue_number: 42,
      };

      const result = IssueContextSchema.safeParse(missingFields);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.issue_number).toBe(42);
        expect(result.data.title).toBe("Untitled");
        expect(result.data.type).toBe("feature");
        expect(result.data.branch).toBe("main");
        expect(result.data.base_branch).toBe("main");
        expect(result.data.labels).toEqual([]);
      }
    });

    it("should coerce branch object to string", () => {
      const objectBranch = {
        schema_version: "1.0",
        issue_number: 42,
        title: "Test",
        type: "feature",
        branch: { name: "feat/42-test" },
        base_branch: "main",
        requirements: { summary: "Test" },
        labels: [],
      };

      const result = IssueContextSchema.safeParse(objectBranch);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.branch).toBe("feat/42-test");
      }
    });

    it("should coerce technical_notes string to array", () => {
      const stringNotes = {
        schema_version: "1.0",
        issue_number: 42,
        title: "Test",
        type: "feature",
        branch: "feat/42-test",
        base_branch: "main",
        requirements: {
          summary: "Test",
          technical_notes: "Use the existing FileService",
        },
        labels: [],
      };

      const result = IssueContextSchema.safeParse(stringNotes);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.requirements.technical_notes).toEqual(["Use the existing FileService"]);
      }
    });
  });

  describe("PlanningContextSchema", () => {
    it("should validate a complete planning context", () => {
      const validPlanning = {
        schema_version: "1.0" as const,
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-user-photo-upload.md",
        approach: "pragmatic",
        files_to_create: ["src/services/PhotoService.ts", "tests/photo.test.ts"],
        files_to_modify: ["src/routes/users.ts"],
        patterns_applied: {
          architecture: "Service pattern from ARCHITECTURE.md",
          security: "Input validation per SECURITY.md",
        },
        coverage_baseline: {
          statements: 85.2,
          branches: 72.1,
          lines: 84.8,
        },
        created_at: "2026-02-01T12:30:00Z",
      };

      const result = PlanningContextSchema.safeParse(validPlanning);
      expect(result.success).toBe(true);
    });

    it("should accept minimal planning context", () => {
      const minimalPlanning = {
        schema_version: "1.0" as const,
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-test.md",
        approach: "simple",
        files_to_create: [],
        files_to_modify: [],
        created_at: "2026-02-01T12:30:00Z",
      };

      const result = PlanningContextSchema.safeParse(minimalPlanning);
      expect(result.success).toBe(true);
    });

    it("should accept schema_version 1.2 with revision fields", () => {
      const revisionPlanning = {
        schema_version: "1.2" as const,
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-test.md",
        approach: "revised-approach",
        files_to_create: [],
        files_to_modify: ["src/services/FooService.ts"],
        revision_count: 1,
        revision_reasons: [
          "FooService.findById() does not exist — use db.query.foo.findFirst()",
          "Scope underestimated: 5 files modified vs 2 planned",
        ],
        created_at: "2026-02-15T10:00:00Z",
      };

      const result = PlanningContextSchema.safeParse(revisionPlanning);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.revision_count).toBe(1);
        expect(result.data.revision_reasons).toHaveLength(2);
      }
    });

    it("should accept revision_count of 0 for first run", () => {
      const firstRunPlanning = {
        schema_version: "1.2" as const,
        issue_number: 99,
        plan_file: ".nightgauge/plans/99-first.md",
        approach: "standard",
        files_to_create: [],
        files_to_modify: [],
        revision_count: 0,
        revision_reasons: [],
        created_at: "2026-02-15T10:00:00Z",
      };

      const result = PlanningContextSchema.safeParse(firstRunPlanning);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.revision_count).toBe(0);
        expect(result.data.revision_reasons).toHaveLength(0);
      }
    });

    it("should accept omitted revision fields (backward compat with 1.0/1.1)", () => {
      const legacyPlanning = {
        schema_version: "1.1" as const,
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-test.md",
        approach: "simple",
        files_to_create: [],
        files_to_modify: [],
        created_at: "2026-02-01T12:30:00Z",
      };

      const result = PlanningContextSchema.safeParse(legacyPlanning);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.revision_count).toBeUndefined();
        expect(result.data.revision_reasons).toBeUndefined();
      }
    });

    it("should reject negative revision_count", () => {
      const invalidPlanning = {
        schema_version: "1.2" as const,
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-test.md",
        approach: "simple",
        files_to_create: [],
        files_to_modify: [],
        revision_count: -1,
        created_at: "2026-02-01T12:30:00Z",
      };

      const result = PlanningContextSchema.safeParse(invalidPlanning);
      expect(result.success).toBe(false);
    });
  });

  describe("DevContextSchema", () => {
    it("should validate a complete v1.0 dev context", () => {
      const validDev = {
        schema_version: "1.0" as const,
        issue_number: 42,
        commit_sha: "abc123def456",
        files_changed: {
          created: ["src/services/PhotoService.ts"],
          modified: ["src/routes/users.ts"],
          deleted: [],
        },
        tests_status: {
          passed: 15,
          failed: 0,
          coverage: 87.5,
        },
        quality_checks: {
          code_standards: "passed" as const,
          security_review: "passed" as const,
        },
        created_at: "2026-02-01T13:00:00Z",
      };

      const result = DevContextSchema.safeParse(validDev);
      expect(result.success).toBe(true);
    });

    it("should validate a v1.1 dev context with all new fields", () => {
      const v11Dev = {
        schema_version: "1.1" as const,
        issue_number: 42,
        commit_sha: "abc123def456",
        files_changed: {
          created: ["src/services/PhotoService.ts"],
          modified: ["src/routes/users.ts"],
          deleted: [],
        },
        build_verification: {
          ran: true,
          status: "passed" as const,
          commands_run: ["npm run build"],
          timestamp: "2026-02-01T13:00:00Z",
        },
        tests_status: {
          passed: 15,
          failed: 0,
          coverage: 87.5,
          test_command: "npx vitest run",
          includes_integration: false,
          includes_e2e: false,
          test_files_run: 5,
        },
        quality_checks: {
          code_standards: "passed" as const,
          security_review: "passed" as const,
          type_check: "passed" as const,
          dead_code_scan: "not_run" as const,
        },
        created_at: "2026-02-01T13:00:00Z",
      };

      const result = DevContextSchema.safeParse(v11Dev);
      expect(result.success).toBe(true);
    });

    it("should validate a v1.0 dev context without new optional fields (backward compat)", () => {
      const v10Dev = {
        schema_version: "1.0" as const,
        issue_number: 99,
        commit_sha: "def789",
        files_changed: {
          created: [],
          modified: ["src/index.ts"],
          deleted: [],
        },
        tests_status: {
          passed: 5,
          failed: 0,
        },
        quality_checks: {
          code_standards: "passed" as const,
          security_review: "skipped" as const,
        },
        created_at: "2026-02-10T10:00:00Z",
      };

      const result = DevContextSchema.safeParse(v10Dev);
      expect(result.success).toBe(true);
    });

    it("should validate dev context with null commit_sha (Issue #1608 — commit deferred to validate)", () => {
      const devWithNullCommit = {
        schema_version: "1.4" as const,
        issue_number: 1608,
        commit_sha: null,
        files_changed: {
          created: ["src/services/NewService.ts"],
          modified: [],
          deleted: [],
        },
        tests_status: {
          passed: 10,
          failed: 0,
        },
        quality_checks: {
          code_standards: "passed" as const,
          security_review: "passed" as const,
        },
        created_at: "2026-03-05T12:00:00Z",
      };

      const result = DevContextSchema.safeParse(devWithNullCommit);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.commit_sha).toBeNull();
      }
    });

    it("should validate dev context with undefined commit_sha (Issue #1608)", () => {
      const devWithoutCommit = {
        schema_version: "1.4" as const,
        issue_number: 1608,
        files_changed: {
          created: [],
          modified: ["src/index.ts"],
          deleted: [],
        },
        tests_status: { passed: 5, failed: 0 },
        quality_checks: {
          code_standards: "passed" as const,
          security_review: "passed" as const,
        },
        created_at: "2026-03-05T12:00:00Z",
      };

      const result = DevContextSchema.safeParse(devWithoutCommit);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.commit_sha).toBeUndefined();
      }
    });

    it("should reject invalid test status", () => {
      const invalidDev = {
        schema_version: "1.0",
        issue_number: 42,
        commit_sha: "abc123",
        files_changed: { created: [], modified: [], deleted: [] },
        tests_status: {
          passed: -1, // Invalid: negative number
          failed: 0,
        },
        quality_checks: {
          code_standards: "passed",
          security_review: "passed",
        },
        created_at: "2026-02-01T13:00:00Z",
      };

      const result = DevContextSchema.safeParse(invalidDev);
      expect(result.success).toBe(false);
    });

    it("should reject invalid build_verification status", () => {
      const invalidBuild = {
        schema_version: "1.1",
        issue_number: 42,
        commit_sha: "abc123",
        files_changed: { created: [], modified: [], deleted: [] },
        build_verification: {
          ran: true,
          status: "unknown", // Invalid: not in enum
          commands_run: [],
        },
        tests_status: { passed: 0, failed: 0 },
        quality_checks: {
          code_standards: "passed",
          security_review: "passed",
        },
        created_at: "2026-02-01T13:00:00Z",
      };

      const result = DevContextSchema.safeParse(invalidBuild);
      expect(result.success).toBe(false);
    });

    it("should reject invalid dead_code_scan value", () => {
      const invalidDeadCode = {
        schema_version: "1.1",
        issue_number: 42,
        commit_sha: "abc123",
        files_changed: { created: [], modified: [], deleted: [] },
        tests_status: { passed: 0, failed: 0 },
        quality_checks: {
          code_standards: "passed",
          security_review: "passed",
          dead_code_scan: "unknown", // Invalid: not in enum
        },
        created_at: "2026-02-01T13:00:00Z",
      };

      const result = DevContextSchema.safeParse(invalidDeadCode);
      expect(result.success).toBe(false);
    });
  });

  describe("PRContextSchema", () => {
    it("should validate a complete PR context", () => {
      const validPR = {
        schema_version: "1.0" as const,
        issue_number: 42,
        pr_number: 87,
        pr_url: "https://github.com/org/repo/pull/87",
        title: "[FEAT][#42] Add user photo upload",
        base_branch: "main",
        status: "open" as const,
        reviewers: ["@teammate"],
        preflight_results: {
          json_validation: "passed" as const,
          yaml_validation: "passed" as const,
          version_consistency: "passed" as const,
          security_scan: "passed" as const,
          coverage_check: "passed" as const,
        },
        created_at: "2026-02-01T13:30:00Z",
      };

      const result = PRContextSchema.safeParse(validPR);
      expect(result.success).toBe(true);
    });

    it("should reject invalid PR URL", () => {
      const invalidPR = {
        schema_version: "1.0",
        issue_number: 42,
        pr_number: 87,
        pr_url: "not-a-valid-url",
        title: "Test PR",
        base_branch: "main",
        status: "open",
        reviewers: [],
        preflight_results: {
          json_validation: "passed",
          yaml_validation: "passed",
          version_consistency: "passed",
          security_scan: "passed",
          coverage_check: "passed",
        },
        created_at: "2026-02-01T13:30:00Z",
      };

      const result = PRContextSchema.safeParse(invalidPR);
      expect(result.success).toBe(false);
    });
  });

  describe("ValidateContextSchema", () => {
    it("should validate a v1.0 validate context", () => {
      const validValidate = {
        schema_version: "1.0" as const,
        issue_number: 42,
        validation_status: "passed" as const,
        integration_tests: {
          ran: true,
          passed: true,
          framework: "vitest",
          tests_run: 10,
          tests_passed: 10,
        },
        e2e_tests: {
          ran: false,
          passed: false,
          reason: "not configured",
        },
        manual_checklist: [
          { item: "Feature works end-to-end", verified: true },
          { item: "No regressions observed", verified: true },
        ],
        project_type: "node-library",
        notes: null,
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(validValidate);
      expect(result.success).toBe(true);
    });

    it("should validate a v1.2 validate context with build and dead_code_warnings", () => {
      const v12Validate = {
        schema_version: "1.2" as const,
        issue_number: 42,
        validation_status: "failed" as const,
        build: {
          ran: true,
          passed: true,
          command: "npm run build",
        },
        integration_tests: {
          ran: true,
          passed: true,
          framework: "vitest",
          tests_run: 10,
          tests_passed: 10,
        },
        e2e_tests: {
          ran: false,
          passed: false,
          reason: "not configured",
        },
        dead_code_warnings: [
          {
            type: "unused-export",
            name: "unusedHelper",
            location: "src/utils.ts:42",
            severity: "error" as const,
          },
          {
            type: "unregistered-command",
            name: "oldCommand",
            location: "src/commands.ts:10",
            severity: "warning" as const,
          },
        ],
        unit_tests: {
          ran: true,
          passed: true,
          framework: "vitest",
          tests_run: 25,
          tests_passed: 25,
        },
        manual_checklist: [{ item: "Feature works end-to-end", verified: true }],
        project_type: "vscode-extension",
        notes: null,
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(v12Validate);
      expect(result.success).toBe(true);
    });

    it("should accept v1.1 schema version", () => {
      const v11Validate = {
        schema_version: "1.1" as const,
        issue_number: 42,
        validation_status: "passed" as const,
        build: {
          ran: true,
          passed: true,
          command: null,
        },
        manual_checklist: [],
        project_type: "generic",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(v11Validate);
      expect(result.success).toBe(true);
    });

    it("should coerce unit_tests.passed from number to boolean", () => {
      const numericPassed = {
        schema_version: "1.2",
        issue_number: 42,
        validation_status: "passed",
        unit_tests: {
          ran: 1,
          passed: 25,
          framework: "vitest",
          tests_run: 25,
          tests_passed: 25,
        },
        manual_checklist: [],
        project_type: "generic",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(numericPassed);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.unit_tests?.ran).toBe(true);
        expect(result.data.unit_tests?.passed).toBe(true);
      }
    });

    it("should coerce manual_checklist from record to array", () => {
      const recordChecklist = {
        schema_version: "1.2",
        issue_number: 42,
        validation_status: "passed",
        manual_checklist: {
          "Feature works end-to-end": true,
          "No regressions observed": false,
        },
        project_type: "generic",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(recordChecklist);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.manual_checklist).toEqual([
          { item: "Feature works end-to-end", verified: true },
          { item: "No regressions observed", verified: false },
        ]);
      }
    });

    it("should coerce manual_checklist from string array", () => {
      const stringChecklist = {
        schema_version: "1.2",
        issue_number: 42,
        validation_status: "passed",
        manual_checklist: ["Check A", "Check B"],
        project_type: "generic",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(stringChecklist);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.manual_checklist).toEqual([
          { item: "Check A", verified: false },
          { item: "Check B", verified: false },
        ]);
      }
    });

    it("should validate v1.9 validate context with commit_sha (Issue #1608)", () => {
      const v19Validate = {
        schema_version: "1.9" as const,
        issue_number: 1608,
        validation_status: "passed" as const,
        build: {
          ran: true,
          passed: true,
          command: "npm run build",
        },
        unit_tests: {
          ran: true,
          passed: true,
          framework: "vitest",
          tests_run: 25,
          tests_passed: 25,
        },
        commit_sha: "abc123def456789",
        manual_checklist: [],
        project_type: "vscode-extension",
        created_at: "2026-03-05T13:00:00Z",
      };

      const result = ValidateContextSchema.safeParse(v19Validate);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.commit_sha).toBe("abc123def456789");
      }
    });

    it("should validate validate context with null commit_sha (validation failed — Issue #1608)", () => {
      const failedValidate = {
        schema_version: "1.9" as const,
        issue_number: 1608,
        validation_status: "failed" as const,
        commit_sha: null,
        manual_checklist: [],
        project_type: "generic",
        created_at: "2026-03-05T13:00:00Z",
      };

      const result = ValidateContextSchema.safeParse(failedValidate);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.commit_sha).toBeNull();
      }
    });

    it("should validate validate context without commit_sha (backward compat pre-1.9)", () => {
      const legacyValidate = {
        schema_version: "1.8" as const,
        issue_number: 42,
        validation_status: "passed" as const,
        manual_checklist: [],
        project_type: "generic",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(legacyValidate);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.commit_sha).toBeUndefined();
      }
    });

    it("should reject invalid validation status", () => {
      const invalidValidate = {
        schema_version: "1.0",
        issue_number: 42,
        validation_status: "unknown-status",
        manual_checklist: [],
        project_type: "node-library",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(invalidValidate);
      expect(result.success).toBe(false);
    });

    it("should reject invalid dead_code_warnings severity", () => {
      const invalidSeverity = {
        schema_version: "1.2",
        issue_number: 42,
        validation_status: "passed",
        dead_code_warnings: [
          {
            type: "unused-export",
            name: "foo",
            location: "src/bar.ts:1",
            severity: "critical",
          },
        ],
        manual_checklist: [],
        project_type: "generic",
        created_at: "2026-02-10T13:30:00Z",
      };

      const result = ValidateContextSchema.safeParse(invalidSeverity);
      expect(result.success).toBe(false);
    });

    it("should validate a v1.4 validate context with skipped_phases", () => {
      const v14Validate = {
        schema_version: "1.4" as const,
        issue_number: 861,
        validation_status: "passed" as const,
        build: {
          ran: false,
          passed: true,
          command: null,
        },
        integration_tests: {
          ran: false,
          passed: false,
        },
        e2e_tests: {
          ran: false,
          passed: false,
          reason: "not configured",
        },
        skipped_phases: [
          {
            phase: "build_verification",
            reason: "build verified by feature-dev (dev context build_verification.status=passed)",
          },
          {
            phase: "unit_tests",
            reason: "dev context shows all unit tests passed (passed=5030, failed=0)",
          },
          {
            phase: "baseline_comparison",
            reason: "dev context shows all tests passed (passed=5030, failed=0)",
          },
          {
            phase: "manual_checklist",
            reason: "auto-passed: 5030 unit tests passed with 0 failures in dev",
          },
        ],
        manual_checklist: [],
        project_type: "vscode-extension",
        notes: null,
        created_at: "2026-02-16T12:00:00Z",
      };

      const result = ValidateContextSchema.safeParse(v14Validate);
      expect(result.success).toBe(true);
      if (result.success) {
        expect(result.data.skipped_phases).toHaveLength(4);
        expect(result.data.skipped_phases![0].phase).toBe("build_verification");
      }
    });

    it("should reject skipped_phases with empty phase name", () => {
      const invalidSkipped = {
        schema_version: "1.4",
        issue_number: 42,
        validation_status: "passed",
        skipped_phases: [{ phase: "", reason: "test reason" }],
        manual_checklist: [],
        project_type: "generic",
        created_at: "2026-02-16T12:00:00Z",
      };

      const result = ValidateContextSchema.safeParse(invalidSkipped);
      expect(result.success).toBe(false);
    });
  });

  describe("SCHEMA_VERSION", () => {
    it("should be 1.0", () => {
      expect(SCHEMA_VERSION).toBe("1.0");
    });
  });

  describe("FeedbackContextSchema", () => {
    it("should validate a complete blocking signal with backtrack_target_stage", () => {
      const signal = {
        signal_type: "PLAN_REVISION_NEEDED",
        emitted_by_stage: "feature-dev",
        backtrack_target_stage: "feature-planning",
        rationale: "Discovered hidden dependency on external API",
        evidence: ["src/service.ts requires OAuth token", "no token provisioned in plan"],
        severity: "blocking",
        timestamp: "2026-02-26T10:00:00Z",
      };
      const result = FeedbackContextSchema.safeParse({
        schema_version: "1.0",
        issue_number: 1341,
        signals: [signal],
        created_at: "2026-02-26T10:00:00Z",
      });
      expect(result.success).toBe(true);
    });

    it("should validate MODEL_ESCALATION_NEEDED signal without backtrack_target_stage", () => {
      const signal = {
        signal_type: "MODEL_ESCALATION_NEEDED",
        emitted_by_stage: "feature-dev",
        backtrack_target_stage: null,
        rationale: "Task complexity exceeds current model capability",
        evidence: ["Multiple interdependent refactors required"],
        severity: "blocking",
      };
      const result = FeedbackContextSchema.safeParse({
        schema_version: "1.0",
        issue_number: 1341,
        signals: [signal],
      });
      expect(result.success).toBe(true);
    });

    it("should validate a warning signal", () => {
      const signal = {
        signal_type: "SCOPE_DISCOVERED",
        emitted_by_stage: "feature-validate",
        rationale: "Found additional test files not in original plan",
        evidence: ["tests/extra.test.ts was not in files_to_create"],
        severity: "warning",
      };
      const result = FeedbackContextSchema.safeParse({
        schema_version: "1.0",
        issue_number: 99,
        signals: [signal],
        created_at: "2026-02-26T12:00:00Z",
      });
      expect(result.success).toBe(true);
    });

    it("should validate FeedbackContext with empty signals array", () => {
      const result = FeedbackContextSchema.safeParse({
        schema_version: "1.0",
        issue_number: 42,
        signals: [],
      });
      expect(result.success).toBe(true);
    });

    it("should reject an unknown signal_type", () => {
      const result = FeedbackContextSchema.safeParse({
        schema_version: "1.0",
        issue_number: 42,
        signals: [
          {
            signal_type: "UNKNOWN_SIGNAL",
            emitted_by_stage: "feature-dev",
            rationale: "test",
            evidence: [],
            severity: "warning",
          },
        ],
      });
      expect(result.success).toBe(false);
    });

    it("should reject an unknown severity value", () => {
      const result = FeedbackContextSchema.safeParse({
        schema_version: "1.0",
        issue_number: 42,
        signals: [
          {
            signal_type: "SCOPE_DISCOVERED",
            emitted_by_stage: "feature-dev",
            rationale: "test",
            evidence: [],
            severity: "critical", // invalid
          },
        ],
      });
      expect(result.success).toBe(false);
    });

    it("should validate DevContext v1.2 with feedback field", () => {
      const result = DevContextSchema.safeParse({
        schema_version: "1.2",
        issue_number: 1341,
        commit_sha: "abc123",
        files_changed: { created: [], modified: [], deleted: [] },
        tests_status: { passed: 10, failed: 0 },
        quality_checks: { code_standards: "passed", security_review: "passed" },
        feedback: [
          {
            signal_type: "COMPLEXITY_UNDERESTIMATED",
            emitted_by_stage: "feature-dev",
            backtrack_target_stage: "feature-planning",
            rationale: "Schema touches more files than planned",
            evidence: ["6 files modified vs 3 estimated"],
            severity: "warning",
          },
        ],
        created_at: "2026-02-26T10:00:00Z",
      });
      expect(result.success).toBe(true);
    });

    it("should validate ValidateContext v1.6 with feedback field", () => {
      const result = ValidateContextSchema.safeParse({
        schema_version: "1.6",
        issue_number: 1341,
        validation_status: "passed",
        feedback: [
          {
            signal_type: "ACCEPTANCE_CRITERIA_AMBIGUOUS",
            emitted_by_stage: "feature-validate",
            rationale: "AC #3 does not specify expected error message format",
            evidence: ['AC: "error is shown" — no message text specified'],
            severity: "warning",
          },
        ],
        manual_checklist: [],
        project_type: "node-library",
        created_at: "2026-02-26T10:00:00Z",
      });
      expect(result.success).toBe(true);
    });

    it("should validate DevContext v1.1 without feedback field (backward compat)", () => {
      const result = DevContextSchema.safeParse({
        schema_version: "1.1",
        issue_number: 42,
        commit_sha: "def456",
        files_changed: { created: [], modified: ["src/index.ts"], deleted: [] },
        build_verification: {
          ran: true,
          status: "passed",
          commands_run: ["npm run build"],
          timestamp: "2026-02-01T13:00:00Z",
        },
        tests_status: { passed: 5, failed: 0 },
        quality_checks: { code_standards: "passed", security_review: "passed" },
        created_at: "2026-02-01T13:00:00Z",
      });
      expect(result.success).toBe(true);
    });

    it("should validate ValidateContext v1.5 without feedback field (backward compat)", () => {
      const result = ValidateContextSchema.safeParse({
        schema_version: "1.5",
        issue_number: 42,
        validation_status: "passed",
        ac_completion_check: {
          status: "passed",
          checked_count: 3,
          unchecked_count: 0,
          applicable: true,
        },
        manual_checklist: [],
        project_type: "node-library",
        created_at: "2026-02-01T13:00:00Z",
      });
      expect(result.success).toBe(true);
    });
  });
});
