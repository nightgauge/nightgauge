/**
 * SkillContextAssembler — singleton service that analyzes the active workspace
 * to detect programming language and framework, then assembles a typed
 * SkillContext payload for downstream platform skill variant resolution.
 *
 * Language detection priority:
 *   manifest file presence > file extension scan (extension scan is out of scope)
 *
 * Caching: per workspace root path, invalidated on onWorkspaceChanged.
 *
 * @see Issue #1475 - Assemble skill variant context from workspace analysis
 */

import * as vscode from "vscode";
import * as path from "node:path";
import type { WorkspaceManager } from "../services/WorkspaceManager";

// ============================================================================
// Types
// ============================================================================

/** Typed workspace context assembled for skill variant selection. */
export interface SkillContext {
  /** e.g. "typescript", "python", "go", "rust", "unknown" */
  primaryLanguage: string;
  /** e.g. ["react", "hono"] — empty if none detected */
  frameworks: string[];
  /** From issue context file when available */
  complexityScore?: number;
  /** True when multiple manifests detected */
  multiLanguage: boolean;
  /** All detected languages, ordered by manifest priority */
  detectedLanguages: string[];
}

// ============================================================================
// Manifest → language map (lower priority number = higher precedence)
// ============================================================================

interface ManifestEntry {
  file: string;
  language: string;
  priority: number;
}

const MANIFEST_ENTRIES: ManifestEntry[] = [
  { file: "package.json", language: "javascript", priority: 1 },
  { file: "tsconfig.json", language: "typescript", priority: 1 },
  { file: "go.mod", language: "go", priority: 2 },
  { file: "Cargo.toml", language: "rust", priority: 3 },
  { file: "pom.xml", language: "java", priority: 4 },
  { file: "build.gradle", language: "java", priority: 4 },
  { file: "pyproject.toml", language: "python", priority: 5 },
  { file: "requirements.txt", language: "python", priority: 5 },
  { file: "Gemfile", language: "ruby", priority: 6 },
];

// ============================================================================
// Framework detection maps
// ============================================================================

/** Maps package.json dependency key → framework label */
const JS_FRAMEWORK_MAP: Record<string, string> = {
  react: "react",
  "react-dom": "react",
  "@angular/core": "angular",
  vue: "vue",
  svelte: "svelte",
  next: "next",
  nuxt: "nuxt",
  "@nestjs/core": "nestjs",
  express: "express",
  hono: "hono",
  fastify: "fastify",
};

/** Maps requirements.txt / pyproject.toml key → framework label */
const PYTHON_FRAMEWORK_MAP: Record<string, string> = {
  django: "django",
  flask: "flask",
  fastapi: "fastapi",
};

/** Maps go.mod require module path → framework label */
const GO_FRAMEWORK_MAP: Record<string, string> = {
  "github.com/gin-gonic/gin": "gin",
  "github.com/gofiber/fiber": "fiber",
  "github.com/labstack/echo": "echo",
};

/** Maps Cargo.toml [dependencies] key → framework label */
const RUST_FRAMEWORK_MAP: Record<string, string> = {
  "actix-web": "actix",
  axum: "axum",
  rocket: "rocket",
};

// ============================================================================
// Internal helpers
// ============================================================================

/** Read a file via vscode.workspace.fs, return null on any error. */
async function readWorkspaceFile(filePath: string): Promise<string | null> {
  try {
    const uri = vscode.Uri.file(filePath);
    const bytes = await vscode.workspace.fs.readFile(uri);
    return new TextDecoder().decode(bytes);
  } catch {
    return null;
  }
}

/** Check whether a file exists via vscode.workspace.fs.stat. */
async function fileExists(filePath: string): Promise<boolean> {
  try {
    await vscode.workspace.fs.stat(vscode.Uri.file(filePath));
    return true;
  } catch {
    return false;
  }
}

// ============================================================================
// Language detection
// ============================================================================

interface DetectedLanguageInfo {
  primaryLanguage: string;
  multiLanguage: boolean;
  detectedLanguages: string[];
}

async function detectLanguage(workspaceRoot: string): Promise<DetectedLanguageInfo> {
  const checks = await Promise.all(
    MANIFEST_ENTRIES.map(async (entry) => ({
      entry,
      found: await fileExists(path.join(workspaceRoot, entry.file)),
    }))
  );

  const found = checks.filter((c) => c.found);

  if (found.length === 0) {
    return {
      primaryLanguage: "unknown",
      multiLanguage: false,
      detectedLanguages: [],
    };
  }

  // Collect unique languages in priority order (lowest number = highest priority)
  const byPriority = found.slice().sort((a, b) => a.entry.priority - b.entry.priority);
  const seen = new Set<string>();
  const orderedLanguages: string[] = [];
  for (const { entry } of byPriority) {
    const lang = entry.language === "javascript" ? "javascript" : entry.language;
    if (!seen.has(lang)) {
      seen.add(lang);
      orderedLanguages.push(lang);
    }
  }

  // Determine primary: lowest priority wins; if package.json present, prefer
  // typescript when tsconfig.json also present.
  let primaryLanguage = orderedLanguages[0];
  const hasPackageJson = found.some((c) => c.entry.file === "package.json");
  const hasTsConfig = found.some((c) => c.entry.file === "tsconfig.json");
  if (hasPackageJson && hasTsConfig) {
    primaryLanguage = "typescript";
    // Replace 'javascript' entry in list with 'typescript'
    const jsIdx = orderedLanguages.indexOf("javascript");
    if (jsIdx !== -1) {
      orderedLanguages.splice(jsIdx, 1, "typescript");
    }
  } else if (hasPackageJson && !hasTsConfig) {
    primaryLanguage = "javascript";
  }

  // Deduplicate orderedLanguages after TS substitution
  const deduped = [...new Set(orderedLanguages)];

  return {
    primaryLanguage,
    multiLanguage: deduped.length > 1,
    detectedLanguages: deduped,
  };
}

// ============================================================================
// Framework detection
// ============================================================================

async function detectFrameworks(workspaceRoot: string, language: string): Promise<string[]> {
  const frameworks = new Set<string>();

  if (language === "typescript" || language === "javascript") {
    const content = await readWorkspaceFile(path.join(workspaceRoot, "package.json"));
    if (content) {
      try {
        const pkg = JSON.parse(content) as {
          dependencies?: Record<string, string>;
          devDependencies?: Record<string, string>;
        };
        const allDeps = {
          ...pkg.dependencies,
          ...pkg.devDependencies,
        };
        for (const key of Object.keys(allDeps)) {
          if (JS_FRAMEWORK_MAP[key]) {
            frameworks.add(JS_FRAMEWORK_MAP[key]);
          }
        }
      } catch {
        // JSON parse error → return empty
      }
    }
  } else if (language === "python") {
    // requirements.txt
    const reqContent = await readWorkspaceFile(path.join(workspaceRoot, "requirements.txt"));
    if (reqContent) {
      for (const line of reqContent.split("\n")) {
        const pkg = line.split("==")[0].split(">=")[0].trim().toLowerCase();
        if (PYTHON_FRAMEWORK_MAP[pkg]) {
          frameworks.add(PYTHON_FRAMEWORK_MAP[pkg]);
        }
      }
    }

    // pyproject.toml — simple line scan for [tool.poetry.dependencies]
    const pyprojectContent = await readWorkspaceFile(path.join(workspaceRoot, "pyproject.toml"));
    if (pyprojectContent) {
      for (const line of pyprojectContent.split("\n")) {
        const key = line.split("=")[0].trim().toLowerCase();
        if (PYTHON_FRAMEWORK_MAP[key]) {
          frameworks.add(PYTHON_FRAMEWORK_MAP[key]);
        }
      }
    }
  } else if (language === "go") {
    const goModContent = await readWorkspaceFile(path.join(workspaceRoot, "go.mod"));
    if (goModContent) {
      for (const [modulePath, label] of Object.entries(GO_FRAMEWORK_MAP)) {
        if (goModContent.includes(modulePath)) {
          frameworks.add(label);
        }
      }
    }
  } else if (language === "rust") {
    const cargoContent = await readWorkspaceFile(path.join(workspaceRoot, "Cargo.toml"));
    if (cargoContent) {
      for (const [crate, label] of Object.entries(RUST_FRAMEWORK_MAP)) {
        // Match crate key in [dependencies] section
        if (new RegExp(`\\b${crate}\\b`).test(cargoContent)) {
          frameworks.add(label);
        }
      }
    }
  }

  return [...frameworks].sort();
}

// ============================================================================
// Complexity score lookup
// ============================================================================

async function getComplexityScore(workspaceRoot: string): Promise<number | undefined> {
  try {
    const pipelineDir = path.join(workspaceRoot, ".nightgauge", "pipeline");
    const dirUri = vscode.Uri.file(pipelineDir);
    let entries: [string, vscode.FileType][];
    try {
      entries = await vscode.workspace.fs.readDirectory(dirUri);
    } catch {
      return undefined;
    }

    // Filter to issue-*.json files, find most recently modified
    const issueFiles = entries.filter(
      ([name, type]) => type === vscode.FileType.File && /^issue-\d+\.json$/.test(name)
    );
    if (issueFiles.length === 0) {
      return undefined;
    }

    // Stat each to find most recent mtime
    const statResults = await Promise.all(
      issueFiles.map(async ([name]) => {
        const filePath = path.join(pipelineDir, name);
        try {
          const stat = await vscode.workspace.fs.stat(vscode.Uri.file(filePath));
          return { filePath, mtime: stat.mtime };
        } catch {
          return { filePath, mtime: 0 };
        }
      })
    );
    statResults.sort((a, b) => b.mtime - a.mtime);
    const mostRecent = statResults[0].filePath;

    const content = await readWorkspaceFile(mostRecent);
    if (!content) return undefined;

    const data = JSON.parse(content) as {
      routing?: { complexity_score?: unknown };
    };
    const score = data?.routing?.complexity_score;
    return typeof score === "number" ? score : undefined;
  } catch {
    return undefined;
  }
}

// ============================================================================
// SkillContextAssembler
// ============================================================================

export class SkillContextAssembler implements vscode.Disposable {
  private static _instance: SkillContextAssembler | undefined;
  private readonly _disposables: vscode.Disposable[] = [];
  private readonly _cache = new Map<string, SkillContext>();

  private constructor(private readonly workspaceManager: WorkspaceManager) {
    // Clear context cache when the workspace repo set changes (e.g. a
    // folder is added/removed). The old onRepositoryChanged hook tracked
    // the removed workspace-global current-repo pointer — callers now pass
    // their target repo explicitly, so a per-repo cache key is sufficient
    // and a broad reload on workspace changes covers the rest.
    const sub = workspaceManager.onWorkspaceChanged(() => {
      this._cache.clear();
    });
    this._disposables.push(sub);
  }

  static initialize(workspaceManager: WorkspaceManager): SkillContextAssembler {
    if (!SkillContextAssembler._instance) {
      SkillContextAssembler._instance = new SkillContextAssembler(workspaceManager);
    }
    return SkillContextAssembler._instance;
  }

  static getInstance(): SkillContextAssembler | undefined {
    return SkillContextAssembler._instance;
  }

  /**
   * Assemble SkillContext for the given workspace root.
   *
   * Results are cached per workspace root and invalidated on repository change.
   * On any detection error, returns a minimal valid context rather than throwing.
   */
  async assemble(workspaceRoot: string): Promise<SkillContext> {
    const cached = this._cache.get(workspaceRoot);
    if (cached) {
      return cached;
    }

    try {
      const [langInfo, complexityScore] = await Promise.all([
        detectLanguage(workspaceRoot),
        getComplexityScore(workspaceRoot),
      ]);

      const frameworks = await detectFrameworks(workspaceRoot, langInfo.primaryLanguage);

      const ctx: SkillContext = {
        primaryLanguage: langInfo.primaryLanguage,
        frameworks,
        multiLanguage: langInfo.multiLanguage,
        detectedLanguages: langInfo.detectedLanguages,
        ...(complexityScore !== undefined && { complexityScore }),
      };

      this._cache.set(workspaceRoot, ctx);
      return ctx;
    } catch (err) {
      console.warn("[SkillContextAssembler] Context assembly failed, using fallback", err);
      const fallback: SkillContext = {
        primaryLanguage: "unknown",
        frameworks: [],
        multiLanguage: false,
        detectedLanguages: [],
      };
      this._cache.set(workspaceRoot, fallback);
      return fallback;
    }
  }

  /**
   * Convert a SkillContext to the Record<string, string> map expected by
   * SkillResolveRequest.context.
   */
  toRequestContext(ctx: SkillContext): Record<string, string> {
    return {
      "workspace.primaryLanguage": ctx.primaryLanguage,
      "workspace.frameworks": ctx.frameworks.join(","),
      "workspace.multiLanguage": String(ctx.multiLanguage),
      ...(ctx.complexityScore !== undefined && {
        "workspace.complexityScore": String(ctx.complexityScore),
      }),
    };
  }

  dispose(): void {
    for (const d of this._disposables) {
      d.dispose();
    }
    this._cache.clear();
    SkillContextAssembler._instance = undefined;
  }
}
