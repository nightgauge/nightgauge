/**
 * SkillContextAssembler unit tests.
 *
 * Covers:
 * - Language detection for all supported manifest files
 * - TypeScript preference when package.json + tsconfig.json present
 * - Multi-language detection
 * - Framework detection for TS/JS, Python, Go, Rust
 * - Cache behavior and invalidation on onWorkspaceChanged
 * - Complexity score extraction from issue context files
 * - Error handling (unreadable files, missing workspace root)
 * - toRequestContext() output shape
 *
 * @see Issue #1475 - Assemble skill variant context from workspace analysis
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// Mock vscode — must be before any imports that reference vscode
// ---------------------------------------------------------------------------

const mockStat = vi.fn();
const mockReadFile = vi.fn();
const mockReadDirectory = vi.fn();

vi.mock("vscode", () => {
  const FileType = { File: 1, Directory: 2, SymbolicLink: 64, Unknown: 0 };

  class Uri {
    constructor(public readonly fsPath: string) {}
    static file(p: string) {
      return new Uri(p);
    }
    toString() {
      return this.fsPath;
    }
  }

  return {
    FileType,
    Uri,
    workspace: {
      fs: {
        stat: (uri: Uri) => mockStat(uri.fsPath),
        readFile: (uri: Uri) => mockReadFile(uri.fsPath),
        readDirectory: (uri: Uri) => mockReadDirectory(uri.fsPath),
      },
    },
    EventEmitter: class {
      private listeners: Array<() => void> = [];
      event = (fn: () => void) => {
        this.listeners.push(fn);
        return {
          dispose: () => {
            this.listeners = this.listeners.filter((l) => l !== fn);
          },
        };
      };
      fire = () => this.listeners.forEach((l) => l());
      dispose = vi.fn();
    },
    Disposable: { from: vi.fn() },
  };
});

// ---------------------------------------------------------------------------
// Now import the module under test
// ---------------------------------------------------------------------------

import { SkillContextAssembler, type SkillContext } from "../../src/platform/SkillContextAssembler";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function utf8(str: string): Uint8Array {
  return new TextEncoder().encode(str);
}

/** Build a minimal WorkspaceManager mock with a controllable
 * onWorkspaceChanged event — SkillContextAssembler's cache is invalidated
 * when the workspace repo set changes (the old onRepositoryChanged hook
 * was removed alongside the workspace-global current-repo pointer). */
function buildWorkspaceManager() {
  let workspaceChangedFire: (() => void) | null = null;
  const onWorkspaceChanged = vi.fn((listener: () => void) => {
    workspaceChangedFire = listener;
    return { dispose: vi.fn() };
  });

  return {
    mock: { onWorkspaceChanged },
    fireWorkspaceChanged: () => workspaceChangedFire?.(),
  };
}

/** Sets up mockStat so every path in `present` resolves successfully and
 *  all other paths reject (file not found). */
function setFilesPresent(present: string[]): void {
  mockStat.mockImplementation((filePath: string) => {
    const basename = filePath.split("/").pop()!;
    if (present.includes(basename)) {
      return Promise.resolve({ mtime: 1000 });
    }
    return Promise.reject(new Error("ENOENT"));
  });
}

/** Make readDirectory return no issue files by default. */
function emptyPipelineDir(): void {
  mockReadDirectory.mockResolvedValue([]);
}

// ---------------------------------------------------------------------------
// Test suite
// ---------------------------------------------------------------------------

describe("SkillContextAssembler", () => {
  beforeEach(() => {
    // Reset singleton between tests
    const existing = SkillContextAssembler.getInstance();
    if (existing) existing.dispose();

    vi.clearAllMocks();
    emptyPipelineDir();
  });

  // ── Language detection ──────────────────────────────────────────────────

  describe("language detection", () => {
    const cases: Array<{
      label: string;
      manifests: string[];
      expectedLanguage: string;
      expectedMultiLanguage: boolean;
    }> = [
      {
        label: "package.json + tsconfig.json → typescript",
        manifests: ["package.json", "tsconfig.json"],
        expectedLanguage: "typescript",
        expectedMultiLanguage: false,
      },
      {
        label: "package.json only → javascript",
        manifests: ["package.json"],
        expectedLanguage: "javascript",
        expectedMultiLanguage: false,
      },
      {
        label: "go.mod → go",
        manifests: ["go.mod"],
        expectedLanguage: "go",
        expectedMultiLanguage: false,
      },
      {
        label: "Cargo.toml → rust",
        manifests: ["Cargo.toml"],
        expectedLanguage: "rust",
        expectedMultiLanguage: false,
      },
      {
        label: "pom.xml → java",
        manifests: ["pom.xml"],
        expectedLanguage: "java",
        expectedMultiLanguage: false,
      },
      {
        label: "requirements.txt → python",
        manifests: ["requirements.txt"],
        expectedLanguage: "python",
        expectedMultiLanguage: false,
      },
      {
        label: "package.json + go.mod → typescript (priority 1), multiLanguage",
        manifests: ["package.json", "tsconfig.json", "go.mod"],
        expectedLanguage: "typescript",
        expectedMultiLanguage: true,
      },
      {
        label: "no manifests → unknown",
        manifests: [],
        expectedLanguage: "unknown",
        expectedMultiLanguage: false,
      },
    ];

    for (const tc of cases) {
      it(tc.label, async () => {
        setFilesPresent(tc.manifests);
        mockReadFile.mockResolvedValue(utf8("{}"));
        const { mock } = buildWorkspaceManager();
        const assembler = SkillContextAssembler.initialize(mock as any);

        const ctx = await assembler.assemble("/workspace");

        expect(ctx.primaryLanguage).toBe(tc.expectedLanguage);
        expect(ctx.multiLanguage).toBe(tc.expectedMultiLanguage);
      });
    }
  });

  // ── Framework detection — TypeScript/JavaScript ─────────────────────────

  describe("framework detection (TypeScript/JavaScript)", () => {
    it("detects react and hono, returns sorted array", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      const pkg = JSON.stringify({
        dependencies: { react: "18.0.0", hono: "3.0.0" },
      });
      mockReadFile.mockResolvedValue(utf8(pkg));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.frameworks).toEqual(["hono", "react"]);
    });

    it("detects @angular/core", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      const pkg = JSON.stringify({
        dependencies: { "@angular/core": "16.0.0" },
      });
      mockReadFile.mockResolvedValue(utf8(pkg));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.frameworks).toEqual(["angular"]);
    });

    it("returns empty array when no matching deps", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadFile.mockResolvedValue(utf8(JSON.stringify({ dependencies: {} })));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.frameworks).toEqual([]);
    });

    it("returns empty array when package.json has invalid JSON", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadFile.mockResolvedValue(utf8("NOT_JSON"));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.frameworks).toEqual([]);
    });
  });

  // ── Framework detection — Go ────────────────────────────────────────────

  describe("framework detection (Go)", () => {
    it("detects gin from go.mod", async () => {
      setFilesPresent(["go.mod"]);
      mockReadFile.mockResolvedValue(
        utf8("module myapp\n\nrequire github.com/gin-gonic/gin v1.9.0\n")
      );
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.frameworks).toEqual(["gin"]);
    });
  });

  // ── Framework detection — Rust ──────────────────────────────────────────

  describe("framework detection (Rust)", () => {
    it("detects axum from Cargo.toml", async () => {
      setFilesPresent(["Cargo.toml"]);
      mockReadFile.mockResolvedValue(utf8('[dependencies]\naxum = "0.6"\n'));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.frameworks).toEqual(["axum"]);
    });
  });

  // ── Cache behavior ──────────────────────────────────────────────────────

  describe("cache behavior", () => {
    it("returns cached result on second call without re-running file I/O", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadFile.mockResolvedValue(utf8(JSON.stringify({ dependencies: {} })));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const first = await assembler.assemble("/workspace");
      mockStat.mockClear();
      mockReadFile.mockClear();

      const second = await assembler.assemble("/workspace");

      expect(second).toBe(first); // same reference
      expect(mockStat).not.toHaveBeenCalled();
    });

    it("clears cache and re-runs detection after onWorkspaceChanged", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadFile.mockResolvedValue(utf8(JSON.stringify({ dependencies: {} })));
      const { mock, fireWorkspaceChanged } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const first = await assembler.assemble("/workspace");
      mockStat.mockClear();

      fireWorkspaceChanged();

      // Now update workspace to Go
      setFilesPresent(["go.mod"]);
      mockReadFile.mockResolvedValue(utf8("module myapp\n"));

      const second = await assembler.assemble("/workspace");
      expect(second).not.toBe(first);
      expect(second.primaryLanguage).toBe("go");
    });
  });

  // ── Complexity score ────────────────────────────────────────────────────

  describe("complexity score", () => {
    it("returns undefined when pipeline directory has no issue files", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadFile.mockResolvedValue(utf8(JSON.stringify({ dependencies: {} })));
      mockReadDirectory.mockResolvedValue([]); // no issue files
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.complexityScore).toBeUndefined();
    });

    it("returns score from routing.complexity_score", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadDirectory.mockResolvedValue([["issue-42.json", 1 /* FileType.File */]]);
      // stat for issue file
      mockStat.mockImplementation((filePath: string) => {
        const basename = filePath.split("/").pop()!;
        if (["package.json", "tsconfig.json"].includes(basename)) {
          return Promise.resolve({ mtime: 1000 });
        }
        if (basename === "issue-42.json") {
          return Promise.resolve({ mtime: 2000 });
        }
        return Promise.reject(new Error("ENOENT"));
      });
      // readFile for issue-42.json
      mockReadFile.mockImplementation((filePath: string) => {
        if (filePath.endsWith("issue-42.json")) {
          return Promise.resolve(utf8(JSON.stringify({ routing: { complexity_score: 7 } })));
        }
        return Promise.resolve(utf8(JSON.stringify({ dependencies: {} })));
      });
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.complexityScore).toBe(7);
    });
  });

  // ── Error handling ──────────────────────────────────────────────────────

  describe("error handling", () => {
    it("returns unknown language when all file stats fail", async () => {
      mockStat.mockRejectedValue(new Error("ENOENT"));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      expect(ctx.primaryLanguage).toBe("unknown");
      expect(ctx.frameworks).toEqual([]);
    });
  });

  // ── toRequestContext() ──────────────────────────────────────────────────

  describe("toRequestContext()", () => {
    it("converts SkillContext to Record<string,string>", async () => {
      setFilesPresent(["package.json", "tsconfig.json"]);
      mockReadFile.mockResolvedValue(utf8(JSON.stringify({ dependencies: { react: "18" } })));
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);

      const ctx = await assembler.assemble("/workspace");
      const record = assembler.toRequestContext(ctx);

      expect(record["workspace.primaryLanguage"]).toBe("typescript");
      expect(record["workspace.frameworks"]).toBe("react");
      expect(record["workspace.multiLanguage"]).toBe("false");
    });

    it("omits workspace.complexityScore when not available", () => {
      const ctx: SkillContext = {
        primaryLanguage: "go",
        frameworks: [],
        multiLanguage: false,
        detectedLanguages: ["go"],
      };
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);
      const record = assembler.toRequestContext(ctx);

      expect(record).not.toHaveProperty("workspace.complexityScore");
    });

    it("includes workspace.complexityScore when present", () => {
      const ctx: SkillContext = {
        primaryLanguage: "python",
        frameworks: ["fastapi"],
        multiLanguage: false,
        detectedLanguages: ["python"],
        complexityScore: 5,
      };
      const { mock } = buildWorkspaceManager();
      const assembler = SkillContextAssembler.initialize(mock as any);
      const record = assembler.toRequestContext(ctx);

      expect(record["workspace.complexityScore"]).toBe("5");
    });
  });
});
