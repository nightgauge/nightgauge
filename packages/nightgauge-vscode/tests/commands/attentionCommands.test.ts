/**
 * Tests for the Action Center commands (ADR 015 / #325): the badge + empty-
 * state context key, the toast on a newly created blocking request, and the
 * quick-pick resolve/steer flows.
 *
 * Overrides the vscode mock with a WORKING EventEmitter (`.fire()` really
 * invokes listeners) so the provider→command wiring (badge/context/toast) is
 * exercised for real, plus the window/commands surfaces the flows need
 * (showQuickPick, showInputBox, withProgress) that the shared setup.ts mock
 * omits — the same technique existing command tests use (e.g.
 * activateLicense.test.ts, RepositoriesTreeProvider.test.ts).
 *
 * @see src/commands/attentionCommands.ts
 * @see Issue #325
 */

import { describe, it, expect, vi, beforeEach, type Mock } from "vitest";
import * as vscode from "vscode";
import { registerAttentionCommands } from "../../src/commands/attentionCommands";
import {
  AttentionTreeProvider,
  type AttentionIpcSource,
} from "../../src/views/attention/AttentionTreeProvider";
import { AttentionRequestTreeItem } from "../../src/views/attention/attentionTreeItems";
import { IpcClient } from "../../src/services/IpcClient";
import type {
  AttentionRequestView,
  AttentionEvent,
  AttentionListResult,
} from "../../src/services/IpcClientBase";
import type { Logger } from "../../src/utils/logger";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: vi.fn() },
}));

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter<T> {
    private _listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this._listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (event?: T) => {
      this._listeners.forEach((l) => l(event as T));
    };
    dispose = vi.fn();
  },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItem: class TreeItem {
    label: string;
    collapsibleState: number;
    iconPath?: unknown;
    contextValue?: string;
    description?: string;
    tooltip?: unknown;
    command?: unknown;
    constructor(label: string, collapsibleState: number = 0) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  ThemeIcon: class ThemeIcon {
    constructor(
      public id: string,
      public color?: unknown
    ) {}
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  MarkdownString: class MarkdownString {
    value = "";
    appendMarkdown(value: string) {
      this.value += value;
      return this;
    }
  },
  ProgressLocation: { Notification: 15 },
  Uri: { parse: (value: string) => ({ toString: () => value, value }) },
  env: { openExternal: vi.fn(() => Promise.resolve(true)) },
  commands: {
    registerCommand: vi.fn((_id: string, _handler: unknown) => ({ dispose: vi.fn() })),
    executeCommand: vi.fn(),
  },
  window: {
    showQuickPick: vi.fn(),
    showInputBox: vi.fn(),
    showInformationMessage: vi.fn(() => Promise.resolve(undefined)),
    showWarningMessage: vi.fn(() => Promise.resolve(undefined)),
    showErrorMessage: vi.fn(() => Promise.resolve(undefined)),
    withProgress: vi.fn(async (_opts: unknown, task: () => Promise<unknown>) => task()),
  },
}));

/** Extract a registered command handler by command ID. */
function getHandler(commandId: string): (...args: unknown[]) => Promise<void> {
  const calls = (vscode.commands.registerCommand as unknown as { mock: { calls: unknown[][] } })
    .mock.calls;
  const match = calls.find((c) => c[0] === commandId);
  if (!match) throw new Error(`Command not registered: ${commandId}`);
  return match[1] as (...args: unknown[]) => Promise<void>;
}

/** A controllable fake IPC source, matching AttentionTreeProvider.test.ts's fixture. */
class FakeSource implements AttentionIpcSource {
  list: AttentionRequestView[] = [];
  private handler: ((data: unknown) => void) | null = null;
  attentionList(): Promise<AttentionListResult> {
    return Promise.resolve({ requests: this.list });
  }
  on(_event: string, handler: (data: unknown) => void): { dispose(): void } {
    this.handler = handler;
    return { dispose: () => {} };
  }
  emit(evt: AttentionEvent): void {
    this.handler?.(evt);
  }
}

function request(overrides: Partial<AttentionRequestView> = {}): AttentionRequestView {
  return {
    schema_version: 1,
    id: "dr_1",
    idempotency_key: "test:1",
    kind: "approve",
    severity: "blocking_run",
    title: "Budget ceiling hit",
    body: "over ceiling",
    context: { repo: "octocat/acme-web", issue: 42 },
    producer: "budget-enforcer",
    options: [
      { id: "raise", label: "Raise to $20", verb: "budget.raiseCeiling", args: { ceilingUsd: 20 } },
      { id: "halt", label: "Halt", verb: "noop" },
    ],
    steer: { enabled: true, hint: "" },
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + 3600_000).toISOString(),
    default_action: "halt",
    lifecycle: { state: "open" },
    ...overrides,
  };
}

const createLogger = (): Logger =>
  ({ info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() }) as unknown as Logger;

describe("registerAttentionCommands", () => {
  let attentionResolve: Mock;
  let attentionMute: Mock;
  let attentionUnmute: Mock;
  let logger: Logger;
  let provider: AttentionTreeProvider;
  let treeView: { badge?: { value: number; tooltip: string }; description?: string };
  let sweep: { sweep: Mock };

  beforeEach(() => {
    vi.clearAllMocks();
    attentionResolve = vi.fn().mockResolvedValue({ ok: true, alreadyResolved: false });
    attentionMute = vi.fn().mockResolvedValue({ ok: true, muted: true });
    attentionUnmute = vi.fn().mockResolvedValue({ ok: true, muted: false });
    (IpcClient.getInstance as unknown as Mock).mockReturnValue({
      attentionResolve,
      attentionMute,
      attentionUnmute,
    });
    logger = createLogger();
    provider = new AttentionTreeProvider();
    treeView = {};
    sweep = { sweep: vi.fn().mockResolvedValue(undefined) };
    registerAttentionCommands({
      provider,
      treeView: treeView as unknown as vscode.TreeView<vscode.TreeItem>,
      logger,
      sweep,
    });
  });

  it("registers the refresh and resolve commands", () => {
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.attentionRefresh",
      expect.any(Function)
    );
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.attentionResolve",
      expect.any(Function)
    );
  });

  it("sets an empty badge and a false context key from the provider's initial (empty) state", () => {
    expect(treeView.badge).toBeUndefined();
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "setContext",
      "nightgauge.attentionHasRequests",
      false
    );
  });

  it("updates the badge to the open-blocking count and the context key as requests arrive", async () => {
    const source = new FakeSource();
    source.list = [
      request({ id: "dr_1", severity: "blocking_run" }),
      request({ id: "dr_2", severity: "blocking_fleet" }),
      request({ id: "dr_3", severity: "fyi" }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(treeView.badge).toEqual({ value: 2, tooltip: "2 blocking decisions pending" });
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "setContext",
      "nightgauge.attentionHasRequests",
      true
    );
  });

  it("shows a toast with an Open Action Center button only for a newly created open blocking request", async () => {
    const source = new FakeSource();
    provider.attach(source);
    await Promise.resolve();

    // Not blocking severity — no toast.
    source.emit({ action: "created", request: request({ severity: "fyi" }) });
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    // Blocking, but not a "created" action. On the event-scoped `raise` path an
    // `updated` is any re-raise of the same open request — identical payload
    // included — so it is the same request, not new news.
    source.emit({ action: "updated", request: request({ severity: "blocking_fleet" }) });
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    (vscode.window.showWarningMessage as Mock).mockResolvedValue("Open Action Center");
    source.emit({
      action: "created",
      request: request({ severity: "blocking_fleet", title: "Fleet stopped" }),
    });

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      "Nightgauge: Fleet stopped",
      "Open Action Center"
    );
    await Promise.resolve();
    await Promise.resolve();
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.attentionView.focus");
  });

  it("resolve flow: picking a declared option calls attention.resolve with its validated id", async () => {
    const item = new AttentionRequestTreeItem(request());
    const handler = getHandler("nightgauge.attentionResolve");

    (vscode.window.showQuickPick as Mock).mockImplementation(
      (items: Array<{ optionId?: string }>) =>
        Promise.resolve(items.find((i) => i.optionId === "raise"))
    );

    await handler(item);

    expect(attentionResolve).toHaveBeenCalledTimes(1);
    const [id, optionId, , steerText] = attentionResolve.mock.calls[0];
    expect(id).toBe("dr_1");
    expect(optionId).toBe("raise");
    expect(steerText).toBeUndefined();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("Resolved — Raise to $20")
    );
  });

  it("steer flow: applies the default_action option and passes the typed free text", async () => {
    const item = new AttentionRequestTreeItem(request()); // default_action: "halt", steer.enabled
    const handler = getHandler("nightgauge.attentionResolve");

    (vscode.window.showQuickPick as Mock).mockImplementation(
      (items: Array<{ isSteer?: boolean }>) => Promise.resolve(items.find((i) => i.isSteer))
    );
    (vscode.window.showInputBox as Mock).mockResolvedValue("  skip this wave, flaky test  ");

    await handler(item);

    expect(attentionResolve).toHaveBeenCalledTimes(1);
    const [id, optionId, , steerText] = attentionResolve.mock.calls[0];
    expect(id).toBe("dr_1");
    expect(optionId).toBe("halt");
    expect(steerText).toBe("skip this wave, flaky test");
  });

  it("steer flow errors out cleanly when default_action has no matching declared option", async () => {
    const item = new AttentionRequestTreeItem(request({ default_action: "expire_noop" }));
    const handler = getHandler("nightgauge.attentionResolve");

    (vscode.window.showQuickPick as Mock).mockImplementation(
      (items: Array<{ isSteer?: boolean }>) => Promise.resolve(items.find((i) => i.isSteer))
    );

    await handler(item);

    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining("no safe default action")
    );
    expect(vscode.window.showInputBox).not.toHaveBeenCalled();
    expect(attentionResolve).not.toHaveBeenCalled();
  });

  it("does not offer Custom steer when the request has no steer box", async () => {
    const item = new AttentionRequestTreeItem(request({ steer: undefined }));
    const handler = getHandler("nightgauge.attentionResolve");

    let offered: Array<{ isSteer?: boolean }> = [];
    (vscode.window.showQuickPick as Mock).mockImplementation(
      (items: Array<{ isSteer?: boolean }>) => {
        offered = items;
        return Promise.resolve(undefined);
      }
    );

    await handler(item);

    expect(offered.some((i) => i.isSteer)).toBe(false);
  });

  it("does nothing when the quick-pick is dismissed", async () => {
    const item = new AttentionRequestTreeItem(request());
    const handler = getHandler("nightgauge.attentionResolve");
    (vscode.window.showQuickPick as Mock).mockResolvedValue(undefined);

    await handler(item);

    expect(attentionResolve).not.toHaveBeenCalled();
  });

  it("attentionRefresh command calls provider.refresh()", async () => {
    const refreshSpy = vi.spyOn(provider, "refresh").mockResolvedValue(undefined);
    const handler = getHandler("nightgauge.attentionRefresh");

    await handler();

    expect(refreshSpy).toHaveBeenCalled();
  });

  // ── Repo-scoped surface (issue #93) ──────────────────────────────────────

  /** A repo-scoped standing card: no run, no issue, one noop dismiss, a URL. */
  const repoCard = (overrides: Partial<AttentionRequestView> = {}) =>
    request({
      id: "dr_repo",
      severity: "blocking_fleet",
      kind: "unblock",
      title: "main is red — 'Security & license gates' is failing on octocat/acme-web",
      producer: "default-branch-health",
      standing: true,
      steer: undefined,
      default_action: "expire_noop",
      options: [{ id: "dismiss", label: "Dismiss — I've seen it", verb: "noop" }],
      context: {
        repo: "octocat/acme-web",
        blocker: "required check(s) failing on main",
        url: "https://github.com/octocat/acme-web/actions/runs/1",
      },
      ...overrides,
    });

  it("registers the sweep, open-link, mute and unmute commands", () => {
    for (const id of [
      "nightgauge.attentionSweep",
      "nightgauge.attentionOpenLink",
      "nightgauge.attentionMute",
      "nightgauge.attentionUnmute",
    ]) {
      expect(vscode.commands.registerCommand).toHaveBeenCalledWith(id, expect.any(Function));
    }
  });

  it("refreshing the Action Center also triggers a repo-scoped sweep", async () => {
    vi.spyOn(provider, "refresh").mockResolvedValue(undefined);
    await getHandler("nightgauge.attentionRefresh")();

    expect(sweep.sweep).toHaveBeenCalledWith("view-refresh");
  });

  it("the explicit sweep command runs a manual sweep and re-reads the tree", async () => {
    const refreshSpy = vi.spyOn(provider, "refresh").mockResolvedValue(undefined);
    await getHandler("nightgauge.attentionSweep")();

    expect(sweep.sweep).toHaveBeenCalledWith("manual");
    expect(refreshSpy).toHaveBeenCalled();
  });

  it("offers the link FIRST for a card whose only option changes nothing", async () => {
    const handler = getHandler("nightgauge.attentionResolve");
    let offered: Array<{ label: string }> = [];
    (vscode.window.showQuickPick as Mock).mockImplementation((items: Array<{ label: string }>) => {
      offered = items;
      return Promise.resolve(items[0]);
    });

    await handler(new AttentionRequestTreeItem(repoCard()));

    expect(offered[0].label).toContain("Open in browser");
    expect(vscode.env.openExternal).toHaveBeenCalled();
    // Opening a link is not a resolution — the condition is still true.
    expect(attentionResolve).not.toHaveBeenCalled();
  });

  it("describes a standing dismiss honestly rather than as 'takes no action'", async () => {
    const handler = getHandler("nightgauge.attentionResolve");
    let offered: Array<{ label: string; description?: string }> = [];
    (vscode.window.showQuickPick as Mock).mockImplementation(
      (items: Array<{ label: string; description?: string }>) => {
        offered = items;
        return Promise.resolve(undefined);
      }
    );

    await handler(new AttentionRequestTreeItem(repoCard()));

    const dismiss = offered.find((i) => i.label.startsWith("Dismiss"));
    expect(dismiss?.description).toContain("returns if the condition changes");
  });

  it("offers mute on a standing card and unmute once it is muted — never on an event card", async () => {
    const handler = getHandler("nightgauge.attentionResolve");
    const capture = async (req: AttentionRequestView) => {
      let offered: Array<{ label: string }> = [];
      (vscode.window.showQuickPick as Mock).mockImplementation(
        (items: Array<{ label: string }>) => {
          offered = items;
          return Promise.resolve(undefined);
        }
      );
      await handler(new AttentionRequestTreeItem(req));
      return offered.map((i) => i.label);
    };

    expect(await capture(repoCard())).toContain("$(bell-slash) Mute until this changes");
    expect(
      await capture(
        repoCard({ lifecycle: { state: "open", muted: { actor: "octocat", at: "now" } } })
      )
    ).toContain("$(bell) Unmute");
    // An event-scoped card has no fingerprint to mute "until it changes".
    expect(await capture(request())).not.toContain("$(bell-slash) Mute until this changes");
  });

  it("mute calls attention.mute and does not resolve the request", async () => {
    const handler = getHandler("nightgauge.attentionMute");
    await handler(new AttentionRequestTreeItem(repoCard()));

    expect(attentionMute).toHaveBeenCalledWith("dr_repo", expect.anything());
    expect(attentionResolve).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("stays in the inbox")
    );
  });

  it("unmute calls attention.unmute", async () => {
    const handler = getHandler("nightgauge.attentionUnmute");
    await handler(
      new AttentionRequestTreeItem(
        repoCard({ lifecycle: { state: "open", muted: { actor: "octocat", at: "now" } } })
      )
    );

    expect(attentionUnmute).toHaveBeenCalledWith("dr_repo", expect.anything());
  });

  it("names a fleet-wide blocker in the view header so it survives a collapsed tree", async () => {
    const source = new FakeSource();
    source.list = [repoCard()];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(treeView.description).toContain("main is red");
    expect(treeView.badge).toEqual({ value: 1, tooltip: "1 blocking decision pending" });
  });

  it("does not badge or headline a muted condition", async () => {
    const source = new FakeSource();
    source.list = [
      repoCard({ lifecycle: { state: "open", muted: { actor: "octocat", at: "now" } } }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(treeView.badge).toBeUndefined();
    expect(treeView.description).toBeUndefined();
    // …but the card is still there.
    expect(provider.hasAny()).toBe(true);
  });

  it("does not badge an acknowledged condition", async () => {
    const source = new FakeSource();
    source.list = [
      repoCard({
        lifecycle: { state: "acknowledged", acknowledged: { actor: "octocat", at: "now" } },
      }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(treeView.badge).toBeUndefined();
  });

  it("re-renders but stays silent for a refreshed standing card, and toasts a real change", async () => {
    const source = new FakeSource();
    provider.attach(source);
    await Promise.resolve();

    // The sweep re-observes an unchanged red `main` every cycle. Toasting each
    // observation is how an operator learns to dismiss without reading.
    source.emit({ action: "refreshed", request: repoCard() });
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();
    expect(provider.getOpenBlockingCount()).toBe(1);

    // A muted card never toasts, even on a genuine transition.
    source.emit({
      action: "updated",
      request: repoCard({ lifecycle: { state: "open", muted: { actor: "octocat", at: "now" } } }),
    });
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    // A second check starting to fail moves the fingerprint, which is the only
    // thing that makes reconciliation emit `updated` rather than `refreshed`.
    // That IS news.
    source.emit({
      action: "updated",
      request: repoCard({ title: "main is red — 2 required checks failing on octocat/acme-web" }),
    });
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("2 required checks failing"),
      "Open Action Center"
    );
  });
});
