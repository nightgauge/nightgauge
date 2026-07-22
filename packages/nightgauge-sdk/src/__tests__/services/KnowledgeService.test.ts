import { describe, expect, it, vi, beforeEach } from "vitest";
import * as path from "node:path";

// Mock node:fs/promises at module level
vi.mock("node:fs/promises");

// Mock js-yaml — use actual implementation for realistic behavior
// (We don't mock js-yaml; it's a pure function library)

import * as fs from "node:fs/promises";
import { KnowledgeService } from "../../services/KnowledgeService.js";

const WORKSPACE = "/workspace";

// Helper: build YAML frontmatter string
function frontmatter(obj: Record<string, unknown>): string {
  // Simple YAML serializer for test fixtures
  const lines: string[] = [];
  for (const [key, value] of Object.entries(obj)) {
    if (Array.isArray(value)) {
      lines.push(`${key}:`);
      for (const item of value) {
        lines.push(`  - ${typeof item === "string" ? `"${item}"` : item}`);
      }
    } else if (typeof value === "string") {
      lines.push(`${key}: "${value}"`);
    } else {
      lines.push(`${key}: ${value}`);
    }
  }
  return `---\n${lines.join("\n")}\n---`;
}

const VALID_ENTRY = {
  title: "Test Entry",
  type: "prd",
  created: "2026-01-01T00:00:00.000Z",
  updated: "2026-01-01T00:00:00.000Z",
};

const VALID_FRONTMATTER_CONTENT = `${frontmatter(VALID_ENTRY)}\n# Test Body\n\nSome content here.`;

const NO_FRONTMATTER_CONTENT = `# Just a regular markdown file\n\nNo frontmatter here.`;

describe("KnowledgeService", () => {
  let service: KnowledgeService;

  beforeEach(() => {
    vi.resetAllMocks();
    service = new KnowledgeService(WORKSPACE);

    // Default: fs.mkdir succeeds
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    // Default: fs.writeFile succeeds
    vi.mocked(fs.writeFile).mockResolvedValue(undefined);
  });

  // ──────────────────────────────────────────────────────────────
  // generateSlug
  // ──────────────────────────────────────────────────────────────

  describe("generateSlug", () => {
    it("converts title to kebab-case", () => {
      expect(service.generateSlug("Hello World")).toBe("hello-world");
    });

    it("strips special characters", () => {
      expect(service.generateSlug("Fix: broken link!")).toBe("fix-broken-link");
    });

    it("truncates to 50 characters", () => {
      const long =
        "This is a very long title that should be truncated to exactly fifty characters or less";
      const slug = service.generateSlug(long);
      expect(slug.length).toBeLessThanOrEqual(50);
    });

    it("strips leading and trailing hyphens", () => {
      expect(service.generateSlug("---test---")).toBe("test");
    });

    it("strips trailing hyphen left by 50-char truncation", () => {
      // 50-char slice lands on a hyphen — must be trimmed so dir names
      // do not end with a dangling '-'. Mirrors internal/knowledge/knowledge.go.
      const title = "Extension Migrate auth and platform services from typescript";
      const slug = service.generateSlug(title);
      expect(slug.endsWith("-")).toBe(false);
      expect(slug.length).toBeLessThanOrEqual(50);
    });

    it("handles empty string", () => {
      expect(service.generateSlug("")).toBe("");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // generatePRD
  // ──────────────────────────────────────────────────────────────

  describe("generatePRD", () => {
    it("generates PRD with extracted sections", () => {
      const body = `## Summary\nA brief summary.\n\n## Acceptance Criteria\n- [ ] Done\n\n## Technical Notes\nUse TypeScript.`;
      const prd = service.generatePRD(42, "Test Issue", body);
      expect(prd).toContain("# PRD: #42 — Test Issue");
      expect(prd).toContain("A brief summary.");
      expect(prd).toContain("- [ ] Done");
      expect(prd).toContain("Use TypeScript.");
    });

    it("seeds the full PRD structure with TRD and QRD sections", () => {
      const prd = service.generatePRD(7, "Structure", "");
      for (const heading of [
        "## Summary",
        "## User Story",
        "## Acceptance Criteria",
        "## Technical Approach",
        "## Quality & Non-Functional Requirements",
        "## Out of Scope",
        "## Status",
      ]) {
        expect(prd).toContain(heading);
      }
    });

    it("extracts User Story and Out of Scope from the issue body", () => {
      const body = `## User Story\nAs a dev, I want X so that Y.\n\n## Out of Scope\nNo realtime updates.`;
      const prd = service.generatePRD(8, "Extracts", body);
      expect(prd).toContain("As a dev, I want X so that Y.");
      expect(prd).toContain("No realtime updates.");
    });

    it("maps legacy '## Technical Notes' into the '## Technical Approach' section", () => {
      const body = `## Technical Approach\nDesign-first approach here.`;
      const prd = service.generatePRD(9, "Approach", body);
      expect(prd).toContain("## Technical Approach");
      expect(prd).toContain("Design-first approach here.");

      // Backward compatibility: an issue using the old heading still populates
      // Technical Approach, never leaving the TRD content stranded.
      const legacy = service.generatePRD(10, "Legacy", `## Technical Notes\nLegacy notes body.`);
      expect(legacy).toContain("## Technical Approach");
      expect(legacy).toContain("Legacy notes body.");
    });

    it("generates PRD with placeholders when sections are absent", () => {
      const prd = service.generatePRD(1, "Empty", "");
      expect(prd).toContain("<!-- TODO:");
      expect(prd).toContain("## Status");
      expect(prd).toContain("embedded TRD");
      expect(prd).toContain("embedded QRD");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // generateDecisionsTemplate
  // ──────────────────────────────────────────────────────────────

  describe("generateDecisionsTemplate", () => {
    it("generates expected template", () => {
      const result = service.generateDecisionsTemplate(42, "Test");
      expect(result).toContain("# Decisions: #42 — Test");
      expect(result).toContain("## ADR-001:");
      expect(result).toContain("**Status**: Proposed");
      expect(result).toContain("**Context**:");
      expect(result).toContain("**Decision**:");
      expect(result).toContain("**Consequences**:");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // contentIsSubstantive
  // ──────────────────────────────────────────────────────────────

  describe("contentIsSubstantive", () => {
    it("returns false for a boilerplate PRD with only TODO placeholders", () => {
      const boilerplate = `# PRD: #42 — Test

## Summary

<!-- TODO: Add a 1-2 sentence summary of the problem this issue solves -->

## Acceptance Criteria

<!-- TODO: Add acceptance criteria as checkboxes
- [ ] Criterion 1
- [ ] Criterion 2 -->

## Technical Notes

<!-- TODO: Add technical notes, file references, and implementation constraints -->

## Status

- [ ] Draft
- [ ] Reviewed
- [ ] Approved
`;
      expect(KnowledgeService.contentIsSubstantive(boilerplate)).toBe(false);
    });

    it("returns false for an empty decisions template", () => {
      const emptyDecisions = `# Decisions: #42 — Test

## Architecture Decisions

<!-- Record key architectural decisions made during implementation.
     Add one ADR block per decision. -->

## ADR-001: [Decision Title]

**Status**: Proposed
**Context**: [Background and constraints that led to this decision]
**Decision**: [What was decided and why]
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`;
      expect(KnowledgeService.contentIsSubstantive(emptyDecisions)).toBe(false);
    });

    it("returns true for a PRD with real extracted content", () => {
      const realPrd = `# PRD: #42 — Add widget support

## Summary

Implement widget rendering in the dashboard view using the existing component framework.

## Acceptance Criteria

- [ ] Widgets can be created via the API
- [ ] Widgets render in the dashboard grid
`;
      expect(KnowledgeService.contentIsSubstantive(realPrd)).toBe(true);
    });

    it("returns true for decisions with actual ADR entries", () => {
      const realDecisions = `# Decisions: #42 — Test

## Decision: Use adapter pattern for LM Studio integration

**Status**: Accepted **Date**: 2026-01-15

**Context**: LM Studio exposes an OpenAI-compatible REST API

**Decision**: Use the API-backed adapter pattern from GeminiSdkAdapter
`;
      expect(KnowledgeService.contentIsSubstantive(realDecisions)).toBe(true);
    });
  });

  // ──────────────────────────────────────────────────────────────
  // scaffoldForIssue
  // ──────────────────────────────────────────────────────────────

  describe("scaffoldForIssue", () => {
    // Issue body with extractable sections — scaffolding proceeds
    const RICH_BODY =
      "## Summary\nImplement the feature for adding widgets\n\n## Acceptance Criteria\n- [ ] Widgets can be created\n- [ ] Widgets render correctly";

    it("creates directories and files for a feature issue", async () => {
      // Files don't exist yet
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await service.scaffoldForIssue(42, "Add feature", RICH_BODY, false, {
        enabled: true,
      });

      expect(result.skipped).toBe(false);
      expect(result.substantive).toBe(true);
      expect(result.knowledge_path).toContain("features/42-add-feature");
      expect(result.files_created).toContain("PRD.md");
      expect(result.files_created).toContain("decisions.md");
      expect(fs.mkdir).toHaveBeenCalled();
      expect(fs.writeFile).toHaveBeenCalledTimes(2);
    });

    it("skips when knowledge.enabled is false", async () => {
      const result = await service.scaffoldForIssue(1, "T", "", false, {
        enabled: false,
      });
      expect(result.skipped).toBe(true);
      expect(result.skip_reason).toContain("enabled");
    });

    it("skips when auto_scaffold is false", async () => {
      const result = await service.scaffoldForIssue(1, "T", "", false, {
        enabled: true,
        auto_scaffold: false,
      });
      expect(result.skipped).toBe(true);
      expect(result.skip_reason).toContain("auto_scaffold");
    });

    it("is idempotent — does not overwrite existing files", async () => {
      // Both files exist
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const result = await service.scaffoldForIssue(42, "Test", RICH_BODY, false, {
        enabled: true,
      });

      expect(result.files_created).toEqual([]);
      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("uses epics/ category for epic issues", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await service.scaffoldForIssue(10, "Epic", RICH_BODY, true, { enabled: true });

      expect(result.knowledge_path).toContain("epics/10-epic");
    });

    it("defers scaffolding when issue body has no extractable content", async () => {
      const result = await service.scaffoldForIssue(99, "Short issue", "no sections here", false, {
        enabled: true,
      });

      expect(result.skipped).toBe(true);
      expect(result.substantive).toBe(false);
      expect(result.skip_reason).toContain("no extractable content");
      expect(fs.mkdir).not.toHaveBeenCalled();
    });

    it("scaffolds when body has enough non-section content (>50 chars)", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const longBody =
        "This is a detailed issue description without standard markdown sections but it has plenty of content to be useful for knowledge extraction and planning.";
      const result = await service.scaffoldForIssue(100, "Detailed issue", longBody, false, {
        enabled: true,
      });

      expect(result.skipped).toBe(false);
      expect(result.substantive).toBe(false); // no standard sections extracted
      expect(result.knowledge_path).toContain("features/100-");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // create
  // ──────────────────────────────────────────────────────────────

  describe("create", () => {
    it("creates a file with YAML frontmatter and body", async () => {
      // File does not exist
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await service.create("prd", "features/42-test", "# My Content", {
        title: "Test PRD",
      });

      expect(result).toContain(".nightgauge/knowledge/features/42-test/PRD.md");
      expect(fs.mkdir).toHaveBeenCalled();
      expect(fs.writeFile).toHaveBeenCalledTimes(1);

      // Verify written content has frontmatter
      const writtenContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(writtenContent).toContain("---");
      expect(writtenContent).toContain("title:");
      expect(writtenContent).toContain("# My Content");
    });

    it("throws when file already exists", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);

      await expect(
        service.create("prd", "features/42-test", "content", {
          title: "T",
        })
      ).rejects.toThrow("File already exists");
    });

    it("creates directories recursively", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      await service.create("note", "features/99-deep/nested", "body", {
        title: "N",
      });

      expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining("features/99-deep/nested"), {
        recursive: true,
      });
    });

    it("uses correct filename for each type", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      await service.create("decision", "features/1-d", "", { title: "D" });
      const writePath = vi.mocked(fs.writeFile).mock.calls[0][0] as string;
      expect(writePath).toContain("decisions.md");
    });

    it("defaults created/updated timestamps", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      await service.create("note", "features/1-t", "", { title: "T" });

      const writtenContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(writtenContent).toContain("created:");
      expect(writtenContent).toContain("updated:");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // read
  // ──────────────────────────────────────────────────────────────

  describe("read", () => {
    it("parses YAML frontmatter and body", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(VALID_FRONTMATTER_CONTENT);

      const result = await service.read("/workspace/test.md");

      expect(result.entry).not.toBeNull();
      expect(result.entry!.title).toBe("Test Entry");
      expect(result.entry!.type).toBe("prd");
      expect(result.body).toContain("# Test Body");
    });

    it("returns entry: null for files without frontmatter", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(NO_FRONTMATTER_CONTENT);

      const result = await service.read("/workspace/test.md");

      expect(result.entry).toBeNull();
      expect(result.body).toContain("# Just a regular markdown file");
    });

    it("throws on missing file", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      await expect(service.read("/workspace/missing.md")).rejects.toThrow("File not found");
    });

    it("resolves relative paths against workspaceRoot", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(NO_FRONTMATTER_CONTENT);

      await service.read(".nightgauge/knowledge/test.md");

      expect(fs.readFile).toHaveBeenCalledWith(
        path.join(WORKSPACE, ".nightgauge/knowledge/test.md"),
        "utf-8"
      );
    });

    it("handles malformed YAML frontmatter gracefully", async () => {
      const malformed = `---\n: invalid yaml [[[}\n---\nBody content`;
      vi.mocked(fs.readFile).mockResolvedValue(malformed);

      const result = await service.read("/workspace/test.md");

      expect(result.entry).toBeNull();
      expect(result.body).toBe("Body content");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // update
  // ──────────────────────────────────────────────────────────────

  describe("update", () => {
    it("merges frontmatter and bumps updated date", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(VALID_FRONTMATTER_CONTENT);

      await service.update("/workspace/test.md", "# Updated Body", {
        tags: ["new-tag"],
      });

      expect(fs.writeFile).toHaveBeenCalledTimes(1);
      const written = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(written).toContain("tags:");
      expect(written).toContain("new-tag");
      expect(written).toContain("# Updated Body");
    });

    it("preserves original created date", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(VALID_FRONTMATTER_CONTENT);

      await service.update("/workspace/test.md", "body", {});

      const written = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(written).toContain("2026-01-01T00:00:00.000Z");
    });

    it("injects frontmatter for files without existing frontmatter", async () => {
      vi.mocked(fs.readFile).mockResolvedValue(NO_FRONTMATTER_CONTENT);

      await service.update("/workspace/test.md", "New body", {
        title: "Added",
        type: "note",
      });

      const written = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(written).toContain("---");
      expect(written).toContain("title:");
      expect(written).toContain("New body");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // list
  // ──────────────────────────────────────────────────────────────

  describe("list", () => {
    function setupWalkMock(files: { name: string; content: string }[]) {
      // Mock fs.access to indicate knowledge root exists
      vi.mocked(fs.access).mockResolvedValue(undefined);

      // walkDirectory uses readdir (plain strings) + stat
      const readdirMock = vi.mocked(fs.readdir);
      let readdirCallIndex = 0;

      readdirMock.mockImplementation((_dirPath: any) => {
        readdirCallIndex++;
        if (readdirCallIndex === 1) {
          // knowledge root: one category directory
          return Promise.resolve(["features"] as any);
        }
        if (readdirCallIndex === 2) {
          // features dir: file names
          return Promise.resolve(files.map((f) => f.name) as any);
        }
        return Promise.resolve([] as any);
      });

      // Mock stat for directory vs file detection
      vi.mocked(fs.stat).mockImplementation((filePath: any) => {
        const name = path.basename(filePath as string);
        if (name === "features") {
          return Promise.resolve({
            isDirectory: () => true,
            isFile: () => false,
          } as any);
        }
        // All other entries are files
        return Promise.resolve({
          isDirectory: () => false,
          isFile: () => true,
        } as any);
      });

      // Mock readFile for each file
      vi.mocked(fs.readFile).mockImplementation((filePath: any) => {
        const name = path.basename(filePath as string);
        const file = files.find((f) => f.name === name);
        if (file) return Promise.resolve(file.content);
        return Promise.reject(new Error("ENOENT"));
      });
    }

    it("returns all entries when no filter provided", async () => {
      setupWalkMock([
        { name: "PRD.md", content: VALID_FRONTMATTER_CONTENT },
        { name: "note.md", content: NO_FRONTMATTER_CONTENT },
      ]);

      const results = await service.list();

      expect(results).toHaveLength(2);
    });

    it("filters by type", async () => {
      const noteEntry = {
        title: "A Note",
        type: "note",
        created: "2026-02-01T00:00:00.000Z",
        updated: "2026-02-01T00:00:00.000Z",
      };
      setupWalkMock([
        { name: "PRD.md", content: VALID_FRONTMATTER_CONTENT },
        {
          name: "note.md",
          content: `${frontmatter(noteEntry)}\nNote body`,
        },
      ]);

      const results = await service.list({ type: "note" });

      expect(results).toHaveLength(1);
      expect(results[0].entry?.type).toBe("note");
    });

    it("filters by tags", async () => {
      const taggedEntry = {
        ...VALID_ENTRY,
        tags: ["sdk", "api"],
      };
      setupWalkMock([
        {
          name: "PRD.md",
          content: `${frontmatter(taggedEntry)}\nBody`,
        },
      ]);

      const results = await service.list({ tags: ["sdk"] });

      expect(results).toHaveLength(1);
    });

    it("filters by related_issues", async () => {
      const relatedEntry = {
        ...VALID_ENTRY,
        related_issues: [42, 100],
      };
      setupWalkMock([
        {
          name: "PRD.md",
          content: `${frontmatter(relatedEntry)}\nBody`,
        },
      ]);

      const results = await service.list({ related_issues: [42] });

      expect(results).toHaveLength(1);
    });

    it("excludes files without frontmatter when filter is provided", async () => {
      setupWalkMock([{ name: "raw.md", content: NO_FRONTMATTER_CONTENT }]);

      const results = await service.list({ type: "prd" });

      expect(results).toHaveLength(0);
    });

    it("returns empty array when knowledge directory does not exist", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const results = await service.list();

      expect(results).toEqual([]);
    });
  });

  // ──────────────────────────────────────────────────────────────
  // search
  // ──────────────────────────────────────────────────────────────

  describe("search", () => {
    function setupSearchMock(files: { name: string; content: string }[]) {
      vi.mocked(fs.access).mockResolvedValue(undefined);

      const readdirMock = vi.mocked(fs.readdir);
      let readdirCallIndex = 0;

      readdirMock.mockImplementation((_dirPath: any) => {
        readdirCallIndex++;
        if (readdirCallIndex === 1) {
          return Promise.resolve(["features"] as any);
        }
        if (readdirCallIndex === 2) {
          return Promise.resolve(files.map((f) => f.name) as any);
        }
        return Promise.resolve([] as any);
      });

      vi.mocked(fs.stat).mockImplementation((filePath: any) => {
        const name = path.basename(filePath as string);
        if (name === "features") {
          return Promise.resolve({
            isDirectory: () => true,
            isFile: () => false,
          } as any);
        }
        return Promise.resolve({
          isDirectory: () => false,
          isFile: () => true,
        } as any);
      });

      vi.mocked(fs.readFile).mockImplementation((filePath: any) => {
        const name = path.basename(filePath as string);
        const file = files.find((f) => f.name === name);
        if (file) return Promise.resolve(file.content);
        return Promise.reject(new Error("ENOENT"));
      });
    }

    it("returns empty array for empty query", async () => {
      const results = await service.search("");
      expect(results).toEqual([]);
    });

    it("returns empty array for whitespace-only query", async () => {
      const results = await service.search("   ");
      expect(results).toEqual([]);
    });

    it("finds case-insensitive substring matches", async () => {
      setupSearchMock([
        {
          name: "test.md",
          content: `${frontmatter(VALID_ENTRY)}\n# Important Feature\n\nThis is a test document.`,
        },
      ]);

      const results = await service.search("important");

      expect(results).toHaveLength(1);
      expect(results[0].excerpt).toContain("Important Feature");
    });

    it("returns empty when no match found", async () => {
      setupSearchMock([{ name: "test.md", content: "No match here" }]);

      const results = await service.search("nonexistent");

      expect(results).toEqual([]);
    });

    it("truncates excerpts to 200 chars", async () => {
      const longLine = "A".repeat(300);
      setupSearchMock([{ name: "test.md", content: longLine }]);

      const results = await service.search("AAAA");

      expect(results).toHaveLength(1);
      expect(results[0].excerpt.length).toBeLessThanOrEqual(200);
    });
  });

  // ──────────────────────────────────────────────────────────────
  // generateIndex
  // ──────────────────────────────────────────────────────────────

  describe("generateIndex", () => {
    it("builds correct index structure", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          // Knowledge root: categories
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          // features/: issue dirs
          return Promise.resolve([
            {
              name: "42-photo-upload",
              isDirectory: () => true,
              isFile: () => false,
            },
          ] as any);
        }
        if (callIndex === 3) {
          // 42-photo-upload/: files
          return Promise.resolve(["PRD.md", "decisions.md"]);
        }
        return Promise.resolve([]) as any;
      });

      vi.mocked(fs.readFile).mockResolvedValue(NO_FRONTMATTER_CONTENT);
      vi.mocked(fs.stat).mockResolvedValue({ mtime: new Date("2026-04-01T00:00:00Z") } as any);

      const index = await service.generateIndex();

      expect(index.total_entries).toBe(1);
      expect(index.categories["features"]).toHaveLength(1);
      expect(index.categories["features"][0].issue_number).toBe(42);
      expect(index.categories["features"][0].slug).toBe("photo-upload");
      expect(index.categories["features"][0].files).toContain("PRD.md");
      expect(fs.writeFile).toHaveBeenCalled(); // README.md written

      // Verify README content
      const readmeContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(readmeContent).toContain("Knowledge Base Index");
      expect(readmeContent).toContain("#42");
    });

    it("returns empty index when knowledge root does not exist", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(new Error("ENOENT"));

      const index = await service.generateIndex();

      expect(index.total_entries).toBe(0);
      expect(index.categories).toEqual({});
    });

    it("skips directories not matching issue pattern", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            {
              name: "not-a-valid-dir",
              isDirectory: () => true,
              isFile: () => false,
            },
          ] as any);
        }
        return Promise.resolve([]) as any;
      });

      const index = await service.generateIndex();

      expect(index.total_entries).toBe(0);
    });

    it("includes entry metadata from files with frontmatter", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            { name: "10-test", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 3) {
          return Promise.resolve(["PRD.md"]);
        }
        return Promise.resolve([]) as any;
      });

      vi.mocked(fs.readFile).mockResolvedValue(VALID_FRONTMATTER_CONTENT);
      vi.mocked(fs.stat).mockResolvedValue({ mtime: new Date("2026-01-01T00:00:00Z") } as any);

      const index = await service.generateIndex();

      expect(index.categories["features"][0].entry).toBeDefined();
      expect(index.categories["features"][0].entry!.title).toBe("Test Entry");
    });

    it("README includes type, title from PRD H1, and last-modified columns", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            { name: "42-photo-upload", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 3) {
          return Promise.resolve(["PRD.md", "decisions.md"]);
        }
        return Promise.resolve([]) as any;
      });

      vi.mocked(fs.readFile).mockResolvedValue("# My Feature Title\n\nSome content.");
      vi.mocked(fs.stat).mockResolvedValue({ mtime: new Date("2026-04-15T12:00:00Z") } as any);

      await service.generateIndex();

      const readmeContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(readmeContent).toContain("| Issue | Type | Title | Last Modified |");
      expect(readmeContent).toContain("feature");
      expect(readmeContent).toContain("My Feature Title");
      expect(readmeContent).toContain("2026-04-15");
    });

    it("README uses slug-derived title when PRD.md has no H1", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            { name: "7-my-feature", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 3) {
          return Promise.resolve(["PRD.md"]);
        }
        return Promise.resolve([]) as any;
      });

      vi.mocked(fs.readFile).mockResolvedValue("No heading here, just body text.");
      vi.mocked(fs.stat).mockResolvedValue({ mtime: new Date("2026-01-01T00:00:00Z") } as any);

      await service.generateIndex();

      const readmeContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(readmeContent).toContain("my feature");
    });

    it("README uses 'epic' type for epics category", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "epics", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            { name: "100-big-epic", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 3) {
          return Promise.resolve(["PRD.md"]);
        }
        return Promise.resolve([]) as any;
      });

      vi.mocked(fs.readFile).mockResolvedValue("# Epic Title\n\nBody.");
      vi.mocked(fs.stat).mockResolvedValue({ mtime: new Date("2026-03-01T00:00:00Z") } as any);

      await service.generateIndex();

      const readmeContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(readmeContent).toContain("epic");
      expect(readmeContent).toContain("Epic Title");
    });
  });

  // ──────────────────────────────────────────────────────────────
  // pruneEmpty
  // ──────────────────────────────────────────────────────────────

  describe("pruneEmpty", () => {
    it("preserves directories with substantive content", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            { name: "42-real-work", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        return Promise.resolve(["PRD.md"] as any);
      });

      vi.mocked(fs.readFile).mockResolvedValue(
        "# PRD\n\nThis feature implements real work with detailed requirements and design decisions." as any
      );

      const pruned = await service.pruneEmpty();

      expect(pruned).toHaveLength(0);
      expect(fs.rm).not.toHaveBeenCalled();
    });

    it("removes directories whose .md files are all boilerplate", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        if (callIndex === 2) {
          return Promise.resolve([
            { name: "99-empty-feature", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        return Promise.resolve(["PRD.md"] as any);
      });

      const boilerplate =
        "# PRD\n\n<!-- TODO: fill in -->\n\n- [ ] Add content\n\n| Key | Value |\n| --- | ----- |";
      vi.mocked(fs.readFile).mockResolvedValue(boilerplate as any);
      vi.mocked(fs.rm).mockResolvedValue(undefined);

      const pruned = await service.pruneEmpty();

      expect(pruned).toHaveLength(1);
      expect(pruned[0]).toContain("99-empty-feature");
      expect(fs.rm).toHaveBeenCalledOnce();
    });

    it("does not touch category dirs with no issue subdirectories", async () => {
      const readdirMock = vi.mocked(fs.readdir);
      let callIndex = 0;

      readdirMock.mockImplementation((_dirPath: any, _options?: any) => {
        callIndex++;
        if (callIndex === 1) {
          return Promise.resolve([
            { name: "features", isDirectory: () => true, isFile: () => false },
          ] as any);
        }
        return Promise.resolve([] as any);
      });

      const pruned = await service.pruneEmpty();

      expect(pruned).toHaveLength(0);
      expect(fs.rm).not.toHaveBeenCalled();
    });

    it("returns empty array when knowledge root does not exist", async () => {
      vi.mocked(fs.readdir).mockRejectedValue(
        Object.assign(new Error("ENOENT"), { code: "ENOENT" })
      );

      const pruned = await service.pruneEmpty();

      expect(pruned).toHaveLength(0);
    });
  });

  // ──────────────────────────────────────────────────────────────
  // regenerateForIssue
  // ──────────────────────────────────────────────────────────────

  describe("regenerateForIssue", () => {
    const KNOWLEDGE_PATH = ".nightgauge/knowledge/features/42-some-feature";
    const PRD_PATH = `${WORKSPACE}/${KNOWLEDGE_PATH}/PRD.md`;

    const ISSUE_BODY_WITH_SECTIONS = `## Summary
This issue adds auto-regeneration.

## Acceptance Criteria
- [ ] regenerateForIssue updates PRD.md
- [ ] decisions.md is preserved

## Technical Notes
Uses the existing update() method internally.`;

    beforeEach(() => {
      vi.mocked(fs.writeFile).mockResolvedValue(undefined);
    });

    it("updates PRD.md with fresh content from issue body", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined); // PRD exists
      vi.mocked(fs.readFile).mockResolvedValue(
        `---\ntitle: "Old PRD"\ntype: "prd"\ncreated: "2026-01-01T00:00:00.000Z"\nupdated: "2026-01-01T00:00:00.000Z"\n---\n# Old content` as any
      );

      const result = await service.regenerateForIssue(
        42,
        "Some Feature",
        ISSUE_BODY_WITH_SECTIONS,
        KNOWLEDGE_PATH
      );

      expect(result.regenerated).toBe(true);
      expect(result.prdUpdated).toBe(true);
      expect(result.decisionsPreserved).toBe(true);
      expect(result.filesUpdated).toHaveLength(1);
      expect(result.filesUpdated[0]).toContain("PRD.md");
      expect(fs.writeFile).toHaveBeenCalledWith(
        PRD_PATH,
        expect.stringContaining("auto-regeneration"),
        "utf-8"
      );
    });

    it("includes extracted acceptance criteria in updated PRD", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(
        `---\ntitle: "T"\ntype: "prd"\ncreated: "2026-01-01T00:00:00.000Z"\nupdated: "2026-01-01T00:00:00.000Z"\n---\n# old` as any
      );

      await service.regenerateForIssue(
        42,
        "Some Feature",
        ISSUE_BODY_WITH_SECTIONS,
        KNOWLEDGE_PATH
      );

      const written = vi.mocked(fs.writeFile).mock.calls[0]?.[1] as string;
      expect(written).toContain("regenerateForIssue updates PRD.md");
    });

    it("regenerates the full PRD structure shared with generatePRD", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(
        `---\ntitle: "T"\ntype: "prd"\ncreated: "2026-01-01T00:00:00.000Z"\nupdated: "2026-01-01T00:00:00.000Z"\n---\n# old` as any
      );

      await service.regenerateForIssue(
        42,
        "Some Feature",
        ISSUE_BODY_WITH_SECTIONS,
        KNOWLEDGE_PATH
      );

      const written = vi.mocked(fs.writeFile).mock.calls[0]?.[1] as string;
      for (const heading of [
        "## Summary",
        "## User Story",
        "## Acceptance Criteria",
        "## Technical Approach",
        "## Quality & Non-Functional Requirements",
        "## Out of Scope",
        "## Status",
      ]) {
        expect(written).toContain(heading);
      }
      // Legacy "## Technical Notes" from the issue body lands under Technical Approach.
      expect(written).toContain("Uses the existing update() method internally.");
    });

    it("uses TODO placeholders when issue body has no sections", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(
        `---\ntitle: "T"\ntype: "prd"\ncreated: "2026-01-01T00:00:00.000Z"\nupdated: "2026-01-01T00:00:00.000Z"\n---\n# old` as any
      );

      await service.regenerateForIssue(
        42,
        "Some Feature",
        "No structured sections here.",
        KNOWLEDGE_PATH
      );

      const written = vi.mocked(fs.writeFile).mock.calls[0]?.[1] as string;
      expect(written).toContain("TODO:");
    });

    it("preserves existing frontmatter created date", async () => {
      const CREATED = "2025-06-15T10:00:00.000Z";
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(
        `---\ntitle: "T"\ntype: "prd"\ncreated: "${CREATED}"\nupdated: "2026-01-01T00:00:00.000Z"\n---\n# old` as any
      );

      await service.regenerateForIssue(
        42,
        "Some Feature",
        ISSUE_BODY_WITH_SECTIONS,
        KNOWLEDGE_PATH
      );

      const written = vi.mocked(fs.writeFile).mock.calls[0]?.[1] as string;
      expect(written).toContain(CREATED);
    });

    it("returns regenerated=false when PRD.md does not exist", async () => {
      vi.mocked(fs.access).mockRejectedValue(new Error("ENOENT"));

      const result = await service.regenerateForIssue(
        42,
        "Some Feature",
        ISSUE_BODY_WITH_SECTIONS,
        KNOWLEDGE_PATH
      );

      expect(result.regenerated).toBe(false);
      expect(result.prdUpdated).toBe(false);
      expect(result.reason).toContain("not found");
      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("returns a valid ISO timestamp", async () => {
      vi.mocked(fs.access).mockResolvedValue(undefined);
      vi.mocked(fs.readFile).mockResolvedValue(
        `---\ntitle: "T"\ntype: "prd"\ncreated: "2026-01-01T00:00:00.000Z"\nupdated: "2026-01-01T00:00:00.000Z"\n---\n# old` as any
      );

      const result = await service.regenerateForIssue(
        42,
        "Some Feature",
        ISSUE_BODY_WITH_SECTIONS,
        KNOWLEDGE_PATH
      );

      expect(result.timestamp).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/);
    });
  });
});
