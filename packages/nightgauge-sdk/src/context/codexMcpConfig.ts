/**
 * codexMcpConfig — pure translation of the pipeline's MCP server definitions
 * into Codex `[mcp_servers.<name>]` TOML, plus the managed-block merge helpers.
 *
 * The pipeline's MCP servers are declared the Claude-native way: `.mcp.json` at
 * the repo root (and, secondarily, `mcpServers` in `.claude/settings.json`).
 * Codex does NOT read either — it reads `[mcp_servers.<name>]` tables from
 * `~/.codex/config.toml`. This module is the provider-neutral, side-effect-free
 * core that {@link CodexMcpProvisioner} uses to make the same servers visible to
 * Codex stages.
 *
 * No TOML library is used (none is vendored): a minimal emitter covers exactly
 * the `[mcp_servers.*]` shapes Codex accepts (string scalars, string arrays,
 * inline tables for env/headers), and collision detection is regex-only — we
 * never need to fully parse a user's config, only locate their server names.
 *
 * @see Issue #4025 - Codex MCP provisioning via ~/.codex/config.toml
 * @see https://developers.openai.com/codex/mcp
 */

import * as fs from "node:fs";
import * as path from "node:path";

/**
 * A pipeline MCP server definition as it appears in `.mcp.json` /
 * `.claude/settings.json`. Mirrors Claude's `.mcp.json` schema: either an http
 * (`url`) or stdio (`command`) transport.
 */
export interface PipelineMcpServer {
  /** Transport hint. Inferred from `url`/`command` when absent. */
  type?: string;
  /** stdio: executable to launch. */
  command?: string;
  /** stdio: arguments for the executable. */
  args?: string[];
  /** stdio: extra environment variables for the child. */
  env?: Record<string, string>;
  /** stdio: working directory for the child. */
  cwd?: string;
  /** http/sse: server URL. */
  url?: string;
  /** http/sse: request headers (an `Authorization: Bearer ${VAR}` is mapped to bearer_token_env_var). */
  headers?: Record<string, string>;
}

/** A Codex MCP server table, normalized to Codex's TOML field names. */
export interface CodexMcpServer {
  // stdio transport
  command?: string;
  args?: string[];
  env?: Record<string, string>;
  cwd?: string;
  // http transport
  url?: string;
  bearer_token_env_var?: string;
  http_headers?: Record<string, string>;
}

// ---------------------------------------------------------------------------
// Managed-block markers (TOML uses `#` line comments — no HTML comments).
// ---------------------------------------------------------------------------

export const CODEX_MCP_MANAGED_BEGIN = "# >>> BEGIN NIGHTGAUGE MANAGED MCP >>>";
export const CODEX_MCP_MANAGED_END = "# <<< END NIGHTGAUGE MANAGED MCP <<<";

const MANAGED_NOTICE =
  "# Managed by the Nightgauge pipeline (issue #4025). Servers inside these\n" +
  "# markers are regenerated from the project's .mcp.json on every Codex stage —\n" +
  "# edits here are overwritten. Define your own [mcp_servers.*] OUTSIDE the block.";

/** Whether a file's text already contains the pipeline-managed MCP block. */
export function hasManagedMcpBlock(existing: string): boolean {
  return lineAnchoredIndex(existing, CODEX_MCP_MANAGED_BEGIN) !== -1;
}

// ---------------------------------------------------------------------------
// Reading pipeline MCP servers (Claude-native sources).
// ---------------------------------------------------------------------------

function readJsonGracefully(filePath: string): Record<string, unknown> | null {
  try {
    const raw = fs.readFileSync(filePath, "utf-8");
    const parsed = JSON.parse(raw) as unknown;
    return parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

function extractServers(obj: Record<string, unknown> | null): Record<string, PipelineMcpServer> {
  if (!obj) return {};
  const servers = obj.mcpServers;
  if (!servers || typeof servers !== "object") return {};
  return servers as Record<string, PipelineMcpServer>;
}

/**
 * Read the pipeline's MCP servers from the same Claude-native sources a Claude
 * stage sees: `.mcp.json` (the committed, project-scoped config — primary) and
 * `.claude/settings.json` `mcpServers` (secondary). `.mcp.json` wins on a name
 * clash, since it is the documented source of truth.
 */
export function readPipelineMcpServers(workspaceRoot: string): Record<string, PipelineMcpServer> {
  const fromSettings = extractServers(
    readJsonGracefully(path.join(workspaceRoot, ".claude", "settings.json"))
  );
  const fromMcpJson = extractServers(readJsonGracefully(path.join(workspaceRoot, ".mcp.json")));
  // .mcp.json takes precedence over .claude/settings.json on duplicate names.
  return { ...fromSettings, ...fromMcpJson };
}

// ---------------------------------------------------------------------------
// Translation: PipelineMcpServer → CodexMcpServer.
// ---------------------------------------------------------------------------

/** `Bearer ${VAR}` / `Bearer $VAR` → the bare env var name, else null. */
function bearerEnvVar(authValue: string): string | null {
  const m = authValue.match(/^Bearer\s+\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$/);
  return m ? m[1] : null;
}

/** Coerce a possibly-non-string map value to a string, dropping null/undefined. */
function coerceStringMap(map: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(map)) {
    // A malformed `.mcp.json` can carry non-string values (JSON numbers/booleans).
    // Coerce them — env vars / headers are strings at the wire level — so the
    // emitter can never crash on `value.replace` of a non-string. (#4025 review #2/#8)
    if (v === null || v === undefined) continue;
    out[k] = typeof v === "string" ? v : String(v);
  }
  return out;
}

/**
 * Normalize one pipeline server into Codex's field names. Returns null when the
 * definition carries neither a `url` nor a `command` (nothing Codex can launch).
 */
export function toCodexMcpServer(server: PipelineMcpServer): CodexMcpServer | null {
  const isHttp =
    typeof server.url === "string" &&
    server.url.length > 0 &&
    // An explicit `"type": null` means "unspecified" — treat it like an absent
    // field (not a non-http type), matching the Go provisioner (#4041).
    (server.type === undefined ||
      server.type === null ||
      server.type === "http" ||
      server.type === "sse" ||
      !server.command);

  if (isHttp) {
    const out: CodexMcpServer = { url: server.url };
    if (server.headers && typeof server.headers === "object") {
      const rest: Record<string, string> = {};
      // Coerce non-string header values (JSON numbers/booleans) to strings — the
      // same treatment env values get below, and what coerceStringMap's own
      // comment intends for headers. Matches the Go provisioner (#4041) so both
      // execution paths emit identical http_headers.
      const headers = coerceStringMap(server.headers as Record<string, unknown>);
      for (const [key, value] of Object.entries(headers)) {
        if (key.toLowerCase() === "authorization") {
          const envVar = bearerEnvVar(value);
          if (envVar) {
            out.bearer_token_env_var = envVar;
            continue; // mapped to bearer_token_env_var, drop from http_headers
          }
        }
        rest[key] = value;
      }
      if (Object.keys(rest).length > 0) out.http_headers = rest;
    }
    return out;
  }

  if (typeof server.command === "string" && server.command.length > 0) {
    const out: CodexMcpServer = { command: server.command };
    if (Array.isArray(server.args) && server.args.length > 0) {
      out.args = server.args.filter((a): a is string => typeof a === "string");
    }
    if (server.env && typeof server.env === "object") {
      const env = coerceStringMap(server.env as Record<string, unknown>);
      if (Object.keys(env).length > 0) out.env = env;
    }
    if (typeof server.cwd === "string" && server.cwd.length > 0) out.cwd = server.cwd;
    return out;
  }

  return null;
}

// ---------------------------------------------------------------------------
// TOML emission (minimal, scoped to the shapes above).
// ---------------------------------------------------------------------------

/**
 * Escape a string for a TOML basic (double-quoted) string. The TOML spec forbids
 * RAW control characters (U+0000–U+001F except tab, plus U+007F) inside basic
 * strings — a single raw control byte makes the entire config.toml unparseable
 * by Codex's strict parser, breaking MCP (and any other settings) for every
 * Codex stage. So every control char is emitted as a named or `\uXXXX` escape.
 * (#4025 review #1)
 */
function tomlString(value: string): string {
  let out = '"';
  for (const ch of value) {
    const code = ch.codePointAt(0)!;
    switch (ch) {
      case "\\":
        out += "\\\\";
        break;
      case '"':
        out += '\\"';
        break;
      case "\n":
        out += "\\n";
        break;
      case "\r":
        out += "\\r";
        break;
      case "\t":
        out += "\\t";
        break;
      case "\b":
        out += "\\b";
        break;
      case "\f":
        out += "\\f";
        break;
      default:
        // Any remaining C0 control char (<= U+001F) or DEL (U+007F) is illegal
        // raw in a TOML basic string; emit a \uXXXX escape. (#4025 review #1)
        if (code <= 0x1f || code === 0x7f) {
          out += "\\u" + code.toString(16).padStart(4, "0").toUpperCase();
        } else {
          out += ch;
        }
    }
  }
  return out + '"';
}

/** Emit a TOML inline table from a string→string map: `{ K = "v", K2 = "v2" }`. */
function tomlInlineTable(map: Record<string, string>): string {
  // Sort keys for deterministic, JSON-key-order-independent output — consistent
  // with the sorted server-name ordering in buildManagedMcpBlockInner and with
  // the Go provisioner (#4041), so both execution paths emit byte-identical
  // bytes (and the idempotency check never thrashes on env/header key order).
  const parts = Object.keys(map)
    .sort()
    .map((k) => `${tomlKey(k)} = ${tomlString(map[k])}`);
  return `{ ${parts.join(", ")} }`;
}

/** A bare TOML key when it is a simple identifier, else a quoted key. */
function tomlKey(key: string): string {
  return /^[A-Za-z0-9_-]+$/.test(key) ? key : tomlString(key);
}

/** Emit the `[mcp_servers.<name>]` table body for one normalized server. */
function emitServerTable(name: string, server: CodexMcpServer): string {
  const lines: string[] = [`[mcp_servers.${tomlKey(name)}]`];
  if (server.command !== undefined) lines.push(`command = ${tomlString(server.command)}`);
  if (server.args && server.args.length > 0) {
    lines.push(`args = [${server.args.map(tomlString).join(", ")}]`);
  }
  if (server.env && Object.keys(server.env).length > 0) {
    lines.push(`env = ${tomlInlineTable(server.env)}`);
  }
  if (server.cwd !== undefined) lines.push(`cwd = ${tomlString(server.cwd)}`);
  if (server.url !== undefined) lines.push(`url = ${tomlString(server.url)}`);
  if (server.bearer_token_env_var !== undefined) {
    lines.push(`bearer_token_env_var = ${tomlString(server.bearer_token_env_var)}`);
  }
  if (server.http_headers && Object.keys(server.http_headers).length > 0) {
    lines.push(`http_headers = ${tomlInlineTable(server.http_headers)}`);
  }
  return lines.join("\n");
}

/**
 * Build the inner content of the managed block (notice + every server table).
 * Returns the empty string when there are no servers to emit.
 */
export function buildManagedMcpBlockInner(servers: Record<string, CodexMcpServer>): string {
  const names = Object.keys(servers).sort(); // deterministic ordering → idempotent bytes
  if (names.length === 0) return "";
  const tables = names.map((name) => emitServerTable(name, servers[name]));
  return [MANAGED_NOTICE, ...tables].join("\n\n");
}

// ---------------------------------------------------------------------------
// Managed-region location — line-anchored & BEGIN-authoritative.
//
// The markers are ordinary TOML `#` comments. Locating them with a bare
// `indexOf` would (a) match marker text appearing INSIDE a user's quoted string
// value (slicing out & destroying real user content), and (b) mishandle a
// truncated block. Both are avoided by anchoring markers to the start of a line
// and treating a present BEGIN as authoritative. (#4025 review #4/#5)
// ---------------------------------------------------------------------------

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Index of the first LINE-ANCHORED occurrence of `marker` (it must begin a line,
 * after optional indentation) at or after `fromIdx`, or -1.
 */
function lineAnchoredIndex(text: string, marker: string, fromIdx = 0): number {
  const re = new RegExp(`^[ \\t]*${escapeRegExp(marker)}`, "gm");
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) {
    const markerIdx = m.index + (m[0].length - marker.length);
    if (markerIdx >= fromIdx) return markerIdx;
    if (re.lastIndex === m.index) re.lastIndex++; // guard against zero-length match loop
  }
  return -1;
}

/**
 * Locate the managed region `[start, endExclusive)`. A present BEGIN is
 * authoritative: when the END marker is missing (a truncated or hand-edited
 * block), the region extends to EOF so the next write HEALS it rather than
 * leaving an orphaned block that poisons collision detection.
 */
function locateManagedRegion(text: string): { start: number; endExclusive: number } | null {
  const start = lineAnchoredIndex(text, CODEX_MCP_MANAGED_BEGIN);
  if (start === -1) return null;
  const endMarkerIdx = lineAnchoredIndex(
    text,
    CODEX_MCP_MANAGED_END,
    start + CODEX_MCP_MANAGED_BEGIN.length
  );
  const endExclusive =
    endMarkerIdx === -1 ? text.length : endMarkerIdx + CODEX_MCP_MANAGED_END.length;
  return { start, endExclusive };
}

// ---------------------------------------------------------------------------
// Collision detection — find user-defined [mcp_servers.X] OUTSIDE the block.
// ---------------------------------------------------------------------------

/** Reverse the basic-string escapes that {@link tomlString} produces. */
function unescapeTomlBasic(s: string): string {
  return s.replace(/\\(u[0-9A-Fa-f]{4}|.)/g, (_full, esc: string) => {
    if (esc[0] === "u") return String.fromCharCode(parseInt(esc.slice(1), 16));
    const map: Record<string, string> = {
      n: "\n",
      r: "\r",
      t: "\t",
      b: "\b",
      f: "\f",
      '"': '"',
      "\\": "\\",
    };
    return map[esc] ?? esc;
  });
}

/**
 * Names of `[mcp_servers.<name>]` servers the user has defined OUTSIDE the
 * managed block. Recognizes the bracketed table-header form (with TOML-legal
 * inner whitespace and quoted/escaped keys) AND the dotted-key inline form
 * (`mcp_servers.foo = { ... }`). Commented-out lines (`# [mcp_servers.x]`) are
 * ignored because a `#` precedes the bracket/key. (#4025 review #3/#6/#7)
 */
export function findUserDefinedServerNames(existing: string): Set<string> {
  const outside = withoutManagedBlock(existing);
  const names = new Set<string>();
  // Bracketed table header: `[ mcp_servers . "a\"b" ]` — whitespace-tolerant,
  // quoted form honors basic-string escapes so it matches what the emitter writes.
  const tableRe =
    /^[ \t]*\[[ \t]*mcp_servers[ \t]*\.[ \t]*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z0-9_-]+))[ \t]*\]/gm;
  // Dotted-key inline form: `mcp_servers.foo = { ... }` (equivalent to the table).
  const dottedRe =
    /^[ \t]*mcp_servers[ \t]*\.[ \t]*(?:"((?:[^"\\]|\\.)*)"|([A-Za-z0-9_-]+))[ \t]*=/gm;
  for (const re of [tableRe, dottedRe]) {
    let m: RegExpExecArray | null;
    while ((m = re.exec(outside)) !== null) {
      const name = m[1] !== undefined ? unescapeTomlBasic(m[1]) : m[2];
      if (name) names.add(name);
    }
  }
  return names;
}

/** Strip the managed block region from text (for scanning user content only). */
function withoutManagedBlock(existing: string): string {
  const region = locateManagedRegion(existing);
  if (!region) return existing;
  return existing.slice(0, region.start) + existing.slice(region.endExclusive);
}

// ---------------------------------------------------------------------------
// Managed-block upsert / strip (TOML-flavored, `#`-comment markers).
// ---------------------------------------------------------------------------

/**
 * Insert or replace the managed MCP block in `existing`, preserving everything
 * outside the markers byte-for-byte (modulo boundary newline normalization).
 * When `blockInner` is empty, any existing block is removed instead.
 */
export function upsertManagedMcpBlock(existing: string | null, blockInner: string): string {
  if (blockInner.trim().length === 0) {
    return existing === null ? "" : stripManagedMcpBlock(existing);
  }

  const wrapped = `${CODEX_MCP_MANAGED_BEGIN}\n${blockInner}\n${CODEX_MCP_MANAGED_END}`;

  if (existing === null || existing.trim().length === 0) {
    return wrapped + "\n";
  }

  const region = locateManagedRegion(existing);
  if (region) {
    const before = existing.slice(0, region.start).replace(/\n+$/, "");
    let after = existing.slice(region.endExclusive);
    while (after.startsWith("\n")) after = after.slice(1);
    if (before === "" && after === "") return wrapped + "\n";
    if (before === "") return wrapped + "\n\n" + after;
    if (after === "") return before + "\n\n" + wrapped + "\n";
    return before + "\n\n" + wrapped + "\n\n" + after;
  }

  // No block yet — append below the user's content.
  return existing.trimEnd() + "\n\n" + wrapped + "\n";
}

/**
 * Remove the managed MCP block, preserving user content. Returns the empty
 * string when the block was the file's only content.
 */
export function stripManagedMcpBlock(existing: string): string {
  const region = locateManagedRegion(existing);
  if (!region) return existing;

  const before = existing.slice(0, region.start).replace(/\n+$/, "");
  const after = existing.slice(region.endExclusive).replace(/^\n+/, "");
  if (before === "" && after === "") return "";
  if (before === "") return after;
  if (after === "") return before + "\n";
  return before + "\n\n" + after;
}

/**
 * Pure end-to-end transform used by the provisioner and unit tests: given the
 * existing config text (or null) and the pipeline servers, return the next
 * config text. Servers whose name collides with a user-defined table outside the
 * block are skipped (user wins); the skipped names are reported for logging.
 */
export function computeNextCodexConfig(
  existing: string | null,
  pipelineServers: Record<string, PipelineMcpServer>
): { next: string; provisioned: string[]; skippedCollisions: string[] } {
  const userDefined = existing ? findUserDefinedServerNames(existing) : new Set<string>();

  const codexServers: Record<string, CodexMcpServer> = {};
  const provisioned: string[] = [];
  const skippedCollisions: string[] = [];

  for (const [name, def] of Object.entries(pipelineServers)) {
    if (userDefined.has(name)) {
      skippedCollisions.push(name);
      continue;
    }
    const normalized = toCodexMcpServer(def);
    if (normalized) {
      codexServers[name] = normalized;
      provisioned.push(name);
    }
  }

  const inner = buildManagedMcpBlockInner(codexServers);
  const next = upsertManagedMcpBlock(existing, inner);
  return { next, provisioned: provisioned.sort(), skippedCollisions: skippedCollisions.sort() };
}
