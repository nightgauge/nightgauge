/**
 * Unit tests for projectFieldWriter utility.
 *
 * Verifies the single-query (no-pagination) lookup path introduced for
 * #2866 and the cross-repo identity plumbing introduced for #2867.
 *
 * @see Issue #1713 — original GraphQL project field write utility
 * @see Issue #2866 — server-side filter / cached projectItem ID lookup
 * @see Issue #2867 — cross-repo issue lookups
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  updateProjectItemStatus,
  getProjectItemStatus,
  ensureIssueOnProject,
  clearConfigCache,
} from "../../src/utils/projectFieldWriter";
import type { Logger } from "../../src/utils/logger";

// ============================================================================
// Mocks
// ============================================================================

vi.mock("child_process", () => {
  const execMock = vi.fn();
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  (execMock as any)[kCustom] = (cmd: string, opts: any) =>
    new Promise<{ stdout: string; stderr: string }>((resolve, reject) => {
      execMock(cmd, opts, (err: Error | null, stdout: string, stderr: string) => {
        if (err) reject(Object.assign(err, { stdout, stderr }));
        else resolve({ stdout, stderr });
      });
    });
  return { exec: execMock };
});

vi.mock("fs", () => ({
  promises: {
    readFile: vi.fn(),
  },
}));

vi.mock("yaml", () => ({
  parse: vi.fn(),
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPath: vi.fn(),
}));

import { exec } from "child_process";
import { promises as fsPromises } from "fs";
import { parse as yamlParse } from "yaml";
import { resolveConfigPath } from "../../src/utils/configPathResolver";

// ============================================================================
// Fixtures
// ============================================================================

const MOCK_CWD = "/test/workspace";
const PROJECT_ID = "PVT_test_project";

const MOCK_YAML_CONFIG = {
  owner: "TestOrg",
  repo: "test-repo",
  project: {
    number: 1,
    id: PROJECT_ID,
    fields: {
      status: {
        id: "PVTSSF_test_status",
        options: {
          backlog: "opt_backlog",
          ready: "opt_ready",
          "in-progress": "opt_in_progress",
          "in-review": "opt_in_review",
          done: "opt_done",
        },
      },
      priority: {
        id: "PVTSSF_test_priority",
        options: { p0: "opt_p0", p1: "opt_p1", p2: "opt_p2" },
      },
      size: {
        id: "PVTSSF_test_size",
        options: { xs: "opt_xs", s: "opt_s", m: "opt_m", l: "opt_l", xl: "opt_xl" },
      },
    },
  },
};

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function setupConfigMocks(configOverride?: unknown) {
  (fsPromises.readFile as ReturnType<typeof vi.fn>).mockImplementation((filePath: string) => {
    if (filePath.includes("config.yaml")) return Promise.resolve("mock yaml");
    return Promise.reject(new Error("File not found"));
  });

  (resolveConfigPath as ReturnType<typeof vi.fn>).mockResolvedValue({
    path: `${MOCK_CWD}/.nightgauge/config.yaml`,
    isLegacy: false,
    exists: true,
  });

  (yamlParse as ReturnType<typeof vi.fn>).mockReturnValue(configOverride ?? MOCK_YAML_CONFIG);
}

/** Build a projectItems response for the new single-query lookup. */
function projectItemsResponse(opts: {
  itemId?: string | null;
  matchProjectId?: string;
  fieldValues?: Array<{ name: string; field: { name: string } }>;
  kind?: "issue" | "pullRequest" | "neither";
}) {
  const node = opts.itemId
    ? {
        projectItems: {
          nodes: [
            {
              id: opts.itemId,
              project: { id: opts.matchProjectId ?? PROJECT_ID },
              fieldValues: { nodes: opts.fieldValues ?? [] },
            },
          ],
        },
      }
    : { projectItems: { nodes: [] } };

  return {
    data: {
      repository:
        opts.kind === "pullRequest"
          ? { issue: null, pullRequest: node }
          : opts.kind === "neither"
            ? { issue: null, pullRequest: null }
            : { issue: node, pullRequest: null },
    },
  };
}

function contentNodeResponse(opts: {
  id?: string | null;
  kind?: "issue" | "pullRequest" | "neither";
}) {
  const node = opts.id ? { id: opts.id } : null;
  return {
    data: {
      repository:
        opts.kind === "pullRequest"
          ? { issue: null, pullRequest: node }
          : opts.kind === "neither"
            ? { issue: null, pullRequest: null }
            : { issue: node, pullRequest: null },
    },
  };
}

function setupExecMockSequential(responses: unknown[]) {
  const execMock = exec as unknown as ReturnType<typeof vi.fn>;
  let callIndex = 0;

  execMock.mockImplementation((cmd: string, opts: unknown, callback?: Function) => {
    if (typeof callback === "function") {
      const response = responses[callIndex] ?? { data: {} };
      callIndex++;
      if (response instanceof Error) callback(response, "", "error");
      else callback(null, JSON.stringify(response), "");
    }
  });

  return execMock;
}

// ============================================================================
// Tests
// ============================================================================

describe("projectFieldWriter", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    clearConfigCache();
    logger = createMockLogger();
  });

  describe("updateProjectItemStatus", () => {
    describe("input validation", () => {
      it("rejects negative issue numbers", async () => {
        const result = await updateProjectItemStatus(-1, "Ready", MOCK_CWD, logger);
        expect(result.success).toBe(false);
        expect(result.error).toContain("Invalid issue number");
      });

      it("rejects zero", async () => {
        const result = await updateProjectItemStatus(0, "Ready", MOCK_CWD, logger);
        expect(result.success).toBe(false);
      });

      it("rejects non-integer", async () => {
        const result = await updateProjectItemStatus(1.5, "In progress", MOCK_CWD, logger);
        expect(result.success).toBe(false);
      });
    });

    describe("config loading", () => {
      it("fails when config.yaml is missing", async () => {
        (resolveConfigPath as ReturnType<typeof vi.fn>).mockResolvedValue({
          path: `${MOCK_CWD}/.nightgauge/config.yaml`,
          isLegacy: false,
          exists: false,
        });

        const result = await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        expect(result.success).toBe(false);
        expect(result.error).toContain("No project configuration found");
      });

      it("fails when config.yaml has no field data", async () => {
        setupConfigMocks({ owner: "TestOrg", repo: "test-repo", project: { number: 1 } });
        const result = await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        expect(result.success).toBe(false);
      });

      it("loads the flat field schema (project.*_field_id + field_options) — #3773", async () => {
        // Mirrors the acmeapp config layout that previously warned
        // "No project configuration found" despite being fully populated.
        setupConfigMocks({
          owner: "TestOrg",
          repo: "test-repo",
          project: {
            number: 1,
            id: PROJECT_ID,
            status_field_id: "PVTSSF_test_status",
            priority_field_id: "PVTSSF_test_priority",
            size_field_id: "PVTSSF_test_size",
            field_options: {
              status: {
                backlog: "opt_backlog",
                ready: "opt_ready",
                in_progress: "opt_in_progress",
                in_review: "opt_in_review",
                done: "opt_done",
              },
              priority: { p0: "opt_p0", p1: "opt_p1", p2: "opt_p2" },
              size: { xs: "opt_xs", s: "opt_s", m: "opt_m", l: "opt_l", xl: "opt_xl" },
            },
          },
        });

        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };
        const execMock = setupExecMockSequential([find, update]);

        // snake_case "in_review" must remap to the "In review" runtime key.
        const result = await updateProjectItemStatus(42, "In review", MOCK_CWD, logger);
        expect(result.success).toBe(true);
        const cmd = execMock.mock.calls[1][0] as string;
        expect(cmd).toContain("PVTSSF_test_status");
        expect(cmd).toContain("opt_in_review");
        expect(cmd).toContain(PROJECT_ID);
      });
    });

    describe("happy path — item already on project", () => {
      it("finds item via single-query lookup and updates status", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };

        const execMock = setupExecMockSequential([find, update]);

        const result = await updateProjectItemStatus(42, "In progress", MOCK_CWD, logger);

        expect(result.success).toBe(true);
        // Two calls: lookup + mutation. NO pagination.
        expect(execMock).toHaveBeenCalledTimes(2);
        expect(logger.info).toHaveBeenCalledWith(
          "Updated project board status",
          expect.objectContaining({ issueNumber: 42, status: "In progress" })
        );
      });

      it("passes correct field and option IDs to GraphQL", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };

        const execMock = setupExecMockSequential([find, update]);

        await updateProjectItemStatus(42, "Done", MOCK_CWD, logger);

        const mutationCall = execMock.mock.calls[1];
        const cmd = mutationCall[0] as string;
        expect(cmd).toContain("PVTSSF_test_status");
        expect(cmd).toContain("opt_done");
        expect(cmd).toContain(PROJECT_ID);
      });

      it("recognizes a PullRequest item (same projectItems shape)", async () => {
        setupConfigMocks();

        const issueMiss = projectItemsResponse({ itemId: null, kind: "neither" });
        const find = projectItemsResponse({ itemId: "PVTI_pr42", kind: "pullRequest" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_pr42" } } },
        };
        setupExecMockSequential([issueMiss, find, update]);

        const result = await updateProjectItemStatus(42, "In review", MOCK_CWD, logger);
        expect(result.success).toBe(true);
      });
    });

    describe("happy path — item not on project", () => {
      it("adds issue then updates status", async () => {
        setupConfigMocks();

        const findIssueMiss = projectItemsResponse({ itemId: null });
        const issueNode = contentNodeResponse({ id: "I_issue42" });
        const addResult = {
          data: { addProjectV2ItemById: { item: { id: "PVTI_new_item42" } } },
        };
        const updateResult = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_new_item42" } } },
        };

        setupExecMockSequential([findIssueMiss, issueNode, addResult, updateResult]);

        const result = await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        expect(result.success).toBe(true);
        expect(logger.info).toHaveBeenCalledWith(
          "Added issue to project board before status update",
          expect.any(Object)
        );
      });
    });

    describe("cross-repo identity (#2867)", () => {
      it("uses the override owner/repo when provided", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_platform714" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_platform714" } } },
        };
        const execMock = setupExecMockSequential([find, update]);

        const result = await updateProjectItemStatus(
          714,
          "In progress",
          MOCK_CWD,
          logger,
          "acme/platform"
        );

        expect(result.success).toBe(true);
        // The lookup query must reference the override repo, not the config default.
        const lookupCall = execMock.mock.calls[0];
        const lookupBody = lookupCall[0] as string;
        expect(lookupBody).toContain("acme");
        expect(lookupBody).toContain("platform");
      });

      it("falls back to config default when override is missing", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_local42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_local42" } } },
        };
        const execMock = setupExecMockSequential([find, update]);

        await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);

        const lookupBody = execMock.mock.calls[0][0] as string;
        expect(lookupBody).toContain("TestOrg");
        expect(lookupBody).toContain("test-repo");
      });

      it("ignores malformed override (no slash) and uses config default", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_local42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_local42" } } },
        };
        const execMock = setupExecMockSequential([find, update]);

        await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger, "not-a-repo-string");

        const lookupBody = execMock.mock.calls[0][0] as string;
        expect(lookupBody).toContain("TestOrg");
      });
    });

    describe("projectItem ID cache (#2866)", () => {
      it("skips the lookup query on a cache hit", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };
        // 1st update: 2 calls (lookup + mutation). 2nd update: 1 call (mutation only).
        const execMock = setupExecMockSequential([find, update, update]);

        await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        await updateProjectItemStatus(42, "In progress", MOCK_CWD, logger);

        expect(execMock).toHaveBeenCalledTimes(3);
      });

      it("clearConfigCache also clears the projectItem ID cache", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };
        // After clear, the second update must re-fetch — so 4 calls total.
        const execMock = setupExecMockSequential([find, update, find, update]);

        await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        clearConfigCache();
        await updateProjectItemStatus(42, "In progress", MOCK_CWD, logger);

        expect(execMock).toHaveBeenCalledTimes(4);
      });
    });

    describe("error handling", () => {
      it("reports error when GraphQL mutation fails", async () => {
        setupConfigMocks();

        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        setupExecMockSequential([find, new Error("GraphQL mutation failed")]);

        const result = await updateProjectItemStatus(42, "Done", MOCK_CWD, logger);
        expect(result.success).toBe(false);
        expect(result.error).toContain("Failed to update status");
      });

      it("reports error for unknown status option", async () => {
        setupConfigMocks();
        const result = await updateProjectItemStatus(42, "NonExistent" as any, MOCK_CWD, logger);
        expect(result.success).toBe(false);
        expect(result.error).toContain("Status option");
      });
    });
  });

  describe("getProjectItemStatus", () => {
    it("returns null for invalid issue number", async () => {
      const result = await getProjectItemStatus(-1, MOCK_CWD, logger);
      expect(result).toBeNull();
    });

    it("returns the Status field value when item is found", async () => {
      setupConfigMocks();

      const response = projectItemsResponse({
        itemId: "PVTI_item42",
        fieldValues: [{ name: "In progress", field: { name: "Status" } }],
      });
      setupExecMockSequential([response]);

      const result = await getProjectItemStatus(42, MOCK_CWD, logger);
      expect(result).toBe("In progress");
    });

    it("returns null when issue is not on this project", async () => {
      setupConfigMocks();

      const otherProjectResponse = projectItemsResponse({
        itemId: "PVTI_other",
        matchProjectId: "PVT_some_other_project",
      });
      setupExecMockSequential([otherProjectResponse]);

      const result = await getProjectItemStatus(42, MOCK_CWD, logger);
      expect(result).toBeNull();
    });

    it("returns null when item is found but Status field is unset", async () => {
      setupConfigMocks();

      const response = projectItemsResponse({ itemId: "PVTI_item42", fieldValues: [] });
      setupExecMockSequential([response]);

      const result = await getProjectItemStatus(42, MOCK_CWD, logger);
      expect(result).toBeNull();
    });

    it("uses cross-repo override in the GraphQL query", async () => {
      setupConfigMocks();

      const response = projectItemsResponse({
        itemId: "PVTI_platform_item",
        fieldValues: [{ name: "Done", field: { name: "Status" } }],
      });
      const execMock = setupExecMockSequential([response]);

      const result = await getProjectItemStatus(714, MOCK_CWD, logger, "acme/platform");

      expect(result).toBe("Done");
      const body = execMock.mock.calls[0][0] as string;
      expect(body).toContain("platform");
    });

    it("handles GraphQL errors gracefully", async () => {
      setupConfigMocks();
      setupExecMockSequential([new Error("Network error")]);

      const result = await getProjectItemStatus(42, MOCK_CWD, logger);
      expect(result).toBeNull();
      expect(logger.warn).toHaveBeenCalledWith(
        "Failed to read project item status",
        expect.objectContaining({ issueNumber: 42 })
      );
    });
  });

  describe("ensureIssueOnProject", () => {
    it("returns null for invalid issue number", async () => {
      const result = await ensureIssueOnProject(0, MOCK_CWD, logger);
      expect(result).toBeNull();
    });

    it("returns existing item ID when issue is already on project", async () => {
      setupConfigMocks();

      const response = projectItemsResponse({ itemId: "PVTI_existing42" });
      setupExecMockSequential([response]);

      const result = await ensureIssueOnProject(42, MOCK_CWD, logger);
      expect(result).toBe("PVTI_existing42");
      expect(logger.debug).toHaveBeenCalledWith(
        "Issue already on project board",
        expect.objectContaining({ issueNumber: 42 })
      );
    });

    it("adds issue when not present", async () => {
      setupConfigMocks();

      const findIssueMiss = projectItemsResponse({ itemId: null });
      const issueNode = contentNodeResponse({ id: "I_issue42" });
      const addResult = {
        data: { addProjectV2ItemById: { item: { id: "PVTI_new42" } } },
      };
      setupExecMockSequential([findIssueMiss, issueNode, addResult]);

      const result = await ensureIssueOnProject(42, MOCK_CWD, logger);
      expect(result).toBe("PVTI_new42");
      expect(logger.info).toHaveBeenCalledWith(
        "Added issue to project board",
        expect.objectContaining({ issueNumber: 42, itemId: "PVTI_new42" })
      );
    });

    it("returns null when issue does not exist", async () => {
      setupConfigMocks();

      const findIssueMiss = projectItemsResponse({ itemId: null });
      const issueNode = contentNodeResponse({ kind: "neither" });
      const prNode = contentNodeResponse({ kind: "neither" });
      setupExecMockSequential([findIssueMiss, issueNode, prNode]);

      const result = await ensureIssueOnProject(42, MOCK_CWD, logger);
      expect(result).toBeNull();
    });

    it("returns null when add mutation fails", async () => {
      setupConfigMocks();

      const findIssueMiss = projectItemsResponse({ itemId: null });
      const issueNode = contentNodeResponse({ id: "I_issue42" });
      setupExecMockSequential([findIssueMiss, issueNode, new Error("Permission denied")]);

      const result = await ensureIssueOnProject(42, MOCK_CWD, logger);
      expect(result).toBeNull();
      expect(logger.error).toHaveBeenCalledWith(
        "Failed to add issue to project",
        expect.any(Error)
      );
    });

    it("forwards cross-repo override to the lookup", async () => {
      setupConfigMocks();

      const response = projectItemsResponse({ itemId: "PVTI_platform_existing" });
      const execMock = setupExecMockSequential([response]);

      const result = await ensureIssueOnProject(714, MOCK_CWD, logger, "acme/platform");
      expect(result).toBe("PVTI_platform_existing");

      const body = execMock.mock.calls[0][0] as string;
      expect(body).toContain("platform");
    });

    it("falls back to PullRequest lookup without throwing when the issue query misses", async () => {
      setupConfigMocks();

      const issueMiss = projectItemsResponse({ itemId: null, kind: "neither" });
      const prHit = projectItemsResponse({ itemId: "PVTI_pr714", kind: "pullRequest" });
      setupExecMockSequential([issueMiss, prHit]);

      const result = await ensureIssueOnProject(714, MOCK_CWD, logger);
      expect(result).toBe("PVTI_pr714");
    });
  });

  describe("multi-project config", () => {
    it("updates status across all configured projects", async () => {
      setupConfigMocks({
        owner: "TestOrg",
        repo: "test-repo",
        project: { id: PROJECT_ID, fields: MOCK_YAML_CONFIG.project.fields },
        projects: [
          {
            name: "Dev Board",
            owner: "TestOrg",
            repo: "test-repo",
            id: "PVT_dev_board",
            default: true,
          },
          {
            name: "Roadmap",
            owner: "TestOrg",
            repo: "test-repo",
            id: "PVT_roadmap",
            default: false,
          },
        ],
      });

      const find1 = projectItemsResponse({ itemId: "PVTI_dev42", matchProjectId: "PVT_dev_board" });
      const find2 = projectItemsResponse({ itemId: "PVTI_road42", matchProjectId: "PVT_roadmap" });
      const update = {
        data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_x" } } },
      };

      setupExecMockSequential([find1, update, find2, update]);

      const result = await updateProjectItemStatus(42, "Done", MOCK_CWD, logger);
      expect(result.success).toBe(true);
    });
  });
});
