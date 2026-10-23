/**
 * WorkspaceThrottleSync.test.ts
 *
 * Each window keeps its dispatch on the throttle the platform holds for its
 * own workspace (#2337): read by the manifest's slug from the workspace
 * list, again on every change signal, one read at a time so an older read
 * never undoes a newer one, and only while the platform session is
 * authenticated.
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";

vi.mock("vscode", () => ({}));

import {
  PlatformWorkspaceThrottleReader,
  WorkspaceThrottleSync,
  type WorkspaceThrottleReader,
} from "../../src/services/WorkspaceThrottleSync";
import {
  WORKSPACE_THROTTLE_STATE_KEY,
  WorkspaceThrottleState,
  type WorkspaceThrottle,
} from "../../src/services/WorkspaceThrottle";

const LATER = "2099-10-02T13:00:00.000Z";

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

function makeMemento(initial: Record<string, unknown> = {}) {
  const values = new Map(Object.entries(initial));
  return {
    values,
    get: vi.fn(<T>(key: string) => values.get(key) as T | undefined),
    update: vi.fn(async (key: string, value: unknown) => {
      if (value === undefined) values.delete(key);
      else values.set(key, value);
    }),
  };
}

function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

/** The platform's workspace list, as `GET /v1/workspaces` returns it. */
function workspaceList(throttles: Record<string, WorkspaceThrottle | null>) {
  return {
    workspaces: Object.entries(throttles).map(([slug, throttle]) => ({
      id: `ws-${slug}`,
      slug,
      display_name: slug,
      throttle: throttle && { ...throttle, setBy: "user-1", setAt: "2026-10-02T12:00:00.000Z" },
    })),
  };
}

/** A window: a sync over a real WorkspaceThrottleState, applying to a spy dispatch. */
function makeWindow(
  reader: WorkspaceThrottleReader,
  slug: string | null,
  kept: Record<string, unknown> = {}
) {
  const dispatch = { setWorkspaceThrottle: vi.fn() };
  const memento = makeMemento(kept);
  const logger = makeLogger();
  const sync = new WorkspaceThrottleSync(
    reader,
    new WorkspaceThrottleState(dispatch, memento as never, logger as never),
    () => slug,
    logger as never
  );
  const applied = () => dispatch.setWorkspaceThrottle.mock.calls.at(-1)?.[0];
  return { sync, dispatch, memento, applied };
}

describe("PlatformWorkspaceThrottleReader", () => {
  const fetchMock = vi.fn();
  const tokens = { retrieve: vi.fn(async () => "token-1") };

  beforeEach(() => {
    fetchMock.mockReset();
    tokens.retrieve.mockClear();
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads the throttle of the workspace with this slug from the workspace list", async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(200, workspaceList({ alpha: { maxConcurrent: 1, resumeAt: LATER }, beta: null }))
    );
    const reader = new PlatformWorkspaceThrottleReader(() => "https://platform.test", tokens);

    expect(await reader.read("alpha")).toEqual({ maxConcurrent: 1, resumeAt: LATER });
    expect(await reader.read("beta")).toBeNull();
    // A workspace the team does not have is not throttled.
    expect(await reader.read("gamma")).toBeNull();

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("https://platform.test/v1/workspaces");
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer token-1");
  });

  it("refreshes an expired access token once and reads again", async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse(401, {}))
      .mockResolvedValueOnce(jsonResponse(200, workspaceList({ alpha: null })));
    const refresher = { forceRefresh: vi.fn(async () => "token-2") };
    const reader = new PlatformWorkspaceThrottleReader(() => "https://p", tokens, refresher);

    expect(await reader.read("alpha")).toBeNull();
    expect(refresher.forceRefresh).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer token-2");
  });

  it.each([
    ["an HTTP error", () => jsonResponse(503, {})],
    ["a malformed list", () => jsonResponse(200, { items: [] })],
    [
      "a malformed throttle",
      () => jsonResponse(200, { workspaces: [{ slug: "alpha", throttle: { maxConcurrent: -1 } }] }),
    ],
    ["a list with no throttle field", () => jsonResponse(200, { workspaces: [{ slug: "alpha" }] })],
  ])("throws on %s rather than reading it as no throttle", async (_kind, response) => {
    fetchMock.mockResolvedValue(response());
    const reader = new PlatformWorkspaceThrottleReader(() => "https://p", tokens);
    await expect(reader.read("alpha")).rejects.toThrow();
  });

  it("throws when there is no access token", async () => {
    const reader = new PlatformWorkspaceThrottleReader(() => "https://p", {
      retrieve: vi.fn(async () => null),
    });
    await expect(reader.read("alpha")).rejects.toThrow(/no access token/);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("WorkspaceThrottleSync", () => {
  /** The platform's throttles by workspace slug; a reader over them. */
  let platform: Record<string, WorkspaceThrottle | null>;
  let reader: { read: Mock<(slug: string) => Promise<WorkspaceThrottle | null>> };

  beforeEach(() => {
    platform = {};
    reader = {
      read: vi.fn<(slug: string) => Promise<WorkspaceThrottle | null>>(
        async (slug: string) => platform[slug] ?? null
      ),
    };
  });

  // Every window of a machine shares one agent, and a `throttle` command
  // names no workspace. Each window reads its own workspace's throttle.
  it("applies only the window's own workspace's throttle", async () => {
    const alpha = makeWindow(reader, "alpha");
    const beta = makeWindow(reader, "beta");
    await alpha.sync.activate();
    await beta.sync.activate();

    platform.alpha = { maxConcurrent: 1, resumeAt: null };
    await alpha.sync.refresh();
    await beta.sync.refresh();
    expect(alpha.applied()).toEqual({ maxConcurrent: 1, resumeAt: null });
    expect(beta.applied()).toBeNull();

    // Beta's own cap survives a clear on alpha.
    platform.beta = { maxConcurrent: 2, resumeAt: LATER };
    await beta.sync.refresh();
    platform.alpha = null;
    await alpha.sync.refresh();
    await beta.sync.refresh();
    expect(alpha.applied()).toBeNull();
    expect(beta.applied()).toEqual({ maxConcurrent: 2, resumeAt: LATER });
  });

  it("applies no throttle, and reads nothing, when the manifest names no workspace", async () => {
    const window = makeWindow(reader, null);
    expect(await window.sync.activate()).toBe("applied");
    expect(reader.read).not.toHaveBeenCalled();
    expect(window.applied()).toBeNull();
  });

  it("holds dispatch with the kept throttle at once on activation, then applies the read", async () => {
    let answer!: (throttle: WorkspaceThrottle | null) => void;
    reader.read.mockImplementationOnce(() => new Promise((resolve) => (answer = resolve)));
    const window = makeWindow(reader, "alpha", {
      [WORKSPACE_THROTTLE_STATE_KEY]: { slug: "alpha", maxConcurrent: 0, resumeAt: null },
    });

    const activated = window.sync.activate();
    await vi.waitFor(() => expect(window.applied()).toEqual({ maxConcurrent: 0, resumeAt: null }));
    expect(reader.read).toHaveBeenCalledTimes(1);

    answer({ maxConcurrent: 2, resumeAt: null });
    expect(await activated).toBe("applied");
    expect(window.applied()).toEqual({ maxConcurrent: 2, resumeAt: null });
    expect(window.memento.values.get(WORKSPACE_THROTTLE_STATE_KEY)).toEqual({
      slug: "alpha",
      maxConcurrent: 2,
      resumeAt: null,
    });
  });

  // The platform's state moved on while a read was in flight: a refresh asked
  // for then reads again once it lands, and the newer value is the one kept.
  it("never lets an older read undo a newer change", async () => {
    const window = makeWindow(reader, "alpha");
    await window.sync.activate();

    const answers: Array<(throttle: WorkspaceThrottle | null) => void> = [];
    reader.read.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));

    platform.alpha = { maxConcurrent: 1, resumeAt: null };
    const first = window.sync.refresh(); // a `set` arrives; its read starts
    platform.alpha = null;
    const second = window.sync.refresh(); // a `cleared` arrives during that read
    await vi.waitFor(() => expect(answers).toHaveLength(1));
    await new Promise((resolve) => setTimeout(resolve, 5));
    expect(answers).toHaveLength(1); // one read at a time

    answers[0]({ maxConcurrent: 1, resumeAt: null }); // the first read saw the set
    await vi.waitFor(() => expect(answers).toHaveLength(2));
    answers[1](null); // the read after the clear

    expect(await first).toBe("applied");
    expect(await second).toBe("applied");
    expect(window.applied()).toBeNull();
    expect(window.memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
  });

  it("changes nothing when a read fails", async () => {
    platform.alpha = { maxConcurrent: 1, resumeAt: null };
    const window = makeWindow(reader, "alpha");
    await window.sync.activate();

    reader.read.mockRejectedValueOnce(new Error("HTTP 503"));
    expect(await window.sync.refresh()).toBe("failed");
    expect(window.dispatch.setWorkspaceThrottle).toHaveBeenCalledTimes(1);
    expect(window.applied()).toEqual({ maxConcurrent: 1, resumeAt: null });
  });

  it("lifts and forgets the throttle when deactivated, and a read in flight applies nothing", async () => {
    platform.alpha = { maxConcurrent: 0, resumeAt: null };
    const window = makeWindow(reader, "alpha");
    await window.sync.activate();
    expect(window.memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(true);

    let answer!: (throttle: WorkspaceThrottle | null) => void;
    reader.read.mockImplementationOnce(() => new Promise((resolve) => (answer = resolve)));
    const inFlight = window.sync.refresh();
    await window.sync.deactivate();
    answer({ maxConcurrent: 0, resumeAt: null });

    expect(await inFlight).toBe("inactive");
    expect(window.applied()).toBeNull();
    expect(window.memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);

    reader.read.mockClear();
    expect(await window.sync.refresh()).toBe("inactive");
    expect(reader.read).not.toHaveBeenCalled();
  });

  // The session can be restored before the workspace manifest is read; the
  // window must not take itself for one with no workspace and drop its cap.
  it("restores and reads nothing until the workspace manifest is loaded", async () => {
    platform.alpha = { maxConcurrent: 1, resumeAt: null };
    let slug: string | null = null;
    let loaded!: () => void;
    const ready = new Promise<void>((resolve) => (loaded = resolve));
    const dispatch = { setWorkspaceThrottle: vi.fn() };
    const memento = makeMemento({
      [WORKSPACE_THROTTLE_STATE_KEY]: { slug: "alpha", maxConcurrent: 0, resumeAt: null },
    });
    const logger = makeLogger();
    const sync = new WorkspaceThrottleSync(
      reader,
      new WorkspaceThrottleState(dispatch, memento as never, logger as never),
      () => slug,
      logger as never,
      ready
    );

    const activated = sync.activate();
    await Promise.resolve();
    expect(dispatch.setWorkspaceThrottle).not.toHaveBeenCalled();
    expect(reader.read).not.toHaveBeenCalled();

    slug = "alpha";
    loaded();
    expect(await activated).toBe("applied");
    expect(dispatch.setWorkspaceThrottle.mock.calls).toEqual([
      [{ maxConcurrent: 0, resumeAt: null }],
      [{ maxConcurrent: 1, resumeAt: null }],
    ]);
  });

  describe("followSession", () => {
    function makeSession(state: string) {
      const listeners: Array<(event: { current: string }) => void> = [];
      return {
        state,
        onSessionChanged: vi.fn((listener: (event: { current: string }) => void) => {
          listeners.push(listener);
          return { dispose: vi.fn() };
        }),
        fire(current: string) {
          this.state = current;
          for (const listener of listeners) listener({ current });
        },
      };
    }

    // A throttle of 0 with no end, kept from an earlier session, must never
    // hold dispatch for a window with no platform session: nothing could
    // refresh or clear it there.
    it("applies a kept throttle only while the session is authenticated", async () => {
      platform.alpha = { maxConcurrent: 0, resumeAt: null };
      const window = makeWindow(reader, "alpha", {
        [WORKSPACE_THROTTLE_STATE_KEY]: { slug: "alpha", maxConcurrent: 0, resumeAt: null },
      });
      const session = makeSession("unauthenticated");
      window.sync.followSession(session as never);
      expect(window.dispatch.setWorkspaceThrottle).not.toHaveBeenCalled();
      expect(window.sync.isActive()).toBe(false);

      session.fire("authenticated");
      await vi.waitFor(() => expect(reader.read).toHaveBeenCalledTimes(1));
      expect(window.applied()).toEqual({ maxConcurrent: 0, resumeAt: null });

      // A token refresh is another authenticated event: the throttle is read again.
      platform.alpha = { maxConcurrent: 3, resumeAt: null };
      session.fire("authenticated");
      await vi.waitFor(() =>
        expect(window.applied()).toEqual({ maxConcurrent: 3, resumeAt: null })
      );

      session.fire("unauthenticated");
      await vi.waitFor(() => expect(window.applied()).toBeNull());
      expect(window.memento.values.has(WORKSPACE_THROTTLE_STATE_KEY)).toBe(false);
      expect(window.sync.isActive()).toBe(false);
    });

    it("follows a session that is already authenticated, and stops on a session error", async () => {
      platform.alpha = { maxConcurrent: 1, resumeAt: null };
      const window = makeWindow(reader, "alpha");
      const session = makeSession("authenticated");
      window.sync.followSession(session as never);
      await vi.waitFor(() =>
        expect(window.applied()).toEqual({ maxConcurrent: 1, resumeAt: null })
      );

      session.fire("error");
      await vi.waitFor(() => expect(window.applied()).toBeNull());
      expect(window.sync.isActive()).toBe(false);
    });
  });
});
