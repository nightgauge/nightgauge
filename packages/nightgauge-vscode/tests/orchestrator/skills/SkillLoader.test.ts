/**
 * SkillLoader unit tests
 *
 * Verifies that the SkillLoader:
 * - Delegates path resolution to findSkillFile()
 * - Reads skill file content correctly
 * - Returns null and logs a warning when file is not found
 * - Returns null and logs a warning on file read errors
 *
 * @see Issue #2770 — HeadlessOrchestrator decomposition Part 3
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Logger } from "../../../src/utils/logger";

// ---------------------------------------------------------------------------
// Module mocks
// ---------------------------------------------------------------------------

vi.mock("fs");
vi.mock("../../../src/utils/skillRunner", () => ({
  findSkillFile: vi.fn(),
}));

import * as fs from "fs";
import { findSkillFile } from "../../../src/utils/skillRunner";
import { SkillLoader } from "../../../src/orchestrator/skills/SkillLoader";

const mockFindSkillFile = vi.mocked(findSkillFile);
const mockReadFileSync = vi.mocked(fs.readFileSync);

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ---------------------------------------------------------------------------
// resolveLocalSkillPath
// ---------------------------------------------------------------------------

describe("SkillLoader.resolveLocalSkillPath", () => {
  it("delegates to findSkillFile and returns the path", () => {
    mockFindSkillFile.mockReturnValue("/skills/feature-dev/SKILL.md");
    const loader = new SkillLoader(makeLogger());

    const result = loader.resolveLocalSkillPath("feature-dev");

    expect(mockFindSkillFile).toHaveBeenCalledWith("feature-dev");
    expect(result).toBe("/skills/feature-dev/SKILL.md");
  });

  it("returns null when findSkillFile returns null", () => {
    mockFindSkillFile.mockReturnValue(null);
    const loader = new SkillLoader(makeLogger());

    expect(loader.resolveLocalSkillPath("feature-dev")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// loadSkillContent
// ---------------------------------------------------------------------------

describe("SkillLoader.loadSkillContent", () => {
  it("returns skill content and path when file exists", () => {
    mockFindSkillFile.mockReturnValue("/skills/feature-dev/SKILL.md");
    mockReadFileSync.mockReturnValue("# Skill content" as any);
    const loader = new SkillLoader(makeLogger());

    const result = loader.loadSkillContent("feature-dev");

    expect(result).toEqual({
      content: "# Skill content",
      path: "/skills/feature-dev/SKILL.md",
      source: "local",
    });
  });

  it("returns null and logs warning when skill file not found", () => {
    mockFindSkillFile.mockReturnValue(null);
    const logger = makeLogger();
    const loader = new SkillLoader(logger);

    const result = loader.loadSkillContent("feature-dev");

    expect(result).toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("skill file not found"),
      expect.objectContaining({ stage: "feature-dev" })
    );
  });

  it("returns null and logs warning when readFileSync throws", () => {
    mockFindSkillFile.mockReturnValue("/skills/feature-dev/SKILL.md");
    mockReadFileSync.mockImplementation(() => {
      throw new Error("ENOENT: no such file");
    });
    const logger = makeLogger();
    const loader = new SkillLoader(logger);

    const result = loader.loadSkillContent("feature-dev");

    expect(result).toBeNull();
    expect(logger.warn).toHaveBeenCalledWith(
      expect.stringContaining("failed to read skill file"),
      expect.objectContaining({
        stage: "feature-dev",
        skillPath: "/skills/feature-dev/SKILL.md",
        err: "ENOENT: no such file",
      })
    );
  });

  it("reads the file as utf-8", () => {
    mockFindSkillFile.mockReturnValue("/skills/feature-dev/SKILL.md");
    mockReadFileSync.mockReturnValue("content" as any);
    const loader = new SkillLoader(makeLogger());

    loader.loadSkillContent("feature-dev");

    expect(mockReadFileSync).toHaveBeenCalledWith("/skills/feature-dev/SKILL.md", "utf-8");
  });

  it("works for all pipeline stages", () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
      "pr-merge",
    ] as const;
    for (const stage of stages) {
      mockFindSkillFile.mockReturnValue(`/skills/${stage}/SKILL.md`);
      mockReadFileSync.mockReturnValue(`# ${stage} skill` as any);
      const loader = new SkillLoader(makeLogger());

      const result = loader.loadSkillContent(stage);

      expect(result?.source).toBe("local");
      expect(result?.path).toBe(`/skills/${stage}/SKILL.md`);
    }
  });
});
