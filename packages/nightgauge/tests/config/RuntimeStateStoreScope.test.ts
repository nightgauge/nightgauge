/**
 * RuntimeStateStoreScope.test.ts
 *
 * Phase 3 of #3313 (#3336) — covers the two new RuntimeStateStore behaviors:
 *
 *   1. The optional `scope: "workspace" | "global"` `RuntimeKeyOptions`
 *      field. Used by `autonomous.enabled_repos` so two open workspaces
 *      with overlapping repo sets don't cross-pollute selections.
 *   2. The `snapshot()` remap that surfaces repo-scoped runtime keys at
 *      their concrete schema location
 *      (`autonomous.repositories.<slug>.{sequential,max_concurrent}`)
 *      instead of the temporary `repos.<slug>.*` overlay.
 *
 * The Phase 2 behaviors (default-global writes, repo-scoped routing,
 * onDidChange payloads, dispose, etc.) are covered in
 * `RuntimeStateStore.test.ts`.
 */

import { describe, it, expect, vi } from "vitest";

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

describe("RuntimeStateStore — workspace-scope option (#3336)", () => {
  it("scope:'workspace' without repoSlug routes to workspaceState with no `repos.` infix", async () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    await store.set("autonomous.enabled_repos", ["alpha", "beta"], { scope: "workspace" });

    // Workspace-state holds the value at the bare runtime path.
    expect(workspace._store[`${RUNTIME_KEY_PREFIX}autonomous.enabled_repos`]).toEqual([
      "alpha",
      "beta",
    ]);
    // No repo-scoped infix added.
    expect(Object.keys(workspace._store).some((k) => k.includes(RUNTIME_REPO_SCOPE_INFIX))).toBe(
      false
    );
    // Global memento is untouched — multi-workspace isolation.
    expect(Object.keys(global._store)).toHaveLength(0);
  });

  it("scope:'workspace' get round-trips through workspaceState", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("autonomous.enabled_repos", ["alpha"], { scope: "workspace" });

    expect(store.get("autonomous.enabled_repos", { scope: "workspace" })).toEqual(["alpha"]);
    // Default-scope (global) read does NOT see the workspace value.
    expect(store.get("autonomous.enabled_repos")).toBeUndefined();
  });

  it("scope:'workspace' delete removes the key from workspaceState only", async () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    await store.set("autonomous.enabled_repos", ["alpha"], { scope: "workspace" });
    await store.delete("autonomous.enabled_repos", { scope: "workspace" });

    expect(workspace._store).not.toHaveProperty(`${RUNTIME_KEY_PREFIX}autonomous.enabled_repos`);
  });

  it("scope:'workspace' onDidChange fires with scope='workspace' and undefined repoSlug", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);
    const events: RuntimeChangeEvent[] = [];
    store.onDidChange((e) => events.push(e));

    await store.set("autonomous.enabled_repos", ["x"], { scope: "workspace" });

    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({
      path: "autonomous.enabled_repos",
      scope: "workspace",
      newValue: ["x"],
    });
    expect(events[0].repoSlug).toBeUndefined();
  });

  it("repoSlug + scope:'workspace' — repoSlug wins (key shape unchanged)", async () => {
    const workspace = makeMemento();
    const store = new RuntimeStateStore(makeMemento() as any, workspace as any);

    // When both are set, the repoSlug-scoped form takes precedence so the
    // `repos.<slug>.*` key shape is preserved (Phase 2 contract).
    await store.set("sequential", true, {
      repoSlug: "nightgauge/repo-a",
      scope: "workspace",
    });

    const expectedKey = `${RUNTIME_KEY_PREFIX}${RUNTIME_REPO_SCOPE_INFIX}nightgauge__repo-a.sequential`;
    expect(workspace._store[expectedKey]).toBe(true);
  });

  it("default scope (no opts) still writes to globalState", async () => {
    const global = makeMemento();
    const workspace = makeMemento();
    const store = new RuntimeStateStore(global as any, workspace as any);

    await store.set("pipeline.max_concurrent", 4);

    expect(global._store[`${RUNTIME_KEY_PREFIX}pipeline.max_concurrent`]).toBe(4);
    expect(Object.keys(workspace._store)).toHaveLength(0);
  });
});

describe("RuntimeStateStore — snapshot remap of repo-scoped sequential/max_concurrent (#3336)", () => {
  it("remaps repos.<slug>.sequential → autonomous.repositories.<slug>.sequential", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("sequential", true, { repoSlug: "nightgauge/repo-a" });

    const snap = store.snapshot() as any;
    expect(snap.autonomous?.repositories?.["nightgauge/repo-a"]?.sequential).toBe(true);
    // Old `repos.<slug>.*` overlay is no longer present for these keys.
    expect(snap.repos?.nightgauge__repo_a).toBeUndefined();
    expect(snap.repos?.["nightgauge__repo-a"]).toBeUndefined();
  });

  it("remaps repos.<slug>.max_concurrent → autonomous.repositories.<slug>.max_concurrent", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("max_concurrent", 3, { repoSlug: "nightgauge/repo-b" });

    const snap = store.snapshot() as any;
    expect(snap.autonomous?.repositories?.["nightgauge/repo-b"]?.max_concurrent).toBe(3);
  });

  it("reverse-sanitizes the slug (`__` → `/`) so the schema path uses owner/repo", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("sequential", true, { repoSlug: "nightgauge/nightgauge" });
    await store.set("max_concurrent", 2, { repoSlug: "nightgauge/nightgauge" });

    const snap = store.snapshot() as any;
    expect(snap.autonomous?.repositories?.["nightgauge/nightgauge"]).toEqual({
      sequential: true,
      max_concurrent: 2,
    });
  });

  it("leaves other repo-scoped keys at the legacy `repos.<slug>.*` overlay (no remap)", async () => {
    // Until later phases route them, anything other than sequential and
    // max_concurrent stays under the generic overlay.
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("autonomous.enabled", true, { repoSlug: "nightgauge/repo-c" });

    const snap = store.snapshot() as any;
    expect(snap.repos?.nightgauge__repo_c).toBeUndefined();
    expect(snap.repos?.["nightgauge__repo-c"]).toEqual({ autonomous: { enabled: true } });
    // And NOT promoted to autonomous.repositories.<slug>.* — that path is
    // reserved for the Phase 3 keys.
    expect(snap.autonomous?.repositories).toBeUndefined();
  });

  it("workspace-scoped non-repo keys flow through at the bare schema path", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("autonomous.enabled_repos", ["alpha", "beta"], { scope: "workspace" });
    await store.set("pipeline.max_concurrent", 5);

    const snap = store.snapshot() as any;
    expect(snap.autonomous?.enabled_repos).toEqual(["alpha", "beta"]);
    expect(snap.pipeline?.max_concurrent).toBe(5);
  });

  it("merges remapped + workspace-scoped + global-scoped values into one tree", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("pipeline.max_concurrent", 4); // global
    await store.set("autonomous.enabled_repos", ["x"], { scope: "workspace" });
    await store.set("sequential", true, { repoSlug: "nightgauge/r1" });
    await store.set("max_concurrent", 2, { repoSlug: "nightgauge/r2" });

    const snap = store.snapshot() as any;
    expect(snap.pipeline?.max_concurrent).toBe(4);
    expect(snap.autonomous?.enabled_repos).toEqual(["x"]);
    expect(snap.autonomous?.repositories?.["nightgauge/r1"]?.sequential).toBe(true);
    expect(snap.autonomous?.repositories?.["nightgauge/r2"]?.max_concurrent).toBe(2);
  });

  it("repo-scoped sequential delete removes the entry from the autonomous.repositories tree", async () => {
    const store = new RuntimeStateStore(makeMemento() as any, makeMemento() as any);

    await store.set("sequential", true, { repoSlug: "nightgauge/r1" });
    await store.delete("sequential", { repoSlug: "nightgauge/r1" });

    const snap = store.snapshot() as any;
    // After delete the autonomous.repositories.<slug> branch is gone (or
    // empty). We only assert the sequential key isn't surfacing — the merge
    // engine is unaffected when the branch isn't present.
    expect(snap.autonomous?.repositories?.["nightgauge/r1"]?.sequential).toBeUndefined();
  });
});
