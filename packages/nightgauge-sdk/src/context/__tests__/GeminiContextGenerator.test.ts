/**
 * Tests for GeminiContextGenerator
 *
 * @see Issue #1055 - Add GEMINI.md context file generation
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import {
  GeminiContextGenerator,
  type GeminiContextOptions,
  type GeminiContextConfig,
} from "../GeminiContextGenerator.js";

describe("GeminiContextGenerator", () => {
  let generator: GeminiContextGenerator;
  let tmpDir: string;

  beforeEach(() => {
    generator = new GeminiContextGenerator();
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "gemini-ctx-"));
  });

  afterEach(() => {
    // Clean up GEMINI.md if it was created
    const geminiPath = path.join(tmpDir, "GEMINI.md");
    try {
      fs.unlinkSync(geminiPath);
    } catch {
      // ignore
    }
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  const baseOptions: GeminiContextOptions = {
    projectRoot: "", // Will be set to tmpDir in each test
    stage: "feature-dev",
    issueNumber: 42,
    adapter: "gemini",
  };

  function opts(overrides?: Partial<GeminiContextOptions>): GeminiContextOptions {
    return { ...baseOptions, projectRoot: tmpDir, ...overrides };
  }

  describe("assembleContent", () => {
    it("produces correct markdown structure with required sections", () => {
      const content = generator.assembleContent(opts());

      expect(content).toContain("# Project Context for Gemini CLI");
      expect(content).toContain("## Current Task");
      expect(content).toContain("Stage: feature-dev");
      expect(content).toContain("Issue: #42");
      expect(content).toContain("## Key Rules");
      expect(content).toContain("Never push directly to main");
      expect(content).toContain("Never hardcode secrets");
    });

    it("includes issue title when provided", () => {
      const content = generator.assembleContent(opts({ issueTitle: "Add photo upload" }));

      expect(content).toContain("Issue: #42 - Add photo upload");
    });

    it("includes acceptance criteria when provided", () => {
      const content = generator.assembleContent(
        opts({
          acceptanceCriteria: ["Must support JPEG", "Max file size 5MB"],
        })
      );

      expect(content).toContain("### Acceptance Criteria");
      expect(content).toContain("- Must support JPEG");
      expect(content).toContain("- Max file size 5MB");
    });

    it("omits acceptance criteria section when not provided", () => {
      const content = generator.assembleContent(opts());

      expect(content).not.toContain("### Acceptance Criteria");
    });

    it("includes project description from CLAUDE.md when available", () => {
      fs.writeFileSync(path.join(tmpDir, "CLAUDE.md"), "# My Project\n\nThis is a test project.\n");

      const content = generator.assembleContent(opts());

      expect(content).toContain("## Project");
      expect(content).toContain("My Project");
    });

    it("falls back to AGENTS.md when CLAUDE.md is missing", () => {
      fs.writeFileSync(
        path.join(tmpDir, "AGENTS.md"),
        "# Agent Config\n\nAgent instructions here.\n"
      );

      const content = generator.assembleContent(opts());

      expect(content).toContain("## Project");
      expect(content).toContain("Agent Config");
    });

    it("does not pull a Codex managed block out of AGENTS.md into Gemini context (#4028)", () => {
      // A stale Codex managed block must never contaminate Gemini's steering —
      // the shared reader strips it before extracting the project description.
      fs.writeFileSync(
        path.join(tmpDir, "AGENTS.md"),
        "# Agent Config\n\nAgent instructions here.\n\n" +
          "<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->\n" +
          "# Nightgauge Pipeline Steering (Codex)\nCODEX-ONLY CONTENT\n" +
          "<!-- END NIGHTGAUGE MANAGED STEERING -->\n"
      );

      const content = generator.assembleContent(opts());

      expect(content).toContain("Agent Config");
      expect(content).not.toContain("NIGHTGAUGE MANAGED STEERING");
      expect(content).not.toContain("CODEX-ONLY CONTENT");
    });

    it("includes coding standards from standards/ when available", () => {
      fs.mkdirSync(path.join(tmpDir, "standards"), { recursive: true });
      fs.writeFileSync(
        path.join(tmpDir, "standards", "code-standards.md"),
        "# Code Standards\n\nUse TypeScript.\n"
      );

      const content = generator.assembleContent(opts());

      expect(content).toContain("## Coding Standards");
      expect(content).toContain("Code Standards");
    });

    it("falls back to docs/CODE_STANDARDS.md for standards", () => {
      fs.mkdirSync(path.join(tmpDir, "docs"), { recursive: true });
      fs.writeFileSync(
        path.join(tmpDir, "docs", "CODE_STANDARDS.md"),
        "# Code Standards\n\nUse TypeScript strict mode.\n"
      );

      const content = generator.assembleContent(opts());

      expect(content).toContain("## Coding Standards");
    });

    it("includes security rules when available", () => {
      fs.mkdirSync(path.join(tmpDir, "standards"), { recursive: true });
      fs.writeFileSync(
        path.join(tmpDir, "standards", "security.md"),
        "# Security\n\nNever log secrets.\n"
      );

      const content = generator.assembleContent(opts());

      expect(content).toContain("## Security");
    });

    it("includes git workflow when available", () => {
      fs.mkdirSync(path.join(tmpDir, "docs"), { recursive: true });
      fs.writeFileSync(
        path.join(tmpDir, "docs", "GIT_WORKFLOW.md"),
        "# Git Workflow\n\nUse feature branches.\n"
      );

      const content = generator.assembleContent(opts());

      expect(content).toContain("## Git Workflow");
    });

    it("gracefully handles missing standards files", () => {
      // No standards files exist in tmpDir
      const content = generator.assembleContent(opts());

      // Should still produce valid content without standards sections
      expect(content).toContain("# Project Context for Gemini CLI");
      expect(content).toContain("## Current Task");
      expect(content).not.toContain("## Coding Standards");
      expect(content).not.toContain("## Security");
      expect(content).not.toContain("## Git Workflow");
    });

    it("respects include_standards=false config", () => {
      fs.mkdirSync(path.join(tmpDir, "standards"), { recursive: true });
      fs.writeFileSync(
        path.join(tmpDir, "standards", "code-standards.md"),
        "# Standards\n\nContent here.\n"
      );

      const config: GeminiContextConfig = { include_standards: false };
      const content = generator.assembleContent(opts(), config);

      expect(content).not.toContain("## Coding Standards");
    });

    it("respects include_git_workflow=false config", () => {
      fs.mkdirSync(path.join(tmpDir, "docs"), { recursive: true });
      fs.writeFileSync(path.join(tmpDir, "docs", "GIT_WORKFLOW.md"), "# Git\n\nWorkflow here.\n");

      const config: GeminiContextConfig = { include_git_workflow: false };
      const content = generator.assembleContent(opts(), config);

      expect(content).not.toContain("## Git Workflow");
    });

    it("includes custom sections from config", () => {
      const config: GeminiContextConfig = {
        custom_sections: [
          { heading: "API Guidelines", content: "Use REST conventions." },
          { heading: "Deployment", content: "Deploy via CI/CD only." },
        ],
      };
      const content = generator.assembleContent(opts(), config);

      expect(content).toContain("## API Guidelines");
      expect(content).toContain("Use REST conventions.");
      expect(content).toContain("## Deployment");
      expect(content).toContain("Deploy via CI/CD only.");
    });
  });

  describe("generate (async)", () => {
    it("writes GEMINI.md for gemini adapter", async () => {
      const result = await generator.generate(opts());

      expect(result).toBe(path.join(tmpDir, "GEMINI.md"));
      expect(fs.existsSync(result!)).toBe(true);

      const content = fs.readFileSync(result!, "utf-8");
      expect(content).toContain("# Project Context for Gemini CLI");
    });

    it("writes GEMINI.md for gemini-sdk adapter", async () => {
      const result = await generator.generate(opts({ adapter: "gemini-sdk" }));

      expect(result).toBe(path.join(tmpDir, "GEMINI.md"));
      expect(fs.existsSync(result!)).toBe(true);
    });

    it("returns null for non-Gemini adapters", async () => {
      const result = await generator.generate(opts({ adapter: "claude" }));
      expect(result).toBeNull();

      // Verify no file was created
      expect(fs.existsSync(path.join(tmpDir, "GEMINI.md"))).toBe(false);
    });

    it("returns null for codex adapter", async () => {
      const result = await generator.generate(opts({ adapter: "codex" }));
      expect(result).toBeNull();
    });

    it("returns null when config.enabled is false", async () => {
      const result = await generator.generate(opts(), { enabled: false });
      expect(result).toBeNull();
    });
  });

  describe("generateSync", () => {
    it("writes GEMINI.md synchronously for gemini adapter", () => {
      const result = generator.generateSync(opts());

      expect(result).toBe(path.join(tmpDir, "GEMINI.md"));
      expect(fs.existsSync(result!)).toBe(true);
    });

    it("returns null for non-Gemini adapters", () => {
      const result = generator.generateSync(opts({ adapter: "claude" }));
      expect(result).toBeNull();
    });

    it("returns null when config.enabled is false", () => {
      const result = generator.generateSync(opts(), { enabled: false });
      expect(result).toBeNull();
    });
  });

  describe("cleanup", () => {
    it("removes GEMINI.md when it exists", async () => {
      // Create the file first
      const filePath = path.join(tmpDir, "GEMINI.md");
      fs.writeFileSync(filePath, "test content");
      expect(fs.existsSync(filePath)).toBe(true);

      await generator.cleanup(tmpDir);

      expect(fs.existsSync(filePath)).toBe(false);
    });

    it("does not throw when GEMINI.md does not exist", async () => {
      // Should not throw for ENOENT
      await expect(generator.cleanup(tmpDir)).resolves.toBeUndefined();
    });
  });
});
