/**
 * Tests for CodexMcpProvisioner — file I/O, adapter guard, idempotency, Codex
 * home resolution, and collision-safe persistence into config.toml.
 *
 * @see Issue #4025 - Codex MCP provisioning via ~/.codex/config.toml
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import {
  CodexMcpProvisioner,
  resolveCodexHome,
  type CodexMcpOptions,
} from "../CodexMcpProvisioner.js";
import { CODEX_MCP_MANAGED_BEGIN } from "../codexMcpConfig.js";

describe("CodexMcpProvisioner (#4025)", () => {
  let workspaceRoot: string;
  let codexHome: string;
  let provisioner: CodexMcpProvisioner;

  beforeEach(() => {
    provisioner = new CodexMcpProvisioner();
    workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "mcp-ws-"));
    codexHome = fs.mkdtempSync(path.join(os.tmpdir(), "codex-home-"));
  });

  afterEach(() => {
    fs.rmSync(workspaceRoot, { recursive: true, force: true });
    fs.rmSync(codexHome, { recursive: true, force: true });
  });

  const configPath = () => path.join(codexHome, "config.toml");
  const read = () => fs.readFileSync(configPath(), "utf-8");

  function opts(overrides?: Partial<CodexMcpOptions>): CodexMcpOptions {
    return { workspaceRoot, adapter: "codex", codexHome, ...overrides };
  }

  function writeMcpJson(servers: Record<string, unknown>): void {
    fs.writeFileSync(
      path.join(workspaceRoot, ".mcp.json"),
      JSON.stringify({ mcpServers: servers })
    );
  }

  describe("adapter guard", () => {
    it("returns null and writes nothing for a non-Codex adapter", async () => {
      writeMcpJson({ github: { type: "http", url: "https://g" } });
      expect(await provisioner.provision(opts({ adapter: "gemini" }))).toBeNull();
      expect(provisioner.provisionSync(opts({ adapter: "claude-sdk" }))).toBeNull();
      expect(fs.existsSync(configPath())).toBe(false);
    });

    it("returns null when disabled via config", async () => {
      writeMcpJson({ github: { url: "https://g" } });
      expect(await provisioner.provision(opts(), { enabled: false })).toBeNull();
      expect(fs.existsSync(configPath())).toBe(false);
    });

    it("returns null when disabled via NIGHTGAUGE_CODEX_MCP_DISABLED env", async () => {
      const original = process.env.NIGHTGAUGE_CODEX_MCP_DISABLED;
      process.env.NIGHTGAUGE_CODEX_MCP_DISABLED = "true";
      try {
        writeMcpJson({ github: { url: "https://g" } });
        expect(await provisioner.provision(opts())).toBeNull();
        expect(provisioner.provisionSync(opts())).toBeNull();
        expect(fs.existsSync(configPath())).toBe(false);
      } finally {
        if (original === undefined) delete process.env.NIGHTGAUGE_CODEX_MCP_DISABLED;
        else process.env.NIGHTGAUGE_CODEX_MCP_DISABLED = original;
      }
    });
  });

  describe("provisioning", () => {
    it("writes a config.toml with the [mcp_servers.*] table for a Codex stage", async () => {
      writeMcpJson({ github: { type: "http", url: "https://api.githubcopilot.com/mcp/" } });
      const result = await provisioner.provision(opts());
      expect(result).not.toBeNull();
      expect(result?.provisioned).toEqual(["github"]);
      expect(result?.changed).toBe(true);
      const text = read();
      expect(text).toContain(CODEX_MCP_MANAGED_BEGIN);
      expect(text).toContain("[mcp_servers.github]");
      expect(text).toContain('url = "https://api.githubcopilot.com/mcp/"');
    });

    it("creates the Codex home directory if it does not exist", async () => {
      const nestedHome = path.join(codexHome, "nested", ".codex");
      writeMcpJson({ github: { url: "https://g" } });
      const result = await provisioner.provision(opts({ codexHome: nestedHome }));
      expect(result?.changed).toBe(true);
      expect(fs.existsSync(path.join(nestedHome, "config.toml"))).toBe(true);
    });

    it("is idempotent — a second run reports changed:false and identical bytes", async () => {
      writeMcpJson({ github: { url: "https://g" } });
      await provisioner.provision(opts());
      const firstBytes = read();
      const second = await provisioner.provision(opts());
      expect(second?.changed).toBe(false);
      expect(read()).toBe(firstBytes);
    });

    it("preserves pre-existing user [mcp_servers.*] entries and other config", async () => {
      fs.writeFileSync(
        configPath(),
        ['model = "gpt-5.5"', "", "[mcp_servers.local]", 'command = "./srv"', ""].join("\n")
      );
      writeMcpJson({ github: { url: "https://g" } });
      await provisioner.provision(opts());
      const text = read();
      expect(text).toContain('model = "gpt-5.5"');
      expect(text).toContain("[mcp_servers.local]");
      expect(text).toContain("[mcp_servers.github]");
    });

    it("skips a server the user already defined (user wins) and reports the collision", async () => {
      fs.writeFileSync(configPath(), '[mcp_servers.github]\nurl = "https://user-override"\n');
      writeMcpJson({ github: { url: "https://pipeline" } });
      const result = await provisioner.provision(opts());
      expect(result?.provisioned).toEqual([]);
      expect(result?.skippedCollisions).toEqual(["github"]);
      expect(read()).toContain("https://user-override");
      expect(read()).not.toContain("https://pipeline");
    });

    it("provisionSync mirrors provision()", () => {
      writeMcpJson({ github: { url: "https://g" } });
      const result = provisioner.provisionSync(opts());
      expect(result?.provisioned).toEqual(["github"]);
      expect(read()).toContain("[mcp_servers.github]");
    });

    it("no-ops cleanly when the workspace has no MCP config at all", async () => {
      const result = await provisioner.provision(opts());
      expect(result?.provisioned).toEqual([]);
      // nothing to write → no file created
      expect(fs.existsSync(configPath())).toBe(false);
      expect(result?.changed).toBe(false);
    });

    it("removes a previously-provisioned server when it leaves the pipeline config", async () => {
      writeMcpJson({ github: { url: "https://g" } });
      await provisioner.provision(opts());
      expect(read()).toContain("[mcp_servers.github]");
      // server removed from .mcp.json
      writeMcpJson({});
      await provisioner.provision(opts());
      expect(read()).not.toContain("[mcp_servers.github]");
    });

    it("accepts pre-resolved servers (bypassing .mcp.json reads)", async () => {
      const result = await provisioner.provision(
        opts({ servers: { custom: { command: "node", args: ["srv.js"] } } })
      );
      expect(result?.provisioned).toEqual(["custom"]);
      const text = read();
      expect(text).toContain("[mcp_servers.custom]");
      expect(text).toContain('args = ["srv.js"]');
    });
  });

  describe("resolveCodexHome", () => {
    const ORIGINAL = process.env.CODEX_HOME;
    afterEach(() => {
      if (ORIGINAL === undefined) delete process.env.CODEX_HOME;
      else process.env.CODEX_HOME = ORIGINAL;
    });

    it("honors an explicit override first", () => {
      expect(resolveCodexHome("/explicit")).toBe("/explicit");
    });

    it("falls back to $CODEX_HOME", () => {
      delete process.env.CODEX_HOME;
      process.env.CODEX_HOME = "/from-env/.codex";
      expect(resolveCodexHome()).toBe("/from-env/.codex");
    });

    it("falls back to ~/.codex when nothing is set", () => {
      delete process.env.CODEX_HOME;
      expect(resolveCodexHome()).toBe(path.join(os.homedir(), ".codex"));
    });
  });
});
