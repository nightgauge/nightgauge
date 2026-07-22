/**
 * Tests for the setConcurrentSlots command's tier gating (Issue #4156).
 *
 * Running more than 1 pipeline concurrently is the "concurrent-pipelines"
 * FEATURE_TIER_MAP entry (pro+). Before #4156 this command was completely
 * ungated — a community-tier user could set concurrent slots to 5 and
 * ConcurrentPipelineManager would actually run 5 concurrent pipelines
 * unrestricted (no server-side enforcement exists either).
 *
 * @see packages/nightgauge-vscode/src/commands/setConcurrentSlots.ts
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

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return {
      writeLocal: vi.fn().mockResolvedValue({ success: true }),
      dispose: vi.fn(),
    };
  }),
}));

import { registerSetConcurrentSlotsCommand } from "../../src/commands/setConcurrentSlots";

function makeConcurrentPipelineManager() {
  return {
    maxConcurrentSlots: 1,
    setMaxConcurrentSlots: vi.fn(),
  };
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

async function invokeCommand() {
  const call = mockRegisterCommand.mock.calls.find(
    ([id]) => id === "nightgauge.setConcurrentSlots"
  );
  await call![1]();
}

describe("registerSetConcurrentSlotsCommand — tier gate (#4156)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    quickPickResponse = undefined;
  });

  it("allows selecting 1 (sequential) regardless of tier", async () => {
    quickPickResponse = { label: "1 — sequential" } as QuickPickItem;
    const manager = makeConcurrentPipelineManager();
    registerSetConcurrentSlotsCommand(
      manager as any,
      null,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );

    await invokeCommand();

    expect(manager.setMaxConcurrentSlots).toHaveBeenCalledWith(1);
  });

  it("blocks selecting >1 slots on community tier and shows an upgrade prompt", async () => {
    quickPickResponse = { label: "5" } as QuickPickItem;
    mockShowInformationMessage.mockResolvedValue(undefined);
    const manager = makeConcurrentPipelineManager();
    registerSetConcurrentSlotsCommand(
      manager as any,
      null,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );

    await invokeCommand();

    expect(manager.setMaxConcurrentSlots).not.toHaveBeenCalled();
    expect(mockShowInformationMessage).toHaveBeenCalled();
  });

  it("opens the upgrade page when the user picks 'View Plans' after being blocked", async () => {
    quickPickResponse = { label: "3" } as QuickPickItem;
    mockShowInformationMessage.mockResolvedValue("View Plans");
    const manager = makeConcurrentPipelineManager();
    registerSetConcurrentSlotsCommand(
      manager as any,
      null,
      makeTierGate(false) as any,
      makeLicensePreflight("community") as any
    );

    await invokeCommand();

    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://nightgauge.dev/pricing" })
    );
  });

  it("allows selecting >1 slots on pro tier", async () => {
    quickPickResponse = { label: "3" } as QuickPickItem;
    const manager = makeConcurrentPipelineManager();
    registerSetConcurrentSlotsCommand(
      manager as any,
      null,
      makeTierGate(true) as any,
      makeLicensePreflight("pro") as any
    );

    await invokeCommand();

    expect(manager.setMaxConcurrentSlots).toHaveBeenCalledWith(3);
  });

  it("skips the gate entirely when tierGate/licensePreflight are not provided", async () => {
    quickPickResponse = { label: "3" } as QuickPickItem;
    const manager = makeConcurrentPipelineManager();
    registerSetConcurrentSlotsCommand(manager as any, null);

    await invokeCommand();

    expect(manager.setMaxConcurrentSlots).toHaveBeenCalledWith(3);
  });
});
