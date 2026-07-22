/**
 * Tests for PipelineTreeProvider subscription section wiring (Issue #4156).
 *
 * SubscriptionSectionTreeItem was a fully-built sidebar tree item that no
 * tree data provider ever instantiated. setLicensePreflight() wires it in,
 * mirroring the existing setTeamMembers() pattern.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import type { SessionManager, SessionStateEvent } from "../../src/platform/SessionManager";
import type { LicensePreflight } from "../../src/platform/LicensePreflight";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

function makeSessionManager(): {
  manager: SessionManager;
  fire: (evt: SessionStateEvent) => void;
} {
  let handler: ((evt: SessionStateEvent) => void) | undefined;
  const manager = {
    onSessionChanged: vi.fn((cb: (evt: SessionStateEvent) => void) => {
      handler = cb;
      return { dispose: vi.fn() };
    }),
  } as unknown as SessionManager;
  return {
    manager,
    fire: (evt) => handler?.(evt),
  };
}

function makeSessionEvent(overrides: Partial<SessionStateEvent> = {}): SessionStateEvent {
  return {
    previous: "unauthenticated",
    current: "authenticated",
    data: { accessToken: null, expiresAt: null, userEmail: null, userTier: "pro", userRole: null },
    reason: "test",
    ...overrides,
  } as SessionStateEvent;
}

describe("PipelineTreeProvider — subscription section (#4156)", () => {
  let provider: PipelineTreeProvider | null = null;

  afterEach(() => {
    provider?.dispose();
    provider = null;
  });

  it("updates the subscription section with the mapped LicensePreflightResult on authentication", async () => {
    provider = new PipelineTreeProvider();
    const { manager, fire } = makeSessionManager();
    const validate = vi.fn().mockResolvedValue({
      allowed: true,
      tier: "pro",
      status: "active",
      expiresAt: "2027-01-01T00:00:00Z",
      offline: false,
      machineBound: true,
      machineCount: 2,
      features: {},
      cacheUntil: "2099-01-01T00:00:00Z",
    });
    const licensePreflight = { validate } as unknown as LicensePreflight;

    provider.setLicensePreflight(manager, licensePreflight);
    fire(makeSessionEvent());
    // onSessionChanged handler is async — flush microtasks.
    await new Promise((resolve) => setTimeout(resolve, 0));

    const subscriptionSection = (provider as any).subscriptionSection;
    expect(
      subscriptionSection.getChildren().some((c: any) => c.contextValue === "subscription-plan")
    ).toBe(true);
    const machineItem = subscriptionSection
      .getChildren()
      .find((c: any) => c.contextValue === "subscription-machine-binding");
    expect(machineItem?.label).toBe("2 machines bound");
  });

  it("resets the subscription section to null when signed out", async () => {
    provider = new PipelineTreeProvider();
    const { manager, fire } = makeSessionManager();
    const licensePreflight = { validate: vi.fn() } as unknown as LicensePreflight;

    provider.setLicensePreflight(manager, licensePreflight);
    fire(makeSessionEvent({ current: "unauthenticated", previous: "authenticated" }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    const subscriptionSection = (provider as any).subscriptionSection;
    expect(
      subscriptionSection
        .getChildren()
        .some((c: any) => c.contextValue === "subscription-signed-out")
    ).toBe(true);
  });

  it("keeps stale data when validate() throws (network failure)", async () => {
    provider = new PipelineTreeProvider();
    const { manager, fire } = makeSessionManager();
    const validate = vi.fn().mockRejectedValue(new Error("network down"));
    const licensePreflight = { validate } as unknown as LicensePreflight;

    provider.setLicensePreflight(manager, licensePreflight);
    fire(makeSessionEvent());
    await new Promise((resolve) => setTimeout(resolve, 0));

    // Should not throw, and section falls back to its "no data" state since
    // update() was never called successfully.
    const subscriptionSection = (provider as any).subscriptionSection;
    expect(
      subscriptionSection
        .getChildren()
        .some((c: any) => c.contextValue === "subscription-signed-out")
    ).toBe(true);
  });

  it("hides the subscription section by default (cloud off)", () => {
    provider = new PipelineTreeProvider();
    // Cloud features are not offered yet — with the master switch off (the
    // default), the subscription section is not rendered in the sidebar.
    const rootChildren = (provider as any).getRootChildren();
    expect(rootChildren).not.toContain((provider as any).subscriptionSection);
  });

  it("includes the subscription section in root children when cloud is enabled", () => {
    provider = new PipelineTreeProvider();
    provider.setCloudEnabled(true);
    // getChildren() with no element returns root children (async in the
    // provider's public API, but getRootChildren is synchronous internally).
    const rootChildren = (provider as any).getRootChildren();
    expect(rootChildren).toContain((provider as any).subscriptionSection);
  });
});
