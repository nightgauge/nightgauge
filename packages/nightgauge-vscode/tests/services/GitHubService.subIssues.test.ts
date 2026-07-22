import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { GitHubService } from "../../src/services/GitHubService";

// Hoist mock IPC methods so they are available inside vi.mock factories
const { mockIssueView, mockIssueLinkSubIssue } = vi.hoisted(() => ({
  mockIssueView: vi.fn(),
  mockIssueLinkSubIssue: vi.fn(),
}));

// Mock IpcClient singleton
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      issueView: mockIssueView,
      issueLinkSubIssue: mockIssueLinkSubIssue,
    }),
  },
}));

describe("GitHubService - Sub-Issues", () => {
  let service: GitHubService;

  beforeEach(() => {
    service = new GitHubService("TestOwner", "test-repo");
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.resetAllMocks();
  });

  describe("fetchSubIssues", () => {
    it("should fetch sub-issues from GitHub API", async () => {
      mockIssueView.mockResolvedValue({
        number: 295,
        title: "Parent Issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
        subIssues: [
          { number: 301, state: "OPEN", title: "" },
          { number: 302, state: "CLOSED", title: "" },
          { number: 303, state: "OPEN", title: "" },
        ],
      });

      const result = await service.fetchSubIssues(295);

      expect(result).toEqual([
        { number: 301, state: "OPEN" },
        { number: 302, state: "CLOSED" },
        { number: 303, state: "OPEN" },
      ]);

      expect(mockIssueView).toHaveBeenCalledWith("TestOwner", "test-repo", 295);
    });

    it("should return empty array when no sub-issues exist", async () => {
      mockIssueView.mockResolvedValue({
        number: 295,
        title: "Parent Issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
        subIssues: [],
      });

      const result = await service.fetchSubIssues(295);

      expect(result).toEqual([]);
    });

    it("should return empty array when subIssues field is unavailable", async () => {
      mockIssueView.mockResolvedValue({
        number: 295,
        title: "Parent Issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
        // No subIssues field
      });

      const result = await service.fetchSubIssues(295);

      expect(result).toEqual([]);
    });

    it("should throw error when parent issue not found", async () => {
      mockIssueView.mockRejectedValue(new Error("Issue not found"));

      await expect(service.fetchSubIssues(999)).rejects.toThrow("Parent issue #999 not found");
    });

    it("should reject invalid parent number (negative)", async () => {
      await expect(service.fetchSubIssues(-1)).rejects.toThrow(
        "Invalid parent issue number: -1. Must be a positive integer."
      );

      expect(mockIssueView).not.toHaveBeenCalled();
    });

    it("should reject invalid parent number (zero)", async () => {
      await expect(service.fetchSubIssues(0)).rejects.toThrow(
        "Invalid parent issue number: 0. Must be a positive integer."
      );

      expect(mockIssueView).not.toHaveBeenCalled();
    });

    it("should reject invalid parent number (non-integer)", async () => {
      await expect(service.fetchSubIssues(3.14)).rejects.toThrow(
        "Invalid parent issue number: 3.14. Must be a positive integer."
      );

      expect(mockIssueView).not.toHaveBeenCalled();
    });

    it("should handle gh CLI stderr errors", async () => {
      mockIssueView.mockRejectedValue(new Error("API rate limit exceeded"));

      await expect(service.fetchSubIssues(295)).rejects.toThrow(
        "Failed to fetch sub-issues for #295: API rate limit exceeded"
      );
    });
  });

  describe("linkSubIssueToParent", () => {
    it("should link child issue to parent", async () => {
      mockIssueLinkSubIssue.mockResolvedValue(undefined);

      await expect(service.linkSubIssueToParent(301, 295)).resolves.toBeUndefined();

      expect(mockIssueLinkSubIssue).toHaveBeenCalledWith("TestOwner", "test-repo", 295, 301);
    });

    it("should throw error when child issue not found", async () => {
      mockIssueLinkSubIssue.mockRejectedValue(new Error("Issue not found"));

      await expect(service.linkSubIssueToParent(999, 295)).rejects.toThrow(
        "Issue #999 or #295 not found"
      );
    });

    it("should reject when child equals parent", async () => {
      await expect(service.linkSubIssueToParent(295, 295)).rejects.toThrow(
        "Cannot link issue to itself: child #295 === parent #295"
      );

      expect(mockIssueLinkSubIssue).not.toHaveBeenCalled();
    });

    it("should reject invalid child number (negative)", async () => {
      await expect(service.linkSubIssueToParent(-1, 295)).rejects.toThrow(
        "Invalid child issue number: -1. Must be a positive integer."
      );

      expect(mockIssueLinkSubIssue).not.toHaveBeenCalled();
    });

    it("should reject invalid parent number (negative)", async () => {
      await expect(service.linkSubIssueToParent(301, -1)).rejects.toThrow(
        "Invalid parent issue number: -1. Must be a positive integer."
      );

      expect(mockIssueLinkSubIssue).not.toHaveBeenCalled();
    });

    it("should handle gh CLI errors gracefully", async () => {
      mockIssueLinkSubIssue.mockRejectedValue(new Error("permission denied"));

      await expect(service.linkSubIssueToParent(301, 295)).rejects.toThrow(
        "Failed to link #301 to parent #295: permission denied"
      );
    });
  });

  describe("fetchIssueMetadata", () => {
    it("should fetch issue title and parent", async () => {
      mockIssueView.mockResolvedValue({
        number: 301,
        title: "Add JWT middleware",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
      });

      const result = await service.fetchIssueMetadata(301);

      expect(result).toEqual({
        title: "Add JWT middleware",
        parent: undefined,
      });

      expect(mockIssueView).toHaveBeenCalledWith("TestOwner", "test-repo", 301);
    });

    it("should handle issue without parent", async () => {
      mockIssueView.mockResolvedValue({
        number: 100,
        title: "Standalone issue",
        body: "",
        state: "OPEN",
        labels: [],
        assignees: [],
        url: "",
        isEpic: false,
      });

      const result = await service.fetchIssueMetadata(100);

      expect(result).toEqual({
        title: "Standalone issue",
        parent: undefined,
      });
    });

    it("should throw error when issue not found", async () => {
      mockIssueView.mockRejectedValue(new Error("Issue not found"));

      await expect(service.fetchIssueMetadata(999)).rejects.toThrow("Issue #999 not found");
    });

    it("should reject invalid issue number", async () => {
      await expect(service.fetchIssueMetadata(-1)).rejects.toThrow(
        "Invalid issue number: -1. Must be a positive integer."
      );

      expect(mockIssueView).not.toHaveBeenCalled();
    });
  });
});
