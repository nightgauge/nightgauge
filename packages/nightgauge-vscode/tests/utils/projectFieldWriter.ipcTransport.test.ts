/**
 * Transport selection for projectFieldWriter's GraphQL calls.
 *
 * @see Issue #1913 — `gh api graphql` subprocesses spend from the GraphQL
 * budget while being invisible to the API ledger and unthrottled by the
 * rate-limit gate. The writer prefers the daemon, which is instrumented, and
 * keeps the subprocess only for the no-daemon case.
 */

import { describe, it, expect, beforeEach, vi, type Mock } from "vitest";
import { getProjectItemStatus, clearConfigCache } from "../../src/utils/projectFieldWriter";
import type { Logger } from "../../src/utils/logger";

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

vi.mock("fs", () => ({ promises: { readFile: vi.fn() } }));
vi.mock("yaml", () => ({ parse: vi.fn() }));
vi.mock("../../src/utils/configPathResolver", () => ({ resolveConfigPath: vi.fn() }));

const ipcState = {
  isConnected: false,
  githubGraphqlRaw: vi.fn(),
};
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: () => ipcState },
}));

import { exec } from "child_process";
import { promises as fsPromises } from "fs";
import { parse as yamlParse } from "yaml";
import { resolveConfigPath } from "../../src/utils/configPathResolver";

const MOCK_CWD = "/test/workspace";
const PROJECT_ID = "PVT_test_project";

function createMockLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

const mockLogger = createMockLogger();

const MOCK_YAML_CONFIG = {
  owner: "TestOrg",
  repo: "test-repo",
  project: {
    number: 1,
    id: PROJECT_ID,
    fields: {
      status: { id: "PVTSSF_status", options: { ready: "opt_ready" } },
      priority: { id: "PVTSSF_priority", options: {} },
      size: { id: "PVTSSF_size", options: {} },
    },
  },
};

/** The single-query projectItems lookup response getProjectItemStatus reads. */
const STATUS_RESPONSE = {
  data: {
    repository: {
      issue: {
        projectItems: {
          nodes: [
            {
              id: "PVTI_item",
              project: { id: PROJECT_ID },
              fieldValues: { nodes: [{ name: "Ready", field: { name: "Status" } }] },
            },
          ],
        },
      },
      pullRequest: null,
    },
  },
};

beforeEach(() => {
  vi.clearAllMocks();
  clearConfigCache();
  ipcState.isConnected = false;
  ipcState.githubGraphqlRaw = vi.fn();

  (fsPromises.readFile as Mock).mockResolvedValue("mock yaml");
  (resolveConfigPath as Mock).mockResolvedValue({
    path: `${MOCK_CWD}/.nightgauge/config.yaml`,
    isLegacy: false,
    exists: true,
  });
  (yamlParse as Mock).mockReturnValue(MOCK_YAML_CONFIG);
  (exec as unknown as Mock).mockImplementation(
    (_cmd: string, _opts: unknown, callback?: Function) => {
      if (typeof callback === "function") callback(null, JSON.stringify(STATUS_RESPONSE), "");
    }
  );
});

describe("projectFieldWriter GraphQL transport (#1913)", () => {
  it("routes through the daemon when one is connected, spawning no gh", async () => {
    ipcState.isConnected = true;
    ipcState.githubGraphqlRaw = vi.fn().mockResolvedValue(STATUS_RESPONSE);

    const status = await getProjectItemStatus(42, MOCK_CWD, mockLogger);

    expect(status).toBe("Ready");
    expect(ipcState.githubGraphqlRaw).toHaveBeenCalled();
    expect(exec as unknown as Mock).not.toHaveBeenCalled();
  });

  it("falls back to gh api graphql when no daemon is connected", async () => {
    ipcState.isConnected = false;

    const status = await getProjectItemStatus(42, MOCK_CWD, mockLogger);

    expect(status).toBe("Ready");
    expect(ipcState.githubGraphqlRaw).not.toHaveBeenCalled();
    const command = (exec as unknown as Mock).mock.calls[0]?.[0] as string;
    expect(command).toContain("gh api graphql");
  });

  // A refused or broken IPC call must not turn a board read into a failure:
  // the subprocess is still a working transport.
  it("falls back to the subprocess when the IPC call fails", async () => {
    ipcState.isConnected = true;
    ipcState.githubGraphqlRaw = vi.fn().mockRejectedValue(new Error("method not found"));

    const status = await getProjectItemStatus(42, MOCK_CWD, mockLogger);

    expect(status).toBe("Ready");
    expect(exec as unknown as Mock).toHaveBeenCalled();
  });
});
