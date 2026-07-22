/**
 * Tests for the setRepoMaxConcurrent command's tier gating (Issue #4156).
 *
 * A per-repo concurrency cap above 1 is the same "concurrent-pipelines"
 * entitlement setConcurrentSlots.ts gates — this command is a second bypass
 * path to the same capability, so it needs the same tier gate.
 *
 * @see packages/nightgauge-vscode/src/commands/setRepoMaxConcurrent.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { QuickPickItem } from "vscode";

let quickPickResponse: QuickPickItem | undefined;
const mockShowQuickPick = vi.fn();
const mockShowInformationMessage = vi.fn();
const mockOpenExternal = vi.fn();
const mockRegisterCommand = vi.fn();

vi.mock("vscode", () => ({
  window: {
    showQuickPick: (...args: unknown[]) => {
      mockShowQuickPick(...args);
      return Promise.resolve(quickPickResponse);
    },
    showInformationMessage: (...args: unknown[]) => mockShowInformationMessage(...args),
    showWarningMessage: vi.fn(),
  },
  env: {
    openExternal: (...args: unknown[]) => mockOpenExternal(...args),
  },
  Uri: {
    parse: (url: string) => ({ toString: () => url, _url: url }),
  },
  commands: {
    registerCommand: (id: string, handler: (...args: unknown[]) => unknown) => {
      mockRegisterCommand(id, handler);
      return { dispose: vi.fn(), _handler: handler };
    },
  },
}));

// Mock RepositoryTreeItem so `item instanceof RepositoryTreeItem` passes
// without needing to construct the real vscode.TreeItem base class.
vi.mock("../../src/views/items/RepositoryTreeItem", () => {
  class RepositoryTreeItem {
    repository: { name: string };
    maxConcurrent: number | undefined;
    isSequential: boolean;
    constructor(name: string, maxConcurrent?: number, isSequential = false) {
      this.repository = { name };
      this.maxConcurrent = maxConcurrent;
      this.isSequential = isSequential;
    }
  }
  return { RepositoryTreeItem };
});

import { registerSetRepoMaxConcurrentCommand } from "../../src/commands/setRepoMaxConcurrent";
import { RepositoryTreeItem } from "../../src/views/items/RepositoryTreeItem";

function makeProvider() {
  return { setRepoMaxConcurrent: vi.fn() };
}

function makeTierGate(allowed: boolean) {
  return {
    check: vi.fn().mockReturnValue({
      allowed,
      requiredTier: "pro",
      upgradeUrl: "https://nightgauge.dev/pricing",
    }),
  };
}

function makeLicensePreflight(tier: string) {
  return { validate: vi.fn().mockResolvedValue({ tier }) };
}

async function invokeCommand(item: unknown) {
  const call = mockRegisterCommand.mock.calls.find(
    ([id]) => id === "nightgauge.repo.setMaxConcurrent"
  );
  await call![1](item);
}

describe("registerSetRepoMaxConcurrentCommand — tier gate (#4156)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    quickPickResponse = undefined;
  });

  it("allows selecting 1 (sequential) regardless of tier", async () => {
    quickPickResponse = { label: "$(symbol-number) 1 (sequential)", value: 1 } as QuickPickItem & {
      value: number;
    };
    const provider = makeProvider();
    const item = new (RepositoryTreeItem as any)("frontend");
    registerSetRepoMaxConcurrentCommand(
      provider as any,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );

    await invokeCommand(item);

    expect(provider.setRepoMaxConcurrent).toHaveBeenCalledWith(item, 1);
  });

  it("allows 'Workspace max' (undefined value) regardless of tier", async () => {
    quickPickResponse = {
      label: "$(circle-large-outline) Workspace max",
      value: undefined,
    } as QuickPickItem & { value: undefined };
    const provider = makeProvider();
    const item = new (RepositoryTreeItem as any)("frontend");
    registerSetRepoMaxConcurrentCommand(
      provider as any,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );

    await invokeCommand(item);

    expect(provider.setRepoMaxConcurrent).toHaveBeenCalledWith(item, undefined);
  });

  it("blocks an explicit cap >1 on community tier and shows an upgrade prompt", async () => {
    quickPickResponse = { label: "$(symbol-number) 5", value: 5 } as QuickPickItem & {
      value: number;
    };
    mockShowInformationMessage.mockResolvedValue(undefined);
    const provider = makeProvider();
    const item = new (RepositoryTreeItem as any)("frontend");
    registerSetRepoMaxConcurrentCommand(
      provider as any,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );

    await invokeCommand(item);

    expect(provider.setRepoMaxConcurrent).not.toHaveBeenCalled();
    expect(mockShowInformationMessage).toHaveBeenCalled();
  });

  it("allows an explicit cap >1 on pro tier", async () => {
    quickPickResponse = { label: "$(symbol-number) 5", value: 5 } as QuickPickItem & {
      value: number;
    };
    const provider = makeProvider();
    const item = new (RepositoryTreeItem as any)("frontend");
    registerSetRepoMaxConcurrentCommand(
      provider as any,
      makeTierGate(true) as any,
      makeLicensePreflight("pro") as any
    );

    await invokeCommand(item);

    expect(provider.setRepoMaxConcurrent).toHaveBeenCalledWith(item, 5);
  });

  it("skips the gate entirely when tierGate/licensePreflight are not provided", async () => {
    quickPickResponse = { label: "$(symbol-number) 5", value: 5 } as QuickPickItem & {
      value: number;
    };
    const provider = makeProvider();
    const item = new (RepositoryTreeItem as any)("frontend");
    registerSetRepoMaxConcurrentCommand(provider as any);

    await invokeCommand(item);

    expect(provider.setRepoMaxConcurrent).toHaveBeenCalledWith(item, 5);
  });
});
