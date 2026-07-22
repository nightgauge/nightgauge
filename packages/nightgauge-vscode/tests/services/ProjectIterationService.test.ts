/**
 * ProjectIterationService.test.ts
 *
 * Unit tests for ProjectIterationService, focusing on:
 * - Date calculations with date-fns (current, next iteration resolution)
 * - Configuration loading
 * - GraphQL query handling
 * - Pagination for project items
 * - Error scenarios and graceful degradation
 *
 * @see Issue #132 - Rewrite sync-project-iteration.sh in TypeScript
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as fsPromises from "fs/promises";
import { parseISO } from "date-fns";
import { ProjectIterationService } from "../../src/services/ProjectIterationService";
import {
  createMockIteration,
  createMockCurrentIteration,
  createMockNextIteration,
  createMockPastIteration,
  createMockIterationSet,
  createMockProjectItem,
  createMockProjectListOutput,
  createMockFieldListOutput,
  createMockIterationQueryOutput,
  createMockProjectItemsOutput,
  createMockIncrediYaml,
} from "../mocks/iteration";
import { isSyncSuccess, isSyncSkipped } from "../../src/services/types/iteration";

/**
 * Helper to create a Date object from an ISO date string.
 * Uses parseISO to ensure consistent timezone handling with the service.
 */
function toDate(isoDate: string): Date {
  return parseISO(isoDate);
}

// Mock child_process with a callback-compatible exec.
// Both test and service create execAsync = promisify(exec), producing separate
// wrappers. By making the mock exec invoke callbacks properly, promisify works
// in both contexts.
let _execMockHandler: ((cmd: string) => Promise<{ stdout: string; stderr: string }>) | null = null;

vi.mock("child_process", () => ({
  exec: vi.fn((...args: any[]) => {
    // promisify calls exec(cmd, opts?, cb) — find the callback
    const cb = args.find((a: any) => typeof a === "function");
    const cmd = args[0] as string;
    const handler = _execMockHandler ?? (async () => ({ stdout: "", stderr: "" }));
    handler(cmd)
      .then((r) => cb?.(null, r))
      .catch((e) => cb?.(e));
  }),
  execFile: vi.fn((...args: any[]) => {
    // promisify calls execFile(file, args, opts?, cb) — find the callback
    const cb = args.find((a: any) => typeof a === "function");
    const command = [args[0], ...(Array.isArray(args[1]) ? args[1] : [])].join(" ");
    const handler = _execMockHandler ?? (async () => ({ stdout: "", stderr: "" }));
    handler(command)
      .then((r) => cb?.(null, r))
      .catch((e) => cb?.(e));
  }),
}));

/** Set the exec mock handler for a test. */
function setExecMock(handler: (cmd: string) => Promise<{ stdout: string; stderr: string }>): void {
  _execMockHandler = handler;
}

// Mock fs/promises module (used by configPathResolver)
vi.mock("fs/promises", () => ({
  access: vi.fn(),
  readFile: vi.fn(),
}));

// Also mock fs.promises for backward compatibility
vi.mock("fs", () => ({
  promises: {
    readFile: vi.fn(),
    access: vi.fn(),
  },
}));

// Mock getGitHubUser — returns null (no per-repo auth) in tests
vi.mock("../../src/utils/incrediConfig", () => ({
  getGitHubUser: vi.fn(() => null),
}));

// Helper type for exec mock responses
interface ExecResponse {
  stdout: string;
  stderr: string;
}

// Helper to create mock responses for different commands
function createExecHandler(
  responses: Record<string, string | Error>
): (cmd: string) => Promise<ExecResponse> {
  return async (cmd: string) => {
    for (const [pattern, response] of Object.entries(responses)) {
      if (cmd.includes(pattern)) {
        if (response instanceof Error) {
          throw response;
        }
        return { stdout: response, stderr: "" };
      }
    }
    return { stdout: "", stderr: "" };
  };
}

describe("ProjectIterationService - Date Calculations", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    _execMockHandler = null;
    ProjectIterationService.resetInstance();
  });

  afterEach(() => {
    ProjectIterationService.resetInstance();
  });

  describe("isCurrentIteration()", () => {
    it("should return true when today is within iteration date range", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      // Iteration: Feb 3-16, 2026 (14 days)
      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-03",
        duration: 14,
      });

      // Test middle of iteration
      const midDate = toDate("2026-02-10");
      expect(service.isCurrentIteration(iteration, midDate)).toBe(true);
    });

    it("should return true when today is the start date", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-03",
        duration: 14,
      });

      const startDate = toDate("2026-02-03");
      expect(service.isCurrentIteration(iteration, startDate)).toBe(true);
    });

    it("should return true when today is the last day of iteration", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      // 14 day iteration starting Feb 3 = Feb 3-16 (last day is Feb 16)
      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-03",
        duration: 14,
      });

      const lastDay = toDate("2026-02-16");
      expect(service.isCurrentIteration(iteration, lastDay)).toBe(true);
    });

    it("should return false when today is before iteration starts", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-03",
        duration: 14,
      });

      const beforeStart = toDate("2026-02-02");
      expect(service.isCurrentIteration(iteration, beforeStart)).toBe(false);
    });

    it("should return false when today is after iteration ends", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-03",
        duration: 14,
      });

      const afterEnd = toDate("2026-02-17");
      expect(service.isCurrentIteration(iteration, afterEnd)).toBe(false);
    });
  });

  describe("isNextIteration()", () => {
    it("should return true when iteration starts after today", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-10",
        duration: 14,
      });

      const today = toDate("2026-02-05");
      expect(service.isNextIteration(iteration, today)).toBe(true);
    });

    it("should return false when iteration starts today", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-05",
        duration: 14,
      });

      const today = toDate("2026-02-05");
      expect(service.isNextIteration(iteration, today)).toBe(false);
    });

    it("should return false when iteration started before today", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-01",
        duration: 14,
      });

      const today = toDate("2026-02-05");
      expect(service.isNextIteration(iteration, today)).toBe(false);
    });
  });

  describe("Edge cases", () => {
    it("should handle single-day iteration", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-02-05",
        duration: 1,
      });

      const today = toDate("2026-02-05");
      expect(service.isCurrentIteration(iteration, today)).toBe(true);

      const nextDay = toDate("2026-02-06");
      expect(service.isCurrentIteration(iteration, nextDay)).toBe(false);
    });

    it("should handle iteration spanning month boundary", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      // Iteration: Jan 25 - Feb 7 (14 days)
      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2026-01-25",
        duration: 14,
      });

      const jan30 = toDate("2026-01-30");
      expect(service.isCurrentIteration(iteration, jan30)).toBe(true);

      const feb5 = toDate("2026-02-05");
      expect(service.isCurrentIteration(iteration, feb5)).toBe(true);

      const feb8 = toDate("2026-02-08");
      expect(service.isCurrentIteration(iteration, feb8)).toBe(false);
    });

    it("should handle iteration spanning year boundary", () => {
      const service = ProjectIterationService.getInstance(workspaceRoot);

      // Iteration: Dec 28, 2025 - Jan 10, 2026 (14 days)
      const iteration = createMockIteration({
        id: "iter1",
        startDate: "2025-12-28",
        duration: 14,
      });

      const dec31 = toDate("2025-12-31");
      expect(service.isCurrentIteration(iteration, dec31)).toBe(true);

      const jan5 = toDate("2026-01-05");
      expect(service.isCurrentIteration(iteration, jan5)).toBe(true);
    });
  });
});

describe("ProjectIterationService - syncIteration()", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    _execMockHandler = null;
    ProjectIterationService.resetInstance();
    // Default: config file exists (primary path)
    vi.mocked(fsPromises.access).mockResolvedValue(undefined);
  });

  afterEach(() => {
    ProjectIterationService.resetInstance();
  });

  describe("Configuration checks", () => {
    it("should skip when no project configured", async () => {
      // No config file exists
      vi.mocked(fsPromises.access).mockRejectedValue(new Error("ENOENT"));
      vi.mocked(fs.promises.readFile).mockRejectedValue(new Error("ENOENT"));

      const service = ProjectIterationService.getInstance(workspaceRoot);
      const result = await service.syncIteration(90, "@current");

      expect(isSyncSkipped(result)).toBe(true);
      if (isSyncSkipped(result)) {
        expect(result.reason).toContain("No project configured");
      }
    });

    it("should skip when sprint feature is disabled", async () => {
      const configContent = createMockIncrediYaml({
        projectNumber: 10,
        sprintEnabled: false,
      });
      vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

      const service = ProjectIterationService.getInstance(workspaceRoot);
      const result = await service.syncIteration(90, "@current");

      expect(isSyncSkipped(result)).toBe(true);
      if (isSyncSkipped(result)) {
        expect(result.reason).toContain("Sprint feature not enabled");
      }
    });
  });

  describe("Repository info checks", () => {
    it("should skip when owner cannot be determined", async () => {
      const configContent = createMockIncrediYaml({
        projectNumber: 10,
        sprintEnabled: true,
      });
      vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

      // Mock exec to fail for owner query
      setExecMock(async () => {
        throw new Error("Not a git repository");
      });

      const service = ProjectIterationService.getInstance(workspaceRoot);
      const result = await service.syncIteration(90, "@current");

      expect(isSyncSkipped(result)).toBe(true);
      if (isSyncSkipped(result)) {
        expect(result.reason).toContain("Could not determine");
      }
    });
  });

  describe("Full sync flow", () => {
    it("should successfully assign @current iteration", async () => {
      const configContent = createMockIncrediYaml({
        projectNumber: 10,
        sprintEnabled: true,
        fieldName: "Sprint",
      });
      vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

      const today = new Date("2026-02-05");
      const currentIteration = createMockCurrentIteration(today);
      const iterations = [
        createMockPastIteration(today),
        currentIteration,
        createMockNextIteration(today),
      ];

      const mockItem = createMockProjectItem({
        content: {
          number: 90,
          repository: { nameWithOwner: "nightgauge/nightgauge" },
        },
      });

      setExecMock(async (cmd: string) => {
        if (cmd.includes("gh repo view --json owner")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh repo view --json name")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh project list")) {
          return { stdout: createMockProjectListOutput(10), stderr: "" };
        }
        if (cmd.includes("gh project field-list")) {
          return { stdout: createMockFieldListOutput("Sprint"), stderr: "" };
        }
        if (cmd.includes("gh api graphql") && cmd.includes("items")) {
          return {
            stdout: createMockProjectItemsOutput([mockItem]),
            stderr: "",
          };
        }
        if (cmd.includes("gh api graphql")) {
          return {
            stdout: createMockIterationQueryOutput(iterations),
            stderr: "",
          };
        }
        if (cmd.includes("gh project item-edit")) {
          return { stdout: "", stderr: "" };
        }
        return { stdout: "", stderr: "" };
      });

      const service = ProjectIterationService.getInstance(workspaceRoot);

      // Use vi.useFakeTimers to control the date
      vi.useFakeTimers();
      vi.setSystemTime(today);

      const result = await service.syncIteration(90, "@current");

      vi.useRealTimers();

      expect(isSyncSuccess(result)).toBe(true);
      if (isSyncSuccess(result)) {
        expect(result.issue).toBe(90);
        expect(result.project).toBe(10);
        expect(result.action).toBe("assigned");
        expect(result.iteration).not.toBeNull();
      }
    });

    it("should successfully clear iteration with none target", async () => {
      const configContent = createMockIncrediYaml({
        projectNumber: 10,
        sprintEnabled: true,
        fieldName: "Sprint",
      });
      vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

      const mockItem = createMockProjectItem({
        content: {
          number: 90,
          repository: { nameWithOwner: "nightgauge/nightgauge" },
        },
      });

      setExecMock(async (cmd: string) => {
        if (cmd.includes("gh repo view --json owner")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh repo view --json name")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh project list")) {
          return { stdout: createMockProjectListOutput(10), stderr: "" };
        }
        if (cmd.includes("gh project field-list")) {
          return { stdout: createMockFieldListOutput("Sprint"), stderr: "" };
        }
        if (cmd.includes("gh api graphql")) {
          return {
            stdout: createMockProjectItemsOutput([mockItem]),
            stderr: "",
          };
        }
        if (cmd.includes("gh project item-edit") && cmd.includes("--clear")) {
          return { stdout: "", stderr: "" };
        }
        return { stdout: "", stderr: "" };
      });

      const service = ProjectIterationService.getInstance(workspaceRoot);
      const result = await service.syncIteration(90, "none");

      expect(isSyncSuccess(result)).toBe(true);
      if (isSyncSuccess(result)) {
        expect(result.action).toBe("cleared");
        expect(result.iteration).toBeNull();
      }
    });
  });

  describe("Error scenarios", () => {
    it("should skip when issue not in project", async () => {
      const configContent = createMockIncrediYaml({
        projectNumber: 10,
        sprintEnabled: true,
        fieldName: "Sprint",
      });
      vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

      setExecMock(async (cmd: string) => {
        if (cmd.includes("gh repo view --json owner")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh repo view --json name")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh project list")) {
          return { stdout: createMockProjectListOutput(10), stderr: "" };
        }
        if (cmd.includes("gh project field-list")) {
          return { stdout: createMockFieldListOutput("Sprint"), stderr: "" };
        }
        if (cmd.includes("gh api graphql")) {
          // Return empty items list - issue not found
          return { stdout: createMockProjectItemsOutput([]), stderr: "" };
        }
        return { stdout: "", stderr: "" };
      });

      const service = ProjectIterationService.getInstance(workspaceRoot);
      const result = await service.syncIteration(999, "@current");

      expect(isSyncSkipped(result)).toBe(true);
      if (isSyncSkipped(result)) {
        expect(result.reason).toContain("not in project");
      }
    });

    it("should skip when iteration field not found", async () => {
      const configContent = createMockIncrediYaml({
        projectNumber: 10,
        sprintEnabled: true,
        fieldName: "Sprint",
      });
      vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

      setExecMock(async (cmd: string) => {
        if (cmd.includes("gh repo view --json owner")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh repo view --json name")) {
          return { stdout: "nightgauge", stderr: "" };
        }
        if (cmd.includes("gh project field-list")) {
          // Return fields without ITERATION type
          return {
            stdout: JSON.stringify({
              fields: [
                { id: "f1", name: "Status", type: "SINGLE_SELECT" },
                { id: "f2", name: "Priority", type: "SINGLE_SELECT" },
              ],
            }),
            stderr: "",
          };
        }
        return { stdout: "", stderr: "" };
      });

      const service = ProjectIterationService.getInstance(workspaceRoot);
      const result = await service.syncIteration(90, "@current");

      expect(isSyncSkipped(result)).toBe(true);
      if (isSyncSkipped(result)) {
        expect(result.reason).toContain("No iteration field");
      }
    });
  });
});

describe("ProjectIterationService - getIterations()", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    _execMockHandler = null;
    ProjectIterationService.resetInstance();
  });

  afterEach(() => {
    ProjectIterationService.resetInstance();
  });

  it("should return empty array when sprint disabled", async () => {
    const configContent = createMockIncrediYaml({
      projectNumber: 10,
      sprintEnabled: false,
    });
    vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

    const service = ProjectIterationService.getInstance(workspaceRoot);
    const iterations = await service.getIterations();

    expect(iterations).toEqual([]);
  });

  it("should return iterations from GraphQL query", async () => {
    const configContent = createMockIncrediYaml({
      projectNumber: 10,
      sprintEnabled: true,
      fieldName: "Sprint",
    });
    vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

    const mockIterations = createMockIterationSet();

    setExecMock(async (cmd: string) => {
      if (cmd.includes("gh repo view --json owner")) {
        return { stdout: "nightgauge", stderr: "" };
      }
      if (cmd.includes("gh project list")) {
        return { stdout: createMockProjectListOutput(10), stderr: "" };
      }
      if (cmd.includes("gh api graphql")) {
        return {
          stdout: createMockIterationQueryOutput(mockIterations),
          stderr: "",
        };
      }
      return { stdout: "", stderr: "" };
    });

    const service = ProjectIterationService.getInstance(workspaceRoot);
    const iterations = await service.getIterations();

    expect(iterations.length).toBe(3);
  });
});

describe("ProjectIterationService - Pagination", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    _execMockHandler = null;
    ProjectIterationService.resetInstance();
  });

  afterEach(() => {
    ProjectIterationService.resetInstance();
  });

  it("should handle paginated project items", async () => {
    const configContent = createMockIncrediYaml({
      projectNumber: 10,
      sprintEnabled: true,
      fieldName: "Sprint",
    });
    vi.mocked(fs.promises.readFile).mockResolvedValue(configContent);

    // Create items for two pages
    const page1Items = Array.from({ length: 100 }, (_, i) =>
      createMockProjectItem({
        id: `PVTI_page1_${i}`,
        content: {
          number: i + 1,
          repository: { nameWithOwner: "nightgauge/nightgauge" },
        },
      })
    );

    const targetItem = createMockProjectItem({
      id: "PVTI_target",
      content: {
        number: 150,
        repository: { nameWithOwner: "nightgauge/nightgauge" },
      },
    });
    const page2Items = [targetItem];

    let queryCount = 0;

    setExecMock(async (cmd: string) => {
      if (cmd.includes("gh repo view --json owner")) {
        return { stdout: "nightgauge", stderr: "" };
      }
      if (cmd.includes("gh repo view --json name")) {
        return { stdout: "nightgauge", stderr: "" };
      }
      if (cmd.includes("gh project list")) {
        return { stdout: createMockProjectListOutput(10), stderr: "" };
      }
      if (cmd.includes("gh project field-list")) {
        return { stdout: createMockFieldListOutput("Sprint"), stderr: "" };
      }
      if (cmd.includes("gh api graphql") && cmd.includes("items")) {
        queryCount++;
        if (queryCount === 1) {
          // First page - has more
          return {
            stdout: createMockProjectItemsOutput(page1Items, true, "cursor1"),
            stderr: "",
          };
        } else {
          // Second page - target item found
          return {
            stdout: createMockProjectItemsOutput(page2Items, false),
            stderr: "",
          };
        }
      }
      if (cmd.includes("gh api graphql")) {
        return {
          stdout: createMockIterationQueryOutput([createMockCurrentIteration()]),
          stderr: "",
        };
      }
      if (cmd.includes("gh project item-edit")) {
        return { stdout: "", stderr: "" };
      }
      return { stdout: "", stderr: "" };
    });

    const service = ProjectIterationService.getInstance(workspaceRoot);

    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-02-05"));

    const result = await service.syncIteration(150, "@current");

    vi.useRealTimers();

    expect(isSyncSuccess(result)).toBe(true);
    if (isSyncSuccess(result)) {
      expect(result.issue).toBe(150);
      expect(result.item_id).toBe("PVTI_target");
    }

    // Should have queried twice due to pagination
    expect(queryCount).toBe(2);
  });
});

describe("ProjectIterationService - Singleton", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    _execMockHandler = null;
    ProjectIterationService.resetInstance();
  });

  afterEach(() => {
    ProjectIterationService.resetInstance();
  });

  it("should return the same instance", () => {
    const instance1 = ProjectIterationService.getInstance(workspaceRoot);
    const instance2 = ProjectIterationService.getInstance(workspaceRoot);

    expect(instance1).toBe(instance2);
  });

  it("should allow reset for testing", () => {
    const instance1 = ProjectIterationService.getInstance(workspaceRoot);
    ProjectIterationService.resetInstance();
    const instance2 = ProjectIterationService.getInstance(workspaceRoot);

    expect(instance1).not.toBe(instance2);
  });
});
