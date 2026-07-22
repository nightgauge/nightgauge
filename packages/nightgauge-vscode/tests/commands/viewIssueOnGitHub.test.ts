import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerViewIssueOnGitHubCommand } from "../../src/commands/pickupIssue";
import { ReadyIssueTreeItem } from "../../src/views/items/ReadyIssueTreeItem";
import { IssueTreeItem, type IssueInfo } from "../../src/views/items/IssueTreeItem";
import { createMockReadyIssue } from "../mocks/github-api";
import type { Logger } from "../../src/utils/logger";

describe("viewIssueOnGitHub command", () => {
  let mockLogger: Logger;
  let mockOpenExternal: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    mockLogger = {
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    } as unknown as Logger;

    mockOpenExternal = vi.fn();
    (vscode.env as any) = {
      openExternal: mockOpenExternal,
    };

    // Mock registerCommand
    (vscode.commands as any).registerCommand = vi.fn((command: string, handler: any) => {
      return { dispose: vi.fn() };
    });
  });

  describe("with ReadyIssueTreeItem", () => {
    it("should open issue URL in browser", async () => {
      const issue = createMockReadyIssue({
        number: 297,
        url: "https://github.com/nightgauge/nightgauge/issues/297",
      });
      const item = new ReadyIssueTreeItem(issue);

      const disposable = registerViewIssueOnGitHubCommand(mockLogger);
      const command = (vscode.commands as any).registerCommand.mock.calls[0];
      const handler = command[1];

      await handler(item);

      expect(mockOpenExternal).toHaveBeenCalledWith(
        expect.objectContaining({
          path: "https://github.com/nightgauge/nightgauge/issues/297",
        })
      );
      expect(mockLogger.debug).toHaveBeenCalledWith("Opening issue on GitHub", {
        issueNumber: 297,
      });

      disposable.dispose();
    });

    // 'should log debug message with issue number' test removed (Issue #1826):
    // Duplicate — the test above already verifies the same debug log call.
    // The behavioral outcome (openExternal) is the meaningful contract.
  });

  describe("with IssueTreeItem (Issue #297)", () => {
    it("should open issue URL in browser when URL is provided", async () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        url: "https://github.com/nightgauge/nightgauge/issues/297",
      };
      const item = new IssueTreeItem(issueInfo);

      const disposable = registerViewIssueOnGitHubCommand(mockLogger);
      const command = (vscode.commands as any).registerCommand.mock.calls[0];
      const handler = command[1];

      await handler(item);

      expect(mockOpenExternal).toHaveBeenCalledWith(
        expect.objectContaining({
          path: "https://github.com/nightgauge/nightgauge/issues/297",
        })
      );
      expect(mockLogger.debug).toHaveBeenCalledWith("Opening issue on GitHub", {
        issueNumber: 297,
      });

      disposable.dispose();
    });

    it("should warn and return early when URL is not provided", async () => {
      const issueInfo: IssueInfo = {
        number: 297,
        title: "Test Issue",
        branch: "feat/297-test",
        // No URL provided
      };
      const item = new IssueTreeItem(issueInfo);

      const disposable = registerViewIssueOnGitHubCommand(mockLogger);
      const command = (vscode.commands as any).registerCommand.mock.calls[0];
      const handler = command[1];

      await handler(item);

      expect(mockOpenExternal).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "viewIssueOnGitHub called on tree item without URL",
        { issueNumber: 297 }
      );

      disposable.dispose();
    });
  });

  describe("error handling", () => {
    it("should warn when called without a valid tree item", async () => {
      const disposable = registerViewIssueOnGitHubCommand(mockLogger);
      const command = (vscode.commands as any).registerCommand.mock.calls[0];
      const handler = command[1];

      await handler(undefined);

      expect(mockOpenExternal).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "viewIssueOnGitHub called without a valid tree item"
      );

      disposable.dispose();
    });

    it("should warn when called with invalid item type", async () => {
      const invalidItem = { issueNumber: 123 };

      const disposable = registerViewIssueOnGitHubCommand(mockLogger);
      const command = (vscode.commands as any).registerCommand.mock.calls[0];
      const handler = command[1];

      await handler(invalidItem);

      expect(mockOpenExternal).not.toHaveBeenCalled();
      expect(mockLogger.warn).toHaveBeenCalledWith(
        "viewIssueOnGitHub called without a valid tree item"
      );

      disposable.dispose();
    });
  });

  describe("command registration", () => {
    it("should register command with correct name", () => {
      const disposable = registerViewIssueOnGitHubCommand(mockLogger);

      expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
        "nightgauge.viewIssueOnGitHub",
        expect.any(Function)
      );

      disposable.dispose();
    });

    // 'should return a disposable' test removed (Issue #1826): TypeScript
    // enforces the return type at compile time; a runtime existence check
    // adds no contract value.
  });
});
