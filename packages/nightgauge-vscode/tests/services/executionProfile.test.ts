import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AgentHeartbeatService } from "../../src/services/AgentHeartbeatService";
import {
  agentCapabilities,
  resolveExecutionProfile,
  toWireProfile,
  type ExecutionProfile,
  type ExecutionProfileSource,
} from "../../src/services/executionProfile";
import { makeMockTokenStorage } from "../mocks/token-storage";
import { makeMockLogger } from "../mocks/logger";

vi.mock("vscode", () => ({}));

const PROFILE: ExecutionProfile = {
  adapter: "codex",
  adapter_display_name: "Codex",
  adapter_source: "config",
  performance_mode: "frontier",
  performance_mode_source: "file",
  effort: "high",
  effort_source: "config",
};

function source(answer: () => unknown): ExecutionProfileSource {
  return {
    agentExecutionProfile: vi.fn(
      async () => answer() as { profile: unknown; conversation: unknown }
    ),
  };
}

describe("toWireProfile", () => {
  it("copies exactly the allowlisted fields", () => {
    const wire = toWireProfile({ ...PROFILE, workspace_root: "/Users/someone/repo" });
    expect(wire).toEqual(PROFILE);
    expect(Object.keys(wire!)).not.toContain("workspace_root");
  });

  it("rejects a profile missing a field or carrying a non-string", () => {
    const { effort: _effort, ...missing } = PROFILE;
    expect(toWireProfile(missing)).toBeNull();
    expect(toWireProfile({ ...PROFILE, effort: 3 })).toBeNull();
    expect(toWireProfile(null)).toBeNull();
  });
});

describe("resolveExecutionProfile", () => {
  it("returns Go's answer", async () => {
    await expect(
      resolveExecutionProfile(source(() => ({ profile: PROFILE, conversation: false })))
    ).resolves.toEqual({ profile: PROFILE, conversation: false });
  });

  it("is null — advertise nothing — without a daemon or when it fails", async () => {
    await expect(resolveExecutionProfile(null)).resolves.toBeNull();
    await expect(
      resolveExecutionProfile(
        source(() => {
          throw new Error("no workspace root configured");
        })
      )
    ).resolves.toBeNull();
  });
});

describe("agentCapabilities", () => {
  it("advertises conversation only when Go says the adapter is viable", () => {
    expect(agentCapabilities(null)).toEqual(["headless", "interactive"]);
    expect(agentCapabilities({ profile: PROFILE, conversation: false })).toEqual([
      "headless",
      "interactive",
    ]);
    expect(agentCapabilities({ profile: PROFILE, conversation: true })).toEqual([
      "headless",
      "interactive",
      "conversation",
    ]);
  });
});

describe("AgentHeartbeatService execution profile (#1567)", () => {
  let service: AgentHeartbeatService;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, status: 200, json: vi.fn() } as unknown as Response)
    );
  });

  afterEach(() => {
    service.dispose();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function bodies(): Array<Record<string, unknown> | undefined> {
    return vi
      .mocked(fetch)
      .mock.calls.map(([, init]) =>
        init?.body === undefined ? undefined : JSON.parse(init.body as string)
      );
  }

  it("re-resolves the profile on every beat, so a changed adapter lands within one interval", async () => {
    // Wired exactly as bootstrap/services.ts wires it: Go is asked every beat.
    let adapter = "codex";
    const ipc = source(() => ({ profile: { ...PROFILE, adapter }, conversation: false }));
    service = new AgentHeartbeatService(
      () => "https://api.nightgauge.dev",
      makeMockTokenStorage(),
      makeMockLogger(),
      undefined,
      undefined,
      async () => (await resolveExecutionProfile(ipc))?.profile ?? null
    );
    service.start("agent-1");

    await vi.advanceTimersByTimeAsync(30_000);
    adapter = "opencode";
    await vi.advanceTimersByTimeAsync(30_000);

    expect(bodies().map((b) => (b?.execution_profile as ExecutionProfile).adapter)).toEqual([
      "codex",
      "opencode",
    ]);
  });

  it("stays bodiless with no profile and no usage, and survives a failing provider", async () => {
    service = new AgentHeartbeatService(
      () => "https://api.nightgauge.dev",
      makeMockTokenStorage(),
      makeMockLogger(),
      undefined,
      undefined,
      async () => {
        throw new Error("daemon gone");
      }
    );
    service.start("agent-1");
    await vi.advanceTimersByTimeAsync(30_000);

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(bodies()).toEqual([undefined]);
  });
});
