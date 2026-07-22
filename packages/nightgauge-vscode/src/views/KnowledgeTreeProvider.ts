/**
 * KnowledgeTreeProvider — TreeDataProvider for the unified Knowledge sidebar.
 *
 * Rewired in #2964 to a three-section model:
 *
 *   📌 Active Issue (#N)
 *     ├── PRD.md          (highlighted when in planning.knowledge_read)
 *     ├── decisions.md    (highlighted when in planning.knowledge_read)
 *     └── outcomes.md     (when present)
 *   🔗 Related Decisions
 *     ├── <recall hit 1>
 *     └── …
 *   🔍 Search Results
 *     └── (empty until "Nightgauge: Search Knowledge" runs)
 *
 * The Related section pulls from `knowledge.relatedToIssue` IPC; the Search
 * section is populated by the `searchKnowledge` command.
 *
 * @see Issue #2964
 * @see docs/KNOWLEDGE_BASE.md
 */

import * as vscode from "vscode";
import * as fs from "node:fs";
import * as path from "node:path";
import { BaseTreeItem } from "./items/BaseTreeItem";
import { KnowledgeSectionItem, type KnowledgeSectionKind } from "./items/KnowledgeSectionItem";
import { KnowledgeActiveFileItem } from "./items/KnowledgeActiveFileItem";
import { KnowledgeSearchResultItem } from "./items/KnowledgeSearchResultItem";
import type { IpcClient } from "../services/IpcClient";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { KnowledgeRecallHit } from "../services/IpcClientBase";

const RELATED_LIMIT = 10;
const WATCHER_DEBOUNCE_MS = 500;
const ACTIVE_ISSUE_FILES = ["PRD.md", "decisions.md", "outcomes.md"];

class KnowledgeEmptyItem extends BaseTreeItem {
  constructor(message: string) {
    super(message, vscode.TreeItemCollapsibleState.None);
    this.contextValue = "knowledgeEmpty";
    this.iconPath = new vscode.ThemeIcon("info");
  }
}

export class KnowledgeTreeProvider
  implements vscode.TreeDataProvider<BaseTreeItem>, vscode.Disposable
{
  private readonly _onDidChangeTreeData = new vscode.EventEmitter<
    BaseTreeItem | undefined | void
  >();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private searchResults: KnowledgeRecallHit[] = [];
  private relatedCache: { issueNumber: number; hits: KnowledgeRecallHit[] } | null = null;
  private watcher: vscode.FileSystemWatcher | null = null;
  private debounceTimer: NodeJS.Timeout | null = null;
  private readonly _disposables: vscode.Disposable[] = [];

  constructor(
    private workspaceRoot: string,
    private readonly pipelineStateService: PipelineStateService,
    private readonly ipcClient: IpcClient
  ) {
    const stateDisposable = pipelineStateService.onStateChanged(() => {
      this.relatedCache = null;
      this._onDidChangeTreeData.fire();
    });
    this._disposables.push(stateDisposable);

    this.initializeWatcher();
  }

  private initializeWatcher(): void {
    if (!this.workspaceRoot) return;
    try {
      const pattern = new vscode.RelativePattern(
        this.workspaceRoot,
        ".nightgauge/knowledge/**/*.md"
      );
      this.watcher = vscode.workspace.createFileSystemWatcher(pattern);
      const handle = () => this.handleFileChange();
      this.watcher.onDidCreate(handle);
      this.watcher.onDidChange(handle);
      this.watcher.onDidDelete(handle);
    } catch {
      // Graceful degradation — watcher errors must not block the tree.
    }
  }

  private handleFileChange(): void {
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => {
      this.debounceTimer = null;
      this.refresh();
    }, WATCHER_DEBOUNCE_MS);
  }

  getTreeItem(element: BaseTreeItem): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: BaseTreeItem): Promise<BaseTreeItem[]> {
    if (!element) {
      return this.buildSections();
    }
    if (element instanceof KnowledgeSectionItem) {
      return this.getSectionChildren(element.sectionKind);
    }
    return element.getChildren();
  }

  private buildSections(): BaseTreeItem[] {
    const issueNumber = this.pipelineStateService.getActiveIssueBlockingPickup();
    const activeLabel = issueNumber !== null ? `Active Issue (#${issueNumber})` : "Active Issue";

    return [
      new KnowledgeSectionItem(activeLabel, "active-issue"),
      new KnowledgeSectionItem("Related Decisions", "related"),
      new KnowledgeSectionItem(
        "Search Results",
        "search",
        this.searchResults.length > 0
          ? vscode.TreeItemCollapsibleState.Expanded
          : vscode.TreeItemCollapsibleState.Collapsed
      ),
    ];
  }

  private async getSectionChildren(kind: KnowledgeSectionKind): Promise<BaseTreeItem[]> {
    switch (kind) {
      case "active-issue":
        return this.getActiveIssueChildren();
      case "related":
        return this.getRelatedChildren();
      case "search":
        return this.getSearchChildren();
    }
  }

  private getActiveIssueChildren(): BaseTreeItem[] {
    const issueNumber = this.pipelineStateService.getActiveIssueBlockingPickup();
    if (issueNumber === null) {
      return [new KnowledgeEmptyItem("No active issue — pick one via /nightgauge:issue-pickup")];
    }

    const knowledgePath = this.resolveKnowledgePath(issueNumber);
    if (!knowledgePath) {
      return [new KnowledgeEmptyItem("No knowledge base scaffolded for this issue")];
    }

    const readSet = this.readKnowledgeReadSet(issueNumber, knowledgePath);

    const items: BaseTreeItem[] = [];
    for (const name of ACTIVE_ISSUE_FILES) {
      const filePath = path.join(knowledgePath, name);
      if (!fs.existsSync(filePath)) continue;
      const rel = path.relative(this.workspaceRoot, filePath);
      const highlighted = readSet.has(rel) || readSet.has(filePath) || readSet.has(name);
      items.push(new KnowledgeActiveFileItem(filePath, highlighted));
    }

    if (items.length === 0) {
      return [new KnowledgeEmptyItem("Knowledge directory exists but is empty")];
    }
    return items;
  }

  private async getRelatedChildren(): Promise<BaseTreeItem[]> {
    const issueNumber = this.pipelineStateService.getActiveIssueBlockingPickup();
    if (issueNumber === null) {
      return [new KnowledgeEmptyItem("No active issue")];
    }
    if (this.relatedCache && this.relatedCache.issueNumber === issueNumber) {
      return this.toSearchItems(this.relatedCache.hits);
    }
    try {
      const result = await this.ipcClient.knowledgeRelatedToIssue(issueNumber, RELATED_LIMIT);
      const hits = result.hits ?? [];
      this.relatedCache = { issueNumber, hits };
      if (hits.length === 0) {
        return [new KnowledgeEmptyItem("No related decisions found")];
      }
      return this.toSearchItems(hits);
    } catch {
      return [new KnowledgeEmptyItem("Related decisions unavailable")];
    }
  }

  private getSearchChildren(): BaseTreeItem[] {
    if (this.searchResults.length === 0) {
      return [
        new KnowledgeEmptyItem('Run "Nightgauge: Search Knowledge" to populate this section'),
      ];
    }
    return this.toSearchItems(this.searchResults);
  }

  private toSearchItems(hits: KnowledgeRecallHit[]): BaseTreeItem[] {
    return hits.map((h) => new KnowledgeSearchResultItem(h, this.workspaceRoot));
  }

  /**
   * Replace the Search section's contents and reveal it. Called by the
   * `searchKnowledge` command after the user enters a query.
   */
  setSearchResults(hits: KnowledgeRecallHit[]): void {
    this.searchResults = hits;
    this._onDidChangeTreeData.fire();
  }

  /** Clear the Search section. */
  clearSearchResults(): void {
    this.searchResults = [];
    this._onDidChangeTreeData.fire();
  }

  /**
   * Resolve the knowledge directory for the given issue by reading
   * `.nightgauge/pipeline/issue-{N}.json` and (optionally) `planning-{N}.json`.
   */
  private resolveKnowledgePath(issueNumber: number): string | null {
    const candidates = [
      path.join(this.workspaceRoot, ".nightgauge", "pipeline", `issue-${issueNumber}.json`),
      path.join(this.workspaceRoot, ".nightgauge", "pipeline", `planning-${issueNumber}.json`),
    ];
    for (const f of candidates) {
      try {
        const raw = fs.readFileSync(f, "utf8");
        const parsed = JSON.parse(raw) as { knowledge_path?: string | null };
        if (parsed.knowledge_path) {
          return path.isAbsolute(parsed.knowledge_path)
            ? parsed.knowledge_path
            : path.join(this.workspaceRoot, parsed.knowledge_path);
        }
      } catch {
        // Continue to next candidate.
      }
    }
    return null;
  }

  /**
   * Read `planning-{N}.json.knowledge_read` as a Set of path strings. Returns
   * an empty set when the field is missing — Active Issue files render without
   * highlights in that case.
   */
  private readKnowledgeReadSet(issueNumber: number, knowledgePath: string): Set<string> {
    const planningFile = path.join(
      this.workspaceRoot,
      ".nightgauge",
      "pipeline",
      `planning-${issueNumber}.json`
    );
    try {
      const raw = fs.readFileSync(planningFile, "utf8");
      const parsed = JSON.parse(raw) as { knowledge_read?: string[] | null };
      const arr = parsed.knowledge_read ?? [];
      const set = new Set<string>();
      for (const p of arr) {
        set.add(p);
        // Also store both basename and joined path to maximize match success
        // against the variant the planning agent recorded (basename, rel, abs).
        set.add(path.basename(p));
        if (!path.isAbsolute(p)) {
          set.add(path.join(this.workspaceRoot, p));
        }
        set.add(path.relative(this.workspaceRoot, path.join(knowledgePath, path.basename(p))));
      }
      return set;
    } catch {
      return new Set();
    }
  }

  refresh(): void {
    this.relatedCache = null;
    this._onDidChangeTreeData.fire();
  }

  updateWorkspaceRoot(workspaceRoot: string): void {
    this.workspaceRoot = workspaceRoot;
    this.searchResults = [];
    this.relatedCache = null;
    if (this.watcher) {
      this.watcher.dispose();
      this.watcher = null;
    }
    this.initializeWatcher();
    this._onDidChangeTreeData.fire();
  }

  dispose(): void {
    if (this.watcher) {
      this.watcher.dispose();
      this.watcher = null;
    }
    if (this.debounceTimer) {
      clearTimeout(this.debounceTimer);
      this.debounceTimer = null;
    }
    for (const d of this._disposables) d.dispose();
    this._disposables.length = 0;
    this._onDidChangeTreeData.dispose();
  }
}
