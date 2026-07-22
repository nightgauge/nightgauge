/**
 * Tests for codexMcpConfig — pure .mcp.json → Codex [mcp_servers.*] translation,
 * TOML emission, managed-block merge, and collision detection.
 *
 * @see Issue #4025 - Codex MCP provisioning via ~/.codex/config.toml
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";
import {
  readPipelineMcpServers,
  toCodexMcpServer,
  buildManagedMcpBlockInner,
  findUserDefinedServerNames,
  upsertManagedMcpBlock,
  stripManagedMcpBlock,
  hasManagedMcpBlock,
  computeNextCodexConfig,
  CODEX_MCP_MANAGED_BEGIN,
  CODEX_MCP_MANAGED_END,
  type CodexMcpServer,
} from "../codexMcpConfig.js";

describe("codexMcpConfig (#4025)", () => {
  describe("toCodexMcpServer — translation", () => {
    it("maps an http server (type:http + url)", () => {
      const out = toCodexMcpServer({ type: "http", url: "https://api.example.com/mcp/" });
      expect(out).toEqual({ url: "https://api.example.com/mcp/" });
    });

    it("infers http from a bare url (no type)", () => {
      const out = toCodexMcpServer({ url: "https://x/mcp" });
      expect(out).toEqual({ url: "https://x/mcp" });
    });

    it("treats sse like http", () => {
      const out = toCodexMcpServer({ type: "sse", url: "https://x/sse" });
      expect(out?.url).toBe("https://x/sse");
    });

    it("maps Authorization: Bearer ${VAR} → bearer_token_env_var and drops the header", () => {
      const out = toCodexMcpServer({
        url: "https://x/mcp",
        headers: { Authorization: "Bearer ${GH_TOKEN}", "X-Trace": "on" },
      });
      expect(out).toEqual({
        url: "https://x/mcp",
        bearer_token_env_var: "GH_TOKEN",
        http_headers: { "X-Trace": "on" },
      });
    });

    it("keeps a non-bearer Authorization header verbatim in http_headers", () => {
      const out = toCodexMcpServer({
        url: "https://x/mcp",
        headers: { Authorization: "Basic abc123" },
      });
      expect(out).toEqual({
        url: "https://x/mcp",
        http_headers: { Authorization: "Basic abc123" },
      });
    });

    it("maps a stdio server (command/args/env/cwd)", () => {
      const out = toCodexMcpServer({
        command: "npx",
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"],
        env: { FOO: "bar" },
        cwd: "/work",
      });
      expect(out).toEqual({
        command: "npx",
        args: ["-y", "@modelcontextprotocol/server-filesystem", "/data"],
        env: { FOO: "bar" },
        cwd: "/work",
      });
    });

    it("returns null when neither url nor command is present", () => {
      expect(toCodexMcpServer({})).toBeNull();
      expect(toCodexMcpServer({ type: "http" })).toBeNull();
    });

    it("prefers stdio when command is present and url is absent", () => {
      const out = toCodexMcpServer({ command: "./srv" });
      expect(out).toEqual({ command: "./srv" });
    });

    it("treats an explicit type:null like an absent type → http (#4041 parity)", () => {
      // `.mcp.json` can carry `"type": null`; it means "unspecified", so a server
      // with a url is HTTP even when a command is also present — matching the Go
      // provisioner, which cannot distinguish null from an absent field.
      const out = toCodexMcpServer({
        url: "https://x/mcp",
        command: "ignored-because-http",
        type: null,
      } as unknown as Parameters<typeof toCodexMcpServer>[0]);
      expect(out).toEqual({ url: "https://x/mcp" });
    });

    it("coerces non-string http header values to strings (#4041 parity)", () => {
      // A malformed header value (JSON number/boolean) is coerced — not dropped —
      // so the Go and TS paths emit identical http_headers.
      const out = toCodexMcpServer({
        url: "https://x/mcp",
        headers: { "X-Version": 2, "X-Enabled": true, "X-Gone": null },
      } as unknown as Parameters<typeof toCodexMcpServer>[0]);
      expect(out).toEqual({
        url: "https://x/mcp",
        http_headers: { "X-Version": "2", "X-Enabled": "true" },
      });
    });
  });

  describe("buildManagedMcpBlockInner — TOML emission", () => {
    it("emits a deterministic, sorted [mcp_servers.*] table set", () => {
      const servers: Record<string, CodexMcpServer> = {
        zeta: { command: "z" },
        github: { url: "https://api.githubcopilot.com/mcp/" },
      };
      const inner = buildManagedMcpBlockInner(servers);
      // github sorts before zeta
      expect(inner.indexOf("[mcp_servers.github]")).toBeLessThan(
        inner.indexOf("[mcp_servers.zeta]")
      );
      expect(inner).toContain('url = "https://api.githubcopilot.com/mcp/"');
      expect(inner).toContain("[mcp_servers.zeta]");
      expect(inner).toContain('command = "z"');
    });

    it("emits args as a TOML string array and env/headers as inline tables", () => {
      const inner = buildManagedMcpBlockInner({
        fs: { command: "npx", args: ["-y", "server"], env: { A: "1", B: "2" } },
        web: { url: "https://w", bearer_token_env_var: "TOK", http_headers: { "X-Y": "z" } },
      });
      expect(inner).toContain('args = ["-y", "server"]');
      expect(inner).toContain('env = { A = "1", B = "2" }');
      expect(inner).toContain('bearer_token_env_var = "TOK"');
      // `X-Y` is a valid TOML bare key (hyphens allowed) → emitted unquoted.
      expect(inner).toContain('http_headers = { X-Y = "z" }');
    });

    it("sorts inline-table keys deterministically, ignoring insertion order (#4041 parity)", () => {
      // Keys are given out of alphabetical order; emission sorts them so the
      // bytes are independent of JSON key order (matches the sorted Go output and
      // keeps the idempotency check stable across execution paths).
      const inner = buildManagedMcpBlockInner({
        srv: { command: "c", env: { ZZ_VAR: "1", AA_VAR: "2", MM_VAR: "3" } },
      });
      expect(inner).toContain('env = { AA_VAR = "2", MM_VAR = "3", ZZ_VAR = "1" }');
    });

    it("quotes header keys that are not valid TOML bare keys", () => {
      const inner = buildManagedMcpBlockInner({
        web: { url: "https://w", http_headers: { "X Trace": "on" } },
      });
      expect(inner).toContain('http_headers = { "X Trace" = "on" }');
    });

    it("escapes special characters in string values", () => {
      const inner = buildManagedMcpBlockInner({ s: { command: 'a"b\\c' } });
      expect(inner).toContain('command = "a\\"b\\\\c"');
    });

    it("returns empty string when there are no servers", () => {
      expect(buildManagedMcpBlockInner({})).toBe("");
    });

    it("is byte-identical across regeneration (idempotent)", () => {
      const servers = { a: { url: "https://a" }, b: { command: "b" } };
      expect(buildManagedMcpBlockInner(servers)).toBe(buildManagedMcpBlockInner(servers));
    });
  });

  describe("findUserDefinedServerNames — collision scan", () => {
    it("finds bare and quoted [mcp_servers.X] outside the managed block", () => {
      const text = [
        "[mcp_servers.local]",
        'command = "x"',
        '[mcp_servers."my server"]',
        'command = "y"',
      ].join("\n");
      const names = findUserDefinedServerNames(text);
      expect(names.has("local")).toBe(true);
      expect(names.has("my server")).toBe(true);
    });

    it("ignores server tables INSIDE the managed block", () => {
      const text = [
        CODEX_MCP_MANAGED_BEGIN,
        "[mcp_servers.github]",
        'url = "https://g"',
        CODEX_MCP_MANAGED_END,
      ].join("\n");
      expect(findUserDefinedServerNames(text).size).toBe(0);
    });

    it("ignores commented-out table headers", () => {
      const text = '# [mcp_servers.disabled]\n[mcp_servers.active]\ncommand = "a"';
      const names = findUserDefinedServerNames(text);
      expect(names.has("active")).toBe(true);
      expect(names.has("disabled")).toBe(false);
    });
  });

  describe("upsert / strip managed block", () => {
    it("creates the block as the whole file when none existed", () => {
      const next = upsertManagedMcpBlock(null, '[mcp_servers.x]\ncommand = "x"');
      expect(hasManagedMcpBlock(next)).toBe(true);
      expect(next.startsWith(CODEX_MCP_MANAGED_BEGIN)).toBe(true);
      expect(next.trimEnd().endsWith(CODEX_MCP_MANAGED_END)).toBe(true);
    });

    it("appends the block below pre-existing user content", () => {
      const existing = '[mcp_servers.local]\ncommand = "x"\n';
      const next = upsertManagedMcpBlock(existing, '[mcp_servers.github]\nurl = "https://g"');
      expect(next).toContain("[mcp_servers.local]");
      expect(next.indexOf("[mcp_servers.local]")).toBeLessThan(
        next.indexOf(CODEX_MCP_MANAGED_BEGIN)
      );
    });

    it("replaces an existing block in place, preserving surrounding user content", () => {
      const existing = [
        "# top user setting",
        'model = "gpt-5.5"',
        "",
        CODEX_MCP_MANAGED_BEGIN,
        "[mcp_servers.old]",
        'url = "https://old"',
        CODEX_MCP_MANAGED_END,
        "",
        "[mcp_servers.local]",
        'command = "z"',
      ].join("\n");
      const next = upsertManagedMcpBlock(existing, '[mcp_servers.new]\nurl = "https://new"');
      expect(next).toContain('model = "gpt-5.5"');
      expect(next).toContain("[mcp_servers.local]");
      expect(next).toContain("[mcp_servers.new]");
      expect(next).not.toContain("[mcp_servers.old]");
      // exactly one managed block
      expect(next.split(CODEX_MCP_MANAGED_BEGIN).length - 1).toBe(1);
    });

    it("removes the block when the inner content is empty", () => {
      const existing = [
        "[mcp_servers.local]",
        'command = "z"',
        "",
        CODEX_MCP_MANAGED_BEGIN,
        "[mcp_servers.github]",
        CODEX_MCP_MANAGED_END,
      ].join("\n");
      const next = upsertManagedMcpBlock(existing, "");
      expect(hasManagedMcpBlock(next)).toBe(false);
      expect(next).toContain("[mcp_servers.local]");
    });

    it("strip returns empty string when the block was the only content", () => {
      const existing = `${CODEX_MCP_MANAGED_BEGIN}\n[mcp_servers.g]\nurl = "x"\n${CODEX_MCP_MANAGED_END}\n`;
      expect(stripManagedMcpBlock(existing)).toBe("");
    });

    it("upsert is idempotent (second pass is byte-identical)", () => {
      const inner = '[mcp_servers.github]\nurl = "https://g"';
      const first = upsertManagedMcpBlock(null, inner);
      const second = upsertManagedMcpBlock(first, inner);
      expect(second).toBe(first);
    });
  });

  describe("computeNextCodexConfig — end-to-end pure transform", () => {
    it("provisions pipeline servers into a fresh config", () => {
      const { next, provisioned, skippedCollisions } = computeNextCodexConfig(null, {
        github: { type: "http", url: "https://api.githubcopilot.com/mcp/" },
      });
      expect(provisioned).toEqual(["github"]);
      expect(skippedCollisions).toEqual([]);
      expect(next).toContain("[mcp_servers.github]");
      expect(next).toContain('url = "https://api.githubcopilot.com/mcp/"');
    });

    it("skips a server the user already defined outside the block (user wins)", () => {
      const existing = '[mcp_servers.github]\nurl = "https://user-override"\n';
      const { next, provisioned, skippedCollisions } = computeNextCodexConfig(existing, {
        github: { type: "http", url: "https://api.githubcopilot.com/mcp/" },
      });
      expect(provisioned).toEqual([]);
      expect(skippedCollisions).toEqual(["github"]);
      // user's override preserved; no managed block added (nothing to provision)
      expect(next).toContain("https://user-override");
      expect(next).not.toContain("https://api.githubcopilot.com/mcp/");
    });

    it("provisions non-colliding servers while skipping colliding ones", () => {
      const existing = '[mcp_servers.github]\nurl = "https://user"\n';
      const { provisioned, skippedCollisions } = computeNextCodexConfig(existing, {
        github: { url: "https://pipeline" },
        extra: { command: "e" },
      });
      expect(provisioned).toEqual(["extra"]);
      expect(skippedCollisions).toEqual(["github"]);
    });

    it("removes a previously-provisioned server when it leaves the pipeline config", () => {
      const seeded = computeNextCodexConfig(null, { github: { url: "https://g" } }).next;
      const { next } = computeNextCodexConfig(seeded, {});
      expect(next).not.toContain("[mcp_servers.github]");
      expect(hasManagedMcpBlock(next)).toBe(false);
    });
  });

  describe("readPipelineMcpServers — Claude-native sources", () => {
    let tmpDir: string;
    beforeEach(() => {
      tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "mcp-src-"));
    });
    afterEach(() => {
      fs.rmSync(tmpDir, { recursive: true, force: true });
    });

    it("reads servers from .mcp.json", () => {
      fs.writeFileSync(
        path.join(tmpDir, ".mcp.json"),
        JSON.stringify({ mcpServers: { github: { type: "http", url: "https://g" } } })
      );
      const servers = readPipelineMcpServers(tmpDir);
      expect(servers.github?.url).toBe("https://g");
    });

    it("merges .claude/settings.json with .mcp.json winning on a name clash", () => {
      fs.mkdirSync(path.join(tmpDir, ".claude"));
      fs.writeFileSync(
        path.join(tmpDir, ".claude", "settings.json"),
        JSON.stringify({
          mcpServers: { github: { url: "https://from-settings" }, extra: { command: "e" } },
        })
      );
      fs.writeFileSync(
        path.join(tmpDir, ".mcp.json"),
        JSON.stringify({ mcpServers: { github: { url: "https://from-mcpjson" } } })
      );
      const servers = readPipelineMcpServers(tmpDir);
      expect(servers.github?.url).toBe("https://from-mcpjson");
      expect(servers.extra?.command).toBe("e");
    });

    it("returns {} when no config files exist", () => {
      expect(readPipelineMcpServers(tmpDir)).toEqual({});
    });

    it("tolerates malformed JSON", () => {
      fs.writeFileSync(path.join(tmpDir, ".mcp.json"), "{ not json");
      expect(readPipelineMcpServers(tmpDir)).toEqual({});
    });
  });

  // Regression coverage for the adversarial-review findings (#4025).
  describe("review hardening", () => {
    // Intentionally matches raw control chars — the point is to assert the
    // emitter never lets one through into the TOML output.
    // eslint-disable-next-line no-control-regex
    const RAW_CONTROL = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/;

    it("(#1) escapes ALL control chars so the emitted TOML has no raw control bytes", () => {
      const ctrl = (n: number) => String.fromCharCode(n);
      // \b=0x08 and \f=0x0c get named escapes; NUL/unit-sep/DEL get \uXXXX.
      const inner = buildManagedMcpBlockInner({
        s: {
          command:
            "a" +
            ctrl(0x08) +
            "b" +
            ctrl(0x0c) +
            "c" +
            ctrl(0x00) +
            "d" +
            ctrl(0x1f) +
            "e" +
            ctrl(0x7f) +
            "f",
          args: ["x" + ctrl(0x01) + "y"],
          env: { K: "v" + ctrl(0x1e) + "w" },
        } as never,
      });
      expect(RAW_CONTROL.test(inner)).toBe(false);
      expect(inner).toContain("\\b");
      expect(inner).toContain("\\f");
      expect(inner).toContain("\\u0000");
      expect(inner).toContain("\\u001F");
      expect(inner).toContain("\\u007F");
      // tab/newline keep their named escapes, never raw
      const inner2 = buildManagedMcpBlockInner({
        s: { command: "a" + ctrl(0x09) + "b" + ctrl(0x0a) + "c" },
      });
      expect(RAW_CONTROL.test(inner2)).toBe(false);
      expect(inner2).toContain("\\t");
      expect(inner2).toContain("\\n");
    });

    it("(#2/#8) coerces non-string env values instead of crashing the emitter", () => {
      const normalized = toCodexMcpServer({
        command: "node",
        env: { PORT: 8080, FLAG: true, NIL: null } as never,
      });
      expect(normalized?.env).toEqual({ PORT: "8080", FLAG: "true" });
      // and the full emit chain does not throw
      const { next } = computeNextCodexConfig(null, {
        srv: { command: "node", env: { PORT: 8080 } as never },
      });
      expect(next).toContain('env = { PORT = "8080" }');
    });

    it("(#3) detects a user table whose quoted name contains an escaped quote", () => {
      const existing = '[mcp_servers."a\\"b"]\ncommand = "x"\n';
      const names = findUserDefinedServerNames(existing);
      expect(names.has('a"b')).toBe(true);
      // → a pipeline server literally named a"b is treated as a collision
      const { provisioned, skippedCollisions } = computeNextCodexConfig(existing, {
        'a"b': { url: "https://pipeline" },
      });
      expect(provisioned).toEqual([]);
      expect(skippedCollisions).toEqual(['a"b']);
    });

    it("(#6) detects the dotted-key inline form mcp_servers.X = { ... }", () => {
      const existing = 'mcp_servers.foo = { command = "x" }\n';
      expect(findUserDefinedServerNames(existing).has("foo")).toBe(true);
      const { skippedCollisions } = computeNextCodexConfig(existing, {
        foo: { url: "https://pipeline" },
      });
      expect(skippedCollisions).toEqual(["foo"]);
    });

    it("(#7) detects bracketed headers with inner whitespace [ mcp_servers.x ]", () => {
      const existing = '[ mcp_servers.spaced ]\ncommand = "x"\n';
      expect(findUserDefinedServerNames(existing).has("spaced")).toBe(true);
    });

    it("(#4) marker text inside a user string value does NOT destroy user content", () => {
      // The marker appears INSIDE a quoted value, not at line start.
      const existing = [
        "[mcp_servers.note]",
        'description = "see # >>> BEGIN NIGHTGAUGE MANAGED MCP >>> in docs"',
        'command = "x"',
      ].join("\n");
      const next = upsertManagedMcpBlock(existing, '[mcp_servers.github]\nurl = "https://g"');
      // user content fully preserved; exactly one REAL managed block appended
      expect(next).toContain("[mcp_servers.note]");
      expect(next).toContain('description = "see # >>> BEGIN');
      expect(next).toContain("[mcp_servers.github]");
      // the only line-anchored BEGIN is the real one we just wrote
      const anchoredBegins = next
        .split("\n")
        .filter((l) => l.trimStart().startsWith(CODEX_MCP_MANAGED_BEGIN)).length;
      expect(anchoredBegins).toBe(1);
    });

    it("(#5) a truncated block (BEGIN, no END) self-heals instead of poisoning collisions", () => {
      // Prior write was killed mid-flight: BEGIN + a managed github, no END line.
      const truncated = [
        "[mcp_servers.local]",
        'command = "z"',
        "",
        CODEX_MCP_MANAGED_BEGIN,
        "[mcp_servers.github]",
        'url = "https://old"',
      ].join("\n");
      // github must NOT be seen as user-defined (it's inside the orphaned block)
      expect(findUserDefinedServerNames(truncated).has("github")).toBe(false);
      expect(findUserDefinedServerNames(truncated).has("local")).toBe(true);
      // re-provisioning github heals the block (END restored, single block)
      const { next, provisioned } = computeNextCodexConfig(truncated, {
        github: { url: "https://new" },
      });
      expect(provisioned).toEqual(["github"]);
      expect(next).toContain("https://new");
      expect(next).not.toContain("https://old");
      expect(next).toContain(CODEX_MCP_MANAGED_END);
      expect(next).toContain("[mcp_servers.local]");
    });
  });
});
