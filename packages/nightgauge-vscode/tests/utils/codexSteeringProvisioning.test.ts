import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { CodexContextGenerator, CODEX_MANAGED_BEGIN } from "@nightgauge/sdk";

/**
 * Extension-boundary guard for Codex AGENTS.md steering (#4028).
 *
 * skillRunner.ts provisions AGENTS.md via `CodexContextGenerator.generateSync`
 * before a Codex spawn and strips the managed block via `cleanupSync` on process
 * close. This test asserts that contract holds through the published SDK barrel
 * (the same import the extension uses) — create preserves user content, cleanup
 * leaves no managed residue — without spawning a real Codex process.
 */
describe("Codex AGENTS.md steering at the extension boundary (#4028)", () => {
  let tmpDir: string;
  const agents = () => path.join(tmpDir, "AGENTS.md");

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "codex-steer-vscode-"));
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  it("provisions a managed block then strips it, preserving a user AGENTS.md", () => {
    const userContent = "# Team Agents\n\nFollow our house style.\n";
    fs.writeFileSync(agents(), userContent);

    const gen = new CodexContextGenerator();
    const written = gen.generateSync({
      projectRoot: tmpDir,
      stage: "feature-dev",
      issueNumber: 7,
      adapter: "codex",
    });
    expect(written).toBe(agents());
    const provisioned = fs.readFileSync(agents(), "utf-8");
    expect(provisioned).toContain(CODEX_MANAGED_BEGIN);
    expect(provisioned).toContain("Follow our house style.");

    gen.cleanupSync(tmpDir);
    const afterCleanup = fs.readFileSync(agents(), "utf-8");
    expect(afterCleanup).toContain("Follow our house style.");
    expect(afterCleanup).not.toContain(CODEX_MANAGED_BEGIN);
  });

  it("is a no-op for non-Codex adapters", () => {
    const gen = new CodexContextGenerator();
    expect(
      gen.generateSync({
        projectRoot: tmpDir,
        stage: "feature-dev",
        issueNumber: 7,
        adapter: "gemini",
      })
    ).toBeNull();
    expect(fs.existsSync(agents())).toBe(false);
  });
});
