/**
 * Tests for AttentionTreeProvider — severity-ordered rendering, the badge
 * count, and live updates off the `attention.event` push (ADR 015 / #325).
 *
 * Drives the provider with a fake {@link AttentionIpcSource} (no real IPC
 * connection) and asserts: the two mockup severity bands ("Blocking" /
 * "Needs a human"), ordering within the blocking band (fleet before run),
 * the open-blocking badge count, and that a resolved/expired push removes a
 * card everywhere ("one queue, many mirrors" — ADR 015 §D) while a created
 * push adds one.
 *
 * Overrides the shared `vscode` mock (tests/setup.ts) with a WORKING
 * EventEmitter (the shared one is spy-only, `.fire()` never invokes
 * listeners) — the same technique RepositoriesTreeProvider.test.ts uses —
 * so the `onDidReceiveEvent` re-broadcast assertion is real, not a no-op.
 */

import { describe, it, expect, vi } from "vitest";
import {
  AttentionTreeProvider,
  type AttentionIpcSource,
} from "../../src/views/attention/AttentionTreeProvider";
import {
  AttentionGroupTreeItem,
  AttentionRequestTreeItem,
  type AttentionTreeItem,
} from "../../src/views/attention/attentionTreeItems";
import type {
  AttentionRequestView,
  AttentionEvent,
  AttentionListResult,
} from "../../src/services/IpcClientBase";

// Override the shared vscode mock with a working EventEmitter (fire() really
// invokes subscribed listeners) plus the TreeItem/ThemeIcon/etc. primitives
// attentionTreeItems.ts needs to render a card.
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
}));

/** A controllable fake IPC source: attentionList is a mock, .on captures the handler. */
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

let seq = 0;
function request(overrides: Partial<AttentionRequestView> = {}): AttentionRequestView {
  seq += 1;
  return {
    schema_version: 1,
    id: overrides.id ?? `dr_${seq}`,
    idempotency_key: `test:${seq}`,
    kind: "choose",
    severity: "fyi",
    title: `Request ${seq}`,
    body: "",
    context: { repo: "octocat/acme-web", issue: 100 + seq },
    producer: "test-producer",
    options: [{ id: "leave", label: "Leave", verb: "noop" }],
    created_at: new Date(2026, 0, 1, 0, 0, seq).toISOString(),
    expires_at: new Date(2026, 0, 2).toISOString(),
    default_action: "leave",
    lifecycle: { state: "open" },
    ...overrides,
  };
}

async function children(
  provider: AttentionTreeProvider,
  el?: AttentionTreeItem
): Promise<AttentionTreeItem[]> {
  return (await provider.getChildren(el)) as AttentionTreeItem[];
}

describe("AttentionTreeProvider", () => {
  it("shows no groups when there are no open requests (empty state)", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    provider.attach(source);
    await Promise.resolve();

    expect(await children(provider)).toHaveLength(0);
    expect(provider.hasAny()).toBe(false);
    expect(provider.getOpenBlockingCount()).toBe(0);
    provider.dispose();
  });

  it("groups into Blocking / Needs a human and orders fleet before run", async () => {
    const fyi = request({ id: "dr_fyi", severity: "fyi", title: "owner-action: DNS checklist" });
    const run = request({ id: "dr_run", severity: "blocking_run", title: "Budget ceiling hit" });
    const fleet = request({ id: "dr_fleet", severity: "blocking_fleet", title: "Fleet stopped" });

    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    // Insertion order deliberately scrambled — the provider must sort, not trust input order.
    source.list = [fyi, run, fleet];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    const roots = await children(provider);
    expect(roots).toHaveLength(2);
    expect(String(roots[0].label)).toBe("Blocking (2)");
    expect(String(roots[1].label)).toBe("Needs a human (1)");

    const blockingChildren = await children(provider, roots[0]);
    expect(blockingChildren.map((c) => (c as AttentionRequestTreeItem).request.id)).toEqual([
      "dr_fleet",
      "dr_run",
    ]);

    const fyiChildren = await children(provider, roots[1]);
    expect(fyiChildren.map((c) => (c as AttentionRequestTreeItem).request.id)).toEqual(["dr_fyi"]);

    provider.dispose();
  });

  it("counts only blocking severities for the badge", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [
      request({ severity: "fyi" }),
      request({ severity: "blocking_run" }),
      request({ severity: "blocking_fleet" }),
      request({ severity: "blocking_fleet" }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(provider.getOpenBlockingCount()).toBe(3);
    provider.dispose();
  });

  it("removes a card everywhere on a resolved/expired push (one queue, many mirrors)", async () => {
    const target = request({ id: "dr_target", severity: "blocking_run", title: "Resolve me" });
    const other = request({ id: "dr_other", severity: "blocking_fleet", title: "Stay" });

    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [target, other];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(provider.getOpenBlockingCount()).toBe(2);

    source.emit({
      action: "resolved",
      request: {
        ...target,
        lifecycle: {
          state: "resolved",
          resolved: { actor: "octocat", at: "now", option_id: "leave" },
        },
      },
    });

    expect(provider.getOpenBlockingCount()).toBe(1);
    const roots = await children(provider);
    const blocking = roots.find((r) =>
      String(r.label).startsWith("Blocking")
    ) as AttentionGroupTreeItem;
    const blockingChildren = await children(provider, blocking);
    expect(blockingChildren.map((c) => (c as AttentionRequestTreeItem).request.id)).toEqual([
      "dr_other",
    ]);

    provider.dispose();
  });

  it("adds a newly created request on the next push and re-broadcasts the event", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [];
    provider.attach(source);
    await Promise.resolve();

    const received: AttentionEvent[] = [];
    provider.onDidReceiveEvent((evt) => received.push(evt));

    const created = request({ id: "dr_new", severity: "blocking_fleet", title: "New blocker" });
    source.emit({ action: "created", request: created });

    expect(provider.getOpenBlockingCount()).toBe(1);
    expect(received).toHaveLength(1);
    expect(received[0].action).toBe("created");
    expect(received[0].request.id).toBe("dr_new");

    provider.dispose();
  });

  it("attach is idempotent for the same source", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [request({ severity: "blocking_fleet" })];
    provider.attach(source);
    provider.attach(source); // second attach must not double-fetch/subscribe
    await Promise.resolve();
    await Promise.resolve();

    expect(provider.getOpenBlockingCount()).toBe(1);
    provider.dispose();
  });
});

/**
 * Repo-scoped cards (issue #93): `context.repo` is set, `run_id` and `issue`
 * are not. The grouping is by severity, so these need no new section — but that
 * is a property worth pinning, because any grouping keyed on run or issue would
 * drop them silently rather than fail loudly.
 */
describe("AttentionTreeProvider — repo-scoped cards", () => {
  const repoScoped = (overrides: Partial<AttentionRequestView> = {}) =>
    request({
      severity: "blocking_fleet",
      title: "main is red — 'Security & license gates' is failing on octocat/acme-web",
      producer: "default-branch-health",
      standing: true,
      fingerprint: "checks:Security & license gates",
      context: {
        repo: "octocat/acme-web",
        blocker: "required check(s) failing on main",
        url: "https://github.com/octocat/acme-web/actions/runs/1",
      },
      ...overrides,
    });

  it("renders a card with no run and no issue in the Blocking band", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [repoScoped({ id: "dr_repo" })];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    const roots = await children(provider);
    expect(roots).toHaveLength(1);
    expect(String(roots[0].label)).toBe("Blocking (1)");

    const [card] = (await children(provider, roots[0])) as AttentionRequestTreeItem[];
    expect(card.request.id).toBe("dr_repo");
    // No placeholder for the fields it does not carry.
    expect(String(card.description)).toContain("octocat/acme-web");
    expect(String(card.description)).not.toContain("undefined");
    expect(String(card.description)).not.toContain("#");
    // The link is a real affordance, not prose.
    expect(card.contextValue).toBe("attention.request.link");

    provider.dispose();
  });

  it("exposes the top fleet blocker so it is visible without expanding a node", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [
      request({ id: "dr_run", severity: "blocking_run" }),
      repoScoped({ id: "dr_fleet" }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(provider.getTopFleetBlocker()?.id).toBe("dr_fleet");
    provider.dispose();
  });

  it("keeps a muted card in the tree but drops it from the badge and the header", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [
      repoScoped({
        id: "dr_muted",
        lifecycle: { state: "open", muted: { actor: "octocat", at: "now" } },
      }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    // Muting is not resolving — the card stays in the inbox at its severity.
    const roots = await children(provider);
    expect(String(roots[0].label)).toBe("Blocking (1)");
    const [card] = (await children(provider, roots[0])) as AttentionRequestTreeItem[];
    expect(String(card.description)).toContain("muted");
    expect(card.contextValue).toBe("attention.request.link.muted");

    // …but it no longer interrupts.
    expect(provider.getOpenBlockingCount()).toBe(0);
    expect(provider.getTopFleetBlocker()).toBeUndefined();

    provider.dispose();
  });

  it("keeps an acknowledged card in the tree but drops it from the badge", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [
      repoScoped({
        id: "dr_ack",
        lifecycle: { state: "acknowledged", acknowledged: { actor: "octocat", at: "now" } },
      }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    expect(provider.hasAny()).toBe(true);
    expect(provider.getOpenBlockingCount()).toBe(0);

    provider.dispose();
  });

  it("drops an auto-resolved card with no operator action (the condition cleared)", async () => {
    const card = repoScoped({ id: "dr_auto" });
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [card];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();
    expect(provider.getOpenBlockingCount()).toBe(1);

    source.emit({
      action: "auto_resolved",
      request: {
        ...card,
        lifecycle: {
          state: "auto_resolved",
          auto_resolved: {
            at: "now",
            producer: "default-branch-health",
            reason: "condition_cleared",
          },
        },
      },
    });

    expect(provider.getOpenBlockingCount()).toBe(0);
    expect(provider.hasAny()).toBe(false);
    provider.dispose();
  });

  it("renders a PR-scoped card as repo#pr rather than a blank run field", async () => {
    const provider = new AttentionTreeProvider();
    const source = new FakeSource();
    source.list = [
      request({
        id: "dr_pr",
        severity: "blocking_run",
        title: "PR #42 is green and waiting — a review is required",
        context: {
          repo: "octocat/acme-web",
          pr: 42,
          url: "https://github.com/octocat/acme-web/pull/42",
        },
      }),
    ];
    provider.attach(source);
    await Promise.resolve();
    await Promise.resolve();

    const roots = await children(provider);
    const [card] = (await children(provider, roots[0])) as AttentionRequestTreeItem[];
    expect(String(card.description)).toContain("octocat/acme-web#42");

    provider.dispose();
  });
});
