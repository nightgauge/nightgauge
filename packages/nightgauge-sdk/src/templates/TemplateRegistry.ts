/**
 * TemplateRegistry - Runtime registry for template lookup and caching
 *
 * Scans a directory for `.handlebars` files, parses them, and provides
 * name/version-based lookup. Templates are loaded lazily on first access.
 *
 * @see docs/PROMPT_TEMPLATES.md for full usage documentation
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { parseTemplateFile } from "./PromptTemplate.js";
import type { PromptTemplate } from "./PromptTemplate.js";

/**
 * TemplateRegistry manages a collection of prompt templates loaded from disk.
 *
 * @example
 * ```typescript
 * const registry = new TemplateRegistry();
 * await registry.loadTemplates('skills/templates');
 *
 * const template = registry.getTemplate('feature-planning-system');
 * if (template) {
 *   const rendered = renderer.render(template, { issueNumber: 42 });
 * }
 * ```
 */
export class TemplateRegistry {
  /**
   * Map of `name@version` → PromptTemplate for direct lookups.
   */
  private readonly byVersionKey = new Map<string, PromptTemplate>();

  /**
   * Map of `name` → sorted versions (ascending semver) for latest resolution.
   */
  private readonly byName = new Map<string, PromptTemplate[]>();

  /**
   * Whether `loadTemplates()` has been called at least once.
   */
  private loaded = false;

  /**
   * Load all `.handlebars` files from a directory tree.
   *
   * Safe to call multiple times — subsequent calls add to the registry
   * without clearing previously loaded templates (union semantics).
   * If two files define the same `name@version`, the last one wins.
   *
   * @param baseDir - Absolute or relative path to the templates directory
   * @param options.ignore - If true, silently skip directories that do not exist
   * @returns Number of templates successfully loaded
   */
  async loadTemplates(baseDir: string, options: { ignore?: boolean } = {}): Promise<number> {
    let count = 0;

    let files: string[];
    try {
      files = await this.collectHandlebarsFiles(baseDir);
    } catch (error) {
      if (options.ignore) return 0;
      throw new Error(
        `TemplateRegistry: failed to scan directory "${baseDir}": ${
          error instanceof Error ? error.message : String(error)
        }`,
        { cause: error }
      );
    }

    for (const filePath of files) {
      try {
        const raw = await fs.readFile(filePath, "utf-8");
        const template = parseTemplateFile(raw, filePath);
        this.register(template);
        count++;
      } catch (error) {
        // Log but continue — a single bad template should not halt the registry
        console.warn(
          `TemplateRegistry: skipping invalid template "${filePath}": ${
            error instanceof Error ? error.message : String(error)
          }`
        );
      }
    }

    this.loaded = true;
    return count;
  }

  /**
   * Register a single template programmatically (e.g., for tests).
   */
  register(template: PromptTemplate): void {
    const versionKey = `${template.name}@${template.version}`;
    this.byVersionKey.set(versionKey, template);

    const versions = this.byName.get(template.name) ?? [];
    // Remove existing entry for the same version before re-adding
    const idx = versions.findIndex((t) => t.version === template.version);
    if (idx !== -1) versions.splice(idx, 1);
    versions.push(template);
    versions.sort((a, b) => compareSemver(a.version, b.version));
    this.byName.set(template.name, versions);
  }

  /**
   * Retrieve a template by name, optionally pinning to a specific version.
   *
   * When `version` is omitted, the **latest** version is returned.
   *
   * @param name - Template name (e.g., `"feature-planning-system"`)
   * @param version - Exact semantic version string (e.g., `"1.0.0"`)
   * @returns The PromptTemplate, or `null` if not found
   */
  getTemplate(name: string, version?: string): PromptTemplate | null {
    if (version) {
      return this.byVersionKey.get(`${name}@${version}`) ?? null;
    }

    const versions = this.byName.get(name);
    if (!versions || versions.length === 0) return null;
    return versions[versions.length - 1]; // highest version (sorted ascending)
  }

  /**
   * Return all registered templates, optionally filtered by name.
   */
  listTemplates(name?: string): PromptTemplate[] {
    if (name) {
      return [...(this.byName.get(name) ?? [])];
    }
    const all: PromptTemplate[] = [];
    for (const versions of this.byName.values()) {
      all.push(...versions);
    }
    return all;
  }

  /**
   * Returns `true` if `loadTemplates()` has been called at least once.
   */
  get isLoaded(): boolean {
    return this.loaded;
  }

  /**
   * Total number of templates (across all versions).
   */
  get size(): number {
    return this.byVersionKey.size;
  }

  /**
   * Clear all registered templates and reset loaded state.
   * Primarily useful in tests.
   */
  clear(): void {
    this.byVersionKey.clear();
    this.byName.clear();
    this.loaded = false;
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  private async collectHandlebarsFiles(dir: string): Promise<string[]> {
    const results: string[] = [];
    const entries = await fs.readdir(dir, { withFileTypes: true });

    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        const nested = await this.collectHandlebarsFiles(full);
        results.push(...nested);
      } else if (entry.isFile() && entry.name.endsWith(".handlebars")) {
        results.push(full);
      }
    }

    return results;
  }
}

/**
 * Compare two semver strings (major.minor.patch).
 * Returns negative, zero, or positive (like Array.sort comparator).
 */
function compareSemver(a: string, b: string): number {
  const parse = (s: string) => s.split(".").map((n) => parseInt(n, 10) || 0);
  const [aMaj, aMin, aPat] = parse(a);
  const [bMaj, bMin, bPat] = parse(b);
  return aMaj !== bMaj ? aMaj - bMaj : aMin !== bMin ? aMin - bMin : aPat - bPat;
}

/**
 * Shared default registry instance.
 * The extension and SDK stages use this singleton unless an isolated
 * registry is explicitly needed (e.g., unit tests).
 */
export const defaultRegistry = new TemplateRegistry();
