/**
 * Parse `opencode run --format json` and complete an OpenCode stage's result:
 * the TypeScript twin of the Go parser (`ParseOpenCodeStreamLine` in
 * internal/execution/stream.go) and of the post-exit half of an opencode stage
 * (internal/execution/opencode_usage.go). Both are tested against the same
 * captured fixtures and one expectations file,
 * internal/execution/testdata/opencode_stream_expected.json.
 *
 * Shapes observed on opencode 1.18.30 (internal/execution/testdata/README.md,
 * § OpenCode). Every line is one event, `{type, timestamp, sessionID}` plus
 * `part`, or `error` for an error event. The types are step_start,
 * step_finish, tool_use, text, reasoning and error. There is no final usage
 * event and no model or version field, and the stream carries the run's own
 * session only: a subagent's steps never appear in it, so their usage is read
 * from `opencode export` once the run has ended ({@link foldOpenCodeSessions}).
 *
 * Nothing here classifies on what the model wrote: a text part is kept only as
 * the stage's display text, and a rejected call's input never reaches the
 * failure text (ADR-022 § 9).
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md § 3, § 9, § 22
 * @see Issue #1637
 */

import {
  MODEL_REGISTRY,
  isLocalProvider,
  parseOpenCodeModel,
  providerFor,
} from "../../eval/modelRegistry.js";

/** Prefixes every drift marker: OpenCode's output did not have the shape this parser knows. */
export const OPENCODE_DRIFT_MARKER = "[opencode-drift]";

/** A permission OpenCode rejected by itself, for a tool the stage's allowed tools grant (#1631). */
export const OPENCODE_PERMISSION_REJECTED_MARKER = "[adapter-permission-rejected]";

/** A permission OpenCode rejected by itself, for a tool the stage's allowed tools do not grant. */
export const OPENCODE_PERMISSION_DENIED_MARKER = "[permission-denied]";

/** The stream carried an `error` event: OpenCode reported the run failed. */
export const OPENCODE_ERROR_EVENT_MARKER = "[opencode-error-event]";

/**
 * The error OpenCode 1.18.30 gives a tool call whose permission request it
 * rejected because the permission resolved to "ask" and a headless run
 * auto-rejects it. Bundled source also has this exact text with " with the
 * following feedback: ${this.feedback}" appended for an interactive human's
 * own rejection, so this is matched as a PREFIX (isOpenCodeRejectedToolError
 * below), never exact equality.
 */
const OPENCODE_REJECTED_TOOL_ERROR = "The user rejected permission to use this specific tool call.";

/**
 * The DIFFERENT error text 1.18.30 gives a tool call whose permission
 * resolved to "deny" directly (bundled source: `The user has specified a
 * rule which prevents you from using this specific tool call. Here are some
 * of the relevant rules ${JSON.stringify(this.ruleset)}`) — nightgauge's own
 * generated OpenCode permission map never sets "ask" (ADR-022 § 9), so every
 * stage running under it hits this path, never OPENCODE_REJECTED_TOOL_ERROR's.
 * A prefix, matched with startsWith: the ruleset list trails it dynamically
 * per call. Go parity: internal/execution/stream.go's
 * openCodeRuleDeniedToolErrorPrefix (#1638 fix round finding, AC3).
 */
const OPENCODE_RULE_DENIED_TOOL_ERROR_PREFIX =
  "The user has specified a rule which prevents you from using this specific tool call.";

/**
 * Reports whether errText is a tool_use event's error for a call OpenCode's
 * own permission config refused — either text above, "ask" auto-rejected or
 * "deny" matched directly.
 */
function isOpenCodeRejectedToolError(errText: string): boolean {
  return (
    errText.startsWith(OPENCODE_REJECTED_TOOL_ERROR) ||
    errText.startsWith(OPENCODE_RULE_DENIED_TOOL_ERROR_PREFIX)
  );
}

/** The event types opencode 1.18.30 writes. */
const OPENCODE_KNOWN_EVENTS: ReadonlySet<string> = new Set([
  "step_start",
  "step_finish",
  "tool_use",
  "text",
  "reasoning",
  "error",
]);

/**
 * OpenCode's usage for one step (step_finish `part.tokens`) or one session
 * (export `info.tokens`). OpenCode subtracts both cache pools from `input` and
 * the reasoning tokens from `output`, so the five fields are disjoint and each
 * sums on its own.
 */
export interface OpenCodeTokens {
  input: number;
  output: number;
  reasoning: number;
  cacheRead: number;
  cacheWrite: number;
}

function emptyTokens(): OpenCodeTokens {
  return { input: 0, output: 0, reasoning: 0, cacheRead: 0, cacheWrite: 0 };
}

function nonNegative(n: number): number {
  return n < 0 ? 0 : n;
}

function addTokens(into: OpenCodeTokens, t: OpenCodeTokens): void {
  into.input += nonNegative(t.input);
  into.output += nonNegative(t.output);
  into.reasoning += nonNegative(t.reasoning);
  into.cacheRead += nonNegative(t.cacheRead);
  into.cacheWrite += nonNegative(t.cacheWrite);
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** An integer JSON field, or `undefined` when present with another type. Absent reads 0. */
function intField(obj: Record<string, unknown>, key: string): number | undefined {
  const v = obj[key];
  if (v === undefined || v === null) return 0;
  return typeof v === "number" && Number.isInteger(v) ? v : undefined;
}

/** `part.tokens` / `info.tokens` in OpenCode's shape, or `undefined` when malformed. */
function readTokens(raw: unknown): OpenCodeTokens | undefined {
  if (!isRecord(raw)) return undefined;
  const cache = raw.cache === undefined || raw.cache === null ? {} : raw.cache;
  if (!isRecord(cache)) return undefined;
  const t = {
    input: intField(raw, "input"),
    output: intField(raw, "output"),
    reasoning: intField(raw, "reasoning"),
    cacheRead: intField(cache, "read"),
    cacheWrite: intField(cache, "write"),
  };
  if (Object.values(t).some((n) => n === undefined)) return undefined;
  return t as OpenCodeTokens;
}

/**
 * Drift markers: each distinct finding once, in the order first seen, with a
 * count when it repeats, so a stream that drifted on every line cannot bury
 * the log. The same rendering as the Go parser's `driftLog`.
 */
export class OpenCodeDriftLog {
  private readonly counts = new Map<string, number>();

  add(message: string): void {
    this.counts.set(message, (this.counts.get(message) ?? 0) + 1);
  }

  list(): string[] {
    return [...this.counts].map(([message, n]) => {
      const marker = `${OPENCODE_DRIFT_MARKER} ${message}`;
      return n > 1 ? `${marker} (${n} times)` : marker;
    });
  }
}

/** What {@link parseOpenCodeStream} learns from one run's stdout. */
export interface OpenCodeStreamState {
  /** The run's own session, from the first event naming one. */
  sessionId?: string;
  /** step_finish events: one per model step. */
  stepFinishes: number;
  /** The sum of every step_finish's `part.tokens`. */
  tokens: OpenCodeTokens;
  /** The largest prompt one step sent: its input plus both cache pools. 0 means none seen. */
  peakStepInputTokens: number;
  /**
   * The sum of step_finish `part.cost` above zero. OpenCode's figure from its
   * bundled catalog, not a bill (ADR-022 § 3): kept for comparison only, never
   * the stage's cost ({@link openCodeStageCostUsd}).
   */
  reportedCostUsd: number;
  /** tool_use events OpenCode failed with its own permission-rejection error. */
  rejectedToolCalls: number;
  /**
   * The FIRST rejected tool_use event's own `part.tool` (opencode's own
   * lowercase name: "bash", "edit", "apply_patch", ...), `undefined` when
   * the event named none. classifyOpenCodeRun's own fallback source for the
   * marker (AC3, #1638 fix round) when stderr carries no auto-reject notice
   * at all — a "deny" match never prints one. Go parity:
   * OpenCodeStream.RejectedTool in opencode_usage.go/stream.go.
   */
  rejectedTool?: string;
  /** The `error.name` of every error event, in order ("unknown" when it has none). */
  errorEvents: string[];
  /** The text parts' text, joined: the stage's display output, never classified. */
  displayText: string;
}

/** Cuts and quotes an unknown event type so a marker stays one bounded line. */
function quotedEventType(t: string): string {
  const max = 40;
  return JSON.stringify(t.length > max ? `${t.slice(0, max)}...` : t);
}

/**
 * Parse a whole `opencode run --format json` stdout.
 *
 * Every step_finish adds its `part.tokens` to the pools, and the sum over
 * steps is the run's usage. Anything opencode 1.18.30 did not emit is a drift
 * marker on `drift`, never a crash: a line that is not a JSON event, an
 * unknown event type, and a step_finish without `part.tokens` or
 * `part.reason`. An error event is recorded, not skipped past: the stage
 * fails on it ({@link classifyOpenCodeRun}), and the usage of every step, the
 * ones after it included, is still counted, as the Go parser counts it.
 */
export function parseOpenCodeStream(
  stdout: string,
  drift: OpenCodeDriftLog = new OpenCodeDriftLog()
): OpenCodeStreamState {
  const s: OpenCodeStreamState = {
    stepFinishes: 0,
    tokens: emptyTokens(),
    peakStepInputTokens: 0,
    reportedCostUsd: 0,
    rejectedToolCalls: 0,
    errorEvents: [],
    displayText: "",
  };
  const text: string[] = [];
  for (const raw of stdout.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    let ev: unknown;
    try {
      ev = line[0] === "{" ? JSON.parse(line) : undefined;
    } catch {
      ev = undefined;
    }
    if (!isRecord(ev) || (ev.type !== undefined && typeof ev.type !== "string")) {
      drift.add("a stdout line is not a JSON event");
      continue;
    }
    const type = typeof ev.type === "string" ? ev.type : "";
    if (s.sessionId === undefined && typeof ev.sessionID === "string" && ev.sessionID !== "") {
      s.sessionId = ev.sessionID;
    }
    if (!OPENCODE_KNOWN_EVENTS.has(type)) {
      drift.add(`unknown event type ${quotedEventType(type)}`);
      continue;
    }
    const part = isRecord(ev.part) ? ev.part : undefined;
    switch (type) {
      case "tool_use": {
        const state = part && isRecord(part.state) ? part.state : undefined;
        if (
          state?.status === "error" &&
          typeof state.error === "string" &&
          isOpenCodeRejectedToolError(state.error)
        ) {
          s.rejectedToolCalls++;
          if (s.rejectedTool === undefined && typeof part?.tool === "string" && part.tool !== "") {
            s.rejectedTool = part.tool;
          }
        }
        break;
      }
      case "text":
        if (part && typeof part.text === "string") text.push(part.text);
        break;
      case "error": {
        const err = isRecord(ev.error) ? ev.error : undefined;
        s.errorEvents.push(typeof err?.name === "string" && err.name !== "" ? err.name : "unknown");
        break;
      }
      case "step_finish": {
        s.stepFinishes++;
        if (!part || part.reason === undefined || part.reason === null) {
          drift.add("a step_finish event has no part.reason");
        }
        const tokens = part ? readTokens(part.tokens) : undefined;
        if (!tokens) {
          drift.add("a step_finish event has no part.tokens");
          break;
        }
        if (typeof part?.cost === "number" && part.cost > 0) s.reportedCostUsd += part.cost;
        addTokens(s.tokens, tokens);
        const prompt =
          nonNegative(tokens.input) +
          nonNegative(tokens.cacheRead) +
          nonNegative(tokens.cacheWrite);
        if (prompt > s.peakStepInputTokens) s.peakStepInputTokens = prompt;
        break;
      }
    }
  }
  s.displayText = text.join("");
  return s;
}

// ---------------------------------------------------------------------------
// stderr: the auto-reject notice (ADR-022 § 9)
// ---------------------------------------------------------------------------

/** Terminal escape sequences, which OpenCode puts around the notice's "!" even on a file. */
// eslint-disable-next-line no-control-regex -- ESC is the escape sequence's first byte
const ANSI_ESCAPE_RE = /\x1b\[[0-9;?]*[A-Za-z]/g;

/**
 * The first line of the notice opencode 1.18.30 prints to stderr when it
 * rejects a permission request, for its own session and for every subagent's:
 * `! permission requested: <permission> (<patterns>); auto-rejecting`. The
 * patterns are the tool's input, printed unescaped, so the notice can span
 * lines; only the permission on the first line is ever read.
 */
const AUTO_REJECT_START_RE = /^!\s*permission requested: (\S+) \(/;
const AUTO_REJECT_END = "); auto-rejecting";

/** The permissions opencode 1.18.30 asks for itself; any other is recorded as "unknown". */
const OPENCODE_PERMISSIONS: ReadonlySet<string> = new Set([
  "bash",
  "read",
  "edit",
  "glob",
  "grep",
  "task",
  "webfetch",
  "websearch",
  "todowrite",
  "skill",
  "lsp",
  "external_directory",
  "doom_loop",
  "workflow_tool_approval",
]);

/**
 * The OpenCode permission each Claude Code tool name grants, a copy of
 * `openCodeToolForClaudeTool` in internal/execution/adapters/adapter.go.
 */
const OPENCODE_TOOL_FOR_CLAUDE_TOOL: Readonly<Record<string, string>> = Object.freeze({
  Bash: "bash",
  Read: "read",
  Write: "edit",
  Edit: "edit",
  MultiEdit: "edit",
  Glob: "glob",
  Grep: "grep",
  Task: "task",
  WebFetch: "webfetch",
});

/** The OpenCode permissions a stage's allowed tools (Claude Code names) grant. */
export function openCodeToolsAllowed(allowedTools: readonly string[] = []): ReadonlySet<string> {
  const set = new Set<string>();
  for (const entry of allowedTools) {
    // A frontmatter entry may carry a pattern, as "Bash(git:*)" does.
    const name = entry.trim().split("(")[0];
    if (Object.hasOwn(OPENCODE_TOOL_FOR_CLAUDE_TOOL, name)) {
      set.add(OPENCODE_TOOL_FOR_CLAUDE_TOOL[name]);
    }
  }
  return set;
}

function plain(line: string): string {
  return line.replace(ANSI_ESCAPE_RE, "").trim();
}

/**
 * Maps a rejected tool_use event's own `part.tool` name (opencode's own
 * lowercase tool id, {@link OpenCodeStreamState.rejectedTool}) to the
 * permission key {@link rejectionMarker} classifies by. 1.18.30 has no
 * permission of its own for "write" or "apply_patch": both are governed by
 * the same "edit" permission the generated map's own "edit" key controls
 * (ADR-022 § 9). Every other tool name already equals its own permission
 * key, so it passes through unchanged. Go parity:
 * openCodeToolRejectionPermission in opencode_usage.go.
 */
function openCodeToolRejectionPermission(tool: string): string {
  return tool === "write" || tool === "apply_patch" ? "edit" : tool;
}

function rejectionMarker(permission: string, allowed: ReadonlySet<string>): string {
  const marker = allowed.has(permission)
    ? OPENCODE_PERMISSION_REJECTED_MARKER
    : OPENCODE_PERMISSION_DENIED_MARKER;
  return `${marker} tool=${permission}`;
}

/** What {@link observeOpenCodeStderr} made of a run's stderr. */
export interface OpenCodeStderrObservation {
  /**
   * The lines the stage keeps, unredacted: every ordinary line up to the first
   * auto-reject notice, and that notice as `! permission requested:
   * <permission> (...); auto-rejecting`, without the rejected call's input.
   */
  kept: string[];
  /** The classification marker of the first notice, when there was one. */
  marker?: string;
  /** True when the first notice never ended with "); auto-rejecting". */
  unterminatedNotice: boolean;
}

/**
 * Read a run's stderr the way the Go stage does (`observeStderr` in
 * opencode_usage.go), before any redaction. The first auto-reject notice
 * decides the marker, and from it on no other line is kept: the lines it
 * spans are the rejected call's input, printed unescaped, so nothing tells
 * that input from what OpenCode prints next, a forged notice included. A later
 * notice or line is dropped with a drift marker saying so.
 */
export function observeOpenCodeStderr(
  stderr: string,
  allowedTools: readonly string[] = [],
  drift: OpenCodeDriftLog = new OpenCodeDriftLog()
): OpenCodeStderrObservation {
  const allowed = openCodeToolsAllowed(allowedTools);
  const kept: string[] = [];
  let marker: string | undefined;
  let inNotice = false;
  const lines = stderr.split("\n");
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  for (const rawLine of lines) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    const p = plain(line);
    if (inNotice) {
      inNotice = !p.endsWith(AUTO_REJECT_END);
      continue;
    }
    const m = AUTO_REJECT_START_RE.exec(p);
    if (!m) {
      if (marker === undefined) {
        kept.push(line);
      } else if (p !== "") {
        drift.add(
          "a stderr line after an auto-reject notice was not kept: OpenCode prints the rejected call's input unescaped, so it cannot be told from that input"
        );
      }
      continue;
    }
    if (marker !== undefined) {
      drift.add(
        "an auto-reject notice after the first was not kept and decides no failure kind: it may be a subagent's, or one the first rejected call's input forged"
      );
      continue;
    }
    inNotice = !p.endsWith(AUTO_REJECT_END);
    let permission = m[1];
    if (!OPENCODE_PERMISSIONS.has(permission)) {
      drift.add("an auto-reject line on stderr names no permission this parser recognizes");
      permission = "unknown";
    }
    marker = rejectionMarker(permission, allowed);
    kept.push(`! permission requested: ${permission} (...${AUTO_REJECT_END}`);
  }
  return { kept, marker, unterminatedNotice: inNotice };
}

// ---------------------------------------------------------------------------
// Redaction (ADR-022 § 22)
// ---------------------------------------------------------------------------

/**
 * What may come right before a credential: the start of the text, a character
 * no credential holds, a JSON escape ending in a letter or digit, or a
 * terminal escape sequence, raw or JSON-escaped. The same left context as
 * `credentialLeft` in opencode_usage.go.
 */
const CREDENTIAL_LEFT = String.raw`(^|[^A-Za-z0-9_]|\\[bfnrt]|\\u[0-9A-Fa-f]{4}|(?:\x1b|\\u001[bB])\[[0-9;?]*[A-Za-z])`;

/**
 * The credential shapes removed from everything an opencode child prints
 * before it reaches an error message: the same list, in the same order, as
 * `credentialPatterns` in internal/execution/opencode_usage.go.
 */
const CREDENTIAL_PATTERNS: ReadonlyArray<{
  hints: readonly string[];
  re: RegExp;
  repl: string;
}> = [
  {
    hints: ["sk-", "xai-", "aiza", "akia", "asia", "gsk_", "hf_"],
    re: new RegExp(
      CREDENTIAL_LEFT +
        String.raw`(?:sk-[A-Za-z0-9_-]{20,}|xai-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|(?:AKIA|ASIA)[0-9A-Z]{16}|gsk_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,})`,
      "g"
    ),
    repl: "$1[REDACTED:api-key]",
  },
  {
    hints: ["ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-"],
    re: new RegExp(
      CREDENTIAL_LEFT +
        String.raw`(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}|glpat-[A-Za-z0-9_-]{20,})`,
      "g"
    ),
    repl: "$1[REDACTED:forge-token]",
  },
  {
    hints: ["bearer"],
    re: new RegExp(CREDENTIAL_LEFT + String.raw`(bearer\s+)[A-Za-z0-9._~+/-]{16,}=*`, "gi"),
    repl: "$1$2[REDACTED:bearer-token]",
  },
  {
    hints: ["authorization"],
    re: new RegExp(
      CREDENTIAL_LEFT +
        String.raw`(authorization\s*[:=]\s*(?:basic|token)\s+)[A-Za-z0-9._~+/-]{8,}=*`,
      "gi"
    ),
    repl: "$1$2[REDACTED:authorization]",
  },
  {
    hints: ["://"],
    re: new RegExp(
      CREDENTIAL_LEFT + String.raw`([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@:"'\\]+:[^\s/@"'\\]+@`,
      "g"
    ),
    repl: "$1$2[REDACTED:userinfo]@",
  },
  {
    hints: ["key=", "token=", "secret=", "passw", "pwd=", "sig=", "signature=", "credential="],
    re: new RegExp(
      String.raw`([?&](?:api[_-]?key|apikey|key|access[_-]?token|auth[_-]?token|token|client[_-]?secret|secret|password|passwd|pwd|sig|signature|x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential)=)[^&#\s"'\\]+`,
      "gi"
    ),
    repl: "$1[REDACTED:query-credential]",
  },
];

/** Remove every credential shape {@link CREDENTIAL_PATTERNS} lists from `s`. */
export function redactOpenCodeCredentials(s: string): string {
  let out = s;
  let lower = out.toLowerCase();
  for (const p of CREDENTIAL_PATTERNS) {
    if (!p.hints.some((h) => lower.includes(h))) continue;
    const r = out.replace(p.re, p.repl);
    if (r !== out) {
      out = r;
      lower = out.toLowerCase();
    }
  }
  return out;
}

/** Values shorter than this are not redacted by value: "1" would strip every digit. */
const REDACTED_SECRET_MIN_LEN = 8;

/**
 * Whether a variable holds a provider setting rather than a credential
 * (`isProviderSetting` in internal/execution/manager.go): OpenCode's catalog
 * binds a provider's region, project, account, host and endpoint beside its
 * key, and redacting their values would strip every "us-east-1" from a
 * stage's output. A name with a credential segment is never a setting.
 */
function isProviderSetting(name: string): boolean {
  const segments = name.split("_");
  if (
    segments.some((s) =>
      ["KEY", "APIKEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "PAT"].includes(s)
    )
  ) {
    return false;
  }
  if (name === "GOOGLE_APPLICATION_CREDENTIALS") return true;
  return [
    "REGION",
    "LOCATION",
    "PROJECT",
    "ACCOUNT",
    "HOST",
    "ENDPOINT",
    "URL",
    "NAME",
    "ID",
  ].includes(segments[segments.length - 1]);
}

/**
 * A redactor for text an opencode child printed: first the values of the
 * named secret variables the child held (longest first, each also in its JSON
 * escaped form), each replaced by `[REDACTED:<NAME>]`, then every credential
 * shape. The value step is the Go manager's `envValueRedactor`, which skips a
 * variable holding a provider setting.
 */
export function openCodeRedactor(
  secrets: Readonly<Record<string, string | undefined>> = {}
): (s: string) => string {
  const forms: Array<{ form: string; name: string }> = [];
  for (const [name, value] of Object.entries(secrets)) {
    if (value === undefined || value.length < REDACTED_SECRET_MIN_LEN) continue;
    if (isProviderSetting(name)) continue;
    forms.push({ form: value, name });
    const escaped = JSON.stringify(value).slice(1, -1);
    if (escaped !== value) forms.push({ form: escaped, name });
  }
  forms.sort((a, b) => b.form.length - a.form.length);
  return (s: string) => {
    let out = s;
    for (const { form, name } of forms) out = out.split(form).join(`[REDACTED:${name}]`);
    return redactOpenCodeCredentials(out);
  };
}

/**
 * Redact one line an opencode child printed, stdout or stderr, the way the Go
 * manager does before it streams or keeps the line (`openCodeOutputRedactor`
 * in opencode_usage.go, ADR-022 § 22). A `--format json` event carries what a
 * tool printed as a JSON string, escaped, so each string of a line holding a
 * JSON object is decoded, redacted and, only when that changed it, re-encoded
 * in place; the whole line is then redacted as text as well, which is all a
 * line that is not JSON gets.
 */
export function redactOpenCodeLine(line: string, redact: (s: string) => string): string {
  return redact(redactJsonStrings(line, redact));
}

/** `redactJSONStrings` in opencode_usage.go: every JSON string of an object line, redacted in place. */
function redactJsonStrings(line: string, redact: (s: string) => string): string {
  const first = line.replace(/^[ \t]+/, "");
  if (first === "" || first[0] !== "{") return line;
  let out = "";
  let changed = false;
  let last = 0;
  for (let i = 0; i < line.length; i++) {
    if (line[i] !== '"') continue;
    let end = i + 1;
    let escaped = false;
    while (end < line.length && line[end] !== '"') {
      if (line[end] === "\\") {
        escaped = true;
        end++;
      }
      end++;
    }
    if (end >= line.length) break;
    const literal = line.slice(i, end + 1);
    let value: string;
    if (!escaped) {
      value = literal.slice(1, -1);
    } else {
      try {
        value = JSON.parse(literal) as string;
      } catch {
        i = end;
        continue;
      }
    }
    const redacted = redact(value);
    if (redacted !== value) {
      out += line.slice(last, i) + JSON.stringify(redacted);
      last = end + 1;
      changed = true;
    }
    i = end;
  }
  return changed ? out + line.slice(last) : line;
}

// ---------------------------------------------------------------------------
// The served model and the stage's cost (ADR-022 § 1-3)
// ---------------------------------------------------------------------------

/** The model that served an opencode stage, as ADR-022 § 1 and § 2 record it. */
export interface OpenCodeServedModel {
  /** The normalized provider: "lm-studio" for key lmstudio, "other" for an unrecognized key. */
  provider: string;
  /**
   * The recorded model: a registry model's bare id, any other model as
   * `<provider>/<id>`, and an "other" model as its raw provider-qualified id.
   */
  model: string;
  /** The raw -m value exactly as dispatched, also when another model served the stage. */
  upstream: string;
  /** The OpenCode provider key of the model that served the stage, before normalization. */
  key: string;
}

/**
 * Record the model that served a stage (`ResolveOpenCodeServedModel` in
 * opencode_usage.go). The stream names no model, so provider and model come
 * from the session export's last assistant message when there is one, and
 * otherwise from the dispatched -m.
 */
export function resolveOpenCodeServedModel(
  providerID: string,
  modelID: string,
  dispatched: string
): OpenCodeServedModel | undefined {
  const raw = providerID !== "" && modelID !== "" ? `${providerID}/${modelID}` : dispatched;
  if (raw === "") return undefined;
  const { provider, bareId } = parseOpenCodeModel(raw);
  const slash = raw.indexOf("/");
  const served: OpenCodeServedModel = {
    provider,
    model: raw,
    upstream: dispatched,
    key: slash < 0 ? "" : raw.slice(0, slash),
  };
  if (provider === "other" || bareId === "") return served;
  if (MODEL_REGISTRY.some((m) => m.id === bareId && m.provider === provider)) {
    served.model = bareId;
    return served;
  }
  served.model = `${provider}/${bareId}`;
  return served;
}

/**
 * The provider that serves an opencode model in either form a stage carries,
 * and the model's id under it (`openCodeServing` in the Go economics): the -m
 * value `<key>/<id>`, or the recorded form, a registry model's bare id or a
 * local model under its normalized provider.
 */
function openCodeServing(model: string): { provider: string; id: string } {
  const slash = model.indexOf("/");
  if (slash < 0) {
    const d = MODEL_REGISTRY.find((m) => m.id === model);
    return d ? { provider: d.provider, id: d.id } : { provider: "", id: "" };
  }
  const key = model.slice(0, slash);
  const rest = model.slice(slash + 1);
  if (isLocalProvider(key) && rest !== "") return { provider: key, id: rest };
  return { provider: providerFor("opencode", model), id: parseOpenCodeModel(model).bareId };
}

/**
 * The USD cost of an opencode stage (ADR-022 § 3), priced exactly as
 * `tokens.CalculateCostFor("opencode", …)` prices it in Go:
 *
 *   - a model served by a local provider costs a stamped `0`;
 *   - a hosted model the registry lists under the same provider is priced at
 *     the registry's rates (input, output with reasoning, cache read, and the
 *     cache writes at the 5-minute rate);
 *   - anything else is unstamped: `undefined`, never a fabricated `0`.
 *
 * OpenCode's own `part.cost` never enters: it comes from OpenCode's catalog,
 * not from the bill, and reads 0 for a provider it holds no price for.
 *
 * @param model - The recorded served model ({@link OpenCodeServedModel.model}) or the -m value.
 */
export function openCodeStageCostUsd(
  model: string,
  usage: { input: number; output: number; cacheRead: number; cacheWrite: number }
): number | undefined {
  const { provider, id } = openCodeServing(model);
  if (isLocalProvider(provider)) return 0;
  if (provider === "" || id === "") return undefined;
  const d = MODEL_REGISTRY.find((m) => m.id === id && m.provider === provider);
  if (!d) return undefined;
  const r = d.rates;
  return (
    (usage.input * r.input +
      usage.output * r.output +
      usage.cacheRead * (r.cache_read ?? 0) +
      usage.cacheWrite * (r.cache_creation_5m ?? 0)) /
    1_000_000
  );
}

// ---------------------------------------------------------------------------
// The subagent roll-up (ADR-022 § 3, § 22)
// ---------------------------------------------------------------------------

/**
 * Runs one `opencode` helper with `args` and resolves its stdout, held in
 * memory only. The real one (OpenCodeAdapter) runs each in its own process
 * group, under a timeout, from the run's own root, with no credential.
 */
export type OpenCodeHelper = (args: readonly string[]) => Promise<string>;

/** The shape of an OpenCode session id, checked before one becomes an argument. */
const OPENCODE_SESSION_ID_RE = /^ses_[0-9A-Za-z]{1,64}$/;

/** The most subagent sessions folded for one stage. */
export const OPENCODE_MAX_DESCENDANTS = 64;

/** The most session rows the session listing reads. */
const OPENCODE_MAX_SESSION_ROWS = 4096;

/**
 * Every session that has a parent, from the run's own database. No value from
 * the stream or the run is ever put into it: the tree is walked here.
 */
export const OPENCODE_SESSION_TREE_QUERY = `SELECT id, parent_id FROM session WHERE parent_id IS NOT NULL ORDER BY time_created, id LIMIT ${OPENCODE_MAX_SESSION_ROWS}`;

/** A session's export argv: --sanitize redacts the transcript, --pure loads no plugin. */
export function openCodeExportArgs(session: string): string[] {
  return ["export", session, "--sanitize", "--pure"];
}

/** What {@link foldOpenCodeSessions} read. */
export interface OpenCodeFoldResult {
  served?: OpenCodeServedModel;
  /** The subagent sessions' summed `info.tokens`. */
  children: OpenCodeTokens;
  /** The subagent sessions' summed `info.cost` above zero, OpenCode's figure. */
  childCostUsd: number;
  /** True when some usage could not be read; the drift log says why. */
  partial: boolean;
}

function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Read what the stream cannot tell, once the run has ended: the served model,
 * from the stage session's sanitized export, and the usage of every subagent
 * session, found in the run's session table and read from each one's
 * sanitized export (`openCodeFold.run` in opencode_usage.go). Only
 * `info.tokens`, `info.cost` and the assistant messages' `providerID` and
 * `modelID` are read; an export is never kept, written or logged. A failure
 * never throws: it marks the result partial and leaves a drift marker.
 */
export async function foldOpenCodeSessions(
  helper: OpenCodeHelper,
  sessionId: string | undefined,
  dispatched: string,
  drift: OpenCodeDriftLog,
  maxDescendants: number = OPENCODE_MAX_DESCENDANTS
): Promise<OpenCodeFoldResult> {
  const res: OpenCodeFoldResult = {
    served: resolveOpenCodeServedModel("", "", dispatched),
    children: emptyTokens(),
    childCostUsd: 0,
    partial: false,
  };
  if (sessionId === undefined || sessionId === "") return res;
  if (!OPENCODE_SESSION_ID_RE.test(sessionId)) {
    drift.add(
      "usage partial: the stream names a session id of an unrecognized shape, so no subagent session was folded"
    );
    res.partial = true;
    return res;
  }

  try {
    const { providerID, modelID } = readServedModel(await helper(openCodeExportArgs(sessionId)));
    if (providerID !== "" && modelID !== "") {
      res.served = resolveOpenCodeServedModel(providerID, modelID, dispatched);
    }
  } catch (err) {
    drift.add(
      `the served model is the dispatched model: exporting the session failed: ${errorText(err)}`
    );
  }

  let children: string[];
  let truncated: boolean;
  try {
    ({ children, truncated } = descendants(
      await helper(["db", OPENCODE_SESSION_TREE_QUERY, "--format", "json", "--pure"]),
      sessionId,
      maxDescendants
    ));
  } catch (err) {
    drift.add(`usage partial: listing the subagent sessions failed: ${errorText(err)}`);
    res.partial = true;
    return res;
  }
  if (truncated) {
    drift.add(
      `usage partial: the stage has more than ${maxDescendants} subagent sessions, and only the first ${maxDescendants} were folded`
    );
    res.partial = true;
  }
  for (const id of children) {
    try {
      const { tokens, cost } = readSessionUsage(await helper(openCodeExportArgs(id)));
      addTokens(res.children, tokens);
      if (cost > 0) res.childCostUsd += cost;
    } catch (err) {
      drift.add(`usage partial: exporting a subagent session failed: ${errorText(err)}`);
      res.partial = true;
    }
  }
  return res;
}

/** The provider and model of an export's last assistant message. */
function readServedModel(out: string): { providerID: string; modelID: string } {
  let exp: unknown;
  try {
    exp = JSON.parse(out);
  } catch {
    throw new Error("the export is not the JSON this parser reads");
  }
  const messages = isRecord(exp) && Array.isArray(exp.messages) ? exp.messages : undefined;
  if (!messages) throw new Error("the export is not the JSON this parser reads");
  for (let i = messages.length - 1; i >= 0; i--) {
    const m: unknown = messages[i];
    const info = isRecord(m) && isRecord(m.info) ? m.info : undefined;
    if (info?.role === "assistant") {
      return {
        providerID: typeof info.providerID === "string" ? info.providerID : "",
        modelID: typeof info.modelID === "string" ? info.modelID : "",
      };
    }
  }
  return { providerID: "", modelID: "" };
}

/** The subagent sessions under `session`, breadth first, at most `max` of them. */
function descendants(
  out: string,
  session: string,
  max: number
): { children: string[]; truncated: boolean } {
  let rows: unknown;
  try {
    rows = JSON.parse(out);
  } catch {
    rows = undefined;
  }
  if (!Array.isArray(rows)) throw new Error("the session list is not the JSON this parser reads");
  let truncated = rows.length >= OPENCODE_MAX_SESSION_ROWS;
  const byParent = new Map<string, string[]>();
  for (const row of rows) {
    if (!isRecord(row) || typeof row.id !== "string" || !OPENCODE_SESSION_ID_RE.test(row.id)) {
      continue;
    }
    const parent = typeof row.parent_id === "string" ? row.parent_id : "";
    byParent.set(parent, [...(byParent.get(parent) ?? []), row.id]);
  }
  const found: string[] = [];
  const seen = new Set([session]);
  const queue = [session];
  while (queue.length > 0) {
    const next = queue.shift() as string;
    for (const id of byParent.get(next) ?? []) {
      if (seen.has(id)) continue;
      if (found.length === max) {
        truncated = true;
        return { children: found, truncated };
      }
      seen.add(id);
      found.push(id);
      queue.push(id);
    }
  }
  return { children: found, truncated };
}

/** A session's `info.tokens` and `info.cost` from its sanitized export, and nothing else. */
function readSessionUsage(out: string): { tokens: OpenCodeTokens; cost: number } {
  let exp: unknown;
  try {
    exp = JSON.parse(out);
  } catch {
    exp = undefined;
  }
  const info = isRecord(exp) && isRecord(exp.info) ? exp.info : undefined;
  const tokens = info ? readTokens(info.tokens) : undefined;
  if (!info || !tokens) throw new Error("the export has no info.tokens");
  return { tokens, cost: typeof info.cost === "number" ? info.cost : 0 };
}

// ---------------------------------------------------------------------------
// The run's outcome
// ---------------------------------------------------------------------------

/** How a finished opencode run is judged ({@link classifyOpenCodeRun}). */
export interface OpenCodeRunInput {
  stdout: string;
  stderr: string;
  exitCode: number;
  /** The stage's allowed tools, as Claude Code names them; decide rejected vs denied. */
  allowedTools?: readonly string[];
  /** The -m value the run was dispatched with. */
  dispatched: string;
  /** The subagent roll-up; omitted, the served model is the dispatched one and no child is read. */
  fold?: OpenCodeHelper;
  /**
   * Redacts every line of the child's output, stdout and stderr alike, before
   * anything is read from it ({@link redactOpenCodeLine}); credential shapes
   * only when omitted.
   */
  redact?: (s: string) => string;
}

/** The completed result of one opencode run. */
export interface OpenCodeRunSummary {
  /** What the redacted stdout says: its display text holds no secret the child held. */
  stream: OpenCodeStreamState;
  /** Stage usage: the stream's steps plus every subagent session read. */
  tokens: OpenCodeTokens;
  peakStepInputTokens: number;
  sessionId?: string;
  served?: OpenCodeServedModel;
  /** ADR-022 § 3's cost: a number when stamped, `undefined` when unstamped. */
  costUsd: number | undefined;
  /** OpenCode's own figure, the stream's and the subagents' summed; never the cost. */
  reportedCostUsd: number;
  usagePartial: boolean;
  driftMarkers: string[];
  /** Why the run failed, when it did: the first line leads with its tag. Redacted. */
  failure?: string;
}

/** The last bytes of kept stderr an error message carries. */
const STDERR_TAIL_CHARS = 4000;

function stderrTail(kept: readonly string[], redact: (s: string) => string): string {
  const text = kept
    .map((line) => redactOpenCodeLine(line, redact))
    .join("\n")
    .trim();
  return text.length > STDERR_TAIL_CHARS ? `...${text.slice(-STDERR_TAIL_CHARS)}` : text;
}

/**
 * Complete and judge a finished run: parse the stream, read stderr's
 * auto-reject notice, fold the subagent sessions, price the stage, and decide
 * whether it failed. A run fails, whatever its exit code, when:
 *
 *   - it exited non-zero;
 *   - OpenCode rejected a permission on its own (the notice on stderr, or a
 *     tool call the stream shows rejected): OpenCode ends the run and exits
 *     0, so the stage reads as a success unless this says otherwise (ADR-022
 *     § 9). The failure leads with `[adapter-permission-rejected] tool=<p>`, or
 *     `[permission-denied] tool=<p>` for a tool the stage was not allowed;
 *   - the stream carried an error event;
 *   - it exited 0 without a single step_finish, which recorded no usage.
 *
 * Every failure text is redacted, and holds OpenCode's own stderr, never the
 * model's text or a rejected call's input. Stdout is redacted line by line
 * before it is parsed, as the Go manager redacts it before streaming it, so
 * nothing read from it (the display text, an error event's name, an unknown
 * event type) carries a secret the child held or a credential it printed.
 */
export async function classifyOpenCodeRun(input: OpenCodeRunInput): Promise<OpenCodeRunSummary> {
  const redact = input.redact ?? redactOpenCodeCredentials;
  const drift = new OpenCodeDriftLog();
  const stdout = input.stdout
    .split("\n")
    .map((line) => redactOpenCodeLine(line, redact))
    .join("\n");
  const stream = parseOpenCodeStream(stdout, drift);
  // Once more over the joined text, for a secret split across two text parts.
  stream.displayText = redact(stream.displayText);
  const stderr = observeOpenCodeStderr(input.stderr, input.allowedTools, drift);

  const fold: OpenCodeFoldResult = input.fold
    ? await foldOpenCodeSessions(input.fold, stream.sessionId, input.dispatched, drift)
    : {
        served: resolveOpenCodeServedModel("", "", input.dispatched),
        children: emptyTokens(),
        childCostUsd: 0,
        partial: false,
      };

  if (stderr.unterminatedNotice) {
    drift.add(
      `an auto-reject notice on stderr did not end with ${JSON.stringify(AUTO_REJECT_END)}`
    );
  }
  let marker = stderr.marker;
  if (stream.rejectedToolCalls > 0 && marker === undefined) {
    // A "deny" match (this map's own rejection shape, ADR-022 § 9's
    // "pattern matching" amendment) never prints the "auto-rejecting"
    // notice a stderr-only read depends on. AC3 (#1638 fix round): the
    // rejected tool_use event's own part.tool names the marker instead of
    // "unknown", mapped through openCodeToolRejectionPermission
    // (write/apply_patch -> edit); an event that named no tool at all
    // still falls back to "unknown".
    drift.add(
      "the stream shows a tool call OpenCode rejected, but stderr carried no auto-reject line naming its permission; classified from the rejected tool_use event's own tool name"
    );
    const tool =
      stream.rejectedTool !== undefined
        ? openCodeToolRejectionPermission(stream.rejectedTool)
        : "unknown";
    marker = rejectionMarker(tool, openCodeToolsAllowed(input.allowedTools));
  }
  const noSteps = input.exitCode === 0 && stream.stepFinishes === 0;
  if (noSteps) {
    drift.add("the run exited 0 without a step_finish event, so it recorded no token usage");
  }

  const tokens = emptyTokens();
  addTokens(tokens, stream.tokens);
  addTokens(tokens, fold.children);
  const served = fold.served;
  const costUsd = openCodeStageCostUsd(served?.model ?? input.dispatched, {
    input: tokens.input,
    output: tokens.output + tokens.reasoning,
    cacheRead: tokens.cacheRead,
    cacheWrite: tokens.cacheWrite,
  });
  const driftMarkers = drift.list();

  const tail = stderrTail(stderr.kept, redact);
  const withTail = (head: string) => (tail ? `${head}\n${tail}` : head);
  let failure: string | undefined;
  if (input.exitCode !== 0) {
    failure = withTail(`opencode runner command failed (exit code ${input.exitCode})`);
    if (marker) failure += `\n${marker}`;
  } else if (marker) {
    failure = withTail(
      `${marker}: OpenCode rejected a permission request on its own and ended the run with exit code 0`
    );
  } else if (stream.errorEvents.length > 0) {
    failure = withTail(
      `${OPENCODE_ERROR_EVENT_MARKER} the stream carried ${stream.errorEvents.length} error event(s): ${stream.errorEvents.join(", ")}`
    );
  } else if (noSteps) {
    failure = withTail(
      `${OPENCODE_DRIFT_MARKER} the run exited 0 without a step_finish event, so it recorded no token usage`
    );
  }

  return {
    stream,
    tokens,
    peakStepInputTokens: stream.peakStepInputTokens,
    sessionId: stream.sessionId,
    served,
    costUsd,
    reportedCostUsd: stream.reportedCostUsd + fold.childCostUsd,
    usagePartial: fold.partial,
    driftMarkers,
    failure,
  };
}
