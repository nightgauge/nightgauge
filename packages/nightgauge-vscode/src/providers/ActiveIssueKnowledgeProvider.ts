/**
 * ActiveIssueKnowledgeProvider — TreeDataProvider for the Active Issue Knowledge panel.
 *
 * Shows the current pipeline issue's PRD.md, decisions.md, and a recall-powered
 * "Related Decisions" feed via the `knowledge.relatedToIssue` IPC endpoint.
 *
 * Migrated in #2964 from `execFileAsync` subprocess to IPC — see ADR-002 in the
 * issue's decisions.md. The 60s per-issue cache stays to reduce IPC traffic.
 *
 * @see Issue #3599 — original provider
 * @see Issue #2964 — IPC migration + KnowledgeTreeProvider rewire
 */

import * as vscode from "vscode";
import * as fs from "node:fs";
import * as path from "node:path";
import { BaseTreeItem } from "../views/items/BaseTreeItem";
import {
  ActiveIssueKnowledgeSectionItem,
  ActiveIssueKnowledgeFileItem,
  ActiveIssueKnowledgeRecallItem,
  ActiveIssueKnowledgeEmptyItem,
} from "./items/ActiveIssueKnowledgeTreeItem";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { IpcClient } from "../services/IpcClient";
import type { KnowledgeRecallHit } from "../services/IpcClientBase";

const RECALL_CACHE_TTL_MS = 60_000;
const RECALL_LIMIT = 10;
const WATCHER_DEBOUNCE_MS = 500;

interface RecallCacheEntry {
  items: (ActiveIssueKnowledgeRecallItem | ActiveIssueKnowledgeEmptyItem)[];
  expiresAt: number;
}

export class ActiveIssueKnowledgeProvider
  implements vscode.TreeDataProvider<BaseTreeItem>, vscode.Disposable
{
  private readonly _onDidChangeTreeData = new vscode.EventEmitter<
    BaseTreeItem | undefined | void
  >();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private watcher: vscode.FileSystemWatcher | null = null;
  private debounceTimer: NodeJS.Timeout | null = null;
  private readonly _disposables: vscode.Disposable[] = [];

  /** Per-issue recall cache keyed by issue number */
  private readonly recallCache = new Map<number, RecallCacheEntry>();
  /** Currently watched knowledge path (to avoid duplicate watchers) */
  private watchedKnowledgePath: string | null = null;

  constructor(
    private readonly workspaceRoot: string,
    private readonly pipelineStateService: PipelineStateService,
    private readonly ipcClient: IpcClient
  ) {
    // Subscribe to pipeline state changes so the panel refreshes when the
    // active issue changes (e.g. on issue-pickup or pipeline-finish).
    const stateDisposable = pipelineStateService.onStateChanged((state) => {
      const issueNumber = state?.issue_number ?? null;
      vscode.commands.executeCommand("setContext", "nightgauge.activeIssue", issueNumber !== null);
      this.refresh();
    });
    this._disposables.push(stateDisposable);

    // Initialize context key from current state (cold start).
    const currentIssue = pipelineStateService.getActiveIssueBlockingPickup();
    vscode.commands.executeCommand("setContext", "nightgauge.activeIssue", currentIssue !== null);
  }

  getTreeItem(element: BaseTreeItem): vscode.TreeItem {
    return element;
  }

  async getChildren(element?: BaseTreeItem): Promise<BaseTreeItem[]> {
    if (element) {
      if (element instanceof ActiveIssueKnowledgeSectionItem) {
        return this.getSectionChildren(element);
      }
      return element.getChildren();
    }
    return this.getRootItems();
  }

  private async getRootItems(): Promise<BaseTreeItem[]> {
    const issueNumber = this.pipelineStateService.getActiveIssueBlockingPickup();
    if (issueNumber === null) {
      return [
        new ActiveIssueKnowledgeEmptyItem(
          "No active issue. Pick one with /nightgauge:issue-pickup"
        ),
      ];
    }

    const knowledgePath = this.readKnowledgePath(issueNumber);
    if (!knowledgePath) {
      return [new ActiveIssueKnowledgeEmptyItem("No knowledge base for this issue")];
    }

    this.initializeWatcher(knowledgePath);

    const sections: BaseTreeItem[] = [];

    const prdFile = path.join(knowledgePath, "PRD.md");
    const prdSection = new ActiveIssueKnowledgeSectionItem("PRD", "prd");
    if (fs.existsSync(prdFile)) {
      prdSection.addChild(new ActiveIssueKnowledgeFileItem(prdFile));
    } else {
      prdSection.addChild(new ActiveIssueKnowledgeEmptyItem("PRD.md not found"));
    }
    sections.push(prdSection);

    const decisionsFile = path.join(knowledgePath, "decisions.md");
    const decisionsSection = new ActiveIssueKnowledgeSectionItem("Decisions", "decisions");
    if (fs.existsSync(decisionsFile)) {
      decisionsSection.addChild(new ActiveIssueKnowledgeFileItem(decisionsFile));
    } else {
      decisionsSection.addChild(new ActiveIssueKnowledgeEmptyItem("No decisions yet"));
    }
    sections.push(decisionsSection);

    const recallSection = new ActiveIssueKnowledgeSectionItem("Related Decisions", "recall");
    // Children loaded lazily in getSectionChildren when the user expands the node.
    sections.push(recallSection);

    return sections;
  }

  private async getSectionChildren(
    section: ActiveIssueKnowledgeSectionItem
  ): Promise<BaseTreeItem[]> {
    if (section.sectionKind !== "recall") {
      return section.getChildren();
    }
    return this.getRecallChildren();
  }

  private async getRecallChildren(): Promise<BaseTreeItem[]> {
    const issueNumber = this.pipelineStateService.getActiveIssueBlockingPickup();
    if (issueNumber === null) return [];

    const cached = this.recallCache.get(issueNumber);
    if (cached && Date.now() < cached.expiresAt) {
      return cached.items;
    }

    const items = await this.fetchRecallItems(issueNumber);
    this.recallCache.set(issueNumber, { items, expiresAt: Date.now() + RECALL_CACHE_TTL_MS });
    return items;
  }

  private async fetchRecallItems(
    issueNumber: number
  ): Promise<(ActiveIssueKnowledgeRecallItem | ActiveIssueKnowledgeEmptyItem)[]> {
    try {
      const result = await this.ipcClient.knowledgeRelatedToIssue(issueNumber, RECALL_LIMIT);
      const hits: KnowledgeRecallHit[] = result.hits ?? [];
      if (hits.length === 0) {
        return [new ActiveIssueKnowledgeEmptyItem("No related decisions found")];
      }
      return hits.map(
        (h) =>
          new ActiveIssueKnowledgeRecallItem({
            path: h.path,
            snippet: h.snippet,
            score: h.score,
            issue_number: h.issue_number,
          })
      );
    } catch {
      return [new ActiveIssueKnowledgeEmptyItem("Recall unavailable")];
    }
  }

  private readKnowledgePath(issueNumber: number): string | null {
    const contextFile = path.join(
      this.workspaceRoot,
      ".nightgauge",
      "pipeline",
      `issue-${issueNumber}.json`
    );
    try {
      const raw = fs.readFileSync(contextFile, "utf8");
      const parsed = JSON.parse(raw) as { knowledge_path?: string | null };
      if (!parsed.knowledge_path) return null;
      return path.isAbsolute(parsed.knowledge_path)
        ? parsed.knowledge_path
        : path.join(this.workspaceRoot, parsed.knowledge_path);
    } catch {
      return null;
    }
  }

  private initializeWatcher(knowledgePath: string): void {
    if (this.watchedKnowledgePath === knowledgePath) return;
    this.watchedKnowledgePath = knowledgePath;

    if (this.watcher) {
      this.watcher.dispose();
      this.watcher = null;
    }

    try {
      const pattern = new vscode.RelativePattern(knowledgePath, "**/*.md");
      this.watcher = vscode.workspace.createFileSystemWatcher(pattern);
      const handleChange = () => this.handleFileChange();
      this.watcher.onDidCreate(handleChange);
      this.watcher.onDidChange(handleChange);
      this.watcher.onDidDelete(handleChange);
    } catch {
      // Graceful degradation
    }
  }

  private handleFileChange(): void {
    if (this.debounceTimer) clearTimeout(this.debounceTimer);
    this.debounceTimer = setTimeout(() => {
      this.debounceTimer = null;
      this.refresh();
    }, WATCHER_DEBOUNCE_MS);
  }

  refresh(): void {
    this.recallCache.clear();
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
