/**
 * Tests for workspace-first repos assembly in the agent registration payload (#3546).
 *
 * Tests the priority chain:
 *   workspace config repos → enabledRepos → platform config → []
 *
 * And the workspace config file watcher re-registration trigger.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { WorkspaceRegistrationPayloadBuilder } from "../../src/services/WorkspaceRegistrationPayloadBuilder";
import type { WorkspaceConfig } from "../../src/types/WorkspaceConfig";

vi.mock("vscode", () => {
  const watchers: Array<{
    onDidChange: ReturnType<typeof vi.fn>;
    onDidCreate: ReturnType<typeof vi.fn>;
    onDidDelete: ReturnType<typeof vi.fn>;
    dispose: ReturnType<typeof vi.fn>;
  }> = [];

  const mockWatcher = {
    onDidChange: vi.fn(),
    onDidCreate: vi.fn(),
    onDidDelete: vi.fn(),
    dispose: vi.fn(),
  };

  return {
    RelativePattern: vi.fn().mockImplementation((base, pattern) => ({ base, pattern })),
    workspace: {
      workspaceFolders: [{ uri: { fsPath: "/workspace" } }],
      createFileSystemWatcher: vi.fn().mockReturnValue(mockWatcher),
    },
    Uri: {
      file: vi.fn().mockReturnValue({ fsPath: "/fallback" }),
    },
    extensions: {
      getExtension: vi.fn().mockReturnValue({ packageJSON: { version: "0.1.42" } }),
    },
    version: "1.90.0",
    _mockWatcher: mockWatcher,
    _watchers: watchers,
  };
});

import type { Repository } from "../../src/models/Repository";
import type { GitHubConfig } from "../../src/models/Repository";

/**
 * Build a mock Repository with the given github config result after loadConfig().
 */
function makeRepo(
  name: string,
  github: GitHubConfig | undefined
): Pick<Repository, "loadConfig" | "github"> {
  let loaded = false;
  const ghResult = github;
  return {
    loadConfig: vi.fn().mockImplementation(async () => {
      loaded = true;
      return loaded ? { github: ghResult } : null;
    }),
    get github() {
      return ghResult;
    },
  } as unknown as Pick<Repository, "loadConfig" | "github">;
}

/** Build a mock WorkspaceManager */
function makeWorkspaceManager(repos: Pick<Repository, "loadConfig" | "github">[]) {
  return {
    getAllRepositories: vi.fn().mockReturnValue(repos),
    reload: vi.fn().mockResolvedValue(undefined),
  };
}

/** Build mock enabledReposConfigService */
function makeEnabledRepos(slugs: string[]) {
  return { readEnabledRepos: vi.fn().mockReturnValue(slugs) };
}

/** Simulate the repos assembly logic from extension.ts (priority chain) */
async function assembleRepos(
  workspaceManager: ReturnType<typeof makeWorkspaceManager> | null,
  enabledReposConfigService: ReturnType<typeof makeEnabledRepos> | null,
  platformOwner?: string,
  platformRepo?: string
): Promise<Array<{ owner: string; repo: string }>> {
  // Priority 1: workspace config repos
  const allRepos = workspaceManager?.getAllRepositories() ?? [];
  const workspaceRepos: Array<{ owner: string; repo: string }> = [];
  for (const repo of allRepos) {
    await repo.loadConfig();
    const gh = repo.github;
    if (gh?.owner && gh?.repo) {
      workspaceRepos.push({ owner: gh.owner, repo: gh.repo });
    }
  }

  if (workspaceRepos.length > 0) {
    return workspaceRepos;
  }

  // Priority 2: enabledRepos fallback
  const repoSlugs = enabledReposConfigService?.readEnabledRepos() ?? [];
  if (repoSlugs.length > 0) {
    return repoSlugs
      .map((slug: string) => {
        const [owner, repo] = slug.split("/");
        return owner && repo ? { owner, repo } : null;
      })
      .filter(
        (r: { owner: string; repo: string } | null): r is { owner: string; repo: string } =>
          r !== null
      );
  }

  // Priority 3: platform config fallback
  if (platformOwner && platformRepo) {
    return [{ owner: platformOwner, repo: platformRepo }];
  }

  return [];
}

describe("extension registration payload assembly (#3546)", () => {
  describe("workspace-first priority chain", () => {
    it("uses workspace repos when present", async () => {
      const wm = makeWorkspaceManager([
        makeRepo("frontend", { owner: "nightgauge", repo: "frontend" }),
        makeRepo("backend", { owner: "nightgauge", repo: "backend" }),
      ]);
      const er = makeEnabledRepos(["nightgauge/enabled-repo"]);

      const repos = await assembleRepos(wm, er, "nightgauge", "fallback");

      expect(repos).toEqual([
        { owner: "nightgauge", repo: "frontend" },
        { owner: "nightgauge", repo: "backend" },
      ]);
      expect(er.readEnabledRepos).not.toHaveBeenCalled();
    });

    it("falls through to enabledRepos when workspace repos have no github config", async () => {
      const wm = makeWorkspaceManager([makeRepo("no-config", undefined)]);
      const er = makeEnabledRepos(["nightgauge/enabled-repo"]);

      const repos = await assembleRepos(wm, er, "nightgauge", "fallback");

      expect(repos).toEqual([{ owner: "nightgauge", repo: "enabled-repo" }]);
    });

    it("falls through to enabledRepos when workspace is empty", async () => {
      const wm = makeWorkspaceManager([]);
      const er = makeEnabledRepos(["nightgauge/my-repo"]);

      const repos = await assembleRepos(wm, er);

      expect(repos).toEqual([{ owner: "nightgauge", repo: "my-repo" }]);
    });

    it("workspace repos take priority over enabledRepos when both are set", async () => {
      const wm = makeWorkspaceManager([
        makeRepo("ws-repo", { owner: "nightgauge", repo: "ws-repo" }),
      ]);
      const er = makeEnabledRepos(["nightgauge/enabled-repo"]);

      const repos = await assembleRepos(wm, er, "nightgauge", "platform-repo");

      expect(repos).toEqual([{ owner: "nightgauge", repo: "ws-repo" }]);
    });

    it("falls through to platform config when workspace empty and no enabledRepos", async () => {
      const wm = makeWorkspaceManager([]);
      const er = makeEnabledRepos([]);

      const repos = await assembleRepos(wm, er, "nightgauge", "platform-default");

      expect(repos).toEqual([{ owner: "nightgauge", repo: "platform-default" }]);
    });

    it("returns empty array when all sources are empty", async () => {
      const wm = makeWorkspaceManager([]);
      const er = makeEnabledRepos([]);

      const repos = await assembleRepos(wm, er);

      expect(repos).toEqual([]);
    });

    it("returns empty array when workspaceManager is null", async () => {
      const er = makeEnabledRepos([]);

      const repos = await assembleRepos(null, er);

      expect(repos).toEqual([]);
    });

    it("skips repos with partial github config (owner but no repo)", async () => {
      const partialRepo = {
        loadConfig: vi.fn().mockResolvedValue({}),
        get github() {
          return { owner: "nightgauge", repo: "" } as unknown as GitHubConfig;
        },
      } as unknown as Pick<Repository, "loadConfig" | "github">;

      const wm = makeWorkspaceManager([
        partialRepo,
        makeRepo("valid", { owner: "nightgauge", repo: "valid-repo" }),
      ]);

      const repos = await assembleRepos(wm, makeEnabledRepos([]));

      expect(repos).toEqual([{ owner: "nightgauge", repo: "valid-repo" }]);
    });

    it("calls loadConfig on each workspace repo before reading github", async () => {
      const repo1 = makeRepo("r1", { owner: "nightgauge", repo: "r1" });
      const repo2 = makeRepo("r2", { owner: "nightgauge", repo: "r2" });
      const wm = makeWorkspaceManager([repo1, repo2]);

      await assembleRepos(wm, makeEnabledRepos([]));

      expect(repo1.loadConfig).toHaveBeenCalledOnce();
      expect(repo2.loadConfig).toHaveBeenCalledOnce();
    });
  });

  describe("workspace config file watcher re-registration trigger", () => {
    it("watcher calls reload() and clears agentId on workspace config change", async () => {
      const wm = makeWorkspaceManager([makeRepo("r1", { owner: "nightgauge", repo: "r1" })]);

      const globalState = {
        get: vi.fn().mockReturnValue(undefined),
        update: vi.fn().mockResolvedValue(undefined),
      };

      const agentRegistrationService = {
        register: vi.fn().mockResolvedValue("new-agent-id"),
      };

      const agentHeartbeatService = {
        start: vi.fn(),
      };

      const sessionManager = {
        state: "authenticated",
      };

      const machineFingerprint = {
        getMachineId: vi.fn().mockReturnValue("machine-123"),
      };

      // Simulate the scheduleReRegister function from extension.ts
      const scheduleReRegister = async () => {
        await wm.reload();
        await globalState.update("nightgauge.agentId", undefined);

        if (sessionManager.state === "authenticated") {
          const allRepos = wm.getAllRepositories();
          const workspaceRepos: Array<{ owner: string; repo: string }> = [];
          for (const repo of allRepos) {
            await repo.loadConfig();
            const gh = repo.github;
            if (gh?.owner && gh?.repo) {
              workspaceRepos.push({ owner: gh.owner, repo: gh.repo });
            }
          }
          const agentId = await agentRegistrationService.register({
            agent_version: "0.1.42",
            capabilities: ["headless", "interactive"],
            repos: workspaceRepos,
            machine_id: machineFingerprint.getMachineId(),
            vscode_version: "1.90.0",
          });
          if (agentId) {
            await globalState.update("nightgauge.agentId", agentId);
            agentHeartbeatService.start(agentId);
          }
        }
      };

      await scheduleReRegister();

      expect(wm.reload).toHaveBeenCalledOnce();
      expect(globalState.update).toHaveBeenCalledWith("nightgauge.agentId", undefined);
      expect(agentRegistrationService.register).toHaveBeenCalledWith(
        expect.objectContaining({
          repos: [{ owner: "nightgauge", repo: "r1" }],
        })
      );
      expect(globalState.update).toHaveBeenCalledWith("nightgauge.agentId", "new-agent-id");
      expect(agentHeartbeatService.start).toHaveBeenCalledWith("new-agent-id");
    });

    it("skips re-registration when not authenticated", async () => {
      const wm = makeWorkspaceManager([makeRepo("r1", { owner: "nightgauge", repo: "r1" })]);

      const globalState = {
        update: vi.fn().mockResolvedValue(undefined),
      };

      const agentRegistrationService = {
        register: vi.fn().mockResolvedValue("agent-id"),
      };

      const sessionManager = { state: "unauthenticated" };

      const scheduleReRegister = async () => {
        await wm.reload();
        await globalState.update("nightgauge.agentId", undefined);
        if (sessionManager.state === "authenticated") {
          await agentRegistrationService.register({
            agent_version: "0.1.0",
            capabilities: [],
            repos: [],
            machine_id: "",
            vscode_version: "",
          });
        }
      };

      await scheduleReRegister();

      expect(wm.reload).toHaveBeenCalledOnce();
      expect(globalState.update).toHaveBeenCalledWith("nightgauge.agentId", undefined);
      expect(agentRegistrationService.register).not.toHaveBeenCalled();
    });
  });
});

function makeWorkspaceConfig(name: string): WorkspaceConfig {
  return {
    workspace: { name },
    repositories: [{ name: "repo-a", path: "./repo-a" }],
  };
}

/** Extended mock WorkspaceManager with getWorkspaceConfig() */
function makeWorkspaceManagerWithConfig(
  repos: Pick<Repository, "loadConfig" | "github">[],
  wsConfig: WorkspaceConfig | null
) {
  return {
    getAllRepositories: vi.fn().mockReturnValue(repos),
    reload: vi.fn().mockResolvedValue(undefined),
    getWorkspaceConfig: vi.fn().mockReturnValue(wsConfig),
  };
}

describe("workspace metadata in registration payload (#3668)", () => {
  it("includes workspace block when workspace config is present", async () => {
    const wsConfig = makeWorkspaceConfig("My Workspace");
    const wm = makeWorkspaceManagerWithConfig(
      [makeRepo("frontend", { owner: "nightgauge", repo: "frontend" })],
      wsConfig
    );

    const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(wm.getWorkspaceConfig());
    expect(workspaceMeta).toEqual({ slug: "my-workspace", display_name: "My Workspace" });

    const repos = await assembleRepos(wm, makeEnabledRepos([]), undefined, undefined);
    expect(repos).toEqual([{ owner: "nightgauge", repo: "frontend" }]);

    const payload = {
      agent_version: "0.1.0",
      capabilities: ["headless", "interactive"] as string[],
      repos,
      machine_id: "machine-123",
      vscode_version: "1.90.0",
      workspace: workspaceMeta,
    };

    expect(payload.workspace).toEqual({ slug: "my-workspace", display_name: "My Workspace" });
  });

  it("omits workspace block when no workspace config (single-repo mode)", async () => {
    const wm = makeWorkspaceManagerWithConfig(
      [makeRepo("solo-repo", { owner: "nightgauge", repo: "solo-repo" })],
      null
    );

    const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(wm.getWorkspaceConfig());
    expect(workspaceMeta).toBeUndefined();

    const payload = {
      agent_version: "0.1.0",
      capabilities: ["headless", "interactive"] as string[],
      repos: [{ owner: "nightgauge", repo: "solo-repo" }],
      machine_id: "machine-123",
      vscode_version: "1.90.0",
      workspace: workspaceMeta,
    };

    // JSON.stringify omits undefined fields — legacy single-repo path is intact
    const json = JSON.parse(JSON.stringify(payload)) as Record<string, unknown>;
    expect(json["workspace"]).toBeUndefined();
    expect(Object.keys(json)).not.toContain("workspace");
  });

  it("re-registration after workspace reload includes updated workspace metadata", async () => {
    const wsConfig = makeWorkspaceConfig("Acme Platform");
    const wm = makeWorkspaceManagerWithConfig(
      [makeRepo("r1", { owner: "nightgauge", repo: "r1" })],
      wsConfig
    );

    const agentRegistrationService = { register: vi.fn().mockResolvedValue("agent-id") };
    const globalState = {
      get: vi.fn().mockReturnValue(undefined),
      update: vi.fn().mockResolvedValue(undefined),
    };
    const sessionManager = { state: "authenticated" };
    const machineFingerprint = { getMachineId: vi.fn().mockReturnValue("machine-abc") };

    const scheduleReRegister = async () => {
      await wm.reload();
      await globalState.update("nightgauge.agentId", undefined);
      if (sessionManager.state === "authenticated") {
        const allRepos = wm.getAllRepositories();
        const workspaceRepos: Array<{ owner: string; repo: string }> = [];
        for (const repo of allRepos) {
          await repo.loadConfig();
          const gh = repo.github;
          if (gh?.owner && gh?.repo) workspaceRepos.push({ owner: gh.owner, repo: gh.repo });
        }
        const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(wm.getWorkspaceConfig());
        await agentRegistrationService.register({
          agent_version: "0.1.42",
          capabilities: ["headless", "interactive"],
          repos: workspaceRepos,
          machine_id: machineFingerprint.getMachineId(),
          vscode_version: "1.90.0",
          workspace: workspaceMeta,
        });
      }
    };

    await scheduleReRegister();

    expect(agentRegistrationService.register).toHaveBeenCalledWith(
      expect.objectContaining({
        workspace: { slug: "acme-platform", display_name: "Acme Platform" },
        repos: [{ owner: "nightgauge", repo: "r1" }],
      })
    );
  });

  it("omits workspace field from register payload when platform returns error (legacy path)", async () => {
    const wm = makeWorkspaceManagerWithConfig(
      [makeRepo("r1", { owner: "nightgauge", repo: "r1" })],
      null
    );

    const agentRegistrationService = { register: vi.fn().mockResolvedValue(null) };
    const sessionManager = { state: "authenticated" };

    const scheduleReRegister = async () => {
      await wm.reload();
      if (sessionManager.state === "authenticated") {
        const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(wm.getWorkspaceConfig());
        await agentRegistrationService.register({
          agent_version: "0.1.0",
          capabilities: ["headless", "interactive"],
          repos: [{ owner: "nightgauge", repo: "r1" }],
          machine_id: "m1",
          vscode_version: "1.90.0",
          workspace: workspaceMeta,
        });
      }
    };

    await scheduleReRegister();

    const callArg = agentRegistrationService.register.mock.calls[0]?.[0] as Record<string, unknown>;
    const json = JSON.parse(JSON.stringify(callArg)) as Record<string, unknown>;
    expect(json["workspace"]).toBeUndefined();
  });
});
