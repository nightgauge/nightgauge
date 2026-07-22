/**
 * Tests for the showMachineBinding command (Issue #4156).
 *
 * Surfaces machineBound/machineCount read-only — no list/unbind API exists
 * on the platform yet, so this must not fabricate one.
 *
 * @see packages/nightgauge-vscode/src/commands/showMachineBinding.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const mockShowInformationMessage = vi.fn();
const mockShowWarningMessage = vi.fn();
const mockOpenExternal = vi.fn();
const mockRegisterCommand = vi.fn();

vi.mock("vscode", () => ({
  window: {
    showInformationMessage: (...args: unknown[]) => mockShowInformationMessage(...args),
    showWarningMessage: (...args: unknown[]) => mockShowWarningMessage(...args),
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

import { registerShowMachineBindingCommand } from "../../src/commands/showMachineBinding";

async function invokeCommand() {
  const call = mockRegisterCommand.mock.calls.find(
    ([id]) => id === "nightgauge.showMachineBinding"
  );
  await call![1]();
}

describe("registerShowMachineBindingCommand", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows an info message when licensePreflight is null (backend not connected)", async () => {
    registerShowMachineBindingCommand(null);
    await invokeCommand();
    expect(mockShowInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("not available")
    );
  });

  it("shows a warning when validate() throws", async () => {
    const licensePreflight = { validate: vi.fn().mockRejectedValue(new Error("boom")) };
    registerShowMachineBindingCommand(licensePreflight as any);
    await invokeCommand();
    expect(mockShowWarningMessage).toHaveBeenCalled();
  });

  it("shows a community-tier message when tier is community", async () => {
    const licensePreflight = {
      validate: vi
        .fn()
        .mockResolvedValue({ tier: "community", machineBound: false, machineCount: 0 }),
    };
    registerShowMachineBindingCommand(licensePreflight as any);
    await invokeCommand();
    expect(mockShowInformationMessage).toHaveBeenCalledWith(expect.stringContaining("Community"));
  });

  it("reports machineBound=true and the total count for a paid tier", async () => {
    mockShowInformationMessage.mockResolvedValue(undefined);
    const licensePreflight = {
      validate: vi.fn().mockResolvedValue({ tier: "pro", machineBound: true, machineCount: 2 }),
    };
    registerShowMachineBindingCommand(licensePreflight as any);
    await invokeCommand();

    const message = mockShowInformationMessage.mock.calls[0]?.[0] as string;
    expect(message).toContain("This machine is bound");
    expect(message).toContain("2 machines are bound");
  });

  it("reports machineBound=false for a paid tier where this machine isn't bound", async () => {
    mockShowInformationMessage.mockResolvedValue(undefined);
    const licensePreflight = {
      validate: vi.fn().mockResolvedValue({ tier: "pro", machineBound: false, machineCount: 1 }),
    };
    registerShowMachineBindingCommand(licensePreflight as any);
    await invokeCommand();

    const message = mockShowInformationMessage.mock.calls[0]?.[0] as string;
    expect(message).toContain("not currently bound");
    expect(message).toContain("1 machine is bound");
  });

  it("does not fabricate a list/unbind capability — notes the limitation", async () => {
    mockShowInformationMessage.mockResolvedValue(undefined);
    const licensePreflight = {
      validate: vi.fn().mockResolvedValue({ tier: "pro", machineBound: true, machineCount: 1 }),
    };
    registerShowMachineBindingCommand(licensePreflight as any);
    await invokeCommand();

    const message = mockShowInformationMessage.mock.calls[0]?.[0] as string;
    expect(message).toContain("isn't available in this version");
  });

  it('opens the support URL when the user picks "Contact Support"', async () => {
    mockShowInformationMessage.mockResolvedValue("Contact Support");
    const licensePreflight = {
      validate: vi.fn().mockResolvedValue({ tier: "pro", machineBound: true, machineCount: 1 }),
    };
    registerShowMachineBindingCommand(licensePreflight as any);
    await invokeCommand();

    expect(mockOpenExternal).toHaveBeenCalledWith(
      expect.objectContaining({ _url: "https://github.com/nightgauge/nightgauge/issues" })
    );
  });
});
