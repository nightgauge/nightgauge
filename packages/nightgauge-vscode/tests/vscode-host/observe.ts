/**
 * Observation layer for the VSCode host smoke tier.
 *
 * The whole tier turns on one property of the extension host: a module loaded
 * through `--extensionTestsPath` that lives *inside* the
 * `--extensionDevelopmentPath` tree resolves to the same extension identity as
 * the extension's own code, and therefore receives the **same** `vscode` API
 * object. Patching `vscode.window.createWebviewPanel` here is consequently
 * visible to `src/**` at runtime.
 *
 * That is what makes this tier able to assert on real panels and real tree
 * data providers without importing a single line of `src/**`. Importing the
 * view classes directly would have been easier and would also have been a
 * lie: it would build a *second* copy of every module (this bundle's copy,
 * not `dist/extension.cjs`'s), with its own module-level singletons, and
 * assert that the copy works.
 *
 * Because the whole design rests on that property, `installObservers()` fails
 * loudly if a patch does not take. A smoke tier that silently observes nothing
 * is worse than no smoke tier, because it reads as green.
 */

import * as vscode from "vscode";

export interface CapturedPanel {
  viewType: string;
  title: string;
  panel: vscode.WebviewPanel;
  createdAt: number;
  disposed: boolean;
  /**
   * Listeners the extension registered with `webview.onDidReceiveMessage`.
   * Calling them is how a case plays the webview's side of the protocol
   * (a tab click, a refresh button) without scripting the webview's DOM.
   */
  messageListeners: Array<(message: unknown) => unknown>;
  /** Every message the extension posted to the webview, in order. */
  posted: unknown[];
  /**
   * Every message the real webview sent the extension, in order. Calls a
   * case makes through `messageListeners` are not recorded here, so this is
   * the webview's own side of the protocol (a tab it actually activated).
   */
  received: unknown[];
}

export interface CapturedNotification {
  severity: "error" | "warning" | "information";
  message: string;
  at: number;
}

export interface CapturedTreeDataProvider {
  viewId: string;
  provider: vscode.TreeDataProvider<unknown>;
  via: "createTreeView" | "registerTreeDataProvider";
}

export interface OutputChannelLine {
  channel: string;
  line: string;
  at: number;
}

export interface ProcessFault {
  kind: "unhandledRejection" | "uncaughtException";
  detail: string;
  at: number;
}

const panels: CapturedPanel[] = [];
const treeProviders: CapturedTreeDataProvider[] = [];
const outputLines: OutputChannelLine[] = [];
const faults: ProcessFault[] = [];
const notifications: CapturedNotification[] = [];
const statusBarItems: vscode.StatusBarItem[] = [];

let installed = false;

/**
 * Lines the logger writes at ERROR level. `Logger.formatMessage` emits
 * `[<iso timestamp>] [ERROR] <message>`, and `LogOutputChannel.error()` is
 * routed through the same recorder below.
 */
const ERROR_LINE = /\[ERROR\]/;

export function installObservers(): void {
  if (installed) {
    throw new Error("installObservers() called twice");
  }
  installed = true;

  installProcessFaultHandlers();
  patch("createWebviewPanel", wrapCreateWebviewPanel);
  patch("createTreeView", wrapCreateTreeView);
  patch("registerTreeDataProvider", wrapRegisterTreeDataProvider);
  patch("createOutputChannel", wrapCreateOutputChannel);
  patch("createStatusBarItem", wrapCreateStatusBarItem);
  patch("showErrorMessage", (original) => wrapNotification("error", original));
  patch("showWarningMessage", (original) => wrapNotification("warning", original));
  patch("showInformationMessage", (original) => wrapNotification("information", original));
}

/**
 * Record every notification toast. The original is still called, so the
 * toast appears exactly as it would for a user; its first argument is the
 * message in every overload.
 */
function wrapNotification<F>(severity: CapturedNotification["severity"], original: F): F {
  return function patchedNotification(this: unknown, ...args: unknown[]): unknown {
    notifications.push({ severity, message: String(args[0]), at: Date.now() });
    return (original as unknown as (...a: unknown[]) => unknown).apply(this, args);
  } as unknown as F;
}

/**
 * Replace one `vscode.window` member and verify the replacement stuck.
 *
 * If the API object is ever frozen (or the member becomes a getter), every
 * assertion downstream would quietly observe an empty capture list. Rather
 * than pass an empty suite, blow up here with the member that refused.
 */
function patch<K extends keyof typeof vscode.window>(
  member: K,
  wrap: (original: (typeof vscode.window)[K]) => (typeof vscode.window)[K]
): void {
  const original = vscode.window[member];
  if (typeof original !== "function") {
    throw new Error(`vscode.window.${String(member)} is not a function — cannot observe it`);
  }
  const replacement = wrap(original);
  (vscode.window as Record<string, unknown>)[member as string] = replacement;
  if (vscode.window[member] !== replacement) {
    throw new Error(
      `Failed to patch vscode.window.${String(member)} — the extension host's API object rejected ` +
        `the assignment, so this tier cannot observe anything. Do not treat a green run as meaningful.`
    );
  }
}

function installProcessFaultHandlers(): void {
  process.on("unhandledRejection", (reason) => {
    faults.push({ kind: "unhandledRejection", detail: describe(reason), at: Date.now() });
  });
  process.on("uncaughtException", (error) => {
    faults.push({ kind: "uncaughtException", detail: describe(error), at: Date.now() });
  });
}

function describe(value: unknown): string {
  if (value instanceof Error) {
    return value.stack ?? `${value.name}: ${value.message}`;
  }
  try {
    return typeof value === "string" ? value : JSON.stringify(value);
  } catch {
    return String(value);
  }
}

type CreateWebviewPanel = typeof vscode.window.createWebviewPanel;

function wrapCreateWebviewPanel(original: CreateWebviewPanel): CreateWebviewPanel {
  return function patchedCreateWebviewPanel(
    this: unknown,
    ...args: Parameters<CreateWebviewPanel>
  ): ReturnType<CreateWebviewPanel> {
    const panel = (original as (...a: unknown[]) => vscode.WebviewPanel).apply(this, args);
    const record: CapturedPanel = {
      viewType: String(args[0]),
      title: String(args[1]),
      panel,
      createdAt: Date.now(),
      disposed: false,
      messageListeners: [],
      posted: [],
      received: [],
    };
    const webview = panel.webview;
    const subscribe = webview.onDidReceiveMessage.bind(webview);
    (webview as { onDidReceiveMessage: unknown }).onDidReceiveMessage = (
      listener: (message: unknown) => unknown,
      thisArg?: unknown,
      disposables?: vscode.Disposable[]
    ) => {
      const bound = thisArg ? listener.bind(thisArg) : listener;
      record.messageListeners.push(bound);
      return subscribe(
        (message: unknown) => {
          record.received.push(message);
          return bound(message);
        },
        undefined,
        disposables
      );
    };
    const post = webview.postMessage.bind(webview);
    (webview as { postMessage: unknown }).postMessage = (message: unknown) => {
      record.posted.push(message);
      return post(message);
    };
    panel.onDidDispose(() => {
      record.disposed = true;
    });
    panels.push(record);
    return panel;
  } as CreateWebviewPanel;
}

type CreateTreeView = typeof vscode.window.createTreeView;

function wrapCreateTreeView(original: CreateTreeView): CreateTreeView {
  return function patchedCreateTreeView(
    this: unknown,
    ...args: Parameters<CreateTreeView>
  ): ReturnType<CreateTreeView> {
    const viewId = String(args[0]);
    const options = args[1] as { treeDataProvider?: vscode.TreeDataProvider<unknown> } | undefined;
    if (options?.treeDataProvider) {
      treeProviders.push({ viewId, provider: options.treeDataProvider, via: "createTreeView" });
    }
    return (original as (...a: unknown[]) => unknown).apply(
      this,
      args
    ) as ReturnType<CreateTreeView>;
  } as CreateTreeView;
}

type RegisterTreeDataProvider = typeof vscode.window.registerTreeDataProvider;

function wrapRegisterTreeDataProvider(
  original: RegisterTreeDataProvider
): RegisterTreeDataProvider {
  return function patchedRegisterTreeDataProvider(
    this: unknown,
    ...args: Parameters<RegisterTreeDataProvider>
  ): vscode.Disposable {
    const viewId = String(args[0]);
    const provider = args[1] as vscode.TreeDataProvider<unknown> | undefined;
    if (provider) {
      treeProviders.push({ viewId, provider, via: "registerTreeDataProvider" });
    }
    return (original as (...a: unknown[]) => vscode.Disposable).apply(this, args);
  } as RegisterTreeDataProvider;
}

type CreateOutputChannel = typeof vscode.window.createOutputChannel;

/**
 * Record every line written to every channel the extension creates.
 *
 * The channel object is wrapped rather than proxied so the real object keeps
 * its identity for VSCode's own bookkeeping; only the write methods are
 * intercepted, and each forwards to the original before recording.
 */
function wrapCreateOutputChannel(original: CreateOutputChannel): CreateOutputChannel {
  return function patchedCreateOutputChannel(
    this: unknown,
    ...args: Parameters<CreateOutputChannel>
  ): ReturnType<CreateOutputChannel> {
    const name = String(args[0]);
    const channel = (original as unknown as (...a: unknown[]) => Record<string, unknown>).apply(
      this,
      args
    );

    for (const method of ["append", "appendLine", "trace", "debug", "info", "warn", "error"]) {
      const fn = channel[method];
      if (typeof fn !== "function") {
        continue;
      }
      // `LogOutputChannel.error()` carries its severity in the method name;
      // `appendLine()` carries it in the text the logger formatted.
      const forcedError = method === "error";
      channel[method] = function recorded(this: unknown, ...writeArgs: unknown[]): unknown {
        const text = writeArgs.map((value) => describe(value)).join(" ");
        outputLines.push({
          channel: name,
          line: forcedError && !ERROR_LINE.test(text) ? `[ERROR] ${text}` : text,
          at: Date.now(),
        });
        return (fn as (...a: unknown[]) => unknown).apply(this, writeArgs);
      };
    }

    return channel as unknown as ReturnType<CreateOutputChannel>;
  } as CreateOutputChannel;
}

type CreateStatusBarItem = typeof vscode.window.createStatusBarItem;

/** Record every status bar item, so a suite can read its id, text and state. */
function wrapCreateStatusBarItem(original: CreateStatusBarItem): CreateStatusBarItem {
  return function patchedCreateStatusBarItem(this: unknown, ...args: unknown[]) {
    const item = (original as (...a: unknown[]) => vscode.StatusBarItem).apply(this, args);
    statusBarItems.push(item);
    return item;
  } as CreateStatusBarItem;
}

export function capturedStatusBarItems(): readonly vscode.StatusBarItem[] {
  return statusBarItems;
}

export function capturedPanels(): readonly CapturedPanel[] {
  return panels;
}

export function capturedTreeProviders(): readonly CapturedTreeDataProvider[] {
  return treeProviders;
}

export function outputChannelErrorsSince(since: number): OutputChannelLine[] {
  return outputLines.filter((entry) => entry.at >= since && ERROR_LINE.test(entry.line));
}

export function allOutputLines(): readonly OutputChannelLine[] {
  return outputLines;
}

export function processFaults(): readonly ProcessFault[] {
  return faults;
}

export function notificationsSince(since: number): CapturedNotification[] {
  return notifications.filter((entry) => entry.at >= since);
}

/** Panels created while `body` ran, in creation order. */
export async function panelsCreatedBy(body: () => Promise<void>): Promise<CapturedPanel[]> {
  const before = panels.length;
  await body();
  return panels.slice(before);
}
