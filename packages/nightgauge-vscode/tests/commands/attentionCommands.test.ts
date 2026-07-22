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

import { describe, it, expect, vi, beforeEach } from "vitest";
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
  let attentionResolve: ReturnType<typeof vi.fn>;
  let logger: Logger;
  let provider: AttentionTreeProvider;
  let treeView: { badge?: { value: number; tooltip: string } };

  beforeEach(() => {
    vi.clearAllMocks();
    attentionResolve = vi.fn().mockResolvedValue({ ok: true, alreadyResolved: false });
    (IpcClient.getInstance as unknown as ReturnType<typeof vi.fn>).mockReturnValue({
      attentionResolve,
    });
    logger = createLogger();
    provider = new AttentionTreeProvider();
    treeView = {};
    registerAttentionCommands({
      provider,
      treeView: treeView as unknown as vscode.TreeView<vscode.TreeItem>,
      logger,
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

    // Blocking, but not a "created" action (e.g. a re-detected idempotency-key
    // update) — no toast; it is the same request, not a new one.
    source.emit({ action: "updated", request: request({ severity: "blocking_fleet" }) });
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    (vscode.window.showWarningMessage as ReturnType<typeof vi.fn>).mockResolvedValue(
      "Open Action Center"
    );
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

    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>).mockImplementation(
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

    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>).mockImplementation(
      (items: Array<{ isSteer?: boolean }>) => Promise.resolve(items.find((i) => i.isSteer))
    );
    (vscode.window.showInputBox as ReturnType<typeof vi.fn>).mockResolvedValue(
      "  skip this wave, flaky test  "
    );

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

    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>).mockImplementation(
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
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>).mockImplementation(
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
    (vscode.window.showQuickPick as ReturnType<typeof vi.fn>).mockResolvedValue(undefined);

    await handler(item);

    expect(attentionResolve).not.toHaveBeenCalled();
  });

  it("attentionRefresh command calls provider.refresh()", async () => {
    const refreshSpy = vi.spyOn(provider, "refresh").mockResolvedValue(undefined);
    const handler = getHandler("nightgauge.attentionRefresh");

    await handler();

    expect(refreshSpy).toHaveBeenCalled();
  });
});
