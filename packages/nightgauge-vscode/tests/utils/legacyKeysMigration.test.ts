/**
 * legacyKeysMigration.test.ts
 *
 * Unit tests for the legacy-key migration. Covers:
 *   - v2 reclassification (#3641): autonomous.enabled_repos and per-repo
 *     autonomous.repositories.<repo>.* now target Machine tier (not Runtime).
 *   - v1 → v2 promotion: previously-runtime values are pulled out of memento
 *     and into machine YAML, with memento cleared.
 *   - detection of each LEGACY_KEYS entry in project YAML
 *   - value copy correctness (primitive and nested object)
 *   - idempotency (STATE_KEY prevents double-run)
 *   - dismissal persistence (DISMISSED_KEY)
 *   - no-op on clean config (STATE_KEY still set, no notification)
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ── vscode mock ───────────────────────────────────────────────────────────────

const { showInformationMessageMock } = vi.hoisted(() => ({
  showInformationMessageMock: vi.fn(),
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private handlers: Array<(e: unknown) => void> = [];
    event = (handler: (e: unknown) => void) => {
      this.handlers.push(handler);
      return {
        dispose: () => {
          this.handlers = this.handlers.filter((h) => h !== handler);
        },
      };
    };
    fire(e: unknown) {
      this.handlers.forEach((h) => h(e));
    }
    dispose() {
      this.handlers = [];
    }
  },
  Disposable: class {
    dispose = vi.fn();
  },
  window: {
    showInformationMessage: showInformationMessageMock,
  },
  commands: {
    executeCommand: vi.fn(),
  },
  Uri: {
    file: vi.fn((p: string) => ({ path: p })),
  },
  workspace: {
    fs: {
      readFile: vi.fn(),
      writeFile: vi.fn(),
      createDirectory: vi.fn(),
      delete: vi.fn(),
    },
  },
}));

// ── IncrediYamlService mock ───────────────────────────────────────────────────

const { mockRead, mockWriteGlobal, mockDispose } = vi.hoisted(() => ({
  mockRead: vi.fn(),
  mockWriteGlobal: vi.fn(),
  mockDispose: vi.fn(),
}));

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: class {
    read = mockRead;
    writeGlobal = mockWriteGlobal;
    dispose = mockDispose;
  },
}));

// ── RuntimeStateStore mock ───────────────────────────────────────────────────

const runtimeSetMock = vi.fn();

function makeRuntimeStore() {
  return {
    set: runtimeSetMock,
    get: vi.fn(),
    delete: vi.fn(),
    snapshot: vi.fn().mockReturnValue({}),
    onDidChange: vi.fn(),
    dispose: vi.fn(),
  };
}

// ── helpers ───────────────────────────────────────────────────────────────────

const STATE_KEY_V2 = "nightgauge.legacyKeysMigrationCompletedAt.v2";
const DISMISSED_KEY_V2 = "nightgauge.legacyKeysMigrationDismissed.v2";
const RUNTIME_PREFIX = "nightgauge.runtime.";

function makeMemento(initial: Record<string, unknown> = {}) {
  const store: Record<string, unknown> = { ...initial };
  return {
    keys: () => Object.keys(store),
    get: vi.fn((key: string) => store[key]),
    update: vi.fn((key: string, value: unknown) => {
      if (value === undefined) delete store[key];
      else store[key] = value;
      return Promise.resolve();
    }),
    _store: store, // exposed for assertions
  };
}

function makeContext(
  globalStateInit: Record<string, unknown> = {},
  workspaceStateInit: Record<string, unknown> = {}
) {
  return {
    globalState: makeMemento(globalStateInit),
    workspaceState: makeMemento(workspaceStateInit),
  };
}

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

import { runLegacyKeysMigration, LEGACY_KEYS } from "../../src/utils/legacyKeysMigration";

// ── tests ─────────────────────────────────────────────────────────────────────

describe("runLegacyKeysMigration", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockWriteGlobal.mockResolvedValue({ success: true });
    showInformationMessageMock.mockResolvedValue(undefined);
  });

  it("LEGACY_KEYS covers all expected keys", () => {
    const keys = LEGACY_KEYS.map((d) => d.key);
    expect(keys).toContain("github_user");
    expect(keys).toContain("pipeline.max_concurrent");
    expect(keys).toContain("autonomous.enabled_repos");
    expect(keys).toContain("notifications.discord.enabled");
    expect(keys).toContain("lm_studio");
  });

  it("autonomous.enabled_repos is now classified as machine tier (#3641)", () => {
    const descriptor = LEGACY_KEYS.find((d) => d.key === "autonomous.enabled_repos");
    expect(descriptor).toBeDefined();
    expect(descriptor?.targetTier).toBe("machine");
  });

  it("fast-paths when v2 STATE_KEY is already set", async () => {
    const ctx = makeContext({ [STATE_KEY_V2]: "2026-01-01T00:00:00Z" });
    const store = makeRuntimeStore();
    const logger = makeLogger();

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockRead).not.toHaveBeenCalled();
    expect(runtimeSetMock).not.toHaveBeenCalled();
    expect(mockWriteGlobal).not.toHaveBeenCalled();
  });

  it("v1 STATE_KEY does NOT short-circuit v2 (users who completed v1 still get v2 pass)", async () => {
    const STATE_KEY_V1 = "nightgauge.legacyKeysMigrationCompletedAt";
    const ctx = makeContext({ [STATE_KEY_V1]: "2026-01-01T00:00:00Z" });
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: { github_user: "octocat" } });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    // v2 ran — it read the project YAML.
    expect(mockRead).toHaveBeenCalled();
    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({ github_user: "octocat" })
    );
  });

  it("no-op on clean config — sets v2 STATE_KEY without notification", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: {} });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(runtimeSetMock).not.toHaveBeenCalled();
    expect(mockWriteGlobal).not.toHaveBeenCalled();
    expect(showInformationMessageMock).not.toHaveBeenCalled();
    expect(ctx.globalState.update).toHaveBeenCalledWith(STATE_KEY_V2, expect.any(String));
  });

  it("copies github_user to machine tier", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: { github_user: "octocat" } });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({ github_user: "octocat" })
    );
  });

  it("copies pipeline.max_concurrent to runtime globalState (still runtime in v2)", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: { pipeline: { max_concurrent: 4 } } });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(runtimeSetMock).toHaveBeenCalledWith("pipeline.max_concurrent", 4, {
      scope: "global",
    });
  });

  it("copies autonomous.enabled_repos to MACHINE tier (#3641 — was runtime in v1)", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({
      success: true,
      config: { autonomous: { enabled_repos: ["nightgauge/nightgauge"] } },
    });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({
        autonomous: expect.objectContaining({
          enabled_repos: ["nightgauge/nightgauge"],
        }),
      })
    );
    // v2: must NOT write to runtime store anymore.
    expect(runtimeSetMock).not.toHaveBeenCalledWith(
      "autonomous.enabled_repos",
      expect.anything(),
      expect.anything()
    );
  });

  it("v1 → v2 promotion: existing workspaceState autonomous.enabled_repos is copied to machine YAML, memento cleared", async () => {
    // Simulate a user who completed v1: enabled_repos lives in workspaceState memento.
    const v1Key = `${RUNTIME_PREFIX}autonomous.enabled_repos`;
    const ctx = makeContext(
      {}, // globalState
      { [v1Key]: ["nightgauge", "acme-mobile"] } // workspaceState
    );
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: {} });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    // Value was copied to machine YAML.
    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({
        autonomous: expect.objectContaining({
          enabled_repos: ["nightgauge", "acme-mobile"],
        }),
      })
    );
    // v1 memento entry was cleared.
    expect(ctx.workspaceState.update).toHaveBeenCalledWith(v1Key, undefined);
  });

  it("v1 → v2 promotion: per-repo workspaceState entries are copied to machine YAML, memento cleared", async () => {
    // v1 RuntimeStateStore.set("sequential", true, { repoSlug: "nightgauge/nightgauge" })
    // → workspaceState key `nightgauge.runtime.repos.nightgauge__nightgauge.sequential`
    const k1 = `${RUNTIME_PREFIX}repos.nightgauge__nightgauge.sequential`;
    const k2 = `${RUNTIME_PREFIX}repos.nightgauge__nightgauge.max_concurrent`;
    const ctx = makeContext({}, { [k1]: true, [k2]: 2 });
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: {} });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    // Both fields written to machine YAML under autonomous.repositories.<slug>.
    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({
        autonomous: expect.objectContaining({
          repositories: expect.objectContaining({
            "nightgauge/nightgauge": expect.objectContaining({
              sequential: true,
              max_concurrent: 2,
            }),
          }),
        }),
      })
    );
    // v1 memento entries cleared.
    expect(ctx.workspaceState.update).toHaveBeenCalledWith(k1, undefined);
    expect(ctx.workspaceState.update).toHaveBeenCalledWith(k2, undefined);
  });

  it("copies notifications.discord.enabled to machine tier", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({
      success: true,
      config: { notifications: { discord: { enabled: true } } },
    });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({
        notifications: expect.objectContaining({
          discord: expect.objectContaining({ enabled: true }),
        }),
      })
    );
  });

  it("copies lm_studio block to machine tier", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    const lmStudio = { base_url: "http://localhost:1234", model: "llama3" };
    mockRead.mockResolvedValue({ success: true, config: { lm_studio: lmStudio } });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockWriteGlobal).toHaveBeenCalledWith(expect.objectContaining({ lm_studio: lmStudio }));
  });

  it("migrates per-repo autonomous keys from project YAML to machine tier (#3641)", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({
      success: true,
      config: {
        autonomous: {
          repositories: {
            "nightgauge/nightgauge": { sequential: true, max_concurrent: 2 },
          },
        },
      },
    });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockWriteGlobal).toHaveBeenCalledWith(
      expect.objectContaining({
        autonomous: expect.objectContaining({
          repositories: expect.objectContaining({
            "nightgauge/nightgauge": expect.objectContaining({
              sequential: true,
              max_concurrent: 2,
            }),
          }),
        }),
      })
    );
    // v2: per-repo migration must NOT write to runtime store.
    expect(runtimeSetMock).not.toHaveBeenCalledWith(
      "sequential",
      expect.anything(),
      expect.anything()
    );
    expect(runtimeSetMock).not.toHaveBeenCalledWith(
      "max_concurrent",
      expect.anything(),
      expect.anything()
    );
  });

  it("idempotency — second run skips because v2 STATE_KEY is set", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: { github_user: "octocat" } });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);
    const callCount = mockWriteGlobal.mock.calls.length;

    // Second run should fast-path
    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(mockWriteGlobal.mock.calls.length).toBe(callCount); // no new calls
  });

  it("dismissal stores DISMISSED_KEY v2 and suppresses future notifications", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: { github_user: "octocat" } });
    showInformationMessageMock.mockResolvedValue("Dismiss");

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    expect(ctx.globalState.update).toHaveBeenCalledWith(DISMISSED_KEY_V2, true);

    // Reset STATE_KEY to simulate forceRun (but DISMISSED_KEY remains)
    (ctx.globalState.get as ReturnType<typeof vi.fn>).mockImplementation((key: string) => {
      if (key === DISMISSED_KEY_V2) return true;
      return undefined;
    });
    showInformationMessageMock.mockClear();

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any, false);
    // Notification not shown because dismissed
    expect(showInformationMessageMock).not.toHaveBeenCalled();
  });

  it("forceRun=true bypasses v2 STATE_KEY guard", async () => {
    const ctx = makeContext({ [STATE_KEY_V2]: "2026-01-01T00:00:00Z" });
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({ success: true, config: { github_user: "octocat" } });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any, true);

    expect(mockWriteGlobal).toHaveBeenCalled();
  });

  it("multi-repo: two independent workspace roots are migrated independently", async () => {
    const ctx1 = makeContext();
    const ctx2 = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead
      .mockResolvedValueOnce({ success: true, config: { github_user: "alice" } })
      .mockResolvedValueOnce({ success: true, config: { github_user: "bob" } });

    await runLegacyKeysMigration(ctx1 as any, "/root1", store as any, logger as any);
    await runLegacyKeysMigration(ctx2 as any, "/root2", store as any, logger as any);

    const writeGlobalCalls = mockWriteGlobal.mock.calls.map((c) => c[0]);
    expect(writeGlobalCalls).toContainEqual(expect.objectContaining({ github_user: "alice" }));
    expect(writeGlobalCalls).toContainEqual(expect.objectContaining({ github_user: "bob" }));
  });

  it("swallows errors — never throws", async () => {
    const ctx = makeContext();
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockRejectedValue(new Error("YAML parse error"));

    await expect(
      runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any)
    ).resolves.toBeUndefined();

    expect(logger.warn).toHaveBeenCalledWith("[legacy-keys-migration] failed", expect.any(Object));
  });

  it("project YAML and v1 memento for same key: project wins (memento still cleared)", async () => {
    // Both sources set autonomous.enabled_repos. Project YAML is the
    // authoritative source for migration intent — memento was the v1
    // intermediate landing zone.
    const v1Key = `${RUNTIME_PREFIX}autonomous.enabled_repos`;
    const ctx = makeContext({}, { [v1Key]: ["memento-only"] });
    const store = makeRuntimeStore();
    const logger = makeLogger();

    mockRead.mockResolvedValue({
      success: true,
      config: { autonomous: { enabled_repos: ["project-wins"] } },
    });

    await runLegacyKeysMigration(ctx as any, "/root", store as any, logger as any);

    // Memento was processed first (v1 promotion runs before project
    // sweep) so the memento value lands first, then project value
    // overwrites. Either way the final machine YAML value must reflect
    // project intent because writeGlobal deep-merges and the final
    // write wins.
    const writes = mockWriteGlobal.mock.calls.map((c) => c[0]);
    const enabledReposWrites = writes
      .map((w: any) => w?.autonomous?.enabled_repos)
      .filter((v: any) => v !== undefined);
    // Last write determines effective merged value. The project sweep
    // runs second; it picks up the project value.
    expect(enabledReposWrites[enabledReposWrites.length - 1]).toEqual(["project-wins"]);
    // Memento still cleared so it doesn't continue shadowing.
    expect(ctx.workspaceState.update).toHaveBeenCalledWith(v1Key, undefined);
  });
});
