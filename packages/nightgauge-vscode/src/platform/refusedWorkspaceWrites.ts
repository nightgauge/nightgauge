/**
 * RefusedWorkspaceWrites — shows the operator the workspace writes the
 * platform refused an agent registration (#2372).
 *
 * A registration still succeeds when the operator's role on the workspace's
 * team is developer or viewer, but the platform does not create the named
 * workspace, update it, or link the declared repositories to it. Those
 * repositories stay unlinked, and every remote trigger for one is refused,
 * so the operator has to hear why.
 *
 * Two registrations can be refused: the window's own (AgentRegistrationService,
 * whose reply this module bounds itself) and the daemon's, which the daemon
 * logs and reports in its platform status. The window shows both through one
 * RefusedWorkspaceWritesNotice: on the daemon's event for each registration
 * that was refused any, from the daemon's status each time the window
 * connects (the first registration can finish before the window listens),
 * and after its own registration. A set the window already showed is not
 * shown again.
 *
 * Every field shown is bounded and printable (RefusedWorkspaceWrite.Report in
 * the Go binary, printableField here); the platform's own message never
 * reaches the window. A notification renders markdown link syntax as a link,
 * and a `command:` link runs a command, so square brackets are shown as
 * parentheses: a hostile reply cannot put a link in front of the operator.
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

/** A field of a platform reply is cut to this many characters. */
const MAX_FIELD_CHARS = 100;

/**
 * One field of a platform reply, bounded for the operator's eyes: at most
 * MAX_FIELD_CHARS characters, only graphic ones (letters, marks, numbers,
 * punctuation, symbols and spaces), so no line break, other control or
 * format character, or bidirectional override gets through, and square
 * brackets as parentheses, so no markdown link does. "unknown" when nothing
 * is left, or the value is not a string. The Go binary's printableField
 * bounds the daemon's fields the same way.
 */
export function printableField(value: unknown): string {
  if (typeof value !== "string") return "unknown";
  let out = "";
  let n = 0;
  for (const ch of value) {
    if (!/[\p{L}\p{M}\p{N}\p{P}\p{S}\p{Zs}]/u.test(ch)) continue;
    if (n === MAX_FIELD_CHARS) {
      out += "…";
      break;
    }
    out += neutralizeLinkSyntax(ch);
    n++;
  }
  return out === "" ? "unknown" : out;
}

/** Square brackets as parentheses: no markdown link survives. */
function neutralizeLinkSyntax(text: string): string {
  return text.replace(/\[/g, "(").replace(/\]/g, ")");
}

/**
 * The operator's line for a refusal: the workspace that was not written and
 * the permission the write needs, from bounded fields. The same line the
 * daemon logs (RefusedWorkspaceWrite.Describe).
 */
export function describeRefusedWorkspaceWrite(
  r: Pick<RefusedWorkspaceWriteReport, "workspace" | "teamId" | "code" | "permission">
): string {
  // JSON quoting matches Go's %q for the printable text a field keeps.
  const workspace =
    r.workspace === "default"
      ? "the team's Default workspace"
      : `workspace ${JSON.stringify(r.workspace)}`;
  return (
    `the platform did not write ${workspace}: ${r.permission} needs the owner or admin role ` +
    `on its team (team ${r.teamId}, ${r.code}); repositories this agent declares stay ` +
    "unlinked from it, so a remote trigger for one is refused"
  );
}

/**
 * The refusals of a registration reply the window received itself
 * (`refused_workspace_writes`), bounded: the platform's own message is
 * dropped, and every other field is cut to its printable form.
 */
export function refusalsFromRegistrationReply(value: unknown): RefusedWorkspaceWriteReport[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((r): r is Record<string, unknown> => typeof r === "object" && r !== null)
    .map((r) => {
      const fields = {
        workspace: printableField(r["workspace"]),
        teamId: printableField(r["team_id"]),
        code: printableField(r["code"]),
        permission: printableField(r["permission"]),
      };
      return { ...fields, description: describeRefusedWorkspaceWrite(fields) };
    });
}

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
  const listed = refusals.slice(0, MAX_LISTED).map((r) => neutralizeLinkSyntax(r.description));
  const more = refusals.length - listed.length;
  return (
    `Nightgauge: the platform refused this agent's registration ${countOf(refusals)}: ` +
    listed.join("; ") +
    (more > 0 ? `; and ${more} more (see the Nightgauge Go Backend output)` : "") +
    ". An owner or admin of the team can make the write, or give you that role."
  );
}

/** A short form for the workspace sync status: how many writes were refused. */
export function refusedWorkspaceWritesSummary(refusals: RefusedWorkspaceWriteReport[]): string {
  return (
    `the platform refused ${countOf(refusals)}, so the workspace's repositories stay ` +
    "unlinked; an owner or admin of the team can make the write, or give you that role"
  );
}

function countOf(refusals: RefusedWorkspaceWriteReport[]): string {
  return refusals.length === 1 ? "a workspace write" : `${refusals.length} workspace writes`;
}

/**
 * Shows each distinct set of refused workspace writes once in this window,
 * whichever registration reported it: the window's own or the daemon's.
 */
export class RefusedWorkspaceWritesNotice {
  private shown = "";

  constructor(private readonly show: (message: string) => void) {}

  /** Show the well-formed refusals in `value`, unless this set was shown last. */
  report(value: unknown): void {
    const refusals = parseRefusedWorkspaceWrites(value);
    if (refusals.length === 0) return;
    const key = JSON.stringify(refusals.map((r) => r.description).sort());
    if (key === this.shown) return;
    this.shown = key;
    this.show(refusedWorkspaceWritesMessage(refusals));
  }
}

/** What the module needs from the IPC client. */
export interface RefusedWorkspaceWritesSource {
  on(event: string, handler: (data: unknown) => void): { dispose(): void };
  platformStatus(): Promise<Pick<PlatformStatus, "refusedWorkspaceWrites">>;
  onDidChangeStatus(listener: (connected: boolean) => void): { dispose(): void };
}

/**
 * Show the daemon's registration refusals through `notice`: from its event,
 * and from its status now and on every connect.
 */
export function followRefusedWorkspaceWrites(
  ipc: RefusedWorkspaceWritesSource,
  notice: Pick<RefusedWorkspaceWritesNotice, "report">
): { dispose(): void } {
  const readStatus = (): void => {
    ipc.platformStatus().then(
      (status) => notice.report(status?.refusedWorkspaceWrites),
      () => {
        // No daemon yet: its event, or the next connect, brings them.
      }
    );
  };
  const subscriptions = [
    ipc.on(WORKSPACE_WRITES_REFUSED_EVENT, (data) =>
      notice.report((data as { refusals?: unknown } | null)?.refusals)
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
