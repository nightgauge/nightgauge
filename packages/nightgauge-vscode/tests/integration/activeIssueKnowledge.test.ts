/**
 * Integration test for ActiveIssueKnowledgeProvider.
 *
 * Creates a fixture workspace with a seeded knowledge directory and verifies
 * the provider renders the expected tree structure.
 *
 * @see Issue #3599
 */

import { describe, it, expect, vi, beforeAll, afterAll } from "vitest";
import * as os from "node:os";
import * as path from "node:path";
import * as fs from "node:fs";
import { ActiveIssueKnowledgeProvider } from "../../src/providers/ActiveIssueKnowledgeProvider";
import {
  ActiveIssueKnowledgeSectionItem,
  ActiveIssueKnowledgeFileItem,
} from "../../src/providers/items/ActiveIssueKnowledgeTreeItem";
import type { PipelineStateService } from "../../src/services/PipelineStateService";

// ---------------------------------------------------------------------------
// VSCode mock (same as unit tests)
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  const EventEmitter = vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  });
  return {
    EventEmitter,
    TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
    TreeItem: vi.fn(function (label: string, collapsibleState: number) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }),
    ThemeIcon: vi.fn(function (id: string) {
      this.id = id;
    }),
    ThemeColor: vi.fn(function (id: string) {
      this.id = id;
    }),
    Uri: { file: (p: string) => ({ fsPath: p }) },
    workspace: {
      createFileSystemWatcher: vi.fn(() => ({
        onDidCreate: vi.fn(),
        onDidChange: vi.fn(),
        onDidDelete: vi.fn(),
        dispose: vi.fn(),
      })),
    },
    RelativePattern: vi.fn(function (base: string, pattern: string) {
      this.base = base;
      this.pattern = pattern;
    }),
    commands: { executeCommand: vi.fn() },
  };
});

vi.mock("node:child_process", () => ({
  execFile: vi.fn((_bin, _args, _opts, cb) => {
    cb(null, JSON.stringify({ hits: [] }), "");
  }),
}));

vi.mock("node:util", () => ({
  promisify: (fn: unknown) => fn,
}));

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

let fixtureRoot: string;
const ISSUE_NUMBER = 42;

function setupFixture(): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "incb-test-"));

  // Pipeline context directory
  const pipelineDir = path.join(root, ".nightgauge", "pipeline");
  fs.mkdirSync(pipelineDir, { recursive: true });

  // Knowledge directory
  const knowledgePath = path.join(
    root,
    ".nightgauge",
    "knowledge",
    "features",
    `${ISSUE_NUMBER}-test-feature`
  );
  fs.mkdirSync(knowledgePath, { recursive: true });

  // Seed KB files
  fs.writeFileSync(path.join(knowledgePath, "PRD.md"), "# PRD\n\nProduct requirements here.");
  fs.writeFileSync(
    path.join(knowledgePath, "decisions.md"),
    "# Decisions\n\n## ADR-001: Use subprocess\n**Decision**: spawn binary."
  );

  // Write issue context file pointing to knowledge path
  fs.writeFileSync(
    path.join(pipelineDir, `issue-${ISSUE_NUMBER}.json`),
    JSON.stringify({
      schema_version: "1.5",
      issue_number: ISSUE_NUMBER,
      knowledge_path: knowledgePath,
    })
  );

  return root;
}

function teardownFixture(root: string): void {
  fs.rmSync(root, { recursive: true, force: true });
}

function makePss(issueNumber: number | null): PipelineStateService {
  const mockDisposable = { dispose: vi.fn() };
  return {
    getActiveIssueBlockingPickup: vi.fn(() => issueNumber),
    onStateChanged: vi.fn(() => mockDisposable),
  } as unknown as PipelineStateService;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("ActiveIssueKnowledgeProvider — integration", () => {
  beforeAll(() => {
    fixtureRoot = setupFixture();
  });

  afterAll(() => {
    teardownFixture(fixtureRoot);
  });

  it("renders PRD, Decisions, and Related Decisions sections from real fixture files", async () => {
    const pss = makePss(ISSUE_NUMBER);
    const provider = new ActiveIssueKnowledgeProvider(
      fixtureRoot,
      pss,
      () => undefined // no binary — recall returns empty
    );

    const sections = await provider.getChildren();

    expect(sections).toHaveLength(3);

    const [prdSection, decSection, recallSection] = sections as ActiveIssueKnowledgeSectionItem[];

    expect(prdSection.sectionKind).toBe("prd");
    expect(decSection.sectionKind).toBe("decisions");
    expect(recallSection.sectionKind).toBe("recall");

    // PRD child is the seeded PRD.md
    const prdChildren = await provider.getChildren(prdSection);
    expect(prdChildren).toHaveLength(1);
    expect(prdChildren[0]).toBeInstanceOf(ActiveIssueKnowledgeFileItem);
    expect((prdChildren[0] as ActiveIssueKnowledgeFileItem).filePath).toContain("PRD.md");

    // Decisions child is the seeded decisions.md
    const decChildren = await provider.getChildren(decSection);
    expect(decChildren).toHaveLength(1);
    expect(decChildren[0]).toBeInstanceOf(ActiveIssueKnowledgeFileItem);
    expect((decChildren[0] as ActiveIssueKnowledgeFileItem).filePath).toContain("decisions.md");

    provider.dispose();
  });
});
