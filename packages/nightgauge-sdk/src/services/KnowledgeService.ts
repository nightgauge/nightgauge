/**
 * KnowledgeService - Knowledge directory scaffolding and CRUD for pipeline issues
 *
 * Scaffolds a `.nightgauge/knowledge/{epics|features}/{N}-{slug}/`
 * directory with PRD.md and decisions.md templates derived from the issue body.
 * Provides full CRUD operations (create, read, update, list, search) and
 * index generation for knowledge entries with YAML frontmatter support.
 *
 * Activated by `knowledge.enabled` and `knowledge.auto_scaffold` config flags.
 * Disabled by default — opt-in per project config.
 *
 * @see Issue #1675 - Implement KnowledgeService CRUD Operations
 * @see Issue #1680 - Integrate Knowledge Scaffolding into Issue-Pickup Skill
 * @see docs/ARCHITECTURE.md - SDK utility pattern (pure TS, no VSCode deps)
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as yaml from "js-yaml";
import {
  KnowledgeEntrySchema,
  KnowledgeIndexSchema,
  RepoTopicTypeSchema,
  type KnowledgeEntry,
  type KnowledgeIndex,
  type KnowledgeType,
  type RepoTopicType,
} from "../context/schemas/knowledge.js";

export interface ScaffoldResult {
  /** Absolute path to the scaffolded knowledge directory */
  knowledge_path: string;
  /** Files created within the knowledge directory */
  files_created: string[];
  /** Whether scaffolding was skipped due to config flags */
  skipped: boolean;
  /** Reason for skipping (when skipped=true) */
  skip_reason?: string;
  /** Whether the scaffolded PRD contains real extracted content (not just TODO placeholders) */
  substantive: boolean;
}

export interface KnowledgeConfig {
  /** Enable knowledge directory scaffolding */
  enabled?: boolean;
  /** Automatically scaffold when picking up an issue (requires enabled=true) */
  auto_scaffold?: boolean;
  /** Enable wiki-link resolution in knowledge documents */
  wiki_links?: boolean;
  /** Regenerate knowledge index on every commit (reserved for future git hook use) */
  index_on_commit?: boolean;
  /**
   * Enforce decisions.md population during planning when the plan contains
   * tradeoff signals (2+ distinct keywords from configs/knowledge-tradeoff-keywords.yaml).
   * When true, `nightgauge knowledge validate` must pass before planning completes.
   * Defaults to false for backward compatibility with existing projects.
   */
  require_decisions?: boolean;
}

export interface KnowledgeReadResult {
  /** Parsed frontmatter metadata (null when file has no frontmatter) */
  entry: KnowledgeEntry | null;
  /** Markdown body content (everything after frontmatter block) */
  body: string;
}

export interface KnowledgeListEntry {
  /** Absolute path to the knowledge file */
  filePath: string;
  /** Path relative to workspace root */
  relativePath: string;
  /** Parsed frontmatter metadata (null when file has no frontmatter) */
  entry: KnowledgeEntry | null;
}

export interface KnowledgeSearchResult {
  /** Path relative to workspace root */
  relativePath: string;
  /** Parsed frontmatter (null when file has no frontmatter) */
  entry: KnowledgeEntry | null;
  /** Line containing the match (up to 200 chars for context) */
  excerpt: string;
}

export interface KnowledgeListFilter {
  type?: KnowledgeType;
  tags?: string[];
  related_issues?: number[];
}

export interface KnowledgeRegenResult {
  /** Whether any files were actually updated */
  regenerated: boolean;
  /** Relative paths of files that were written */
  filesUpdated: string[];
  /** Whether PRD.md was updated */
  prdUpdated: boolean;
  /** Whether decisions.md was preserved (always true — never modified) */
  decisionsPreserved: boolean;
  /** ISO timestamp of regeneration */
  timestamp: string;
  /** Reason when regenerated=false */
  reason?: string;
}

export interface RepoTopicResult {
  /** Absolute path to the category directory */
  knowledge_path: string;
  /** Absolute path to the created/existing entry file */
  file_path: string;
  /** Whether the entry already existed (idempotent call) */
  created: boolean;
  /** Files created during this call (empty when created=false) */
  files_created: string[];
}

/** Map from KnowledgeType to default filename */
const TYPE_FILENAME_MAP: Record<string, string> = {
  prd: "PRD.md",
  decision: "decisions.md",
  adr: "decisions.md",
  conversation: "conversation.md",
  reference: "reference.md",
  note: "note.md",
};

export class KnowledgeService {
  constructor(private workspaceRoot: string) {}

  /**
   * Scaffold a knowledge directory for the given issue.
   *
   * Creates `.nightgauge/knowledge/{epics|features}/{N}-{slug}/` with
   * `PRD.md` and `decisions.md`. Operation is idempotent — re-running when the
   * directory already exists returns the path without error or overwriting.
   *
   * @param issueNumber  - GitHub issue number
   * @param issueTitle   - Issue title (used for slug generation)
   * @param issueBody    - Full issue body (used for PRD template)
   * @param isEpic       - Whether this is an epic issue (epics/ vs features/ path)
   * @param config       - Knowledge config flags (enabled, auto_scaffold)
   */
  async scaffoldForIssue(
    issueNumber: number,
    issueTitle: string,
    issueBody: string,
    isEpic: boolean,
    config: KnowledgeConfig
  ): Promise<ScaffoldResult> {
    if (!config.enabled) {
      return {
        knowledge_path: "",
        files_created: [],
        skipped: true,
        skip_reason: "knowledge.enabled is false",
        substantive: false,
      };
    }

    if (config.auto_scaffold === false) {
      return {
        knowledge_path: "",
        files_created: [],
        skipped: true,
        skip_reason: "knowledge.auto_scaffold is false",
        substantive: false,
      };
    }

    // Check whether the issue body has extractable content before scaffolding.
    // If all sections are empty, defer scaffolding to feature-planning which
    // has richer context. This prevents empty boilerplate entries.
    const summary = this.extractSection(issueBody, "Summary");
    const acceptanceCriteria = this.extractSection(issueBody, "Acceptance Criteria");
    const technicalNotes = this.extractSection(issueBody, "Technical Notes");
    const hasExtractableContent = !!(summary || acceptanceCriteria || technicalNotes);

    if (!hasExtractableContent) {
      // Check if the issue body itself has meaningful content (>50 chars of
      // non-whitespace text, excluding markdown headings and HTML comments).
      const strippedBody = issueBody
        .replace(/<!--[\s\S]*?-->/g, "")
        .replace(/^#+\s.*$/gm, "")
        .replace(/\s+/g, " ")
        .trim();
      if (strippedBody.length < 50) {
        return {
          knowledge_path: "",
          files_created: [],
          skipped: true,
          skip_reason:
            "issue body has no extractable content — knowledge will be scaffolded during feature-planning when richer context is available",
          substantive: false,
        };
      }
    }

    const slug = this.generateSlug(issueTitle);
    const category = isEpic ? "epics" : "features";
    const dirName = `${issueNumber}-${slug}`;
    const knowledgePath = path.join(
      this.workspaceRoot,
      ".nightgauge",
      "knowledge",
      category,
      dirName
    );

    await fs.mkdir(knowledgePath, { recursive: true });

    const prdPath = path.join(knowledgePath, "PRD.md");
    const decisionsPath = path.join(knowledgePath, "decisions.md");
    const filesCreated: string[] = [];

    // Idempotent: skip files that already exist
    const prdExists = await fs
      .access(prdPath)
      .then(() => true)
      .catch(() => false);
    if (!prdExists) {
      await fs.writeFile(prdPath, this.generatePRD(issueNumber, issueTitle, issueBody), "utf-8");
      filesCreated.push("PRD.md");
    }

    const decisionsExists = await fs
      .access(decisionsPath)
      .then(() => true)
      .catch(() => false);
    if (!decisionsExists) {
      await fs.writeFile(
        decisionsPath,
        this.generateDecisionsTemplate(issueNumber, issueTitle),
        "utf-8"
      );
      filesCreated.push("decisions.md");
    }

    // Return path relative to workspaceRoot for portability in context files
    const relativeKnowledgePath = path.relative(this.workspaceRoot, knowledgePath);

    return {
      knowledge_path: relativeKnowledgePath,
      files_created: filesCreated,
      skipped: false,
      substantive: hasExtractableContent,
    };
  }

  /**
   * Create a new knowledge entry with YAML frontmatter.
   *
   * @param type - Knowledge entry type (prd, decision, note, etc.)
   * @param slug - Relative path under knowledge root (e.g., "features/1675-foo")
   * @param content - Markdown body content
   * @param frontmatter - Metadata for YAML frontmatter block
   * @returns Relative path to the created file
   * @throws Error if the file already exists
   */
  async create(
    type: KnowledgeType,
    slug: string,
    content: string,
    frontmatter: Partial<KnowledgeEntry>
  ): Promise<string> {
    const now = new Date().toISOString();
    const merged: Partial<KnowledgeEntry> = {
      type,
      created: now,
      updated: now,
      ...frontmatter,
    };

    // Validate frontmatter shape (partial — title may be omitted by caller)
    KnowledgeEntrySchema.partial().parse(merged);

    const knowledgeRoot = path.join(this.workspaceRoot, ".nightgauge", "knowledge");
    const dirPath = path.join(knowledgeRoot, slug);
    await fs.mkdir(dirPath, { recursive: true });

    const filename = this.typeToFilename(type);
    const filePath = path.join(dirPath, filename);

    // Non-idempotent: throw if file exists
    const exists = await fs
      .access(filePath)
      .then(() => true)
      .catch(() => false);
    if (exists) {
      throw new Error(`File already exists: ${filePath}`);
    }

    const fileContent = this.serializeFrontmatter(merged) + "\n" + content;
    await fs.writeFile(filePath, fileContent, "utf-8");

    return path.relative(this.workspaceRoot, filePath);
  }

  /**
   * Read a knowledge file, parsing YAML frontmatter and body.
   *
   * @param filePath - Absolute path or path relative to workspaceRoot
   * @returns Parsed frontmatter entry (null if no frontmatter) and body
   * @throws Error if file does not exist
   */
  async read(filePath: string): Promise<KnowledgeReadResult> {
    const resolvedPath = path.isAbsolute(filePath)
      ? filePath
      : path.join(this.workspaceRoot, filePath);

    let content: string;
    try {
      content = await fs.readFile(resolvedPath, "utf-8");
    } catch {
      throw new Error(`File not found: ${resolvedPath}`);
    }

    return this.parseFrontmatter(content);
  }

  /**
   * Update a knowledge file, merging frontmatter and replacing body.
   *
   * Always bumps the `updated` timestamp. Preserves the original `created` date.
   * If the file has no existing frontmatter, injects a new frontmatter block.
   *
   * @param filePath - Absolute path or path relative to workspaceRoot
   * @param content - New markdown body content
   * @param frontmatter - Metadata fields to merge into existing frontmatter
   */
  async update(
    filePath: string,
    content: string,
    frontmatter: Partial<KnowledgeEntry>
  ): Promise<void> {
    const resolvedPath = path.isAbsolute(filePath)
      ? filePath
      : path.join(this.workspaceRoot, filePath);

    const existing = await this.read(resolvedPath);

    const merged: Partial<KnowledgeEntry> = {
      ...(existing.entry ?? {}),
      ...frontmatter,
      updated: new Date().toISOString(),
    };

    // Preserve original created date
    if (existing.entry?.created) {
      merged.created = existing.entry.created;
    }

    const fileContent = this.serializeFrontmatter(merged) + "\n" + content;
    await fs.writeFile(resolvedPath, fileContent, "utf-8");
  }

  /**
   * List all knowledge entries, optionally filtered.
   *
   * @param filter - Optional filter by type, tags, or related_issues
   * @returns Array of entries sorted by created date (oldest first)
   */
  async list(filter?: KnowledgeListFilter): Promise<KnowledgeListEntry[]> {
    const entries: KnowledgeListEntry[] = [];

    for await (const absPath of this.walkKnowledgeDirectory()) {
      let content: string;
      try {
        content = await fs.readFile(absPath, "utf-8");
      } catch {
        continue;
      }

      const { entry } = this.parseFrontmatter(content);

      if (filter && entry) {
        if (filter.type && entry.type !== filter.type) continue;
        if (filter.tags && (!entry.tags || !filter.tags.some((t) => entry.tags!.includes(t))))
          continue;
        if (
          filter.related_issues &&
          (!entry.related_issues ||
            !filter.related_issues.some((i) => entry.related_issues!.includes(i)))
        )
          continue;
      } else if (filter && !entry) {
        // Files without frontmatter cannot match any filter
        if (filter.type || filter.tags || filter.related_issues) continue;
      }

      entries.push({
        filePath: absPath,
        relativePath: path.relative(this.workspaceRoot, absPath),
        entry,
      });
    }

    // Sort by created date (oldest first), files without frontmatter last
    entries.sort((a, b) => {
      if (!a.entry?.created && !b.entry?.created) return 0;
      if (!a.entry?.created) return 1;
      if (!b.entry?.created) return -1;
      return a.entry.created.localeCompare(b.entry.created);
    });

    return entries;
  }

  /**
   * Search knowledge entries by substring (case-insensitive).
   *
   * @param query - Search string (empty returns [])
   * @returns Matching entries with excerpts
   */
  async search(query: string): Promise<KnowledgeSearchResult[]> {
    if (query.trim().length === 0) return [];

    const results: KnowledgeSearchResult[] = [];
    const lowerQuery = query.toLowerCase();

    for await (const absPath of this.walkKnowledgeDirectory()) {
      let content: string;
      try {
        content = await fs.readFile(absPath, "utf-8");
      } catch {
        continue;
      }

      const lowerContent = content.toLowerCase();
      const matchIndex = lowerContent.indexOf(lowerQuery);
      if (matchIndex === -1) continue;

      const { entry } = this.parseFrontmatter(content);

      // Extract the line containing the match
      const lines = content.split("\n");
      let charCount = 0;
      let excerpt = "";
      for (const line of lines) {
        if (charCount + line.length >= matchIndex) {
          excerpt = line.slice(0, 200);
          break;
        }
        charCount += line.length + 1; // +1 for newline
      }

      results.push({
        relativePath: path.relative(this.workspaceRoot, absPath),
        entry,
        excerpt,
      });
    }

    return results;
  }

  /**
   * Generate a knowledge index from all entries under the knowledge directory.
   *
   * Scans category directories, parses frontmatter, builds a KnowledgeIndex
   * object, validates it, and writes a README.md table of contents.
   *
   * @returns The validated KnowledgeIndex object
   */
  async generateIndex(): Promise<KnowledgeIndex> {
    const knowledgeRoot = path.join(this.workspaceRoot, ".nightgauge", "knowledge");

    const categories: KnowledgeIndex["categories"] = {};
    let totalEntries = 0;

    // Scan top-level category directories
    let categoryDirs: string[];
    try {
      const entries = await fs.readdir(knowledgeRoot, {
        withFileTypes: true,
      });
      categoryDirs = entries.filter((e) => e.isDirectory()).map((e) => e.name);
    } catch {
      categoryDirs = [];
    }

    for (const category of categoryDirs) {
      const categoryPath = path.join(knowledgeRoot, category);
      let issueDirs: string[];
      try {
        const entries = await fs.readdir(categoryPath, {
          withFileTypes: true,
        });
        issueDirs = entries.filter((e) => e.isDirectory()).map((e) => e.name);
      } catch {
        continue;
      }

      const categoryEntries: KnowledgeIndex["categories"][string] = [];

      for (const issueDir of issueDirs) {
        const match = issueDir.match(/^(\d+)-(.+)$/);
        if (!match) continue;

        const issueNumber = parseInt(match[1], 10);
        const slug = match[2];
        const issueDirPath = path.join(categoryPath, issueDir);
        const relativeDirPath = path.relative(this.workspaceRoot, issueDirPath);

        // Scan for .md files
        let files: string[];
        try {
          const dirEntries = await fs.readdir(issueDirPath);
          files = dirEntries.filter((f) => f.endsWith(".md"));
        } catch {
          files = [];
        }

        // Try to parse entry metadata from the first file with frontmatter
        let entryMeta: KnowledgeEntry | undefined;
        for (const file of files) {
          try {
            const content = await fs.readFile(path.join(issueDirPath, file), "utf-8");
            const { entry } = this.parseFrontmatter(content);
            if (entry) {
              entryMeta = entry;
              break;
            }
          } catch {
            continue;
          }
        }

        // Extract PRD title from H1 heading in PRD.md
        let prdTitle: string | undefined;
        if (files.includes("PRD.md")) {
          try {
            const prdContent = await fs.readFile(path.join(issueDirPath, "PRD.md"), "utf-8");
            prdTitle = this.extractH1Title(prdContent);
          } catch {
            // ignore
          }
        }

        // Get last-modified date from newest .md file in the directory
        let lastModified: string | undefined;
        for (const file of files) {
          try {
            const stat = await fs.stat(path.join(issueDirPath, file));
            const mtime = stat.mtime.toISOString();
            if (!lastModified || mtime > lastModified) {
              lastModified = mtime;
            }
          } catch {
            // ignore
          }
        }

        categoryEntries.push({
          issue_number: issueNumber,
          slug,
          path: relativeDirPath,
          files,
          ...(entryMeta ? { entry: entryMeta } : {}),
          ...(prdTitle ? { prd_title: prdTitle } : {}),
          ...(lastModified ? { last_modified: lastModified } : {}),
        });

        totalEntries++;
      }

      if (categoryEntries.length > 0) {
        categories[category] = categoryEntries;
      }
    }

    const index: KnowledgeIndex = {
      total_entries: totalEntries,
      generated_at: new Date().toISOString(),
      categories,
    };

    // Validate
    KnowledgeIndexSchema.parse(index);

    // Write README.md
    const readmePath = path.join(knowledgeRoot, "README.md");
    await fs.mkdir(knowledgeRoot, { recursive: true });
    await fs.writeFile(readmePath, this.renderIndexReadme(index), "utf-8");

    return index;
  }

  /**
   * Check whether a knowledge file contains substantive content.
   *
   * Returns false for files that are pure template boilerplate — i.e., only
   * TODO comment placeholders, empty table rows, and markdown structure with
   * no real information. Downstream consumers (feature-dev, feature-planning)
   * should skip reading non-substantive files to avoid wasting tokens.
   *
   * @param filePath - Absolute path or path relative to workspaceRoot
   */
  async isSubstantive(filePath: string): Promise<boolean> {
    const resolvedPath = path.isAbsolute(filePath)
      ? filePath
      : path.join(this.workspaceRoot, filePath);

    let content: string;
    try {
      content = await fs.readFile(resolvedPath, "utf-8");
    } catch {
      return false;
    }

    return KnowledgeService.contentIsSubstantive(content);
  }

  /**
   * Static helper to check content substantiveness without file I/O.
   * Strips markdown structure, HTML comments, empty table rows, status
   * checkboxes, and heading lines. If what remains is <30 chars, the
   * content is considered boilerplate.
   *
   * @internal — implementation detail; use `isSubstantive()` for public access
   */
  static contentIsSubstantive(content: string): boolean {
    const stripped = content
      // Remove HTML comments (including TODO placeholders)
      .replace(/<!--[\s\S]*?-->/g, "")
      // Remove markdown headings
      .replace(/^#+\s.*$/gm, "")
      // Remove ALL markdown table rows (header, separator, and data rows)
      .replace(/^\|.*\|$/gm, "")
      // Remove status checkboxes
      .replace(/^-\s*\[[ x]\]\s*\w+$/gim, "")
      // Remove horizontal rules
      .replace(/^---+$/gm, "")
      // Remove ADR field placeholder lines (e.g. "**Context**: [Background...]")
      .replace(/^\*\*[\w\s]+\*\*:\s*\[.+\]$/gm, "")
      // Collapse whitespace
      .replace(/\s+/g, " ")
      .trim();

    return stripped.length >= 30;
  }

  /**
   * Regenerate PRD.md for an issue from a fresh issue body.
   *
   * Extracts the standard PRD sections (## Summary, ## User Story,
   * ## Acceptance Criteria, ## Technical Approach, ## Quality & Non-Functional
   * Requirements, ## Out of Scope) from `issueBody` and rewrites PRD.md using
   * `update()`. Shares {@link renderPrdBody} with `generatePRD` so the
   * regenerated structure can never drift from the scaffold.
   * Preserves existing frontmatter metadata (created, tags, related_issues).
   * Always bumps the `updated` timestamp. decisions.md is never touched.
   *
   * @param issueNumber   - GitHub issue number
   * @param issueTitle    - Issue title (used in PRD heading)
   * @param issueBody     - Fresh issue body content
   * @param knowledgePath - Path to knowledge directory (relative or absolute)
   */
  async regenerateForIssue(
    issueNumber: number,
    issueTitle: string,
    issueBody: string,
    knowledgePath: string
  ): Promise<KnowledgeRegenResult> {
    const timestamp = new Date().toISOString();

    const resolvedPath = path.isAbsolute(knowledgePath)
      ? knowledgePath
      : path.join(this.workspaceRoot, knowledgePath);

    const prdPath = path.join(resolvedPath, "PRD.md");

    const prdExists = await fs
      .access(prdPath)
      .then(() => true)
      .catch(() => false);

    if (!prdExists) {
      return {
        regenerated: false,
        filesUpdated: [],
        prdUpdated: false,
        decisionsPreserved: true,
        timestamp,
        reason: `PRD.md not found at ${prdPath} — run scaffoldForIssue first`,
      };
    }

    const newBody = this.renderPrdBody(issueNumber, issueTitle, issueBody);

    await this.update(prdPath, newBody, {
      title: `PRD: #${issueNumber} — ${issueTitle}`,
      type: "prd",
      related_issues: [issueNumber],
    });

    const relativePrdPath = path.relative(this.workspaceRoot, prdPath);
    return {
      regenerated: true,
      filesUpdated: [relativePrdPath],
      prdUpdated: true,
      decisionsPreserved: true,
      timestamp,
    };
  }

  /**
   * Remove knowledge directories for closed issues that contain only
   * boilerplate content (no substantive PRD or decisions).
   *
   * @returns Array of removed directory paths (relative to workspaceRoot)
   */
  async pruneEmpty(): Promise<string[]> {
    const knowledgeRoot = path.join(this.workspaceRoot, ".nightgauge", "knowledge");
    const pruned: string[] = [];

    let categoryDirs: string[];
    try {
      const entries = await fs.readdir(knowledgeRoot, {
        withFileTypes: true,
      });
      categoryDirs = entries.filter((e) => e.isDirectory()).map((e) => e.name);
    } catch {
      return pruned;
    }

    for (const category of categoryDirs) {
      const categoryPath = path.join(knowledgeRoot, category);
      let issueDirs: string[];
      try {
        const entries = await fs.readdir(categoryPath, {
          withFileTypes: true,
        });
        issueDirs = entries.filter((e) => e.isDirectory()).map((e) => e.name);
      } catch {
        continue;
      }

      for (const issueDir of issueDirs) {
        const issueDirPath = path.join(categoryPath, issueDir);
        const relativePath = path.relative(this.workspaceRoot, issueDirPath);

        // Check all .md files in the directory
        let files: string[];
        try {
          files = (await fs.readdir(issueDirPath)).filter((f) => f.endsWith(".md"));
        } catch {
          continue;
        }

        let anySubstantive = false;
        for (const file of files) {
          const filePath = path.join(issueDirPath, file);
          if (await this.isSubstantive(filePath)) {
            anySubstantive = true;
            break;
          }
        }

        if (!anySubstantive) {
          await fs.rm(issueDirPath, { recursive: true, force: true });
          pruned.push(relativePath);
        }
      }
    }

    return pruned;
  }

  /**
   * Scaffold a repo-topic knowledge entry (idempotent).
   *
   * Creates `.nightgauge/knowledge/{type}/{slug}.md`. When the category
   * directory is new, also creates `README.md` and `_template.md`. Returns
   * `created: false` when the entry file already exists.
   *
   * Templates match Go `generateRepoTopicTemplate()` output verbatim.
   *
   * @param type - Repo-topic category type (architecture, glossary, runbook, post-mortem)
   * @param slug - URL-safe slug for the entry filename (without .md extension)
   */
  async createRepoTopicEntry(type: RepoTopicType, slug: string): Promise<RepoTopicResult> {
    RepoTopicTypeSchema.parse(type);

    const categoryDir = path.join(this.workspaceRoot, ".nightgauge", "knowledge", type);
    const entryPath = path.join(categoryDir, `${slug}.md`);
    const filesCreated: string[] = [];

    // Detect whether category dir is new before creating it.
    const categoryExists = await fs
      .access(categoryDir)
      .then(() => true)
      .catch(() => false);

    await fs.mkdir(categoryDir, { recursive: true });

    // Idempotent: if entry already exists, return without modifying it.
    const entryExists = await fs
      .access(entryPath)
      .then(() => true)
      .catch(() => false);

    if (entryExists) {
      return {
        knowledge_path: categoryDir,
        file_path: entryPath,
        created: false,
        files_created: [],
      };
    }

    // Create README.md and _template.md when the category dir is brand new.
    if (!categoryExists) {
      const readmePath = path.join(categoryDir, "README.md");
      await fs.writeFile(readmePath, this.generateRepoTopicREADME(type), "utf-8");
      filesCreated.push("README.md");

      const templatePath = path.join(categoryDir, "_template.md");
      await fs.writeFile(templatePath, this.generateRepoTopicTemplate(type, "slug"), "utf-8");
      filesCreated.push("_template.md");
    }

    // Create the entry file.
    await fs.writeFile(entryPath, this.generateRepoTopicTemplate(type, slug), "utf-8");
    filesCreated.push(`${slug}.md`);

    return {
      knowledge_path: categoryDir,
      file_path: entryPath,
      created: true,
      files_created: filesCreated,
    };
  }

  /**
   * Generate README.md content for a repo-topic category directory.
   * Matches Go generateRepoTopicREADME() output verbatim.
   */
  private generateRepoTopicREADME(type: RepoTopicType): string {
    switch (type) {
      case "architecture":
        return `# Knowledge Base — architecture/

Stores cross-issue architectural principles, layer diagrams, and pattern docs.

**What belongs here**: Repo-wide architectural patterns that AI agents should
consult when planning or implementing features. These are agent-facing working
notes, not public documentation (\`docs/ARCHITECTURE.md\` is the human-facing counterpart).

**What does NOT belong here**: Per-issue implementation decisions (use
\`features/{N}-{slug}/decisions.md\`) or content stable enough for \`docs/\`.

**When to add an entry**: When an agent discovers or codifies a pattern that
will be relevant to future pipeline runs.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See \`_template.md\` for the file structure to follow when adding entries.
`;
      case "glossary":
        return `# Knowledge Base — glossary/

Stores one-file-per-term definitions of domain vocabulary used across issues.

**What belongs here**: Domain terms, concepts, and jargon that recur across
issues and that agents should look up rather than re-derive.

**What does NOT belong here**: Per-issue acronyms or transient terminology.
Stable terms that belong in developer docs should graduate to \`docs/\`.

**When to add an entry**: When a term appears in multiple issues or requires
more than one sentence to explain correctly.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See \`_template.md\` for the file structure to follow when adding entries.
`;
      case "runbook":
        return `# Knowledge Base — runbooks/

Stores operational procedures for recurring maintenance tasks and recovery workflows.

**What belongs here**: Step-by-step procedures that agents or operators follow
in response to specific situations (stuck pipelines, stale indices, crashed
processes).

**What does NOT belong here**: One-off fixes or post-mortems (use
\`post-mortems/\`). Stable runbooks should graduate to \`docs/HEALTH_MONITORING.md\` or
a dedicated docs page.

**When to add an entry**: When you resolve an operational problem and want to
preserve the steps for next time.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See \`_template.md\` for the file structure to follow when adding entries.
`;
      case "post-mortem":
        return `# Knowledge Base — post-mortems/

Stores incident write-ups and retrospective analyses.

**What belongs here**: Factual accounts of what went wrong, the root cause,
and action items that resulted from an incident.

**What does NOT belong here**: Recurring operational procedures (use
\`runbooks/\`). Systemic fixes that affect architecture should also be recorded in
\`architecture/\` after the post-mortem.

**When to add an entry**: After any pipeline failure, data loss event, or
unexpected outage that took more than 30 minutes to resolve.

**Author**: Pipeline / AI agents. Humans may edit for clarity.

## Entries

See \`_template.md\` for the file structure to follow when adding entries.
`;
    }
  }

  /**
   * Generate template content for a repo-topic entry.
   * Matches Go generateRepoTopicTemplate() output verbatim.
   */
  private generateRepoTopicTemplate(type: RepoTopicType, slug: string): string {
    const now = new Date().toISOString();
    switch (type) {
      case "architecture":
        return `---
type: architecture
created: "${now}"
tags: [architecture, pattern, layer]
status: draft
---

# ${slug}

## Overview

<!-- TODO: Describe the architectural principle, pattern, or design decision. -->

## Context

<!-- TODO: Why does this exist? What problem does it solve? -->

## Details

<!-- TODO: Technical details, diagrams, file references. -->

## Related

<!-- TODO: Links to docs/, related issues, related architecture entries. -->
`;
      case "glossary":
        return `---
type: glossary
created: "${now}"
tags: [domain-term]
status: draft
---

# ${slug}

## Definition

<!-- TODO: One-sentence definition of the term. -->

## Context

<!-- TODO: Where is this term used? What is the broader context? -->

## Examples

<!-- TODO: Concrete examples of the term in use. -->
`;
      case "runbook":
        return `---
type: runbook
created: "${now}"
tags: [operational, procedure]
status: draft
---

# ${slug}

## Purpose

<!-- TODO: What situation does this runbook address? -->

## Prerequisites

<!-- TODO: What must be true before following this runbook? -->

## Steps

<!-- TODO: Step-by-step procedure. -->

1. Step 1

## Verification

<!-- TODO: How do you know the procedure succeeded? -->

## Rollback

<!-- TODO: How to undo the steps if something goes wrong. -->
`;
      case "post-mortem":
        return `---
type: post-mortem
created: "${now}"
tags: [incident, post-mortem]
status: draft
---

# ${slug}

## Summary

<!-- TODO: One-paragraph description of what happened. -->

## Timeline

<!-- TODO: Chronological list of events. -->

## Root Cause

<!-- TODO: The underlying cause of the incident. -->

## Impact

<!-- TODO: Who was affected? For how long? -->

## Action Items

<!-- TODO: What changes will prevent this from recurring? -->

- [ ] Action item 1

## Lessons Learned

<!-- TODO: Key takeaways for the team. -->
`;
    }
  }

  /**
   * Generate a URL-safe kebab-case slug from an issue title.
   *
   * Lowercases, replaces non-alphanumeric characters with hyphens, strips
   * leading/trailing hyphens, truncates to 50 characters, and strips any
   * trailing hyphen left by truncation (matches the Go binary).
   */
  generateSlug(title: string): string {
    return title
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 50)
      .replace(/-+$/, "");
  }

  /**
   * Generate PRD.md content (frontmatter + body) from the issue body.
   *
   * Delegates the body to {@link renderPrdBody}, which seeds the full PRD
   * structure — Summary, User Story, Acceptance Criteria, Technical Approach
   * (embedded TRD), Quality & Non-Functional Requirements (embedded QRD), Out of
   * Scope, Status — extracting matching sections from the issue body and falling
   * back to guided placeholders when absent.
   *
   * @internal — template generation detail; called only by scaffoldForIssue()
   */
  generatePRD(issueNumber: number, issueTitle: string, issueBody: string): string {
    const now = new Date().toISOString();

    return `---
title: "PRD: #${issueNumber} — ${issueTitle}"
type: prd
created: "${now}"
updated: "${now}"
related_issues: [${issueNumber}]
---

${this.renderPrdBody(issueNumber, issueTitle, issueBody)}`;
  }

  /**
   * Render the PRD markdown body (everything from the `# PRD:` heading down,
   * without frontmatter). Shared by {@link generatePRD} and
   * {@link regenerateForIssue} so the two paths can never drift.
   *
   * The PRD is the single source of truth for an issue's requirements. Technical
   * requirements (the embedded "TRD") live in `## Technical Approach`; quality
   * and non-functional requirements (the embedded "QRD") live in
   * `## Quality & Non-Functional Requirements`. These are sections, not separate
   * files — see docs/KNOWLEDGE_BASE.md#information-architecture for the rationale.
   *
   * Content is extracted from the issue body when the matching section is
   * present; absent sections fall back to a guided placeholder comment. For
   * backward compatibility, `## Technical Approach` also accepts a legacy
   * `## Technical Notes` heading from the issue body.
   *
   * @internal — template generation detail
   */
  private renderPrdBody(issueNumber: number, issueTitle: string, issueBody: string): string {
    const summary = this.extractSection(issueBody, "Summary");
    const userStory = this.extractSection(issueBody, "User Story");
    const acceptanceCriteria = this.extractSection(issueBody, "Acceptance Criteria");
    const technicalApproach =
      this.extractSection(issueBody, "Technical Approach") ||
      this.extractSection(issueBody, "Technical Notes");
    const quality =
      this.extractSection(issueBody, "Quality & Non-Functional Requirements") ||
      this.extractSection(issueBody, "Non-Functional Requirements") ||
      this.extractSection(issueBody, "Quality");
    const outOfScope = this.extractSection(issueBody, "Out of Scope");

    return `# PRD: #${issueNumber} — ${issueTitle}

## Summary

${summary || "<!-- TODO: 1-2 sentence problem statement — what is missing/broken and why it matters -->"}

## User Story

${userStory || "<!-- TODO: As a <role>, I want <capability> so that <benefit>. Omit for pure infra/chore work. -->"}

## Acceptance Criteria

${acceptanceCriteria || "<!-- TODO: Testable checkboxes — each one a behavior feature-validate can verify\n- [ ] Criterion 1\n- [ ] Criterion 2 -->"}

## Technical Approach

${technicalApproach || "<!-- TODO (embedded TRD): design, key components/files, data flow, and implementation constraints.\n     This IS the technical requirements doc — keep it here, do not split into a separate TRD file. -->"}

## Quality & Non-Functional Requirements

${quality || '<!-- TODO (embedded QRD): test strategy (unit/integration/e2e) plus any performance, security,\n     accessibility, or reliability budgets. "None beyond the acceptance criteria" is a valid answer. -->'}

## Out of Scope

${outOfScope || "<!-- TODO: What this issue explicitly will NOT do — names the boundary to prevent scope creep. -->"}

## Status

- [ ] Draft
- [ ] Reviewed
- [ ] Approved
`;
  }

  /**
   * Generate an empty decisions.md template for recording architectural decisions.
   *
   * @internal — template generation detail; called only by scaffoldForIssue()
   */
  generateDecisionsTemplate(issueNumber: number, issueTitle: string): string {
    const now = new Date().toISOString();
    return `---
title: "Decisions: #${issueNumber} — ${issueTitle}"
type: decision
created: "${now}"
updated: "${now}"
related_issues: [${issueNumber}]
---

# Decisions: #${issueNumber} — ${issueTitle}

## Architecture Decisions

<!-- Record key architectural decisions made during implementation.
     Add one ADR block per decision. -->

## ADR-001: [Decision Title]

**Status**: Proposed
**Context**: [Background and constraints that led to this decision]
**Decision**: [What was decided and why]
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
`;
  }

  /**
   * Parse YAML frontmatter from file content.
   *
   * Detects `---` delimited YAML blocks at the start of the content.
   * Returns { entry, body } where entry is the parsed/validated metadata
   * or null if no frontmatter is detected or parsing fails.
   */
  private parseFrontmatter(content: string): KnowledgeReadResult {
    if (!content.startsWith("---\n") && !content.startsWith("---\r\n")) {
      return { entry: null, body: content };
    }

    // Find the closing --- delimiter
    const endIndex = content.indexOf("\n---", 3);
    if (endIndex === -1) {
      return { entry: null, body: content };
    }

    const yamlBlock = content.slice(4, endIndex);
    const body = content.slice(endIndex + 4).replace(/^\r?\n/, "");

    try {
      const parsed = yaml.load(yamlBlock);
      if (typeof parsed !== "object" || parsed === null) {
        return { entry: null, body };
      }

      const result = KnowledgeEntrySchema.safeParse(parsed);
      if (result.success) {
        return { entry: result.data, body };
      }
      return { entry: null, body };
    } catch {
      return { entry: null, body };
    }
  }

  /**
   * Serialize metadata to a YAML frontmatter block.
   */
  private serializeFrontmatter(frontmatter: Partial<KnowledgeEntry>): string {
    const yamlStr = yaml.dump(frontmatter, {
      lineWidth: 100,
      noRefs: true,
      quoteStyle: "double",
    });
    return `---\n${yamlStr}---`;
  }

  /**
   * Map a KnowledgeType to its default filename.
   */
  private typeToFilename(type: KnowledgeType): string {
    return TYPE_FILENAME_MAP[type] || `${type}.md`;
  }

  /**
   * Recursively yield absolute paths to all .md files under the knowledge directory.
   * Skips README.md (the index file).
   */
  private async *walkKnowledgeDirectory(): AsyncGenerator<string> {
    const knowledgeRoot = path.join(this.workspaceRoot, ".nightgauge", "knowledge");

    const rootExists = await fs
      .access(knowledgeRoot)
      .then(() => true)
      .catch(() => false);
    if (!rootExists) return;

    yield* this.walkDirectory(knowledgeRoot);
  }

  /**
   * Recursively walk a directory yielding .md file paths (excluding README.md).
   */
  private async *walkDirectory(dirPath: string): AsyncGenerator<string> {
    let names: string[];
    try {
      names = await fs.readdir(dirPath);
    } catch {
      return;
    }

    for (const name of names) {
      const fullPath = path.join(dirPath, name);
      let stat;
      try {
        stat = await fs.stat(fullPath);
      } catch {
        continue;
      }
      if (stat.isDirectory()) {
        yield* this.walkDirectory(fullPath);
      } else if (stat.isFile() && name.endsWith(".md") && name !== "README.md") {
        yield fullPath;
      }
    }
  }

  /**
   * Render the knowledge index as a Markdown README.
   */
  private renderIndexReadme(index: KnowledgeIndex): string {
    let content = `# Knowledge Base Index\n\n`;
    content += `> Auto-generated by \`KnowledgeService.generateIndex()\`\n`;
    content += `> Generated at: ${index.generated_at}\n\n`;
    content += `**Total entries:** ${index.total_entries}\n\n`;

    for (const [category, entries] of Object.entries(index.categories)) {
      const type = category === "epics" ? "epic" : "feature";
      content += `## ${category}\n\n`;
      content += `| Issue | Type | Title | Last Modified |\n`;
      content += `| ----- | ---- | ----- | ------------- |\n`;
      for (const entry of entries) {
        const extEntry = entry as typeof entry & { prd_title?: string; last_modified?: string };
        const title = extEntry.prd_title ?? entry.slug.replace(/-/g, " ");
        const link = `[#${entry.issue_number}](${entry.path})`;
        const lastMod = extEntry.last_modified ? extEntry.last_modified.slice(0, 10) : "";
        content += `| ${link} | ${type} | ${title} | ${lastMod} |\n`;
      }
      content += `\n`;
    }

    return content;
  }

  /** Extract the first H1 heading text from markdown content. */
  private extractH1Title(content: string): string | undefined {
    const match = content.match(/^#\s+(.+)$/m);
    return match ? match[1].trim() : undefined;
  }

  /**
   * Extract the content of a named section from an issue body.
   *
   * Matches `## SectionName` and captures everything until the next `##`
   * heading or end of string. Returns trimmed content or empty string if not found.
   */
  private extractSection(body: string, sectionName: string): string {
    const escapedName = sectionName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const pattern = new RegExp(`##\\s+${escapedName}\\s*\\n([\\s\\S]*?)(?=\\n##\\s|$)`, "i");
    const match = body.match(pattern);
    if (!match) return "";
    return match[1].trim();
  }
}
