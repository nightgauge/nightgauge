import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  ALL_ITEMS_SCOPE,
  BoardSnapshotStore,
  boardSnapshotKey,
} from "../../src/services/BoardSnapshotStore";
import type { BoardItem } from "../../src/services/IpcClient";

function item(number: number, repo: string): BoardItem {
  return { number, title: `#${number}`, repo, status: "Ready" } as BoardItem;
}

const BOARD = { owner: "acme", projectNumber: 3, scope: "ready" };

describe("BoardSnapshotStore", () => {
  let store: BoardSnapshotStore;

  beforeEach(() => {
    store = new BoardSnapshotStore();
  });

  // The acceptance criterion this whole change exists for: refresh cost must
  // stop growing with repository count. N services on one board, one fetch.
  it("issues one fetch for N repositories sharing a board", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a"), item(2, "acme/b")]);

    const results = await Promise.all(
      Array.from({ length: 8 }, () => store.fetch(BOARD, 60_000, fetcher))
    );

    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(results).toHaveLength(8);
    for (const r of results) expect(r).toHaveLength(2);
  });

  it("keeps query count flat as repository count grows", async () => {
    for (const repoCount of [1, 2, 5, 20]) {
      const fresh = new BoardSnapshotStore();
      const fetcher = vi.fn(async () => [item(1, "acme/a")]);

      await Promise.all(
        Array.from({ length: repoCount }, () => fresh.fetch(BOARD, 60_000, fetcher))
      );

      expect(fetcher, `${repoCount} repositories`).toHaveBeenCalledTimes(1);
    }
  });

  it("serves a second sequential caller from the snapshot", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a")]);

    await store.fetch(BOARD, 60_000, fetcher);
    await store.fetch(BOARD, 60_000, fetcher);

    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(store.getMetrics()).toMatchObject({ misses: 1, hits: 1 });
  });

  it("refetches once the caller's TTL has passed", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a")]);

    await store.fetch(BOARD, 60_000, fetcher);
    // A zero TTL makes every existing snapshot stale by definition.
    await store.fetch(BOARD, 0, fetcher);

    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("distinguishes scopes, owners, projects and users", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a")]);

    await store.fetch({ ...BOARD, scope: "ready" }, 60_000, fetcher);
    await store.fetch({ ...BOARD, scope: "done" }, 60_000, fetcher);
    await store.fetch({ ...BOARD, scope: ALL_ITEMS_SCOPE }, 60_000, fetcher);
    await store.fetch({ ...BOARD, owner: "other" }, 60_000, fetcher);
    await store.fetch({ ...BOARD, projectNumber: 4 }, 60_000, fetcher);
    await store.fetch({ ...BOARD, githubUser: "someone" }, 60_000, fetcher);

    expect(fetcher).toHaveBeenCalledTimes(6);
    expect(store.size()).toBe(6);
  });

  // Two different boards must never collide into one entry through key
  // concatenation. owner "a" + project 11 and owner "a 1" + project 1 both
  // render digits and a separator, and a naive join would fuse them.
  it("does not collide keys across owner/project boundaries", () => {
    const left = boardSnapshotKey({ owner: "a", projectNumber: 11, scope: "ready" });
    const right = boardSnapshotKey({ owner: "a 1", projectNumber: 1, scope: "ready" });

    expect(left).not.toBe(right);
  });

  // One repository's auth failure must not become another's cached answer.
  it("never caches a rejected fetch", async () => {
    const failing = vi.fn(async () => {
      throw new Error("HTTP 403");
    });

    await expect(store.fetch(BOARD, 60_000, failing)).rejects.toThrow("HTTP 403");

    expect(store.size()).toBe(0);
    expect(store.stale(BOARD)).toBeUndefined();

    // The next caller gets a real attempt rather than an empty cached list.
    const ok = vi.fn(async () => [item(1, "acme/a")]);
    await expect(store.fetch(BOARD, 60_000, ok)).resolves.toHaveLength(1);
    expect(ok).toHaveBeenCalledTimes(1);
  });

  it("propagates a rejection to every coalesced caller", async () => {
    const failing = vi.fn(async () => {
      throw new Error("boom");
    });

    const results = await Promise.allSettled([
      store.fetch(BOARD, 60_000, failing),
      store.fetch(BOARD, 60_000, failing),
      store.fetch(BOARD, 60_000, failing),
    ]);

    expect(failing).toHaveBeenCalledTimes(1);
    for (const r of results) expect(r.status).toBe("rejected");
    // A failed fetch must not wedge the key against later attempts.
    expect(store.size()).toBe(0);
  });

  it("invalidateBoard drops only the named board", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a")]);
    await store.fetch(BOARD, 60_000, fetcher);
    await store.fetch({ ...BOARD, scope: "done" }, 60_000, fetcher);
    await store.fetch({ ...BOARD, owner: "other" }, 60_000, fetcher);

    store.invalidateBoard("acme", 3);

    expect(store.stale(BOARD)).toBeUndefined();
    expect(store.stale({ ...BOARD, scope: "done" })).toBeUndefined();
    expect(store.stale({ ...BOARD, owner: "other" })).toBeDefined();
  });

  // softInvalidate's contract: force a network read, but keep the payload so a
  // failed refresh degrades to stale data rather than an empty board.
  it("expireBoard forces a refetch while keeping the fallback", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a")]);
    await store.fetch(BOARD, 60_000, fetcher);

    store.expireBoard("acme", 3);

    expect(store.peek(BOARD, 60_000)).toBeUndefined();
    expect(store.stale(BOARD)).toHaveLength(1);

    await store.fetch(BOARD, 60_000, fetcher);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  it("reports hit, miss and coalesced counts without board contents", async () => {
    const fetcher = vi.fn(async () => [item(1, "acme/a")]);

    await Promise.all([store.fetch(BOARD, 60_000, fetcher), store.fetch(BOARD, 60_000, fetcher)]);
    await store.fetch(BOARD, 60_000, fetcher);

    const metrics = store.getMetrics();
    expect(metrics).toEqual({ misses: 1, coalesced: 1, hits: 1 });
    expect(JSON.stringify(metrics)).not.toContain("acme/a");
  });
});
