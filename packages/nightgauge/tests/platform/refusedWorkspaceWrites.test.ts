/**
 * #2372: the operator is shown the workspace writes the platform refused the
 * daemon's agent registration, once per distinct set, whether the window
 * hears the daemon's event or reads its status on connect.
 */

import { describe, it, expect, vi } from "vitest";

import {
  RefusedWorkspaceWritesNotice,
  WORKSPACE_WRITES_REFUSED_EVENT,
  followRefusedWorkspaceWrites,
  printableField,
  refusalsFromRegistrationReply,
  refusedWorkspaceWritesMessage,
  refusedWorkspaceWritesSummary,
  type RefusedWorkspaceWriteReport,
} from "../../src/platform/refusedWorkspaceWrites";

function refusal(workspace: string): RefusedWorkspaceWriteReport {
  return {
    workspace,
    teamId: "team-1",
    code: "PERMISSION_DENIED",
    permission: "workspace:update",
    description: `the platform did not write workspace "${workspace}": workspace:update needs the owner or admin role on its team`,
  };
}

/** The daemon side: its events, its status, its connection. */
function makeDaemon(status: { refusedWorkspaceWrites?: RefusedWorkspaceWriteReport[] } = {}) {
  const handlers = new Map<string, (data: unknown) => void>();
  const connections: Array<(connected: boolean) => void> = [];
  const ipc = {
    on: vi.fn((event: string, handler: (data: unknown) => void) => {
      handlers.set(event, handler);
      return { dispose: () => handlers.delete(event) };
    }),
    platformStatus: vi.fn(async () => status),
    onDidChangeStatus: vi.fn((listener: (connected: boolean) => void) => {
      connections.push(listener);
      return { dispose: () => connections.splice(connections.indexOf(listener), 1) };
    }),
  };
  return {
    ipc,
    emit: (data: unknown) => handlers.get(WORKSPACE_WRITES_REFUSED_EVENT)?.(data),
    connect: () => connections.forEach((l) => l(true)),
    handlers,
    connections,
  };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

describe("followRefusedWorkspaceWrites (#2372)", () => {
  it("shows a registration's refusals from the daemon's event, once per distinct set", async () => {
    const daemon = makeDaemon();
    const show = vi.fn();
    followRefusedWorkspaceWrites(daemon.ipc, new RefusedWorkspaceWritesNotice(show));
    await flush();
    expect(show).not.toHaveBeenCalled();

    daemon.emit({ refusals: [refusal("acme-platform")] });
    expect(show).toHaveBeenCalledTimes(1);
    expect(show.mock.calls[0][0]).toContain('workspace "acme-platform"');
    expect(show.mock.calls[0][0]).toContain("workspace:update needs the owner or admin role");

    // A re-registration refused the same writes: not shown again.
    daemon.emit({ refusals: [refusal("acme-platform")] });
    expect(show).toHaveBeenCalledTimes(1);
    // Another workspace is news.
    daemon.emit({ refusals: [refusal("acme-platform"), refusal("default")] });
    expect(show).toHaveBeenCalledTimes(2);
  });

  it("reads the daemon's status at start and on every connect, for a registration it missed", async () => {
    const daemon = makeDaemon({ refusedWorkspaceWrites: [refusal("acme-platform")] });
    const show = vi.fn();
    followRefusedWorkspaceWrites(daemon.ipc, new RefusedWorkspaceWritesNotice(show));
    await flush();
    expect(daemon.ipc.platformStatus).toHaveBeenCalledTimes(1);
    expect(show).toHaveBeenCalledTimes(1);

    daemon.connect();
    await flush();
    expect(daemon.ipc.platformStatus).toHaveBeenCalledTimes(2);
    // The same set, already shown.
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("ignores a payload without well-formed refusals, and a daemon it cannot reach", async () => {
    const daemon = makeDaemon();
    daemon.ipc.platformStatus.mockRejectedValue(new Error("not connected"));
    const show = vi.fn();
    followRefusedWorkspaceWrites(daemon.ipc, new RefusedWorkspaceWritesNotice(show));
    await flush();
    for (const payload of [null, {}, { refusals: "x" }, { refusals: [{ workspace: "a" }] }]) {
      daemon.emit(payload);
    }
    expect(show).not.toHaveBeenCalled();
  });

  it("stops following once disposed", () => {
    const daemon = makeDaemon();
    const show = vi.fn();
    followRefusedWorkspaceWrites(daemon.ipc, new RefusedWorkspaceWritesNotice(show)).dispose();
    expect(daemon.handlers.size).toBe(0);
    expect(daemon.connections).toHaveLength(0);
  });

  it("spells out at most three refusals in one notification", () => {
    const message = refusedWorkspaceWritesMessage(["a", "b", "c", "d", "e"].map((w) => refusal(w)));
    expect(message).toContain("5 workspace writes");
    expect(message).toContain('workspace "c"');
    expect(message).not.toContain('workspace "d"');
    expect(message).toContain("and 2 more");
    expect(refusedWorkspaceWritesMessage([refusal("a")])).toContain("a workspace write:");
  });

  // #2372: the window's own registration and the daemon's report through one
  // notice, so a set the operator saw from one is not shown again by the other.
  it("shows a set the window's own registration reported once, whichever source repeats it", async () => {
    const daemon = makeDaemon();
    const show = vi.fn();
    const notice = new RefusedWorkspaceWritesNotice(show);
    followRefusedWorkspaceWrites(daemon.ipc, notice);
    notice.report([refusal("acme-platform")]);
    expect(show).toHaveBeenCalledTimes(1);
    daemon.emit({ refusals: [refusal("acme-platform")] });
    expect(show).toHaveBeenCalledTimes(1);
  });

  // A notification renders markdown link syntax as a link, and a command:
  // link runs a command when clicked; a description from the daemon cannot
  // put one in front of the operator.
  it("shows no markdown link a description carries", () => {
    const hostile = {
      ...refusal("acme"),
      description: "[Fix it](command:workbench.action.terminal.sendSequence?%7B%7D)",
    };
    const message = refusedWorkspaceWritesMessage([hostile]);
    expect(message).not.toMatch(/\[[^\]]*\]\(/);
    expect(message).toContain("(Fix it)(command:");
  });
});

describe("the window's own registration reply (#2372)", () => {
  it("bounds every field the platform sends, and drops its message", () => {
    const [r] = refusalsFromRegistrationReply([
      {
        workspace: "acme-platform",
        team_id: "team-1",
        code: "PERMISSION_DENIED",
        permission: "workspace:update",
        message: "anything at all, never shown",
      },
    ]);
    expect(r).toEqual({
      workspace: "acme-platform",
      teamId: "team-1",
      code: "PERMISSION_DENIED",
      permission: "workspace:update",
      description:
        'the platform did not write workspace "acme-platform": workspace:update needs the owner ' +
        "or admin role on its team (team team-1, PERMISSION_DENIED); repositories this agent " +
        "declares stay unlinked from it, so a remote trigger for one is refused",
    });
    expect(JSON.stringify(r)).not.toContain("never shown");
    const [byDefault] = refusalsFromRegistrationReply([
      {
        workspace: "default",
        team_id: "t",
        code: "PERMISSION_DENIED",
        permission: "workspace:update",
      },
    ]);
    expect(byDefault.description).toContain("did not write the team's Default workspace:");
  });

  it("keeps no control, format or link character, and no more than 100 characters", () => {
    expect(printableField("acme\nrm -rf \u202e/\u2028x")).toBe("acmerm -rf /x");
    expect(printableField("[Fix](command:x)")).toBe("(Fix)(command:x)");
    expect(printableField("a".repeat(150))).toBe(`${"a".repeat(100)}…`);
    expect(printableField("")).toBe("unknown");
    expect(printableField("\u0000\u200b")).toBe("unknown");
    expect(printableField(42)).toBe("unknown");
    // A reply that is not a list, or entries that are not objects, refuse nothing.
    expect(refusalsFromRegistrationReply(null)).toEqual([]);
    expect(refusalsFromRegistrationReply(["x", null, 7])).toEqual([]);
    const [hostile] = refusalsFromRegistrationReply([
      { workspace: "[Fix](command:workbench.action.reloadWindow)", team_id: 1 },
    ]);
    expect(hostile.description).not.toMatch(/\[[^\]]*\]\(/);
    expect(hostile.teamId).toBe("unknown");
  });

  it("summarises the refusals for the workspace sync status", () => {
    expect(refusedWorkspaceWritesSummary([refusal("a")])).toMatch(
      /^the platform refused a workspace write, /
    );
    expect(refusedWorkspaceWritesSummary([refusal("a"), refusal("b")])).toMatch(
      /^the platform refused 2 workspace writes, /
    );
  });
});
