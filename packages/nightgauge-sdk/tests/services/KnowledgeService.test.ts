import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { KnowledgeService } from "../../src/services/KnowledgeService.js";

describe("KnowledgeService", () => {
  let tempDir: string;
  let service: KnowledgeService;

  beforeEach(async () => {
    tempDir = await fs.mkdtemp(path.join(os.tmpdir(), "knowledge-test-"));
    service = new KnowledgeService(tempDir);
  });

  afterEach(async () => {
    await fs.rm(tempDir, { recursive: true, force: true });
  });

  // -------------------------------------------------------------------------
  // scaffoldForIssue
  // -------------------------------------------------------------------------

  describe("scaffoldForIssue", () => {
    it("returns skipped=true when knowledge.enabled is false", async () => {
      const result = await service.scaffoldForIssue(42, "My feature", "issue body", false, {
        enabled: false,
        auto_scaffold: true,
      });

      expect(result.skipped).toBe(true);
      expect(result.skip_reason).toBe("knowledge.enabled is false");
      expect(result.files_created).toHaveLength(0);
      expect(result.knowledge_path).toBe("");
    });

    it("returns skipped=true when knowledge.enabled is undefined (falsy)", async () => {
      const result = await service.scaffoldForIssue(42, "My feature", "", false, {});

      expect(result.skipped).toBe(true);
      expect(result.skip_reason).toBe("knowledge.enabled is false");
    });

    it("returns skipped=true when knowledge.auto_scaffold is false", async () => {
      const result = await service.scaffoldForIssue(42, "My feature", "", false, {
        enabled: true,
        auto_scaffold: false,
      });

      expect(result.skipped).toBe(true);
      expect(result.skip_reason).toBe("knowledge.auto_scaffold is false");
    });

    // Issue body with extractable sections to pass the content quality check
    const RICH_BODY =
      "## Summary\nImplement the feature for adding widgets to the dashboard\n\n## Acceptance Criteria\n- [ ] Widgets can be created\n- [ ] Widgets render correctly in the grid";

    it("creates knowledge directory and returns correct path for feature issue", async () => {
      const result = await service.scaffoldForIssue(42, "My Cool Feature", RICH_BODY, false, {
        enabled: true,
        auto_scaffold: true,
      });

      expect(result.skipped).toBe(false);
      expect(result.substantive).toBe(true);
      expect(result.knowledge_path).toBe(".nightgauge/knowledge/features/42-my-cool-feature");
      expect(result.files_created).toContain("PRD.md");
      expect(result.files_created).toContain("decisions.md");

      const absPath = path.join(tempDir, result.knowledge_path);
      const stat = await fs.stat(absPath);
      expect(stat.isDirectory()).toBe(true);
    });

    it("uses epics/ path for epic issues", async () => {
      const result = await service.scaffoldForIssue(10, "Big Epic Issue", RICH_BODY, true, {
        enabled: true,
        auto_scaffold: true,
      });

      expect(result.skipped).toBe(false);
      expect(result.knowledge_path).toBe(".nightgauge/knowledge/epics/10-big-epic-issue");
    });

    it("uses features/ path for non-epic issues", async () => {
      const result = await service.scaffoldForIssue(99, "Small Bug Fix", RICH_BODY, false, {
        enabled: true,
        auto_scaffold: true,
      });

      expect(result.knowledge_path).toContain("/features/");
    });

    it("is idempotent — re-running when files exist returns same path without error", async () => {
      const config = { enabled: true, auto_scaffold: true };

      const first = await service.scaffoldForIssue(
        42,
        "Idempotent Feature",
        RICH_BODY,
        false,
        config
      );
      const second = await service.scaffoldForIssue(
        42,
        "Idempotent Feature",
        RICH_BODY,
        false,
        config
      );

      expect(first.knowledge_path).toBe(second.knowledge_path);
      expect(second.skipped).toBe(false);
      // On second run, files already exist — no files created
      expect(second.files_created).toHaveLength(0);
    });

    it("defers scaffolding when issue body has no extractable content", async () => {
      const result = await service.scaffoldForIssue(50, "Tiny fix", "short", false, {
        enabled: true,
        auto_scaffold: true,
      });

      expect(result.skipped).toBe(true);
      expect(result.substantive).toBe(false);
      expect(result.skip_reason).toContain("no extractable content");
    });

    it("writes PRD.md and decisions.md content", async () => {
      const result = await service.scaffoldForIssue(
        5,
        "Test Issue",
        "## Summary\nA quick summary.\n\n## Acceptance Criteria\n- [ ] AC1\n",
        false,
        { enabled: true, auto_scaffold: true }
      );

      const absPath = path.join(tempDir, result.knowledge_path);
      const prd = await fs.readFile(path.join(absPath, "PRD.md"), "utf-8");
      const decisions = await fs.readFile(path.join(absPath, "decisions.md"), "utf-8");

      expect(prd).toContain("# PRD: #5 — Test Issue");
      expect(prd).toContain("A quick summary.");
      expect(prd).toContain("- [ ] AC1");
      expect(decisions).toContain("# Decisions: #5 — Test Issue");
      expect(decisions).toContain("## ADR-001:");
    });
  });

  // -------------------------------------------------------------------------
  // generateSlug
  // -------------------------------------------------------------------------

  describe("generateSlug", () => {
    it("converts spaces to hyphens and lowercases", () => {
      expect(service.generateSlug("My Cool Feature")).toBe("my-cool-feature");
    });

    it("strips special characters", () => {
      expect(service.generateSlug("Fix: broken [API]!")).toBe("fix-broken-api");
    });

    it("collapses multiple consecutive non-alphanumeric chars into single hyphen", () => {
      expect(service.generateSlug("Hello  --  World")).toBe("hello-world");
    });

    it("strips leading and trailing hyphens", () => {
      expect(service.generateSlug("---title---")).toBe("title");
    });

    it("truncates to 50 characters", () => {
      const long = "a".repeat(60);
      expect(service.generateSlug(long)).toHaveLength(50);
    });

    it("handles unicode by replacing non-alphanumeric characters", () => {
      expect(service.generateSlug("Résumé upload")).toBe("r-sum-upload");
    });

    it("returns empty string for all-special input", () => {
      expect(service.generateSlug("---")).toBe("");
    });
  });

  // -------------------------------------------------------------------------
  // generatePRD
  // -------------------------------------------------------------------------

  describe("generatePRD", () => {
    const issueBody = `## Summary
This is the summary.

## Acceptance Criteria
- [ ] First criterion
- [ ] Second criterion

## Technical Notes
Some technical detail here.`;

    it("extracts Summary from issue body", () => {
      const prd = service.generatePRD(42, "My Feature", issueBody);
      expect(prd).toContain("This is the summary.");
    });

    it("extracts Acceptance Criteria from issue body", () => {
      const prd = service.generatePRD(42, "My Feature", issueBody);
      expect(prd).toContain("- [ ] First criterion");
      expect(prd).toContain("- [ ] Second criterion");
    });

    it("extracts Technical Notes from issue body", () => {
      const prd = service.generatePRD(42, "My Feature", issueBody);
      expect(prd).toContain("Some technical detail here.");
    });

    it("includes issue number and title in heading", () => {
      const prd = service.generatePRD(42, "My Feature", issueBody);
      expect(prd).toContain("# PRD: #42 — My Feature");
    });

    it("uses placeholder when Summary section is absent", () => {
      const prd = service.generatePRD(1, "No Sections", "No standard sections here.");
      expect(prd).toContain("<!-- TODO:");
    });

    it("uses placeholder when Acceptance Criteria section is absent", () => {
      const prd = service.generatePRD(1, "No AC", "");
      expect(prd).toContain("<!-- TODO:");
    });

    it("includes Status checklist", () => {
      const prd = service.generatePRD(1, "Title", "");
      expect(prd).toContain("- [ ] Draft");
      expect(prd).toContain("- [ ] Reviewed");
      expect(prd).toContain("- [ ] Approved");
    });
  });

  // -------------------------------------------------------------------------
  // generateDecisionsTemplate
  // -------------------------------------------------------------------------

  describe("generateDecisionsTemplate", () => {
    it("includes issue number and title in heading", () => {
      const result = service.generateDecisionsTemplate(42, "My Feature");
      expect(result).toContain("# Decisions: #42 — My Feature");
    });

    it("includes ADR block format", () => {
      const result = service.generateDecisionsTemplate(1, "Title");
      expect(result).toContain("## ADR-001:");
      expect(result).toContain("**Status**: Proposed");
      expect(result).toContain("**Context**:");
      expect(result).toContain("**Decision**:");
      expect(result).toContain("**Consequences**:");
    });
  });
});
