/**
 * wikiLinkResolver - Resolves [[wiki-link]] references in knowledge base markdown files
 *
 * Extracts wiki-links from markdown content and resolves them to absolute file paths
 * within the `.nightgauge/knowledge/` directory. Supports relative references,
 * case-insensitive matching, ambiguous match detection, and cross-repo resolution
 * via `[[repo-name:path]]` syntax.
 *
 * Resolution order:
 *   1. Exact match (path as given, with .md extension)
 *   2. Case-insensitive match
 *   3. Partial filename match
 *
 * For cross-repo links (`[[repo-name:path]]`), resolution targets the sibling
 * repo's `.nightgauge/knowledge/` directory as configured in `workspaceConfig`.
 *
 * @see Issue #1676 - Implement wiki-link resolver utility
 * @see Issue #1697 - Implement cross-repo wiki-link resolution
 * @see docs/KNOWLEDGE_BASE.md - Knowledge directory structure
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

/** A parsed wiki-link extracted from markdown */
export interface WikiLink {
  /** Raw link text between [[ and ]], e.g. "architecture/ADR-001" */
  raw: string;
  /** Full matched string including brackets, e.g. "[[architecture/ADR-001]]" */
  match: string;
  /** Character offset in the source string */
  index: number;
}

/** Result of resolving a single wiki-link */
export interface ResolvedWikiLink {
  /** The raw link text that was resolved */
  link: string;
  /** Whether the target file exists on disk */
  exists: boolean;
  /** Resolved absolute path (best candidate, or first candidate for ambiguous) */
  resolvedPath: string;
  /** All candidate paths when multiple matches found */
  candidates: string[];
  /** Whether multiple files matched (ambiguous resolution) */
  isAmbiguous: boolean;
  /** Present when link used [[repo-name:path]] syntax */
  repoName?: string;
  /** True when link used [[repo-name:path]] cross-repo syntax */
  isCrossRepo?: boolean;
  /** True when resolved via [[#NNNN]] issue-number syntax */
  isIssueRef?: boolean;
  /** Anchor fragment from [[#NNNN#anchor]] syntax, without the # prefix */
  anchor?: string;
  /** True when resolved via [[topic:term]] namespace syntax */
  isTopicRef?: boolean;
  /** When resolved via a workspace namespace ([[product:x]], [[cross-repo:x]], [[architecture:x]]) — the namespace name */
  workspaceNamespace?: "product" | "cross-repo" | "architecture";
}

/** Minimal workspace repository entry used for cross-repo link resolution */
interface RepoEntry {
  name: string;
  path: string;
}

/** Optional workspace config subset used for cross-repo resolution */
export interface CrossRepoConfig {
  repositories?: RepoEntry[];
  knowledge?: { cross_repo_links?: boolean };
}

const WIKI_LINK_REGEX = /\[\[([^\]]+)\]\]/g;
const KNOWLEDGE_DIR = ".nightgauge/knowledge";

/**
 * Literal workspace-namespace prefixes recognized by the resolver. Adding a
 * fourth namespace requires a code change — intentional so unknown prefixes
 * fall through to existing `repo-name:` / relative-path behavior with
 * actionable error messages.
 */
const WORKSPACE_NAMESPACES = ["product", "cross-repo", "architecture"] as const;
type WorkspaceNamespace = (typeof WORKSPACE_NAMESPACES)[number];

/**
 * Extracts all [[wiki-links]] from a markdown string.
 *
 * @param content - Markdown string to scan
 * @returns Array of extracted wiki-links with their positions
 */
export function extractWikiLinks(content: string): WikiLink[] {
  const links: WikiLink[] = [];
  let match: RegExpExecArray | null;
  const regex = new RegExp(WIKI_LINK_REGEX.source, "g");

  while ((match = regex.exec(content)) !== null) {
    links.push({
      raw: match[1].trim(),
      match: match[0],
      index: match.index,
    });
  }

  return links;
}

/**
 * Resolves a single wiki-link to an absolute file path.
 *
 * Resolution order:
 *   1. Exact match relative to knowledge root or from-file directory
 *   2. Case-insensitive match within knowledge root
 *   3. Partial filename match within knowledge root
 *
 * For cross-repo links (`[[repo-name:path]]`), delegates to `resolveCrossRepoLink`
 * using the sibling repo's knowledge directory from `workspaceConfig`.
 *
 * @param link - The raw link text (e.g. "architecture/ADR-001" or "platform:architecture/ADR-001")
 * @param fromFile - Absolute path of the file containing the wiki-link
 * @param workspaceRoot - Absolute path to the workspace root
 * @param workspaceConfig - Optional workspace config for cross-repo resolution
 * @returns Resolved wiki-link with existence check and all candidates
 */
export async function resolveWikiLink(
  link: string,
  fromFile: string,
  workspaceRoot: string,
  workspaceConfig?: CrossRepoConfig
): Promise<ResolvedWikiLink> {
  const normalizedLink = link.trim();

  // 1. Issue-ref: [[#NNNN]] or [[#NNNN#anchor]] — must check before cross-repo colon logic
  if (normalizedLink.startsWith("#")) {
    return resolveIssueRef(normalizedLink, workspaceRoot);
  }

  // 2. Topic-ref: [[topic:glossary-term]] — must check before cross-repo colon logic
  if (normalizedLink.startsWith("topic:")) {
    return resolveTopicRef(normalizedLink, workspaceRoot);
  }

  // 3. Workspace-namespace refs: [[product:slug]], [[cross-repo:slug]], [[architecture:slug]].
  //    Must check BEFORE generic repo-name:path because `cross-repo:` overlaps that syntax.
  for (const ns of WORKSPACE_NAMESPACES) {
    if (normalizedLink.startsWith(`${ns}:`)) {
      return resolveWorkspaceNamespaceRef(ns, normalizedLink, workspaceRoot);
    }
  }

  // 4. Cross-repo: [[repo-name:path]]
  const colonIndex = normalizedLink.indexOf(":");
  const hasCrossRepoSyntax = colonIndex > 0;

  if (hasCrossRepoSyntax && workspaceConfig) {
    const repoName = normalizedLink.slice(0, colonIndex);
    const linkPath = normalizedLink.slice(colonIndex + 1);
    return resolveCrossRepoLink(repoName, linkPath, workspaceRoot, workspaceConfig);
  }

  const knowledgeRoot = path.join(workspaceRoot, KNOWLEDGE_DIR);
  const fromDir = path.dirname(fromFile);

  // Ensure .md extension
  const withExt = normalizedLink.endsWith(".md") ? normalizedLink : `${normalizedLink}.md`;

  // Candidate resolution strategies
  const candidates: string[] = [];

  // 1. Exact match: relative to the file containing the link
  const relativeToFrom = path.resolve(fromDir, withExt);
  const exactRelative = await fileExists(relativeToFrom);
  if (exactRelative) {
    candidates.push(relativeToFrom);
  }

  // 2. Exact match: relative to knowledge root
  const relativeToKnowledge = path.join(knowledgeRoot, withExt);
  const exactKnowledge = await fileExists(relativeToKnowledge);
  if (exactKnowledge && relativeToKnowledge !== relativeToFrom) {
    candidates.push(relativeToKnowledge);
  }

  // 3. Fuzzy matches within knowledge root (case-insensitive + partial)
  if (candidates.length === 0) {
    const fuzzyMatches = await findFuzzyMatches(withExt, knowledgeRoot);
    candidates.push(...fuzzyMatches);
  }

  const resolvedPath = candidates[0] ?? relativeToKnowledge;
  const exists = candidates.length > 0 && (await fileExists(resolvedPath));

  return {
    link: normalizedLink,
    exists,
    resolvedPath,
    candidates,
    isAmbiguous: candidates.length > 1,
  };
}

/**
 * Resolves a cross-repo wiki-link using the workspace config's repositories array.
 *
 * @param repoName - Repository name from [[repo-name:path]] syntax
 * @param linkPath - Path portion after the colon
 * @param workspaceRoot - Absolute path to the workspace root
 * @param config - Workspace config containing repository entries
 * @returns Resolved wiki-link with isCrossRepo=true and repoName set
 */
async function resolveCrossRepoLink(
  repoName: string,
  linkPath: string,
  workspaceRoot: string,
  config: CrossRepoConfig
): Promise<ResolvedWikiLink> {
  const repoEntry = config.repositories?.find((r) => r.name === repoName);

  if (!repoEntry) {
    return {
      link: `${repoName}:${linkPath}`,
      exists: false,
      resolvedPath: "",
      candidates: [],
      isAmbiguous: false,
      isCrossRepo: true,
      repoName,
    };
  }

  const repoKnowledgeRoot = path.resolve(workspaceRoot, repoEntry.path, KNOWLEDGE_DIR);

  const withExt = linkPath.endsWith(".md") ? linkPath : `${linkPath}.md`;

  // Exact match within sibling repo's knowledge root
  const exactPath = path.join(repoKnowledgeRoot, withExt);
  const candidates: string[] = [];

  if (await fileExists(exactPath)) {
    candidates.push(exactPath);
  }

  // Fuzzy match if no exact hit
  if (candidates.length === 0) {
    const fuzzyMatches = await findFuzzyMatches(withExt, repoKnowledgeRoot);
    candidates.push(...fuzzyMatches);
  }

  const resolvedPath = candidates[0] ?? exactPath;
  const exists = candidates.length > 0 && (await fileExists(resolvedPath));

  return {
    link: `${repoName}:${linkPath}`,
    exists,
    resolvedPath,
    candidates,
    isAmbiguous: candidates.length > 1,
    isCrossRepo: true,
    repoName,
  };
}

/**
 * Finds fuzzy matches for a link within the knowledge root directory.
 * Returns case-insensitive exact matches first, then partial filename matches.
 *
 * @param withExt - Link with .md extension
 * @param knowledgeRoot - Absolute path to knowledge root directory
 * @returns Sorted list of matching absolute paths
 */
async function findFuzzyMatches(withExt: string, knowledgeRoot: string): Promise<string[]> {
  const targetBasename = path.basename(withExt).toLowerCase();

  let allFiles: string[];
  try {
    allFiles = await collectMarkdownFiles(knowledgeRoot);
  } catch {
    return [];
  }

  const caseInsensitive: string[] = [];
  const partial: string[] = [];

  for (const file of allFiles) {
    const basename = path.basename(file).toLowerCase();
    if (basename === targetBasename) {
      caseInsensitive.push(file);
    } else if (basename.includes(targetBasename.replace(".md", ""))) {
      partial.push(file);
    }
  }

  return [...caseInsensitive, ...partial];
}

/**
 * Recursively collects all .md files under a directory.
 */
async function collectMarkdownFiles(dir: string): Promise<string[]> {
  const entries = await fs.readdir(dir, { withFileTypes: true });
  const files: string[] = [];

  for (const entry of entries) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await collectMarkdownFiles(full)));
    } else if (entry.isFile() && entry.name.endsWith(".md")) {
      files.push(full);
    }
  }

  return files;
}

/**
 * Resolves [[#NNNN]] or [[#NNNN#anchor]] by scanning knowledge/features/ and
 * knowledge/epics/ for a directory whose name starts with "{issueNumber}-".
 */
async function resolveIssueRef(link: string, workspaceRoot: string): Promise<ResolvedWikiLink> {
  // Strip leading #, then split on # to extract optional anchor.
  const withoutHash = link.slice(1);
  const hashIdx = withoutHash.indexOf("#");
  const issueNum = hashIdx >= 0 ? withoutHash.slice(0, hashIdx) : withoutHash;
  const anchor = hashIdx >= 0 ? withoutHash.slice(hashIdx + 1) : undefined;

  const prefix = `${issueNum}-`;
  const knowledgeRoot = path.join(workspaceRoot, KNOWLEDGE_DIR);

  for (const category of ["features", "epics"]) {
    const categoryDir = path.join(knowledgeRoot, category);
    let entries: { name: string; isDirectory(): boolean }[];
    try {
      entries = await fs.readdir(categoryDir, { withFileTypes: true });
    } catch {
      continue;
    }

    for (const entry of entries) {
      if (entry.isDirectory() && entry.name.startsWith(prefix)) {
        const resolvedPath = anchor
          ? `${path.join(categoryDir, entry.name)}#${anchor}`
          : path.join(categoryDir, entry.name);
        return {
          link,
          exists: true,
          resolvedPath,
          candidates: [path.join(categoryDir, entry.name)],
          isAmbiguous: false,
          isIssueRef: true,
          anchor,
        };
      }
    }
  }

  return {
    link,
    exists: false,
    resolvedPath: "",
    candidates: [],
    isAmbiguous: false,
    isIssueRef: true,
    anchor,
  };
}

/**
 * Resolves [[product:slug]], [[cross-repo:slug]], or [[architecture:slug]]
 * to <workspaceRoot>/.nightgauge/knowledge/<namespace>/<slug>.md.
 */
async function resolveWorkspaceNamespaceRef(
  namespace: WorkspaceNamespace,
  link: string,
  workspaceRoot: string
): Promise<ResolvedWikiLink> {
  let slug = link.slice(namespace.length + 1).trim();
  if (!slug.endsWith(".md")) {
    slug = `${slug}.md`;
  }
  const resolvedPath = path.join(workspaceRoot, KNOWLEDGE_DIR, namespace, slug);
  const exists = await fileExists(resolvedPath);
  return {
    link,
    exists,
    resolvedPath,
    candidates: exists ? [resolvedPath] : [],
    isAmbiguous: false,
    workspaceNamespace: namespace,
  };
}

/**
 * Resolves [[topic:glossary-term]] by checking knowledge/glossary/{term}.md.
 * Gracefully degrades to exists=false when the glossary file is not found.
 */
async function resolveTopicRef(link: string, workspaceRoot: string): Promise<ResolvedWikiLink> {
  const term = link.slice("topic:".length);
  const glossaryPath = path.join(workspaceRoot, KNOWLEDGE_DIR, "glossary", `${term}.md`);
  const exists = await fileExists(glossaryPath);
  return {
    link,
    exists,
    resolvedPath: glossaryPath,
    candidates: exists ? [glossaryPath] : [],
    isAmbiguous: false,
    isTopicRef: true,
  };
}

/**
 * Renders all [[wiki-links]] in content to Markdown links.
 * Broken links (exists=false) are preserved as-is; a warning is added to the returned array.
 *
 * Display text rules:
 * - [[#NNNN]]        → #NNNN
 * - [[#NNNN#anchor]] → #NNNN § anchor
 * - [[topic:term]]   → term
 * - [[repo:path]]    → repo:basename
 * - [[relative/path]]→ basename without .md
 */
export async function renderWikiLinks(
  content: string,
  fromFile: string,
  workspaceRoot: string,
  workspaceConfig?: CrossRepoConfig
): Promise<{ rendered: string; warnings: string[] }> {
  const links = extractWikiLinks(content);
  const warnings: string[] = [];

  if (links.length === 0) {
    return { rendered: content, warnings };
  }

  // Resolve all links concurrently.
  const resolved = await Promise.all(
    links.map((l) => resolveWikiLink(l.raw, fromFile, workspaceRoot, workspaceConfig))
  );

  // Build output by replacing occurrences right-to-left to preserve indices.
  let rendered = content;
  for (let i = links.length - 1; i >= 0; i--) {
    const wl = links[i];
    const r = resolved[i];

    if (!r.exists) {
      warnings.push(`broken wiki-link [[${wl.raw}]]: target not found`);
      continue; // keep the raw [[...]] in output
    }

    const display = getDisplayText(wl.raw, r);
    const mdLink = `[${display}](${r.resolvedPath})`;
    rendered = rendered.slice(0, wl.index) + mdLink + rendered.slice(wl.index + wl.match.length);
  }

  return { rendered, warnings };
}

/** Derives display text for a resolved wiki-link. */
function getDisplayText(raw: string, resolved: ResolvedWikiLink): string {
  if (resolved.isIssueRef) {
    const withoutHash = raw.slice(1);
    const hashIdx = withoutHash.indexOf("#");
    if (hashIdx >= 0) {
      const issueNum = withoutHash.slice(0, hashIdx);
      const anchor = withoutHash.slice(hashIdx + 1);
      return `#${issueNum} § ${anchor}`;
    }
    return raw; // already starts with #
  }

  if (resolved.isTopicRef) {
    return raw.slice("topic:".length);
  }

  if (resolved.workspaceNamespace) {
    const slug = raw.slice(resolved.workspaceNamespace.length + 1).replace(/\.md$/, "");
    return slug;
  }

  if (resolved.isCrossRepo && resolved.repoName) {
    const linkPath = raw.slice(resolved.repoName.length + 1);
    const base = path.basename(linkPath).replace(/\.md$/, "");
    return `${resolved.repoName}:${base}`;
  }

  // Relative path — basename without extension.
  return path.basename(raw).replace(/\.md$/, "");
}

/** Checks whether a file exists at the given absolute path. */
async function fileExists(filePath: string): Promise<boolean> {
  try {
    await fs.access(filePath);
    return true;
  } catch {
    return false;
  }
}
