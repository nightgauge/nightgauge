/**
 * RefusedWorkspaceWrites — shows the operator the workspace writes the
 * platform refused the daemon's agent registration (#2372).
 *
 * A registration still succeeds when the operator's role on the workspace's
 * team is developer or viewer, but the platform does not create the named
 * workspace, update it, or link the declared repositories to it. Those
 * repositories stay unlinked, and every remote trigger for one is refused,
 * so the operator has to hear why. The daemon logs each refusal and reports
 * the latest registration's in its platform status; this module shows them
 * in the window: on the daemon's event for each registration that was
 * refused any, and from the daemon's status each time the window connects,
 * since the first registration can finish before the window listens. A set
 * the window already showed is not shown again.
 *
 * Every field the daemon sends is bounded and printable
 * (RefusedWorkspaceWrite.Report in the Go binary); the platform's own
 * message never reaches the window.
 */

import type { PlatformStatus } from "../services/IpcClientBase";

/** The daemon's event for a registration that was refused workspace writes. */
export const WORKSPACE_WRITES_REFUSED_EVENT = "platform.workspaceWritesRefused";

/** One refused write, as the daemon reports it. */
export type RefusedWorkspaceWriteReport = NonNullable<
  PlatformStatus["refusedWorkspaceWrites"]
>[number];

/** At most this many refusals are spelled out in one notification. */
const MAX_LISTED = 3;

/** The refusals in a payload, keeping only well-formed ones. */
export function parseRefusedWorkspaceWrites(value: unknown): RefusedWorkspaceWriteReport[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (r): r is RefusedWorkspaceWriteReport =>
      typeof r === "object" &&
      r !== null &&
      typeof (r as RefusedWorkspaceWriteReport).description === "string" &&
      (r as RefusedWorkspaceWriteReport).description !== ""
  );
}

/** The notification's text: what was not written, and who can write it. */
export function refusedWorkspaceWritesMessage(refusals: RefusedWorkspaceWriteReport[]): string {
  const listed = refusals.slice(0, MAX_LISTED).map((r) => r.description);
  const more = refusals.length - listed.length;
  const count = refusals.length === 1 ? "a workspace write" : `${refusals.length} workspace writes`;
  return (
    `Nightgauge: the platform refused this agent's registration ${count}: ` +
    listed.join("; ") +
    (more > 0 ? `; and ${more} more (see the Nightgauge Go Backend output)` : "") +
    ". An owner or admin of the team can make the write, or give you that role."
  );
}

/** What the module needs from the IPC client. */
export interface RefusedWorkspaceWritesSource {
  on(event: string, handler: (data: unknown) => void): { dispose(): void };
  platformStatus(): Promise<Pick<PlatformStatus, "refusedWorkspaceWrites">>;
  onDidChangeStatus(listener: (connected: boolean) => void): { dispose(): void };
}

/**
 * Show each distinct set of refused workspace writes once in this window:
 * from the daemon's event, and from its status now and on every connect.
 */
export function followRefusedWorkspaceWrites(
  ipc: RefusedWorkspaceWritesSource,
  show: (message: string) => void
): { dispose(): void } {
  let shown = "";
  const showOnce = (value: unknown): void => {
    const refusals = parseRefusedWorkspaceWrites(value);
    if (refusals.length === 0) return;
    const key = JSON.stringify(refusals.map((r) => r.description).sort());
    if (key === shown) return;
    shown = key;
    show(refusedWorkspaceWritesMessage(refusals));
  };
  const readStatus = (): void => {
    ipc.platformStatus().then(
      (status) => showOnce(status?.refusedWorkspaceWrites),
      () => {
        // No daemon yet: its event, or the next connect, brings them.
      }
    );
  };
  const subscriptions = [
    ipc.on(WORKSPACE_WRITES_REFUSED_EVENT, (data) =>
      showOnce((data as { refusals?: unknown } | null)?.refusals)
    ),
    ipc.onDidChangeStatus((connected) => {
      if (connected) readStatus();
    }),
  ];
  readStatus();
  return {
    dispose: () => {
      for (const s of subscriptions) s.dispose();
    },
  };
}
