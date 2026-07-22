/**
 * Integration tests for workspace sync payload delivery to the platform (#3668).
 *
 * Uses a mock fetch to capture the POST body sent to /v1/agents/register and
 * asserts the workspace block is present with the correct slug/display_name.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { AgentRegistrationService } from "../../src/services/AgentRegistrationService";
import { WorkspaceRegistrationPayloadBuilder } from "../../src/services/WorkspaceRegistrationPayloadBuilder";
import type { WorkspaceConfig } from "../../src/types/WorkspaceConfig";

vi.mock("vscode", () => ({
  Disposable: { from: vi.fn() },
}));

function makeTokenStorage(token: string | null) {
  return { retrieve: vi.fn().mockResolvedValue(token) };
}

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn() };
}

function makeWorkspaceConfig(name: string, repoCount = 2): WorkspaceConfig {
  return {
    workspace: { name },
    repositories: Array.from({ length: repoCount }, (_, i) => ({
      name: `repo-${i}`,
      path: `./repo-${i}`,
    })),
  };
}

describe("workspace sync payload → platform register endpoint (#3668)", () => {
  let mockFetch: ReturnType<typeof vi.fn>;
  const PLATFORM_URL = "https://api.nightgauge.dev";

  beforeEach(() => {
    mockFetch = vi.fn();
    global.fetch = mockFetch;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("sends workspace block with correct slug and display_name in register POST", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({ agentId: "agent-abc" }),
    });

    const service = new AgentRegistrationService(
      () => PLATFORM_URL,
      makeTokenStorage("access-token") as never,
      makeLogger() as never
    );

    const wsConfig = makeWorkspaceConfig("Acme Platform");
    const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(wsConfig);

    await service.register({
      agent_version: "0.1.42",
      capabilities: ["headless", "interactive"],
      repos: [
        { owner: "nightgauge", repo: "nightgauge" },
        { owner: "nightgauge", repo: "acme-platform" },
      ],
      machine_id: "machine-abc",
      vscode_version: "1.90.0",
      workspace: workspaceMeta,
    });

    expect(mockFetch).toHaveBeenCalledOnce();
    const [url, options] = mockFetch.mock.calls[0] as [string, RequestInit];
    expect(url).toBe(`${PLATFORM_URL}/v1/agents/register`);
    expect(options.method).toBe("POST");

    const body = JSON.parse(options.body as string) as Record<string, unknown>;
    expect(body["workspace"]).toEqual({
      slug: "acme-platform",
      display_name: "Acme Platform",
    });
    expect(body["repos"]).toEqual([
      { owner: "nightgauge", repo: "nightgauge" },
      { owner: "nightgauge", repo: "acme-platform" },
    ]);
  });

  it("omits workspace field from POST body when no workspace config (single-repo)", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({ agentId: "agent-solo" }),
    });

    const service = new AgentRegistrationService(
      () => PLATFORM_URL,
      makeTokenStorage("access-token") as never,
      makeLogger() as never
    );

    // No workspace config → workspaceMeta is undefined
    const workspaceMeta = WorkspaceRegistrationPayloadBuilder.build(null);
    expect(workspaceMeta).toBeUndefined();

    await service.register({
      agent_version: "0.1.42",
      capabilities: ["headless", "interactive"],
      repos: [{ owner: "nightgauge", repo: "solo-repo" }],
      machine_id: "machine-solo",
      vscode_version: "1.90.0",
      workspace: workspaceMeta,
    });

    const [, options] = mockFetch.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(options.body as string) as Record<string, unknown>;

    // undefined is omitted by JSON.stringify — platform sees legacy payload
    expect(body["workspace"]).toBeUndefined();
    expect(Object.keys(body)).not.toContain("workspace");
    expect(body["repos"]).toEqual([{ owner: "nightgauge", repo: "solo-repo" }]);
  });

  it("slug satisfies platform validator ^[a-z0-9-]{1,50}$ for common workspace names", () => {
    const names = [
      "My Workspace",
      "Nightgauge Platform",
      "acme / backend",
      "Company Name — 2025",
      "a".repeat(60), // over-length input
    ];
    const pattern = /^[a-z0-9-]{1,50}$/;
    for (const name of names) {
      const slug = WorkspaceRegistrationPayloadBuilder.toSlug(name);
      if (slug) {
        expect(pattern.test(slug), `slug for "${name}" → "${slug}" should match pattern`).toBe(
          true
        );
      }
    }
  });

  it("returns agentId when platform accepts workspace payload", async () => {
    mockFetch.mockResolvedValue({
      ok: true,
      status: 200,
      json: vi.fn().mockResolvedValue({ agentId: "new-agent-id" }),
    });

    const service = new AgentRegistrationService(
      () => PLATFORM_URL,
      makeTokenStorage("token") as never,
      makeLogger() as never
    );

    const result = await service.register({
      agent_version: "0.1.0",
      capabilities: [],
      repos: [{ owner: "nightgauge", repo: "test" }],
      machine_id: "m1",
      vscode_version: "1.90.0",
      workspace: { slug: "test-ws", display_name: "Test WS" },
    });

    expect(result).toBe("new-agent-id");
  });

  it("handles platform rejecting workspace payload gracefully (returns null, does not throw)", async () => {
    mockFetch.mockResolvedValue({
      ok: false,
      status: 400,
      json: vi.fn().mockResolvedValue({ error: "invalid workspace slug" }),
    });

    const service = new AgentRegistrationService(
      () => PLATFORM_URL,
      makeTokenStorage("token") as never,
      makeLogger() as never
    );

    const result = await service.register({
      agent_version: "0.1.0",
      capabilities: [],
      repos: [{ owner: "nightgauge", repo: "test" }],
      machine_id: "m1",
      vscode_version: "1.90.0",
      workspace: { slug: "test-ws", display_name: "Test WS" },
    });

    expect(result).toBeNull();
  });
});
