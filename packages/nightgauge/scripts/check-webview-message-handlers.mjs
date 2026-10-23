#!/usr/bin/env node
// check-webview-message-handlers.mjs — Abort if any webview's extension-host
// message dispatch and its webview-side `vscode.postMessage()` calls
// disagree, in EITHER direction (#752).
//
// Issue #752 found four `case` labels in Dashboard.ts's `handleMessage`
// switch (`retryHealthTab`, `retryRunsTab`, `retryTrendsTab`,
// `retryComplianceTab`) that no webview script ever posted — a second,
// parallel retry mechanism left behind when the wired path (`healthRefresh`,
// `runsRefresh`, `trendsRefresh`, `complianceRefresh`) was built instead. The
// scan that found them is mechanical and belongs in CI, generalized to every
// webview in the extension, not just the dashboard — and checked in both
// directions, since a message the webview posts with no handler is the same
// bug shape (the click silently does nothing) as a handler nothing reaches.
//
// ── Extraction strategy ──────────────────────────────────────────────────
//
// Every webview panel in this codebase (verified across all 18 as of #1199)
// routes `panel.webview.onDidReceiveMessage` to a member literally named
// `handleMessage` — either defined inline in the panel class, or in a
// sibling `*MessageHandler.ts` class the panel delegates to
// (`this.messageHandler.handleMessage`), and declared either as a **method**
// (`handleMessage(...) { ... }`) or an **arrow-function class property**
// (`handleMessage = (...) => { ... }`, needed so `this` stays bound when the
// function is torn off and handed to `onDidReceiveMessage` by reference — see
// OutputWindowMessageHandler.ts, SettingsMessageHandler.ts,
// TelemetrySettingsMessageHandler.ts, NotifierSettingsMessageHandler.ts,
// invisible to this script before #1199). That naming convention is what
// makes generic extraction possible without a hand-maintained per-webview
// registry: find either declaration form, take its balanced-brace body, and
// read the dispatch out of it — a top-level `switch (x.type) { case "...":
// }`, or an if/else chain of `x.type === "..."` comparisons.
//
// A third guard (`HANDLE_MESSAGE_DECL_RE`) scans for *any* declaration-shaped
// `handleMessage` occurrence — modifiers/type-annotation-agnostic — so that a
// declaration form neither extractor recognises is reported as UNPARSED
// rather than silently dropping the whole webview out of coverage the way
// the arrow-property form did pre-#1199 (`handled.size === 0` skip below).
//
// Scoping extraction to the `handleMessage` body (rather than scanning the
// whole file for any `case "...":`) is what avoids the false positives a
// naive scan hits: Dashboard.ts's `handleFirewallFilter` and `handleExport`
// are separate methods with their own `switch (filter)` / `switch (format)`
// blocks whose case labels (`category`, `eventType`, `search`, `timeRange`,
// `json`, `csv-runs`, `csv-stages`) are object-property matches, not message
// types — a whole-file scan for `case "...":` treats them identically to a
// real dispatch case and reports eight phantom "orphans" on top of the four
// real ones. They sit outside `handleMessage`'s brace range and are
// invisible to this script for that reason, not because of a name denylist.
//
// A webview-side `switch (message.type)` (e.g. inside SettingsHtml.ts /
// OutputWindowHtml.ts's embedded `window.addEventListener('message', ...)`
// receiver, handling EXTENSION → WEBVIEW messages) is excluded the same
// way: nothing in an `*Html.ts` file's embedded script literally calls
// `onDidReceiveMessage(` (that is an extension-host-only API), so no
// `handleMessage` match is ever found there.
//
// Posted types are read from every `.ts` file in the group: a literal
// `vscode.postMessage({ type: "..." })` call, or (dashboard-specific) a
// `getPlatformRetryButtonHtml(id, { type: "..." })` call — the shared retry
// button helper posts its message via `vscode.postMessage(JSON.parse(...))`
// at runtime, so the literal type only appears at the call site that BUILDS
// the button, not at the generic `postMessage` call that fires it.
//
// ── Grouping ────────────────────────────────────────────────────────────
//
// Files are grouped by their directory under `src/`: everything under
// `src/views/<name>/**` (recursively, so `views/dashboard/tabs/**` joins
// `views/dashboard`) forms one webview's file set. This is what lets a tab
// module post a message a sibling panel file handles, and what keeps one
// webview's types from being diffed against an unrelated webview's posts.
//
// ── Known limitation ────────────────────────────────────────────────────
//
// A `type:` value that is a variable rather than a string literal (e.g.
// `postMessage({ type: someVar })`) cannot be read statically and
// contributes nothing to either side — silent under-detection, never a
// false positive, in keeping with this script's priority (a check that
// cries wolf gets disabled; see #752 and the #539/#549 lesson elsewhere in
// this scripts/ directory).
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join, relative, sep } from "node:path";

const SCRIPT_DIR = dirname(fileURLToPath(import.meta.url));
const PACKAGE_DIR = join(SCRIPT_DIR, "..");
const SRC_DIR = join(PACKAGE_DIR, "src");

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

function walk(dir, out = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name.startsWith(".")) continue;
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(full, out);
    } else if (entry.isFile() && entry.name.endsWith(".ts")) {
      out.push(full);
    }
  }
  return out;
}

function groupKeyFor(file) {
  const rel = relative(SRC_DIR, file);
  const parts = rel.split(sep);
  if (parts[0] === "views" && parts.length > 1) {
    return `views/${parts[1]}`;
  }
  return parts[0];
}

// ---------------------------------------------------------------------------
// String-aware brace/paren balancing — skips over '...' / "..." / `...`
// bodies (respecting backslash escapes) so a stray brace or paren inside a
// string literal never desyncs the scan.
// ---------------------------------------------------------------------------

/** Starting at `openIdx` (the index of the opening char), return the index
 * one past the matching close char, or -1 if unbalanced. Treats string
 * literals AND `//` / `/* *\/` comments as opaque — otherwise a stray
 * apostrophe in an English comment (e.g. "doesn't") reads as opening a
 * string, desyncs the scan, and the whole method silently drops out of
 * extraction (caught empirically: it zeroed out every "handled" type in
 * Dashboard.ts on first run, because its handleMessage body contains
 * exactly one such comment). */
function findMatchingClose(text, openIdx, openChar, closeChar) {
  let depth = 0;
  let inString = null; // one of ' " ` or null
  for (let i = openIdx; i < text.length; i++) {
    const c = text[i];
    if (inString) {
      if (c === "\\") {
        i++; // skip escaped char
      } else if (c === inString) {
        inString = null;
      }
      continue;
    }
    if (c === "/" && text[i + 1] === "/") {
      const nl = text.indexOf("\n", i);
      i = nl === -1 ? text.length : nl;
      continue;
    }
    if (c === "/" && text[i + 1] === "*") {
      const close = text.indexOf("*/", i + 2);
      i = close === -1 ? text.length : close + 1;
      continue;
    }
    if (c === "'" || c === '"' || c === "`") {
      inString = c;
      continue;
    }
    if (c === openChar) depth++;
    else if (c === closeChar) {
      depth--;
      if (depth === 0) return i + 1;
    }
  }
  return -1;
}

// ---------------------------------------------------------------------------
// handleMessage body extraction
// ---------------------------------------------------------------------------

// Method declaration: `handleMessage(...) { ... }` / `async handleMessage(...): T { ... }`
const HANDLE_MESSAGE_METHOD_RE =
  /(?:private|public|protected)?\s*(?:static\s+)?(?:async\s+)?handleMessage\s*\([^)]*\)\s*(?::\s*[^{]+)?\{/g;

// Arrow-function class property: `handleMessage = (...) => { ... }` /
// `handleMessage = async (...): Promise<T> => { ... }`. This is the
// declaration form the pre-#1199 extractor never matched (#1199).
const HANDLE_MESSAGE_ARROW_RE =
  /(?:private|public|protected)?\s*(?:readonly\s+)?(?:static\s+)?handleMessage\s*(?::\s*[^=]+)?=\s*(?:async\s+)?\([^)]*\)\s*(?::\s*[^=]+)?=>\s*\{/g;

// Any declaration-shaped `handleMessage` occurrence, regardless of whether
// either extractor above recognises it — used only to detect a declaration
// form neither pattern parses, so it is reported as unparsed instead of
// silently vanishing (see `findUnparsedDeclarations` and #1199). Excludes a
// `.handleMessage` reference/call (e.g. `this.handleMessage(msg)`,
// `handler.handleMessage`) via the negative lookbehind, and excludes a
// longer identifier merely containing the substring (e.g.
// `handleMessageSentFeedback`) via the leading/trailing word boundaries.
const HANDLE_MESSAGE_DECL_RE = /(?<![.\w])handleMessage\b\s*[:=(]/g;

function extractHandleMessageBodies(text) {
  const bodies = [];
  const matchedRanges = []; // [start, end) covered by a recognised declaration + body
  for (const re of [HANDLE_MESSAGE_METHOD_RE, HANDLE_MESSAGE_ARROW_RE]) {
    re.lastIndex = 0;
    let m;
    while ((m = re.exec(text))) {
      const openBraceIdx = m.index + m[0].length - 1;
      const endIdx = findMatchingClose(text, openBraceIdx, "{", "}");
      if (endIdx === -1) continue;
      bodies.push(text.slice(openBraceIdx + 1, endIdx - 1));
      matchedRanges.push([m.index, endIdx]);
      re.lastIndex = endIdx;
    }
  }
  return { bodies, matchedRanges };
}

/** Declaration-shaped `handleMessage` occurrences not covered by either
 * extraction pattern's matched range — a form the guard cannot parse. Returns
 * 1-based line numbers for the report. */
function findUnparsedDeclarations(text, matchedRanges) {
  const lines = [];
  HANDLE_MESSAGE_DECL_RE.lastIndex = 0;
  let m;
  while ((m = HANDLE_MESSAGE_DECL_RE.exec(text))) {
    const idx = m.index;
    const covered = matchedRanges.some(([start, end]) => idx >= start && idx < end);
    if (!covered) {
      lines.push(text.slice(0, idx).split("\n").length);
    }
  }
  return lines;
}

const CASE_RE = /case\s+(['"])((?:\\.|(?!\1).)*)\1\s*:/g;
const TYPE_EQ_RE =
  /(?:[\w.]+\.type\s*===?\s*(['"])((?:\\.|(?!\1).)*)\1)|(?:(['"])((?:\\.|(?!\3).)*)\3\s*===?\s*[\w.]+\.type)/g;

function extractHandledTypes(body) {
  const types = new Set();
  CASE_RE.lastIndex = 0;
  let m;
  while ((m = CASE_RE.exec(body))) types.add(m[2]);
  TYPE_EQ_RE.lastIndex = 0;
  while ((m = TYPE_EQ_RE.exec(body))) types.add(m[2] ?? m[4]);
  return types;
}

// ---------------------------------------------------------------------------
// Posted-type extraction
// ---------------------------------------------------------------------------

function extractTypeFromCallArgs(text, callOpenParenIdx) {
  const endIdx = findMatchingClose(text, callOpenParenIdx, "(", ")");
  if (endIdx === -1) return null;
  const args = text.slice(callOpenParenIdx + 1, endIdx - 1);
  const m = /type\s*:\s*(['"])((?:\\.|(?!\1).)*)\1/.exec(args);
  return m ? m[2] : null;
}

function extractPostedTypes(text) {
  const types = new Set();
  for (const needle of ["vscode.postMessage(", "getPlatformRetryButtonHtml("]) {
    let idx = text.indexOf(needle);
    while (idx !== -1) {
      const type = extractTypeFromCallArgs(text, idx + needle.length - 1);
      if (type) types.add(type);
      idx = text.indexOf(needle, idx + needle.length);
    }
  }
  return types;
}

// ---------------------------------------------------------------------------
// Pre-existing violations
// ---------------------------------------------------------------------------

// This guard was added by #752, scoped to the dashboard webview. Running it
// against every webview (as the issue asks) also surfaced one pre-existing
// orphan in a webview #752 has no license to touch: PipelineSummary.ts's
// `handleMessage` has a `case "close":` branch that PipelineSummaryHtml.ts
// never posts (its own comment says panel-close is "handled by extension" —
// this looks like genuinely dead code, not a design decision). Fixing an
// unrelated webview here would violate the concurrent-work-conflict-free
// rule (AGENTS.md), so it is allowlisted instead of silently dropped or
// blocking this PR. A ratchet, not a permanent exemption — remove the entry
// the moment its webview is fixed (tracking: file a follow-up issue for
// "PipelineSummary.ts's close message handler is never posted" if one does
// not already exist).
const KNOWN_PRE_EXISTING_ORPHANS = new Set(["views/summary::handler::close"]);

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function analyze(files) {
  /** @type {Map<string, {handled: Map<string,string[]>, posted: Map<string,string[]>, unparsed: string[]}>} */
  const groups = new Map();

  for (const file of files) {
    const key = groupKeyFor(file);
    if (!groups.has(key)) {
      groups.set(key, { handled: new Map(), posted: new Map(), unparsed: [] });
    }
    const group = groups.get(key);
    const text = readFileSync(file, "utf8");
    const relFile = relative(PACKAGE_DIR, file);

    const { bodies, matchedRanges } = extractHandleMessageBodies(text);
    for (const body of bodies) {
      for (const type of extractHandledTypes(body)) {
        if (!group.handled.has(type)) group.handled.set(type, []);
        group.handled.get(type).push(relFile);
      }
    }
    for (const line of findUnparsedDeclarations(text, matchedRanges)) {
      group.unparsed.push(`${relFile}:${line}`);
    }
    for (const type of extractPostedTypes(text)) {
      if (!group.posted.has(type)) group.posted.set(type, []);
      group.posted.get(type).push(relFile);
    }
  }

  return groups;
}

function main() {
  const files = walk(SRC_DIR);
  const groups = analyze(files);

  let failed = false;
  const report = [];
  const allowlistedNotes = [];

  for (const [key, { handled, posted, unparsed }] of [...groups.entries()].sort()) {
    // A group with no recognised handler AND no unparsed declaration really
    // has no webview to check. A group with an unparsed declaration is
    // reported below even when `handled` is otherwise empty — that is
    // exactly the "lost a webview without saying so" failure #1199 fixed:
    // silently treating it as "not a webview" is what let four go blind.
    if (handled.size === 0 && unparsed.length === 0) continue;

    let headerPrinted = false;
    if (unparsed.length > 0) {
      failed = true;
      report.push(`\n${key}:`);
      headerPrinted = true;
      for (const loc of unparsed.sort()) {
        report.push(
          `  UNPARSED handleMessage: ${loc} declares \`handleMessage\` in a form this guard cannot extract (neither method nor arrow-property syntax matched). Fix the guard's extraction patterns, not this webview — losing coverage here defeats the whole check.`
        );
      }
    }

    const orphanedHandlers = [...handled.keys()]
      .filter((t) => !posted.has(t))
      .filter((t) => {
        if (KNOWN_PRE_EXISTING_ORPHANS.has(`${key}::handler::${t}`)) {
          allowlistedNotes.push(`  (allowlisted) ${key}: handler "${t}" has no poster`);
          return false;
        }
        return true;
      })
      .sort();
    const unhandledPosts = [...posted.keys()]
      .filter((t) => !handled.has(t))
      .filter((t) => {
        if (KNOWN_PRE_EXISTING_ORPHANS.has(`${key}::post::${t}`)) {
          allowlistedNotes.push(`  (allowlisted) ${key}: post "${t}" has no handler`);
          return false;
        }
        return true;
      })
      .sort();

    if (orphanedHandlers.length === 0 && unhandledPosts.length === 0) continue;

    failed = true;
    if (!headerPrinted) report.push(`\n${key}:`);
    for (const type of orphanedHandlers) {
      report.push(
        `  ORPHANED HANDLER: "${type}" is handled in ${handled.get(type).join(", ")} but no webview script posts it.`
      );
    }
    for (const type of unhandledPosts) {
      report.push(
        `  UNHANDLED POST: "${type}" is posted from ${posted.get(type).join(", ")} but no handleMessage() handles it.`
      );
    }
  }

  if (failed) {
    console.error("ERROR: webview message handler / poster mismatch found:");
    console.error(report.join("\n"));
    console.error(
      "\nEach webview's handleMessage() case/branch set and its scripts' vscode.postMessage() calls must match exactly."
    );
    console.error(
      "Delete the unused side (per the no-backwards-compatibility rule — never keep both), or wire the missing one up."
    );
    console.error("\nRECOVERABLE: orphaned_message_handler");
    process.exit(1);
  }

  if (allowlistedNotes.length > 0) {
    console.log(
      "Allowlisted pre-existing findings (not blocking, see KNOWN_PRE_EXISTING_ORPHANS):"
    );
    console.log(allowlistedNotes.join("\n"));
  }
  console.log(
    `✓ ${groups.size} directories scanned; every non-allowlisted webview message handler and post matches.`
  );
  process.exit(0);
}

main();
