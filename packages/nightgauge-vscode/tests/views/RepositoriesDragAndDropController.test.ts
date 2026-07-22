/**
 * Tests for RepositoriesDragAndDropController cross-status drops.
 *
 * Regression coverage for the bug where dragging a Backlog issue onto the
 * "Ready" section header silently did nothing: the controller was never given
 * a resolvable workspace root, so the cross-column guard
 * (`targetColumnStatus && dropRoot`) failed and the drop fell through to a
 * silent rejection. A drop on the empty tree root must be a no-op (it must
 * never start a pipeline like the base controller does).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

// --- Module mocks ---

vi.mock("../../src/utils/projectFieldWriter", () => ({
  updateProjectItemStatus: vi.fn(),
  clearConfigCache: vi.fn(),
}));

vi.mock("../../src/config/warningSettings", () => ({
  getWarningSettings: vi.fn(() => ({ enabled: false })),
}));

vi.mock("../../src/utils/dialogs", () => ({
  showStatusWarningDialog: vi.fn(),
  openUrl: vi.fn(),
}));

vi.mock("../../src/utils/prDetection", () => ({
  getPRForIssue: vi.fn(),
}));

vi.mock("../../src/utils/logger", () => ({
  Logger: class {
    info = vi.fn();
    warn = vi.fn();
    error = vi.fn();
    debug = vi.fn();
  },
}));

const mockEpicTransitionStatus = vi.fn();
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({ epicTransitionStatus: mockEpicTransitionStatus }),
  },
}));

// --- Imports after mocks ---

import { RepositoriesDragAndDropController } from "../../src/views/RepositoriesDragAndDropController";
import { IssueSummaryTreeItem } from "../../src/views/items/IssueSummaryTreeItem";
import { updateProjectItemStatus } from "../../src/utils/projectFieldWriter";
import type { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";

// --- Helpers ---

function makePayload(overrides: Partial<any> = {}): any {
  return {
    issueNumber: 3799,
    title: "Test issue",
    labels: [],
    url: "https://github.com/nightgauge/nightgauge/issues/3799",
    sourceTabStatus: "Backlog",
    isEpic: false,
    subIssueNumbers: undefined,
    repoName: "nightgauge",
    ...overrides,
  };
}

function makeDataTransfer(payload: any[]): vscode.DataTransfer {
  const mime = "application/vnd.code.tree.nightgauge-issue";
  const dt: any = {
    get: vi.fn((m: string) =>
      m === mime ? new vscode.DataTransferItem(JSON.stringify(payload)) : undefined
    ),
    set: vi.fn(),
    forEach: vi.fn((cb: (item: any, m: string) => void) => {
      const item = dt.get(mime);
      if (item) cb(item, mime);
    }),
  };
  return dt as vscode.DataTransfer;
}

const cancelToken: vscode.CancellationToken = {
  isCancellationRequested: false,
  onCancellationRequested: vi.fn() as any,
};

function makeController(): {
  controller: RepositoriesDragAndDropController;
  provider: any;
} {
  const provider = {
    getRepositoryPath: vi.fn((name?: string) =>
      name === "nightgauge" ? "/repos/nightgauge" : "/repos/active"
    ),
    refreshRepository: vi.fn(),
    refreshAll: vi.fn(),
  };
  const controller = new RepositoriesDragAndDropController(
    provider as unknown as RepositoriesTreeProvider
  );
  controller.setLogger({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as any);
  return { controller, provider };
}

describe("RepositoriesDragAndDropController", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (vscode.window as any).setStatusBarMessage = vi.fn();
    (vscode.window as any).withProgress = vi.fn(async (_opts: any, task: any) => {
      await task({ report: vi.fn() });
    });
    (vscode.commands as any).executeCommand = vi.fn();
  });

  it("moves a Backlog issue dropped on the Ready header, using the issue's repo path", async () => {
    vi.mocked(updateProjectItemStatus).mockResolvedValue({ success: true });
    const { controller, provider } = makeController();

    const readyHeader = new IssueSummaryTreeItem("ready", "nightgauge", 5);

    await controller.handleDrop(readyHeader, makeDataTransfer([makePayload()]), cancelToken);

    expect(updateProjectItemStatus).toHaveBeenCalledWith(
      3799,
      "Ready",
      "/repos/nightgauge",
      expect.any(Object)
    );
    expect(provider.refreshRepository).toHaveBeenCalledWith("nightgauge");
  });

  it("does NOT start a pipeline when dropped on the empty tree root", async () => {
    const { controller } = makeController();

    await controller.handleDrop(undefined, makeDataTransfer([makePayload()]), cancelToken);

    expect(updateProjectItemStatus).not.toHaveBeenCalled();
    expect(vscode.commands.executeCommand).not.toHaveBeenCalledWith(
      "nightgauge.startPipelineForIssue",
      expect.anything()
    );
  });

  it("treats a same-status drop (Ready → Ready) as a no-op", async () => {
    const { controller } = makeController();
    const readyHeader = new IssueSummaryTreeItem("ready", "nightgauge", 5);

    await controller.handleDrop(
      readyHeader,
      makeDataTransfer([makePayload({ sourceTabStatus: "Ready" })]),
      cancelToken
    );

    expect(updateProjectItemStatus).not.toHaveBeenCalled();
  });
});
