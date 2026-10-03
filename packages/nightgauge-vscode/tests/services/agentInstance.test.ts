/**
 * The window's platform agent instance id (#2395): one random UUID v4 per
 * activation, in memory only, carried by the registration, every heartbeat
 * and the deregistration, so the platform keeps one record per window.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  agentInstanceId,
  beginAgentInstance,
  isValidAgentInstanceId,
} from "../../src/services/agentInstance";
import { AgentRegistrationService } from "../../src/services/AgentRegistrationService";
import { AgentHeartbeatService } from "../../src/services/AgentHeartbeatService";
import { makeMockTokenStorage } from "../mocks/token-storage";
import { makeMockLogger } from "../mocks/logger";

vi.mock("vscode", () => ({}));

/**
 * A random UUID v4 (RFC 9562), checked field by field: lowercase hex in
 * 8-4-4-4-12 groups, version nibble 4, variant bits 10.
 */
function expectUuidV4(id: string): void {
  const groups = id.split("-");
  expect(groups.map((group) => group.length)).toEqual([8, 4, 4, 4, 12]);
  expect(groups.join("")).toMatch(/^[0-9a-f]{32}$/);
  expect(groups[2][0]).toBe("4");
  expect(["8", "9", "a", "b"]).toContain(groups[3][0]);
}
const PLATFORM_URL = "https://api.nightgauge.dev";

describe("agent instance id", () => {
  it("is a random UUID v4 within the platform's bound, stable within an activation", () => {
    beginAgentInstance();
    const id = agentInstanceId();
    expectUuidV4(id);
    expect(isValidAgentInstanceId(id)).toBe(true);
    expect(agentInstanceId()).toBe(id);
  });

  it("is new for every activation", () => {
    beginAgentInstance();
    const first = agentInstanceId();
    beginAgentInstance();
    expect(agentInstanceId()).not.toBe(first);
  });

  it("accepts only 1–64 of [A-Za-z0-9_-]", () => {
    for (const ok of ["a", "A_b-9", "x".repeat(64)]) expect(isValidAgentInstanceId(ok)).toBe(true);
    for (const bad of ["", "x".repeat(65), "has space", "/Users/someone", "a.b", "é"]) {
      expect(isValidAgentInstanceId(bad)).toBe(false);
    }
  });

  it("is started afresh at the top of activate(), before any service exists", () => {
    const source = readFileSync(path.resolve(__dirname, "../../src/extension.ts"), "utf-8");
    expect(source).toMatch(
      /export async function activate\([^)]*\)[^{]*\{[\s\S]*?beginAgentInstance\(\);[\s\S]*?initializeServices\(context\)/
    );
  });
});

describe("the window's requests carry its instance id", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        status: 201,
        json: vi.fn().mockResolvedValue({ agentId: "agent-1" }),
        text: vi.fn().mockResolvedValue(""),
      } as unknown as Response)
    );
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function build(): { registration: AgentRegistrationService; heartbeat: AgentHeartbeatService } {
    return {
      registration: new AgentRegistrationService(
        () => PLATFORM_URL,
        makeMockTokenStorage(),
        makeMockLogger()
      ),
      heartbeat: new AgentHeartbeatService(
        () => PLATFORM_URL,
        makeMockTokenStorage(),
        makeMockLogger()
      ),
    };
  }

  function requests(): Array<{ url: string; body: Record<string, unknown> | undefined }> {
    return vi.mocked(fetch).mock.calls.map(([url, init]) => ({
      url: String(url),
      body: init?.body === undefined ? undefined : JSON.parse(init.body as string),
    }));
  }

  it("on registration, on every beat and on deregistration, top-level and never in the profile", async () => {
    beginAgentInstance();
    const id = agentInstanceId();
    const { registration, heartbeat } = build();

    await registration.register({
      agent_version: "1.0.0",
      capabilities: [],
      repos: [],
      machine_id: "machine-1",
      vscode_version: "1.99.0",
      execution_profile: {
        adapter: "claude",
        adapter_display_name: "Claude",
        adapter_source: "config",
        performance_mode: "balanced",
        performance_mode_source: "default",
        effort: "",
        effort_source: "default",
      },
    });
    heartbeat.start("agent-1");
    await vi.advanceTimersByTimeAsync(60_000);
    heartbeat.dispose();
    await registration.deregister("agent-1");

    const [register, beat1, beat2, deregister] = requests();
    expect(register.body?.instance_id).toBe(id);
    expect(register.body?.execution_profile).not.toHaveProperty("instance_id");
    // A beat with no usage report and no profile still carries the id: a
    // reloaded window reuses its agent id and never registers.
    expect(beat1.body).toEqual({ instance_id: id });
    expect(beat2.body).toEqual({ instance_id: id });
    expect(deregister.url).toBe(`${PLATFORM_URL}/v1/agents/agent-1?instance_id=${id}`);
  });

  it("keeps a service's own id when a later activation starts a new instance", async () => {
    beginAgentInstance();
    const before = build();
    const firstId = agentInstanceId();
    beginAgentInstance();
    const after = build();

    await before.registration.deregister("agent-1");
    await after.registration.deregister("agent-1");

    const [old, current] = requests();
    expect(old.url).toBe(`${PLATFORM_URL}/v1/agents/agent-1?instance_id=${firstId}`);
    expect(current.url).toBe(`${PLATFORM_URL}/v1/agents/agent-1?instance_id=${agentInstanceId()}`);
    expect(agentInstanceId()).not.toBe(firstId);
  });
});
