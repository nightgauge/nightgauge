/**
 * Tests for Knowledge Entry Creation commands
 *
 * @see src/commands/knowledge/newEntry.ts
 * @see src/commands/knowledge/scaffoldForIssue.ts
 * @see src/commands/knowledge/newADR.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------
vi.mock("vscode", () => ({
  window: {
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showQuickPick: vi.fn(),
    showInputBox: vi.fn(),
    showTextDocument: vi.fn().mockResolvedValue(undefined),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    openTextDocument: vi.fn().mockResolvedValue({}),
    getConfiguration: vi.fn(),
  },
  commands: {
    registerCommand: vi.fn((_, handler) => ({ dispose: vi.fn(), handler })),
    executeCommand: vi.fn(),
  },
  QuickPickItemKind: {},
  Uri: { file: vi.fn((p) => ({ fsPath: p })) },
}));

// ---------------------------------------------------------------------------
// SDK mock
// ---------------------------------------------------------------------------
const mockCreate = vi.fn();
const mockScaffoldForIssue = vi.fn();
const mockGenerateSlug = vi.fn((title: string) =>
  title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 50)
);

vi.mock("@nightgauge/sdk", () => ({
  KnowledgeService: vi.fn(function () {
    return {
      create: mockCreate,
      scaffoldForIssue: mockScaffoldForIssue,
      generateSlug: mockGenerateSlug,
    };
  }),
}));

// ---------------------------------------------------------------------------
// node:fs/promises mock (for newADR directory scanning)
// ---------------------------------------------------------------------------
const mockMkdir = vi.fn().mockResolvedValue(undefined);
const mockReaddir = vi.fn().mockResolvedValue([] as string[]);
const mockWriteFile = vi.fn().mockResolvedValue(undefined);

vi.mock("node:fs/promises", () => ({
  mkdir: (...args: any[]) => mockMkdir(...args),
  readdir: (...args: any[]) => mockReaddir(...args),
  writeFile: (...args: any[]) => mockWriteFile(...args),
}));

// ---------------------------------------------------------------------------
// Settings mock
// ---------------------------------------------------------------------------
vi.mock("../../src/config/settings", () => ({
  getWorkspaceRoot: vi.fn(() => "/test/workspace"),
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function getHandler(disposable: any): () => Promise<void> {
  return (disposable as any).handler;
}

function mockGetConfiguration(enabled: boolean): void {
  vi.mocked(vscode.workspace.getConfiguration).mockReturnValue({
    get: vi.fn((key: string, defaultVal: any) => {
      if (key === "enabled") return enabled;
      return defaultVal;
    }),
    update: vi.fn(),
    has: vi.fn(),
    inspect: vi.fn(),
  } as any);
}

// ---------------------------------------------------------------------------
// newEntry tests
// ---------------------------------------------------------------------------
describe("registerKnowledgeNewEntryCommand", () => {
  let handler: () => Promise<void>;
  let mockLogger: any;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockCreate.mockResolvedValue(".nightgauge/knowledge/standalone/db-strategy/note.md");
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    const { registerKnowledgeNewEntryCommand } =
      await import("../../src/commands/knowledge/newEntry");
    const disposable = registerKnowledgeNewEntryCommand(mockLogger);
    handler = getHandler(disposable);
  });

  it("should call service.create() and open file when type and title are provided", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "$(file-text) Note",
      value: "note",
      description: "General project notes",
    } as any);
    vi.mocked(vscode.window.showInputBox).mockResolvedValue("Database strategy");

    await handler();

    expect(mockCreate).toHaveBeenCalledWith("note", "standalone/database-strategy", "", {
      title: "Database strategy",
      type: "note",
    });
    expect(vscode.workspace.openTextDocument).toHaveBeenCalled();
    expect(vscode.window.showTextDocument).toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Knowledge entry created: Database strategy"
    );
  });

  it("should return early without service.create() when QuickPick is cancelled", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(undefined);

    await handler();

    expect(mockCreate).not.toHaveBeenCalled();
  });

  it("should show info message and return when knowledge is disabled", async () => {
    mockGetConfiguration(false);

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Knowledge base is disabled. Enable it in settings (nightgauge.knowledge.enabled)."
    );
    expect(mockCreate).not.toHaveBeenCalled();
  });

  it("should show error message when service.create() throws", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "$(file-text) Note",
      value: "note",
    } as any);
    vi.mocked(vscode.window.showInputBox).mockResolvedValue("Duplicate entry");
    mockCreate.mockRejectedValue(new Error("File already exists: /test/workspace/..."));

    await handler();

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Entry already exists at that path."
    );
  });
});

// ---------------------------------------------------------------------------
// scaffoldForIssue tests
// ---------------------------------------------------------------------------
describe("registerKnowledgeScaffoldForIssueCommand", () => {
  let handler: () => Promise<void>;
  let mockLogger: any;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };

    const { registerKnowledgeScaffoldForIssueCommand } =
      await import("../../src/commands/knowledge/scaffoldForIssue");
    const disposable = registerKnowledgeScaffoldForIssueCommand(mockLogger);
    handler = getHandler(disposable);
  });

  it("should call scaffoldForIssue() and open PRD.md when all inputs provided", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox)
      .mockResolvedValueOnce("1688")
      .mockResolvedValueOnce("Knowledge commands");
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Feature (features/)" as any);
    mockScaffoldForIssue.mockResolvedValue({
      knowledge_path: ".nightgauge/knowledge/features/1688-knowledge-commands",
      files_created: ["PRD.md", "decisions.md"],
      skipped: false,
    });

    await handler();

    expect(mockScaffoldForIssue).toHaveBeenCalledWith(1688, "Knowledge commands", "", false, {
      enabled: true,
      auto_scaffold: true,
    });
    expect(vscode.workspace.openTextDocument).toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("Knowledge scaffolded at")
    );
  });

  it("should return early without service call when cancelled at issue number", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox).mockResolvedValueOnce(undefined);

    await handler();

    expect(mockScaffoldForIssue).not.toHaveBeenCalled();
  });

  it("should show info message with skip reason when result.skipped=true", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox)
      .mockResolvedValueOnce("42")
      .mockResolvedValueOnce("Some issue");
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Feature (features/)" as any);
    mockScaffoldForIssue.mockResolvedValue({
      knowledge_path: "",
      files_created: [],
      skipped: true,
      skip_reason: "knowledge.enabled is false",
    });

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("knowledge.enabled is false")
    );
  });

  it("should pass isEpic=true when Epic type selected", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox)
      .mockResolvedValueOnce("100")
      .mockResolvedValueOnce("My Epic");
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue("Epic (epics/)" as any);
    mockScaffoldForIssue.mockResolvedValue({
      knowledge_path: ".nightgauge/knowledge/epics/100-my-epic",
      files_created: ["PRD.md", "decisions.md"],
      skipped: false,
    });

    await handler();

    expect(mockScaffoldForIssue).toHaveBeenCalledWith(100, "My Epic", "", true, expect.any(Object));
  });
});

// ---------------------------------------------------------------------------
// newADR tests
// ---------------------------------------------------------------------------
describe("registerKnowledgeNewADRCommand", () => {
  let handler: () => Promise<void>;
  let mockLogger: any;

  beforeEach(async () => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    };
    mockMkdir.mockResolvedValue(undefined);
    mockReaddir.mockResolvedValue([]);
    mockWriteFile.mockResolvedValue(undefined);

    const { registerKnowledgeNewADRCommand } = await import("../../src/commands/knowledge/newADR");
    const disposable = registerKnowledgeNewADRCommand(mockLogger);
    handler = getHandler(disposable);
  });

  it("should create 001-*.md when no existing ADRs", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox).mockResolvedValue("Use PostgreSQL");
    mockReaddir.mockResolvedValue([]);

    await handler();

    expect(mockWriteFile).toHaveBeenCalledWith(
      expect.stringContaining("001-use-postgresql.md"),
      expect.stringContaining("# ADR 001: Use PostgreSQL"),
      "utf-8"
    );
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "ADR 001 created: Use PostgreSQL"
    );
  });

  it("should increment ADR number from existing files", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox).mockResolvedValue("Use Redis for caching");
    mockReaddir.mockResolvedValue(["001-use-postgresql.md", "002-add-auth.md"]);

    await handler();

    expect(mockWriteFile).toHaveBeenCalledWith(
      expect.stringContaining("003-use-redis-for-caching.md"),
      expect.stringContaining("# ADR 003: Use Redis for caching"),
      "utf-8"
    );
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "ADR 003 created: Use Redis for caching"
    );
  });

  it("should return early without writing file when cancelled", async () => {
    mockGetConfiguration(true);
    vi.mocked(vscode.window.showInputBox).mockResolvedValue(undefined);

    await handler();

    expect(mockWriteFile).not.toHaveBeenCalled();
  });

  it("should show info message when knowledge is disabled", async () => {
    mockGetConfiguration(false);

    await handler();

    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Knowledge base is disabled. Enable it in settings (nightgauge.knowledge.enabled)."
    );
    expect(mockWriteFile).not.toHaveBeenCalled();
  });
});
