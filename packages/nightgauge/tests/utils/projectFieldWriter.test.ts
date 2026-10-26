/**
 * Unit tests for projectFieldWriter utility.
 *
 * Verifies the single-query (no-pagination) lookup path introduced for
 * #2866 and the cross-repo identity plumbing introduced for #2867.
 *
 * @see Issue #1713 — original GraphQL project field write utility
 * @see Issue #2866 — server-side filter / cached projectItem ID lookup
 * @see Issue #2867 — cross-repo issue lookups
 * @see Issue #1501 — board fields resolved from the board, not from config
 */

import { describe, it, expect, beforeEach, vi, type Mock } from "vitest";
import {
  updateProjectItemStatus,
  getProjectItemStatus,
  ensureIssueOnProject,
  clearConfigCache,
  _testing,
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
  return { exec: execMock, execFile: vi.fn() };
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

/** A repo config with a project number and no `project.fields` (#1501). */
const MOCK_YAML_CONFIG = {
  owner: "TestOrg",
  repo: "test-repo",
  project: { number: 1 },
};

/** The board's single-select fields as `ProjectV2.fields` returns them. */
const BOARD_FIELDS = [
  {
    id: "PVTSSF_test_status",
    name: "Status",
    options: [
      { id: "opt_backlog", name: "Backlog" },
      { id: "opt_ready", name: "Ready" },
      { id: "opt_in_progress", name: "In progress" },
      { id: "opt_in_review", name: "In review" },
      { id: "opt_done", name: "Done" },
    ],
  },
  { id: "PVTSSF_test_priority", name: "Priority", options: [{ id: "opt_p1", name: "P1" }] },
  { id: "PVTSSF_test_size", name: "Size", options: [{ id: "opt_m", name: "M" }] },
  {},
];

let boardFields: unknown[] = BOARD_FIELDS;
const resolveExec = vi.fn();

function resolvesTo(project: unknown) {
  resolveExec.mockResolvedValue({ stdout: JSON.stringify(project) });
}

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function setupConfigMocks(configOverride?: unknown) {
  (fsPromises.readFile as Mock).mockImplementation((filePath: string) => {
    if (filePath.includes("config.yaml")) return Promise.resolve("mock yaml");
    return Promise.reject(new Error("File not found"));
  });

  (resolveConfigPath as Mock).mockResolvedValue({
    path: `${MOCK_CWD}/.nightgauge/config.yaml`,
    isLegacy: false,
    exists: true,
  });

  (yamlParse as Mock).mockReturnValue(configOverride ?? MOCK_YAML_CONFIG);
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

/**
 * Answer `gh api graphql` calls in order. The board field-discovery query is
 * answered out of band from `boardFields` and counted separately, so each
 * test's sequence and call indexes name only issue lookups and mutations.
 */
function setupExecMockSequential(responses: unknown[]) {
  const execMock = exec as unknown as Mock;
  let callIndex = 0;

  execMock.mockImplementation((cmd: string, opts: unknown, callback?: Function) => {
    if (typeof callback !== "function") return;
    if (isFieldQuery(cmd)) {
      callback(null, JSON.stringify({ data: { node: { fields: { nodes: boardFields } } } }), "");
      return;
    }
    const response = responses[callIndex] ?? { data: {} };
    callIndex++;
    if (response instanceof Error) callback(response, "", "error");
    else callback(null, JSON.stringify(response), "");
  });

  const calls = () => execMock.mock.calls.filter((c) => !isFieldQuery(c[0] as string));
  const fieldQueries = () => execMock.mock.calls.filter((c) => isFieldQuery(c[0] as string)).length;
  return { calls, fieldQueries };
}

function isFieldQuery(cmd: string): boolean {
  return cmd.includes("ProjectV2SingleSelectField { id name options");
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
    boardFields = BOARD_FIELDS;
    resolvesTo({ number: 1, owner: "TestOrg", owner_type: "org", id: PROJECT_ID });
    _testing.setBoardResolverDeps({
      resolveBinary: async () => "/bin/nightgauge",
      exec: resolveExec,
    });
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

    describe("board resolution (#1501)", () => {
      it("resolves fields from the board with no project.fields in config", async () => {
        setupConfigMocks();
        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };
        const execMock = setupExecMockSequential([find, update]);

        const result = await updateProjectItemStatus(42, "In review", MOCK_CWD, logger);

        expect(result).toEqual({ success: true });
        expect(resolveExec).toHaveBeenCalledWith(
          "/bin/nightgauge",
          ["project", "resolve", "--repo", "TestOrg/test-repo", "--json"],
          MOCK_CWD
        );
        expect(execMock.fieldQueries()).toBe(1);
        const cmd = execMock.calls()[1][0] as string;
        expect(cmd).toContain("PVTSSF_test_status");
        expect(cmd).toContain("opt_in_review");
        expect(cmd).toContain(PROJECT_ID);
      });

      it("caches the board: a second transition issues no discovery", async () => {
        setupConfigMocks();
        const find = projectItemsResponse({ itemId: "PVTI_item42" });
        const update = {
          data: { updateProjectV2ItemFieldValue: { projectV2Item: { id: "PVTI_item42" } } },
        };
        const execMock = setupExecMockSequential([find, update, update]);

        await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        const second = await updateProjectItemStatus(42, "In progress", MOCK_CWD, logger);

        expect(second.success).toBe(true);
        expect(resolveExec).toHaveBeenCalledTimes(1);
        expect(execMock.fieldQueries()).toBe(1);
      });

      it("fails with an unresolvable-board error when no project is mapped", async () => {
        setupConfigMocks();
        resolveExec.mockRejectedValue(
          Object.assign(new Error("exit 1"), { stderr: "no project mapped for TestOrg/test-repo" })
        );
        const execMock = setupExecMockSequential([]);

        const result = await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);

        expect(result.success).toBe(false);
        expect(result.error).toBe(
          "Board fields unresolvable for TestOrg/test-repo: no project mapped for TestOrg/test-repo"
        );
        expect(execMock.calls()).toHaveLength(0);
      });

      it("fails when the board has no Status field, and does not cache the failure", async () => {
        setupConfigMocks();
        boardFields = [BOARD_FIELDS[1]];
        setupExecMockSequential([]);

        const first = await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        expect(first.success).toBe(false);
        expect(first.error).toContain("has no Status field");

        await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        expect(resolveExec).toHaveBeenCalledTimes(2);
      });

      it("fails when no repository is named and config has none", async () => {
        (resolveConfigPath as Mock).mockResolvedValue({
          path: `${MOCK_CWD}/.nightgauge/config.yaml`,
          isLegacy: false,
          exists: false,
        });

        const result = await updateProjectItemStatus(42, "Ready", MOCK_CWD, logger);
        expect(result.success).toBe(false);
        expect(result.error).toContain("Board fields unresolvable");
        expect(resolveExec).not.toHaveBeenCalled();
      });

      it("resolves the board of the override repository", async () => {
        setupConfigMocks();
        setupExecMockSequential([projectItemsResponse({ itemId: "PVTI_x" }), { data: {} }]);

        await updateProjectItemStatus(7, "Ready", MOCK_CWD, logger, "OtherOrg/other-repo");

        expect(resolveExec.mock.calls[0][1]).toContain("OtherOrg/other-repo");
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
        expect(execMock.calls()).toHaveLength(2);
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

        const mutationCall = execMock.calls()[1];
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
        const lookupCall = execMock.calls()[0];
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

        const lookupBody = execMock.calls()[0][0] as string;
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

        const lookupBody = execMock.calls()[0][0] as string;
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

        expect(execMock.calls()).toHaveLength(3);
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

        expect(execMock.calls()).toHaveLength(4);
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
      const body = execMock.calls()[0][0] as string;
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

      const body = execMock.calls()[0][0] as string;
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
});
