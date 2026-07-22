/**
 * Tests for CodexContextGenerator (AGENTS.md provider-aware steering).
 *
 * @see Issue #4028 - Provider-aware system steering (Codex AGENTS.md)
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import {
  CodexContextGenerator,
  upsertManagedBlock,
  stripManagedBlock,
  CODEX_MANAGED_BEGIN,
  CODEX_MANAGED_END,
  type CodexContextOptions,
} from "../CodexContextGenerator.js";

describe("CodexContextGenerator (#4028)", () => {
  let generator: CodexContextGenerator;
  let tmpDir: string;

  beforeEach(() => {
    generator = new CodexContextGenerator();
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "codex-ctx-"));
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  const baseOptions: CodexContextOptions = {
    projectRoot: "",
    stage: "feature-dev",
    issueNumber: 42,
    adapter: "codex",
  };

  function opts(overrides?: Partial<CodexContextOptions>): CodexContextOptions {
    return { ...baseOptions, projectRoot: tmpDir, ...overrides };
  }

  const agentsPath = () => path.join(tmpDir, "AGENTS.md");
  const read = () => fs.readFileSync(agentsPath(), "utf-8");

  describe("adapter guard", () => {
    it("returns null and writes nothing for a non-Codex adapter", async () => {
      expect(await generator.generate(opts({ adapter: "gemini" }))).toBeNull();
      expect(generator.generateSync(opts({ adapter: "claude-sdk" }))).toBeNull();
      expect(fs.existsSync(agentsPath())).toBe(false);
    });

    it("returns null when disabled via config", async () => {
      expect(await generator.generate(opts(), { enabled: false })).toBeNull();
      expect(fs.existsSync(agentsPath())).toBe(false);
    });
  });

  describe("assembleContent", () => {
    it("includes stable baseline steering (key rules), never per-issue task data", () => {
      const content = generator.assembleContent(opts());
      expect(content).toContain("# Nightgauge Pipeline Steering (Codex)");
      expect(content).toContain("## Key Rules");
      expect(content).toContain("Never push directly to main");
      expect(content).toContain("Never hardcode secrets");
      // Stable + commit-safe: no transient Stage/Issue/Acceptance data.
      expect(content).not.toContain("## Current Task");
      expect(content).not.toContain("Stage: feature-dev");
      expect(content).not.toContain("Issue: #42");
    });

    it("pulls standards / security / git-workflow from project files when present", () => {
      fs.mkdirSync(path.join(tmpDir, "standards"), { recursive: true });
      fs.writeFileSync(
        path.join(tmpDir, "standards", "code-standards.md"),
        "# Code Standards\nUse tabs.\n"
      );
      fs.writeFileSync(path.join(tmpDir, "standards", "security.md"), "# Security\nNo secrets.\n");
      fs.mkdirSync(path.join(tmpDir, "docs"), { recursive: true });
      fs.writeFileSync(path.join(tmpDir, "docs", "GIT_WORKFLOW.md"), "# Git\nUse branches.\n");

      const content = generator.assembleContent(opts());
      expect(content).toContain("## Coding Standards");
      expect(content).toContain("## Security");
      expect(content).toContain("## Git Workflow");
    });
  });

  describe("generate (managed block, non-destructive)", () => {
    it("creates AGENTS.md wrapped in the managed markers when none exists", async () => {
      const filePath = await generator.generate(opts());
      expect(filePath).toBe(agentsPath());
      const content = read();
      expect(content).toContain(CODEX_MANAGED_BEGIN);
      expect(content).toContain(CODEX_MANAGED_END);
      expect(content).toContain("## Key Rules");
    });

    it("preserves a user-authored AGENTS.md, appending the managed block below it", async () => {
      const userContent = "# My Project Agents\n\nUse my conventions.\n";
      fs.writeFileSync(agentsPath(), userContent);

      await generator.generate(opts());
      const content = read();
      expect(content).toContain("# My Project Agents");
      expect(content).toContain("Use my conventions.");
      expect(content.indexOf("# My Project Agents")).toBeLessThan(
        content.indexOf(CODEX_MANAGED_BEGIN)
      );
    });

    it("is idempotent — regenerating replaces the block, never duplicating it", async () => {
      await generator.generate(opts());
      await generator.generate(opts());
      const content = read();
      const begins = content.split(CODEX_MANAGED_BEGIN).length - 1;
      const ends = content.split(CODEX_MANAGED_END).length - 1;
      expect(begins).toBe(1);
      expect(ends).toBe(1);
    });
  });

  describe("cleanup (non-destructive)", () => {
    it("deletes AGENTS.md when it held only the generated block", async () => {
      await generator.generate(opts());
      expect(fs.existsSync(agentsPath())).toBe(true);
      await generator.cleanup(tmpDir);
      expect(fs.existsSync(agentsPath())).toBe(false);
    });

    it("strips only the managed block, preserving user content", async () => {
      const userContent = "# My Project Agents\n\nUse my conventions.\n";
      fs.writeFileSync(agentsPath(), userContent);
      await generator.generate(opts());
      await generator.cleanup(tmpDir);

      const content = read();
      expect(content).toContain("# My Project Agents");
      expect(content).toContain("Use my conventions.");
      expect(content).not.toContain(CODEX_MANAGED_BEGIN);
      expect(content).not.toContain("## Key Rules");
    });

    it("leaves a user AGENTS.md without our markers completely untouched", async () => {
      const userContent = "# Untouched\n\nNo managed block here.\n";
      fs.writeFileSync(agentsPath(), userContent);
      await generator.cleanup(tmpDir);
      expect(read()).toBe(userContent);
    });

    it("cleanupSync mirrors cleanup semantics", () => {
      const userContent = "# User\n\nkeep me\n";
      fs.writeFileSync(agentsPath(), userContent);
      generator.generateSync(opts());
      generator.cleanupSync(tmpDir);
      const content = read();
      expect(content).toContain("keep me");
      expect(content).not.toContain(CODEX_MANAGED_BEGIN);
    });

    // #4024 review #3: cleanup is reference-counted so a concurrent same-root
    // Codex stage doesn't get its steering block stripped out from under it.
    it("does NOT strip the block until the LAST concurrent provisioner releases", () => {
      // Two stages provision against the same project root.
      generator.generateSync(opts());
      new CodexContextGenerator().generateSync(opts());
      expect(read()).toContain(CODEX_MANAGED_BEGIN);

      // First stage finishes — block must remain for the second.
      generator.cleanupSync(tmpDir);
      expect(read()).toContain(CODEX_MANAGED_BEGIN);

      // Second stage finishes — now the block is stripped.
      new CodexContextGenerator().cleanupSync(tmpDir);
      expect(fs.existsSync(agentsPath())).toBe(false);
    });
  });

  describe("upsert/strip helpers", () => {
    it("upsert into null yields just the wrapped block", () => {
      const out = upsertManagedBlock(null, "BODY");
      expect(out).toBe(`${CODEX_MANAGED_BEGIN}\nBODY\n${CODEX_MANAGED_END}\n`);
    });

    it("strip is the inverse for a block-only file (round-trips to empty)", () => {
      const wrapped = upsertManagedBlock(null, "BODY");
      expect(stripManagedBlock(wrapped).trim()).toBe("");
    });

    it("upsert replaces an existing block in place, keeping surrounding content", () => {
      const first = upsertManagedBlock("# Top\n\ntext\n", "V1");
      const second = upsertManagedBlock(first, "V2");
      expect(second).toContain("# Top");
      expect(second).toContain("V2");
      expect(second).not.toContain("V1");
      expect(second.split(CODEX_MANAGED_BEGIN).length - 1).toBe(1);
    });
  });
});
