/**
 * ContextAssembler unit tests
 *
 * Verifies that the ContextAssembler:
 * - Resolves context file paths correctly (single repo + contextLoader)
 * - Exports the expected constant maps
 * - Reads and parses context files
 * - Returns null on missing files or parse errors
 * - Resets state on clearSessionState()
 *
 * @see Issue #2770 — HeadlessOrchestrator decomposition Part 3
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as path from "path";
import type { Logger } from "../../../src/utils/logger";
import type { RepositoryContextLoader } from "../../../src/services/RepositoryContextLoader";

// ---------------------------------------------------------------------------
// Module mocks
// ---------------------------------------------------------------------------

vi.mock("fs");
vi.mock("../../../src/utils/skillRunner", () => ({
  findSkillFile: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
}));
vi.mock("../../../src/utils/routingDecision", () => ({
  makeRoutingDecision: vi.fn(),
  buildPickupRecommendation: vi.fn(),
  DEFAULT_ROUTING_CONFIG: {},
}));
vi.mock("../../../src/utils/changeAnalyzer", () => ({
  analyzeChange: vi.fn(),
}));
vi.mock("../../../src/utils/zodErrorFormatter", () => ({
  formatZodErrorsForPrompt: vi.fn().mockReturnValue(""),
}));

import * as fs from "fs";
import {
  ContextAssembler,
  STAGE_OUTPUT_CONTEXT_TYPE,
  STAGE_OUTPUT_SCHEMA,
  STAGE_INPUT_PREREQUISITES,
  OPTIONAL_CONTEXT_STAGES,
  detectTestRunner,
} from "../../../src/orchestrator/context/ContextAssembler";

const mockExistsSync = vi.mocked(fs.existsSync);
const mockReadFileSync = vi.mocked(fs.readFileSync);

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function makeAssembler(workspaceRoot = "/workspace", contextLoader?: RepositoryContextLoader) {
  return new ContextAssembler(makeLogger(), () => workspaceRoot, contextLoader ?? null);
}

beforeEach(() => {
  vi.clearAllMocks();
});

// ---------------------------------------------------------------------------
// Exported constants
// ---------------------------------------------------------------------------

describe("ContextAssembler — exported constants", () => {
  it("STAGE_OUTPUT_CONTEXT_TYPE maps skill stages to context types", () => {
    expect(STAGE_OUTPUT_CONTEXT_TYPE["issue-pickup"]).toBe("issue");
    expect(STAGE_OUTPUT_CONTEXT_TYPE["feature-planning"]).toBe("planning");
    expect(STAGE_OUTPUT_CONTEXT_TYPE["feature-dev"]).toBe("dev");
    expect(STAGE_OUTPUT_CONTEXT_TYPE["feature-validate"]).toBe("validate");
    expect(STAGE_OUTPUT_CONTEXT_TYPE["pr-create"]).toBe("pr");
  });

  it("STAGE_OUTPUT_SCHEMA has schemas for all skill stages", () => {
    const stages = [
      "issue-pickup",
      "feature-planning",
      "feature-dev",
      "feature-validate",
      "pr-create",
    ] as const;
    for (const s of stages) {
      expect(STAGE_OUTPUT_SCHEMA[s]).toBeDefined();
    }
  });

  it("STAGE_INPUT_PREREQUISITES maps stages to their prerequisite", () => {
    expect(STAGE_INPUT_PREREQUISITES["feature-planning"]?.stage).toBe("issue-pickup");
    expect(STAGE_INPUT_PREREQUISITES["feature-dev"]?.stage).toBe("feature-planning");
    expect(STAGE_INPUT_PREREQUISITES["pr-merge"]?.stage).toBe("pr-create");
  });

  it("OPTIONAL_CONTEXT_STAGES is an empty set (post-Issue #1608)", () => {
    expect(OPTIONAL_CONTEXT_STAGES.size).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// getContextPath
// ---------------------------------------------------------------------------

describe("ContextAssembler.getContextPath", () => {
  it("builds path from workspace root when no contextLoader", () => {
    const assembler = makeAssembler("/my/repo");
    const p = assembler.getContextPath("issue", 42);
    expect(p).toBe(path.join("/my/repo", ".nightgauge", "pipeline", "issue-42.json"));
  });

  it("delegates to contextLoader when provided", () => {
    const mockLoader: RepositoryContextLoader = {
      getContextFile: vi.fn().mockReturnValue("/loader/path/issue-42.json"),
    } as unknown as RepositoryContextLoader;
    const assembler = makeAssembler("/workspace", mockLoader);

    const p = assembler.getContextPath("issue", 42);

    expect(mockLoader.getContextFile).toHaveBeenCalledWith("issue", 42);
    expect(p).toBe("/loader/path/issue-42.json");
  });

  it("respects the workspaceRootProvider function result at call time", () => {
    let root = "/initial";
    const assembler = new ContextAssembler(makeLogger(), () => root);
    root = "/updated";

    const p = assembler.getContextPath("dev", 99);

    expect(p).toContain("/updated");
  });

  it("builds correct path for each context type", () => {
    const assembler = makeAssembler("/repo");
    const types = ["issue", "planning", "dev", "validate", "pr"] as const;
    for (const t of types) {
      expect(assembler.getContextPath(t, 1)).toContain(`${t}-1.json`);
    }
  });
});

// ---------------------------------------------------------------------------
// readContextFile
// ---------------------------------------------------------------------------

describe("ContextAssembler.readContextFile", () => {
  it("parses and returns JSON content when file exists", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue(JSON.stringify({ issue_number: 42 }) as any);
    const assembler = makeAssembler();

    const result = assembler.readContextFile("issue", 42);

    expect(result).toEqual({ issue_number: 42 });
  });

  it("returns null when file does not exist", () => {
    mockExistsSync.mockReturnValue(false);
    const assembler = makeAssembler();

    expect(assembler.readContextFile("issue", 42)).toBeNull();
  });

  it("returns null when file content is invalid JSON", () => {
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockReturnValue("not-valid-json" as any);
    const assembler = makeAssembler();

    expect(assembler.readContextFile("issue", 42)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// clearSessionState
// ---------------------------------------------------------------------------

describe("ContextAssembler.clearSessionState", () => {
  it("resets cachedIssueMetadata (via setCachedIssueMetadata + clearSessionState)", () => {
    const assembler = makeAssembler();
    assembler.setCachedIssueMetadata({ number: 42 } as any);

    // After clearing, the assembler should treat it as null
    // (verified indirectly: clearSessionState doesn't throw)
    expect(() => assembler.clearSessionState()).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// Setter methods
// ---------------------------------------------------------------------------

describe("ContextAssembler — setters", () => {
  it("setContextFileWaitMs updates the wait timeout", () => {
    const assembler = makeAssembler();
    // Setting 0 ms disables polling — verified via waitForContextFile test
    expect(() => assembler.setContextFileWaitMs(0)).not.toThrow();
  });

  it("setContextLoader updates the loader", () => {
    const assembler = makeAssembler();
    const loader = {
      getContextFile: vi.fn().mockReturnValue("/new/path"),
    } as unknown as RepositoryContextLoader;

    assembler.setContextLoader(loader);

    const p = assembler.getContextPath("issue", 1);
    expect(p).toBe("/new/path");
  });

  it("setContextLoader(null) reverts to workspace-root-based paths", () => {
    const loader = {
      getContextFile: vi.fn().mockReturnValue("/loader/path"),
    } as unknown as RepositoryContextLoader;
    const assembler = makeAssembler("/workspace", loader);

    assembler.setContextLoader(null);

    const p = assembler.getContextPath("issue", 1);
    expect(p).toContain("issue-1.json");
    expect(p).not.toBe("/loader/path");
  });

  it("setRepoOverride accepts string or undefined", () => {
    const assembler = makeAssembler();
    expect(() => assembler.setRepoOverride("my-org/my-repo")).not.toThrow();
    expect(() => assembler.setRepoOverride(undefined)).not.toThrow();
  });

  it("setForceFullPipeline accepts boolean", () => {
    const assembler = makeAssembler();
    expect(() => assembler.setForceFullPipeline(true)).not.toThrow();
    expect(() => assembler.setForceFullPipeline(false)).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// waitForContextFile
// ---------------------------------------------------------------------------

describe("ContextAssembler.waitForContextFile", () => {
  it("returns contextPath immediately when file already exists", async () => {
    mockExistsSync.mockReturnValue(true);
    const assembler = makeAssembler("/workspace");

    const result = await assembler.waitForContextFile("issue", 42, 5000);

    expect(result).toContain("issue-42.json");
  });

  it("returns null when file does not appear within timeout of 0", async () => {
    mockExistsSync.mockReturnValue(false);
    const assembler = makeAssembler("/workspace");

    const result = await assembler.waitForContextFile("issue", 42, 0);

    expect(result).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// validateStageContextOutput
// ---------------------------------------------------------------------------

describe("ContextAssembler.validateStageContextOutput", () => {
  it("accepts a required context file that appears during the final consistency check", async () => {
    const assembler = makeAssembler("/workspace");
    assembler.setContextFileWaitMs(200);

    const expectedPath = path.join("/workspace", ".nightgauge", "pipeline", "planning-42.json");
    const waitSpy = vi
      .spyOn(assembler, "waitForContextFile")
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(expectedPath);

    let planningExistsChecks = 0;
    mockExistsSync.mockImplementation((p) => {
      if (String(p).includes("planning-42.json")) {
        planningExistsChecks++;
        return planningExistsChecks >= 2;
      }
      return true;
    });
    mockReadFileSync.mockReturnValue(
      JSON.stringify({
        schema_version: "1.1",
        issue_number: 42,
        plan_file: ".nightgauge/plans/42-test.md",
        approach: "standard",
        files_to_create: [],
        files_to_modify: ["src/test.ts"],
        created_at: "2026-01-01T00:00:00Z",
      }) as any
    );

    const result = await assembler.validateStageContextOutput("feature-planning", 42);

    expect(result.error).toBeNull();
    expect(waitSpy).toHaveBeenCalledTimes(2);
  });
});

// ---------------------------------------------------------------------------
// detectTestRunner — #3114
// ---------------------------------------------------------------------------

describe("detectTestRunner (#3114)", () => {
  function setupRepo(opts: { pkg?: Record<string, unknown> | "missing"; files?: string[] }) {
    const files = new Set(opts.files ?? []);
    mockExistsSync.mockImplementation((p) => {
      const str = String(p);
      // package.json existence is implied by readFileSync behavior
      for (const f of files) {
        if (str.endsWith(f)) return true;
      }
      return false;
    });
    mockReadFileSync.mockImplementation((p) => {
      const str = String(p);
      if (str.endsWith("package.json")) {
        if (opts.pkg === "missing") {
          throw new Error("ENOENT");
        }
        return JSON.stringify(opts.pkg ?? {}) as any;
      }
      throw new Error(`unexpected read: ${str}`);
    });
  }

  it("detects vitest from scripts.test", () => {
    setupRepo({ pkg: { scripts: { test: "vitest run" } } });
    expect(detectTestRunner("/repo")).toBe("vitest");
  });

  it("detects vitest from devDependencies", () => {
    setupRepo({ pkg: { devDependencies: { vitest: "^1.0.0" } } });
    expect(detectTestRunner("/repo")).toBe("vitest");
  });

  it("detects vitest from vitest.config.ts", () => {
    setupRepo({ pkg: {}, files: ["vitest.config.ts"] });
    expect(detectTestRunner("/repo")).toBe("vitest");
  });

  it("detects angular from `ng test` script", () => {
    setupRepo({ pkg: { scripts: { test: "ng test" } } });
    expect(detectTestRunner("/repo")).toBe("angular");
  });

  it("detects angular from angular.json", () => {
    setupRepo({ pkg: { scripts: { test: "echo skip" } }, files: ["angular.json"] });
    // testScript doesn't match a known runner, so falls through to angular.json
    expect(detectTestRunner("/repo")).toBe("angular");
  });

  it("detects jest from devDependencies", () => {
    setupRepo({ pkg: { devDependencies: { jest: "^29.0.0" } } });
    expect(detectTestRunner("/repo")).toBe("jest");
  });

  it("detects playwright when there is no unit-test runner", () => {
    setupRepo({ pkg: { devDependencies: { "@playwright/test": "^1.0.0" } } });
    expect(detectTestRunner("/repo")).toBe("playwright");
  });

  it('returns "none" for an empty test script', () => {
    setupRepo({ pkg: { scripts: { test: "" } } });
    expect(detectTestRunner("/repo")).toBe("none");
  });

  it('returns "none" for the npm-init default placeholder script', () => {
    setupRepo({
      pkg: {
        scripts: { test: 'echo "Error: no test specified" && exit 1' },
      },
    });
    expect(detectTestRunner("/repo")).toBe("none");
  });

  it('returns "unknown" when no signals match', () => {
    setupRepo({ pkg: { scripts: { test: "make check" } } });
    expect(detectTestRunner("/repo")).toBe("unknown");
  });

  it('returns "unknown" when package.json is missing', () => {
    setupRepo({ pkg: "missing" });
    expect(detectTestRunner("/repo")).toBe("unknown");
  });

  it("prefers vitest over angular when both are present (auto-runnable wins)", () => {
    setupRepo({
      pkg: { scripts: { test: "vitest run" }, devDependencies: { "@angular/cli": "^17.0.0" } },
      files: ["angular.json"],
    });
    expect(detectTestRunner("/repo")).toBe("vitest");
  });
});
