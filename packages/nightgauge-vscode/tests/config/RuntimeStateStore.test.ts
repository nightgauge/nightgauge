/**
 * RuntimeStateStore.test.ts
 *
 * Unit tests for the memento-backed runtime config tier introduced in
 * Phase 2 of epic #3313 (Issue #3335). Covers:
 *   - default scope (global), repo-scoped routing to workspaceState
 *   - multi-workspace isolation for repo-scoped keys
 *   - onDidChange event payload (path, repoSlug, scope, oldValue, newValue)
 *   - get/set/delete round-trips for primitive and structured values
 *   - snapshot() reconstruction of nested config from dotted-path memento keys
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => {
  class EventEmitter<T> {
    private handlers: Array<(e: T) => void> = [];
    event = (handler: (e: T) => void) => {
      this.handlers.push(handler);
      return {
        dispose: () => {
          this.handlers = this.handlers.filter((h) => h !== handler);
        },
      };
    };
    fire(e: T) {
      this.handlers.forEach((h) => h(e));
    }
    dispose() {
      this.handlers = [];
    }
  }

  return {
    EventEmitter,
    Disposable: class {
      dispose = vi.fn();
    },
  };
});

import {
  RuntimeStateStore,
  RUNTIME_KEY_PREFIX,
  RUNTIME_REPO_SCOPE_INFIX,
  type RuntimeChangeEvent,
} from "../../src/config/RuntimeStateStore";

// ── helpers ──────────────────────────────────────────────────────────────────

function makeMemento(initial: Record<string, unknown> = {}) {
  const store: Record<string, unknown> = { ...initial };
  return {
    get: vi.fn((key: string, defaultValue?: unknown) => (key in store ? store[key] : defaultValue)),
    update: vi.fn((key: string, value: unknown) => {
      if (value === undefined) {
        delete store[key];
      } else {
        store[key] = value;
      }
      return Promise.resolve();
    }),
    keys: vi.fn(() => Object.keys(store)),
    _store: store,
  };
}

// ── basic get/set ─────────────────────────────────────────────────────────────

describe("RuntimeStateStore — get/set", () => {
  it("set without repoSlug routes to globalState with the runtime prefix", async () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    await store.set("pipeline.max_concurrent", 4);

    expect(global._store[`${RUNTIME_KEY_PREFIX}pipeline.max_concurrent`]).toBe(4);
    expect(Object.keys(workspace._store)).toHaveLength(0);
  });

  it("set with repoSlug routes to workspaceState with repos.<slug>.<path>", async () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    await store.set("autonomous.enabled", true, { repoSlug: "nightgauge/nightgauge" });

    const expectedKey = `${RUNTIME_KEY_PREFIX}${RUNTIME_REPO_SCOPE_INFIX}nightgauge__nightgauge.autonomous.enabled`;
    expect(workspace._store[expectedKey]).toBe(true);
    expect(Object.keys(global._store)).toHaveLength(0);
  });

  it("get returns the value previously set (round-trip)", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("project.number", 42);
    expect(store.get("project.number")).toBe(42);
  });

  it("get returns undefined for an unset path", () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    expect(store.get("nope.never.set")).toBeUndefined();
  });

  it("round-trips non-string payloads (boolean, array, nested object)", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.auto_fix", false);
    await store.set("automations.enabled_repos", ["a", "b"]);
    await store.set("notifications.discord", { webhook_url: "https://x", enabled: true });

    expect(store.get("pipeline.auto_fix")).toBe(false);
    expect(store.get("automations.enabled_repos")).toEqual(["a", "b"]);
    expect(store.get("notifications.discord")).toEqual({
      webhook_url: "https://x",
      enabled: true,
    });
  });
});

// ── multi-workspace isolation ────────────────────────────────────────────────

describe("RuntimeStateStore — multi-workspace isolation", () => {
  it("two stores sharing globalState but with separate workspaceState do not see each other's repo-scoped values", async () => {
    const sharedGlobal = makeMemento();
    const workspaceA = makeMemento();
    const workspaceB = makeMemento();

    const storeA = new RuntimeStateStore(sharedGlobal as any, workspaceA as any);
    const storeB = new RuntimeStateStore(sharedGlobal as any, workspaceB as any);

    await storeA.set("pipeline.max_concurrent", 8, { repoSlug: "nightgauge/repoA" });

    expect(storeA.get("pipeline.max_concurrent", { repoSlug: "nightgauge/repoA" })).toBe(8);
    expect(storeB.get("pipeline.max_concurrent", { repoSlug: "nightgauge/repoA" })).toBeUndefined();
  });

  it("global-scoped values ARE shared across workspaces", async () => {
    const sharedGlobal = makeMemento();
    const storeA = new RuntimeStateStore(sharedGlobal as any, makeMemento() as any);
    const storeB = new RuntimeStateStore(sharedGlobal as any, makeMemento() as any);

    await storeA.set("github_user", "alice");

    expect(storeB.get("github_user")).toBe("alice");
  });
});

// ── delete ───────────────────────────────────────────────────────────────────

describe("RuntimeStateStore — delete", () => {
  it("removes the value so get returns undefined", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.max_concurrent", 4);
    await store.delete("pipeline.max_concurrent");
    expect(store.get("pipeline.max_concurrent")).toBeUndefined();
  });

  it("removes the value from snapshot output", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.max_concurrent", 4);
    await store.set("project.number", 42);
    await store.delete("pipeline.max_concurrent");

    const snap = store.snapshot();
    expect(snap).toEqual({ project: { number: 42 } });
  });
});

// ── onDidChange ──────────────────────────────────────────────────────────────

describe("RuntimeStateStore — onDidChange", () => {
  it("fires on set with the correct payload", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    const events: RuntimeChangeEvent[] = [];
    store.onDidChange((e) => events.push(e));

    await store.set("pipeline.max_concurrent", 4);

    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      path: "pipeline.max_concurrent",
      scope: "global",
      oldValue: undefined,
      newValue: 4,
    });
    expect(events[0].repoSlug).toBeUndefined();
  });

  it("fires on overwrite with the previous value as oldValue", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.max_concurrent", 4);

    const events: RuntimeChangeEvent[] = [];
    store.onDidChange((e) => events.push(e));

    await store.set("pipeline.max_concurrent", 8);

    expect(events).toHaveLength(1);
    expect(events[0].oldValue).toBe(4);
    expect(events[0].newValue).toBe(8);
  });

  it("fires on delete with newValue undefined", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.max_concurrent", 4);

    const events: RuntimeChangeEvent[] = [];
    store.onDidChange((e) => events.push(e));

    await store.delete("pipeline.max_concurrent");

    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      path: "pipeline.max_concurrent",
      scope: "global",
      oldValue: 4,
      newValue: undefined,
    });
  });

  it("includes repoSlug + workspace scope for repo-scoped writes", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    const events: RuntimeChangeEvent[] = [];
    store.onDidChange((e) => events.push(e));

    await store.set("autonomous.enabled", true, { repoSlug: "nightgauge/nightgauge" });

    expect(events[0]).toMatchObject({
      path: "autonomous.enabled",
      repoSlug: "nightgauge/nightgauge",
      scope: "workspace",
      oldValue: undefined,
      newValue: true,
    });
  });

  it("stops firing after dispose", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    const events: RuntimeChangeEvent[] = [];
    store.onDidChange((e) => events.push(e));

    store.dispose();
    await store.set("pipeline.max_concurrent", 4);

    expect(events).toHaveLength(0);
  });
});

// ── snapshot ─────────────────────────────────────────────────────────────────

describe("RuntimeStateStore — snapshot", () => {
  it("returns an empty object when nothing is stored", () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    expect(store.snapshot()).toEqual({});
  });

  it("rebuilds nested objects from dotted-path memento keys", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.max_concurrent", 4);
    await store.set("pipeline.auto_fix", false);
    await store.set("project.number", 42);

    const snap = store.snapshot();
    expect(snap).toEqual({
      pipeline: { max_concurrent: 4, auto_fix: false },
      project: { number: 42 },
    });
  });

  it("includes both global-scoped and workspace-scoped values, with repos.<slug> overlay for repo-scoped", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    await store.set("pipeline.max_concurrent", 4);
    await store.set("autonomous.enabled", true, { repoSlug: "nightgauge/nightgauge" });

    const snap = store.snapshot() as any;
    expect(snap.pipeline).toEqual({ max_concurrent: 4 });
    // Slash sanitized in the snapshot key (Phase 3 will route to autonomous.repositories.*)
    expect(snap.repos).toBeDefined();
    expect(snap.repos.nightgauge__nightgauge).toEqual({ autonomous: { enabled: true } });
  });

  it("workspace-scoped values override global-scoped values for the same path", async () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    // Manually plant clashing keys at the same dotted path under both mementos.
    // Outside the public API this is unusual, but snapshot must define a
    // deterministic resolution rule: workspace wins (read second).
    global._store[`${RUNTIME_KEY_PREFIX}github_user`] = "global-user";
    workspace._store[`${RUNTIME_KEY_PREFIX}github_user`] = "workspace-user";

    expect(store.snapshot()).toEqual({ github_user: "workspace-user" });
  });

  it("ignores keys that do not start with the runtime prefix (other extension state coexists safely)", () => {
    const global = makeMemento({
      "nightgauge.devpletedIssues": { foo: "bar" },
      "some-other-extension.key": 42,
    });
    const store = new RuntimeStateStore(global as any, makeMemento() as any);

    expect(store.snapshot()).toEqual({});
  });
});

// ── dispose ──────────────────────────────────────────────────────────────────

describe("RuntimeStateStore — dispose", () => {
  it("does not throw when called multiple times", () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    expect(() => {
      store.dispose();
      store.dispose();
    }).not.toThrow();
  });

  it("does not dispose the underlying mementos (owned by ExtensionContext)", () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    store.dispose();

    // Mementos remain usable after the store is disposed
    expect(typeof global.get).toBe("function");
    expect(typeof workspace.update).toBe("function");
  });
});

// ── prefix constants ─────────────────────────────────────────────────────────

beforeEach(() => {
  // sanity check — prefixes are stable contracts
  expect(RUNTIME_KEY_PREFIX).toBe("nightgauge.runtime.");
  expect(RUNTIME_REPO_SCOPE_INFIX).toBe("repos.");
});
