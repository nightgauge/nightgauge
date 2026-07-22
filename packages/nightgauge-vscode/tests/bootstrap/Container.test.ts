/**
 * Container.test.ts
 *
 * Unit tests for the service container registry introduced in Issue #2771.
 * Covers: registration, retrieval, error handling, and presence checks.
 *
 * No vscode mocking required — Container.ts uses only `import type` for
 * service types, so no runtime vscode dependency exists.
 *
 * @see Issue #2771 — DI container proof-of-concept (Part 1)
 * @see src/bootstrap/Container.ts
 */

import { describe, it, expect, beforeEach } from "vitest";
import { Container } from "../../src/bootstrap/Container";
import type { ProjectBoardService } from "../../src/services/ProjectBoardService";
import type { GitHubService } from "../../src/services/GitHubService";
import type { GitHubAuthService } from "../../src/services/GitHubAuthService";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { IpcClient } from "../../src/services/IpcClient";
import type { PipelineBridge } from "../../src/services/PipelineBridge";
import type { SkillRunner } from "../../src/services/SkillRunner";
import type { ConfigBridge } from "../../src/services/ConfigBridge";
import type { TelemetryStore } from "../../src/services/TelemetryStore";
import type { TelemetryService } from "../../src/services/TelemetryService";
import type { DiscordService } from "../../src/services/DiscordService";
import type { RepositorySettingsService } from "../../src/services/RepositorySettingsService";
import type { OfflineManager } from "../../src/platform";
import type { KnowledgeTreeProvider } from "../../src/views/KnowledgeTreeProvider";
import type { KnowledgeDocumentLinkProvider } from "../../src/views/KnowledgeDocumentLinkProvider";
import type { RepositoriesTreeProvider, QueryResultsTreeProvider } from "../../src/views";
import type { SlotOutputManager } from "../../src/views/SlotOutputManager";

// ---------------------------------------------------------------------------
// Helpers — minimal stubs satisfying the service types for container testing.
// The container stores and retrieves by reference; stub shape is irrelevant.
// ---------------------------------------------------------------------------

function makeProjectBoardService(): ProjectBoardService {
  return { _kind: "ProjectBoardService" } as unknown as ProjectBoardService;
}

function makeGitHubService(): GitHubService {
  return { _kind: "GitHubService" } as unknown as GitHubService;
}

function makeGitHubAuthService(): GitHubAuthService {
  return { _kind: "GitHubAuthService" } as unknown as GitHubAuthService;
}

function makePipelineStateService(): PipelineStateService {
  return { _kind: "PipelineStateService" } as unknown as PipelineStateService;
}

function makeIpcClient(): IpcClient {
  return { _kind: "IpcClient" } as unknown as IpcClient;
}

function makePipelineBridge(): PipelineBridge {
  return { _kind: "PipelineBridge" } as unknown as PipelineBridge;
}

function makeSkillRunner(): SkillRunner {
  return { _kind: "SkillRunner" } as unknown as SkillRunner;
}

function makeConfigBridge(): ConfigBridge {
  return { _kind: "ConfigBridge" } as unknown as ConfigBridge;
}

function makeTelemetryStore(): TelemetryStore {
  return { _kind: "TelemetryStore" } as unknown as TelemetryStore;
}

function makeTelemetryService(): TelemetryService {
  return { _kind: "TelemetryService" } as unknown as TelemetryService;
}

function makeOfflineManager(): OfflineManager {
  return { _kind: "OfflineManager" } as unknown as OfflineManager;
}

function makeDiscordService(): DiscordService {
  return { _kind: "DiscordService" } as unknown as DiscordService;
}

function makeKnowledgeTreeProvider(): KnowledgeTreeProvider {
  return { _kind: "KnowledgeTreeProvider" } as unknown as KnowledgeTreeProvider;
}

function makeKnowledgeDocumentLinkProvider(): KnowledgeDocumentLinkProvider {
  return { _kind: "KnowledgeDocumentLinkProvider" } as unknown as KnowledgeDocumentLinkProvider;
}

function makeRepositoriesTreeProvider(): RepositoriesTreeProvider {
  return { _kind: "RepositoriesTreeProvider" } as unknown as RepositoriesTreeProvider;
}

function makeQueryResultsTreeProvider(): QueryResultsTreeProvider {
  return { _kind: "QueryResultsTreeProvider" } as unknown as QueryResultsTreeProvider;
}

function makeSlotOutputManager(): SlotOutputManager {
  return { _kind: "SlotOutputManager" } as unknown as SlotOutputManager;
}

function makeRepositorySettingsService(): RepositorySettingsService {
  return { _kind: "RepositorySettingsService" } as unknown as RepositorySettingsService;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("Container", () => {
  let container: Container;

  beforeEach(() => {
    container = new Container();
  });

  // ── Registration & Retrieval ──────────────────────────────────────────────

  it("registers and retrieves a service by key", () => {
    const service = makeProjectBoardService();

    container.register("projectBoardService", service);

    expect(container.get("projectBoardService")).toBe(service);
  });

  it("stores each service independently — different keys return different instances", () => {
    const pbs = makeProjectBoardService();
    const ghs = makeGitHubService();

    container.register("projectBoardService", pbs);
    container.register("githubService", ghs);

    expect(container.get("projectBoardService")).toBe(pbs);
    expect(container.get("githubService")).toBe(ghs);
    expect(container.get("projectBoardService")).not.toBe(ghs);
  });

  it("allows all three GitHub services to be registered together", () => {
    const pbs = makeProjectBoardService();
    const ghs = makeGitHubService();
    const gas = makeGitHubAuthService();

    container.register("projectBoardService", pbs);
    container.register("githubService", ghs);
    container.register("gitHubAuthService", gas);

    expect(container.get("projectBoardService")).toBe(pbs);
    expect(container.get("githubService")).toBe(ghs);
    expect(container.get("gitHubAuthService")).toBe(gas);
  });

  // ── Error Handling ────────────────────────────────────────────────────────

  it("throws a descriptive error when getting an unregistered service", () => {
    expect(() => container.get("projectBoardService")).toThrow(
      "Service projectBoardService not found in container"
    );
  });

  it("throws when getting a different unregistered service", () => {
    expect(() => container.get("githubService")).toThrow(
      "Service githubService not found in container"
    );
  });

  it("throws on duplicate registration of the same key", () => {
    const first = makeProjectBoardService();
    const second = makeProjectBoardService();

    container.register("projectBoardService", first);

    expect(() => container.register("projectBoardService", second)).toThrow(
      "Service projectBoardService already registered"
    );
  });

  it("still returns the first instance after a duplicate registration is rejected", () => {
    const first = makeProjectBoardService();
    const second = makeProjectBoardService();

    container.register("projectBoardService", first);

    try {
      container.register("projectBoardService", second);
    } catch {
      // expected — ignore
    }

    expect(container.get("projectBoardService")).toBe(first);
  });

  // ── Presence Checks ───────────────────────────────────────────────────────

  it("has() returns false for an unregistered service", () => {
    expect(container.has("projectBoardService")).toBe(false);
    expect(container.has("githubService")).toBe(false);
    expect(container.has("gitHubAuthService")).toBe(false);
  });

  it("has() returns true after the service is registered", () => {
    const service = makeProjectBoardService();

    expect(container.has("projectBoardService")).toBe(false);
    container.register("projectBoardService", service);
    expect(container.has("projectBoardService")).toBe(true);
  });

  it("has() for one key is independent of other keys", () => {
    container.register("projectBoardService", makeProjectBoardService());

    expect(container.has("projectBoardService")).toBe(true);
    expect(container.has("githubService")).toBe(false);
    expect(container.has("gitHubAuthService")).toBe(false);
  });

  // ── Isolation ─────────────────────────────────────────────────────────────

  it("separate Container instances are independent — registration in one does not affect another", () => {
    const containerA = new Container();
    const containerB = new Container();
    const service = makeProjectBoardService();

    containerA.register("projectBoardService", service);

    expect(containerA.has("projectBoardService")).toBe(true);
    expect(containerB.has("projectBoardService")).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Pipeline Services — Issue #2772 (Part 2)
// ---------------------------------------------------------------------------

describe("Container — Pipeline Services", () => {
  let container: Container;

  beforeEach(() => {
    container = new Container();
  });

  it("registers and retrieves PipelineStateService", () => {
    const service = makePipelineStateService();

    container.register("pipelineStateService", service);

    expect(container.get("pipelineStateService")).toBe(service);
  });

  it("registers and retrieves IpcClient", () => {
    const service = makeIpcClient();

    container.register("ipcClient", service);

    expect(container.get("ipcClient")).toBe(service);
  });

  it("registers and retrieves PipelineBridge", () => {
    const service = makePipelineBridge();

    container.register("pipelineBridge", service);

    expect(container.get("pipelineBridge")).toBe(service);
  });

  it("registers and retrieves SkillRunner", () => {
    const service = makeSkillRunner();

    container.register("skillRunner", service);

    expect(container.get("skillRunner")).toBe(service);
  });

  it("allows all GitHub and pipeline services to be registered together", () => {
    const pbs = makeProjectBoardService();
    const ghs = makeGitHubService();
    const gas = makeGitHubAuthService();
    const pss = makePipelineStateService();
    const ipc = makeIpcClient();
    const bridge = makePipelineBridge();
    const runner = makeSkillRunner();

    container.register("projectBoardService", pbs);
    container.register("githubService", ghs);
    container.register("gitHubAuthService", gas);
    container.register("pipelineStateService", pss);
    container.register("ipcClient", ipc);
    container.register("pipelineBridge", bridge);
    container.register("skillRunner", runner);

    expect(container.get("projectBoardService")).toBe(pbs);
    expect(container.get("githubService")).toBe(ghs);
    expect(container.get("gitHubAuthService")).toBe(gas);
    expect(container.get("pipelineStateService")).toBe(pss);
    expect(container.get("ipcClient")).toBe(ipc);
    expect(container.get("pipelineBridge")).toBe(bridge);
    expect(container.get("skillRunner")).toBe(runner);
  });
});

// ---------------------------------------------------------------------------
// Config, Telemetry, and View-Provider Services — Issue #2773 (Part 3)
// ---------------------------------------------------------------------------

describe("Container — Config, Telemetry, and View-Provider Services", () => {
  let container: Container;

  beforeEach(() => {
    container = new Container();
  });

  it("registers and retrieves ConfigBridge", () => {
    const service = makeConfigBridge();

    container.register("configBridge", service);

    expect(container.get("configBridge")).toBe(service);
  });

  it("registers and retrieves TelemetryStore", () => {
    const service = makeTelemetryStore();

    container.register("telemetryStore", service);

    expect(container.get("telemetryStore")).toBe(service);
  });

  it("registers and retrieves TelemetryService", () => {
    const service = makeTelemetryService();

    container.register("telemetryService", service);

    expect(container.get("telemetryService")).toBe(service);
  });

  it("registers and retrieves OfflineManager", () => {
    const service = makeOfflineManager();

    container.register("offlineManager", service);

    expect(container.get("offlineManager")).toBe(service);
  });

  it("registers and retrieves DiscordService", () => {
    const service = makeDiscordService();

    container.register("discordService", service);

    expect(container.get("discordService")).toBe(service);
  });

  it("registers and retrieves KnowledgeTreeProvider", () => {
    const service = makeKnowledgeTreeProvider();

    container.register("knowledgeTreeProvider", service);

    expect(container.get("knowledgeTreeProvider")).toBe(service);
  });

  it("registers and retrieves KnowledgeDocumentLinkProvider", () => {
    const service = makeKnowledgeDocumentLinkProvider();

    container.register("knowledgeDocumentLinkProvider", service);

    expect(container.get("knowledgeDocumentLinkProvider")).toBe(service);
  });

  it("registers and retrieves RepositoriesTreeProvider", () => {
    const service = makeRepositoriesTreeProvider();

    container.register("repositoriesTreeProvider", service);

    expect(container.get("repositoriesTreeProvider")).toBe(service);
  });

  it("registers and retrieves QueryResultsTreeProvider", () => {
    const service = makeQueryResultsTreeProvider();

    container.register("queryResultsTreeProvider", service);

    expect(container.get("queryResultsTreeProvider")).toBe(service);
  });

  it("registers and retrieves SlotOutputManager", () => {
    const service = makeSlotOutputManager();

    container.register("slotOutputManager", service);

    expect(container.get("slotOutputManager")).toBe(service);
  });

  it("registers and retrieves RepositorySettingsService", () => {
    const service = makeRepositorySettingsService();

    container.register("repositorySettingsService", service);

    expect(container.get("repositorySettingsService")).toBe(service);
  });

  it("allows all Part 3 services to be registered together", () => {
    const cb = makeConfigBridge();
    const ts = makeTelemetryStore();
    const tsvc = makeTelemetryService();
    const om = makeOfflineManager();
    const ds = makeDiscordService();
    const ktp = makeKnowledgeTreeProvider();
    const kdlp = makeKnowledgeDocumentLinkProvider();
    const rtp = makeRepositoriesTreeProvider();
    const qrtp = makeQueryResultsTreeProvider();
    const som = makeSlotOutputManager();
    const rss = makeRepositorySettingsService();

    container.register("configBridge", cb);
    container.register("telemetryStore", ts);
    container.register("telemetryService", tsvc);
    container.register("offlineManager", om);
    container.register("discordService", ds);
    container.register("knowledgeTreeProvider", ktp);
    container.register("knowledgeDocumentLinkProvider", kdlp);
    container.register("repositoriesTreeProvider", rtp);
    container.register("queryResultsTreeProvider", qrtp);
    container.register("slotOutputManager", som);
    container.register("repositorySettingsService", rss);

    expect(container.get("configBridge")).toBe(cb);
    expect(container.get("telemetryStore")).toBe(ts);
    expect(container.get("telemetryService")).toBe(tsvc);
    expect(container.get("offlineManager")).toBe(om);
    expect(container.get("discordService")).toBe(ds);
    expect(container.get("knowledgeTreeProvider")).toBe(ktp);
    expect(container.get("knowledgeDocumentLinkProvider")).toBe(kdlp);
    expect(container.get("repositoriesTreeProvider")).toBe(rtp);
    expect(container.get("queryResultsTreeProvider")).toBe(qrtp);
    expect(container.get("slotOutputManager")).toBe(som);
    expect(container.get("repositorySettingsService")).toBe(rss);
  });

  it("has() returns false for unregistered Part 3 services", () => {
    expect(container.has("configBridge")).toBe(false);
    expect(container.has("telemetryStore")).toBe(false);
    expect(container.has("telemetryService")).toBe(false);
    expect(container.has("offlineManager")).toBe(false);
    expect(container.has("discordService")).toBe(false);
    expect(container.has("knowledgeTreeProvider")).toBe(false);
    expect(container.has("knowledgeDocumentLinkProvider")).toBe(false);
    expect(container.has("repositoriesTreeProvider")).toBe(false);
    expect(container.has("queryResultsTreeProvider")).toBe(false);
    expect(container.has("slotOutputManager")).toBe(false);
    expect(container.has("repositorySettingsService")).toBe(false);
  });

  it("has() returns true after Part 3 service is registered", () => {
    container.register("configBridge", makeConfigBridge());
    container.register("telemetryStore", makeTelemetryStore());

    expect(container.has("configBridge")).toBe(true);
    expect(container.has("telemetryStore")).toBe(true);
    expect(container.has("telemetryService")).toBe(false);
  });
});
