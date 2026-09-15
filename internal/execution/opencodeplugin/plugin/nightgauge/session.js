// session.js gives an OpenCode stage the same session-lifecycle coverage
// Claude Code's hooks give a stage (#1641): compaction context re-injection,
// post-compaction auto-continue suppression, idle stop-verification,
// skill-usage telemetry and a rate-limited permission-ask notification. Every
// export here is OPTIONAL from nightgauge.js's point of view
// (optionalHooks): a missing or throwing function here degrades that one
// hook to a no-op, never breaks plugin load the way gates.js's mandatory
// import does — this module observes and records, it is never a gate.
//
// Every spawn below is bounded (SPAWN_TIMEOUT_MS) and its result is never
// allowed to change what the tool call, permission decision or model turn
// does: a hung, missing or failing verb degrades to "nothing recorded",
// never a throw. Model-authored content never reaches a spawned verb's argv;
// every payload travels as stdin JSON, exactly as gates.js's careful-gate
// already does.
import fs from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

const SPAWN_TIMEOUT_MS = 5000;

// EVENTS_MAX_BYTES mirrors opencodeplugin.eventsMaxBytes (events.go): once
// the events file reaches this size, one "truncated" line is appended and
// every further write this process makes is skipped.
const EVENTS_MAX_BYTES = 1024 * 1024; // 1 MiB

// PERMISSION_NOTIFY_INTERVAL_MS is the per-session notify throttle: at most
// one desktop notification per session per 60 s, however many times
// permission.ask fires in that window.
const PERMISSION_NOTIFY_INTERVAL_MS = 60000;

// INJECT_CONTEXT_MAX_BYTES bounds what `hook inject-context` may add to a
// compaction prompt (technical notes: "append its JSON (≤4 KB)").
const INJECT_CONTEXT_MAX_BYTES = 4096;

// eventsTruncated is module-level (not per-call) state: Node/Bun evaluates
// this module once per opencode process, so the flag correctly survives
// across every session and hook call that process ever makes, and a second
// "truncated" line is never written once the first one is.
let eventsTruncated = false;

// lastNotifyAt tracks, per sessionID, the epoch ms of this process's last
// notify spawn — the whole of the 60 s per-session throttle.
const lastNotifyAt = new Map();

// childSessionIDs is every session id this process has ever observed with a
// non-empty session.info.parentID, learned from session.created/
// session.updated events (1.18.30's Session.Info carries an optional
// parentID: observed directly on the bundled client bundle's own
// `session.updated` reducer and `experimental.session.list({roots: ...})`
// call). Module-level for the same reason as eventsTruncated/lastNotifyAt: it
// must survive across every hook call this one opencode process makes.
// gates.js denies the `task` tool unconditionally (ADR-022, AC9), so no
// child session is known to reach any hook this plugin registers today —
// this set is expected to stay empty in production until that denial lifts,
// and every appendEvent call below still reads it rather than hard-coding
// false, so child reporting is correct the day it does.
const childSessionIDs = new Set();

// SKILL_ID_RE bounds what toolExecuteBefore ever treats as a skill id: opaque
// identifiers only (the "skill" tool's own schema field), never a sentence.
// A model-authored value that fails this (an object, or free text) is never
// recorded verbatim — see toolExecuteBefore's own comment.
const SKILL_ID_RE = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;

// isChildSession reports whether sessionID was ever seen with a non-empty
// parentID.
function isChildSession(sessionID) {
  return !!sessionID && childSessionIDs.has(sessionID);
}

// trackSessionParentage records session.created/session.updated's own
// parentage, when present, so later events for the same sessionID can be
// tagged as a child session rather than always reporting false.
function trackSessionParentage(props) {
  const info = props && props.info;
  if (info && info.id && info.parentID) {
    childSessionIDs.add(info.id);
  }
}

function resolveCwd(ctx) {
  return (ctx && (ctx.directory || ctx.worktree)) || process.cwd();
}

// resolveNightgaugeBin returns NIGHTGAUGE_BIN only when it is an absolute
// path, the same closed-by-default rule gates.js's careful-gate uses: a
// relative or unset value means nothing here is ever spawned.
function resolveNightgaugeBin() {
  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") return null;
  return bin;
}

// runHook spawns bin with args, feeding stdinPayload (a string, or null for
// no stdin) and returns {ok, stdout} — ok is false on any spawn error,
// signal, timeout or non-zero exit, in which case stdout is always "". Every
// caller below treats a not-ok result as "nothing to record", never as a
// reason to throw: this module is telemetry, not a gate.
function runHook(bin, args, cwd, stdinPayload) {
  const result = spawnSync(bin, args, {
    input: stdinPayload == null ? undefined : stdinPayload,
    cwd,
    timeout: SPAWN_TIMEOUT_MS,
    shell: false,
    encoding: "utf8",
  });
  if (result.error || result.signal || result.status !== 0) {
    return { ok: false, stdout: "" };
  }
  return { ok: true, stdout: (result.stdout || "").trim() };
}

// eventsPath mirrors opencodeplugin.EventsPath (events.go) byte for byte: a
// relative or ".."-bearing NIGHTGAUGE_OUTPUT_FILE, or a missing run id,
// disables the file rather than writing elsewhere. Both env vars are the
// same ones every adapter already sets on a dispatch (adapters.RunIDEnvVar,
// opts.OutputFile) — no new plumbing is added for this.
function eventsPath() {
  const outputFile = process.env.NIGHTGAUGE_OUTPUT_FILE;
  const runID = process.env.NIGHTGAUGE_RUN_ID;
  if (!outputFile || !runID) return null;
  if (outputFile[0] !== "/") return null;
  for (const part of outputFile.split("/")) {
    if (part === "..") return null;
  }
  return path.join(path.dirname(outputFile), `opencode-events-${runID}.jsonl`);
}

// appendEvent writes one JSONL line: {v, ts, kind, session_id, child,
// detail}. detail must hold only ids, counts and verdict codes — never
// transcript, summary, tool output or prompt text (the file's own retention
// contract; internal/execution/opencodeplugin/events.go's reader drops any
// line that violates it anyway, as a second line of defence). A write
// failure of any kind is swallowed: telemetry never surfaces to the model.
function appendEvent(kind, sessionID, child, detail) {
  const target = eventsPath();
  if (!target || eventsTruncated) return;
  try {
    let size = 0;
    try {
      size = fs.statSync(target).size;
    } catch {
      size = 0;
    }
    if (size >= EVENTS_MAX_BYTES) {
      eventsTruncated = true;
      try {
        fs.appendFileSync(
          target,
          JSON.stringify({ v: 1, ts: new Date().toISOString(), kind: "truncated" }) + "\n",
          { mode: 0o600 }
        );
      } catch {
        // best effort
      }
      return;
    }
    const line =
      JSON.stringify({
        v: 1,
        ts: new Date().toISOString(),
        kind,
        session_id: sessionID || "",
        child: !!child,
        detail: detail || {},
      }) + "\n";
    fs.appendFileSync(target, line, { mode: 0o600 });
  } catch {
    // best effort
  }
}

// sessionCompacting re-injects this stage's own context (branch, last
// commit, plan progress) into the compaction prompt via the same `hook
// inject-context` verb the Claude Code SessionStart hook runs
// (internal/hooks/context.go). It only ever appends to output.context
// (opencode's own "additional context strings" channel, observed on
// 1.18.30's @opencode-ai/plugin types: experimental.session.compacting's
// output is {context: string[], prompt?: string}) — output.prompt, which
// would replace the default compaction prompt outright, is never touched.
// Any failure (no NIGHTGAUGE_BIN, a non-zero exit, a timeout, output over
// INJECT_CONTEXT_MAX_BYTES, output that is not JSON) is skipped silently:
// compaction proceeds with no added context rather than failing.
export async function sessionCompacting(ctx, input, output) {
  if (!output) return;
  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const { ok, stdout } = runHook(bin, ["hook", "inject-context", "--workdir", cwd], cwd, null);
  if (!ok || stdout === "" || stdout.length > INJECT_CONTEXT_MAX_BYTES) return;
  try {
    JSON.parse(stdout); // validate before injecting
  } catch {
    return;
  }
  if (!Array.isArray(output.context)) output.context = [];
  output.context.push(stdout);
}

// compactionAutocontinue always disables the synthetic "Continue if you have
// next steps..." user turn opencode 1.18.30 otherwise adds once a
// compaction succeeds (ADR-022): the session ends at idle instead, and
// unfinished work surfaces through the existing Go stage gates and #1643's
// resume/retry path, never as a silent continuation a $0 local run could
// loop on for hours with every USD guardrail inert. #1625's declared steps
// cap is NOT itself a hard stop underneath this on 1.18.30 — a step at or
// past the cap still executes a tool call rather than ending the turn (only
// an extra assistant nudge message is added); ADR-022's "Session-lifecycle
// events plugin" amendment measures this directly (13 loop steps against a
// declared cap of 8). So this suppression, not the steps cap, is what
// actually bounds a compacted session's runtime. There is nothing to spawn
// here, only a value to set, so this cannot itself hang or fail.
export async function compactionAutocontinue(ctx, input, output) {
  if (output) output.enabled = false;
}

// shouldNotify enforces the per-session, per-60s notify throttle: the first
// call for a session always notifies; every call inside the following 60s
// does not, whatever else it does.
function shouldNotify(sessionID) {
  const now = Date.now();
  const last = lastNotifyAt.get(sessionID) || 0;
  if (now - last < PERMISSION_NOTIFY_INTERVAL_MS) return false;
  lastNotifyAt.set(sessionID, now);
  return true;
}

// permissionAsk is the plugin's `permission.ask` hook contribution, kept for
// forward compatibility only: empirically, opencode 1.18.30 never calls this
// hook at all (0 occurrences of the literal "permission.ask" in the pinned
// binary's own trigger sites; permissions are published only as the bus
// event "permission.asked", handled in event() below). It records the ask in
// the events file and, at most once per session per 60 s, runs `hook notify`
// to alert the operator. It never reads or writes output.status: the
// permission decision is opencode's own to make, and this hook only
// observes it.
export async function permissionAsk(ctx, input, output) {
  if (!input) return;
  const sessionID = input.sessionID || "";
  appendEvent(
    "permission_ask",
    sessionID,
    isChildSession(sessionID),
    input.type ? { permission_type: input.type } : {}
  );
  notifyOnce(ctx, sessionID, input.type);
}

// notifyOnce runs `hook notify` at most once per sessionID per 60 s (the
// shared throttle both the permission.ask export and event()'s own
// permission.asked handling apply).
function notifyOnce(ctx, sessionID, permissionType) {
  if (!shouldNotify(sessionID)) return;
  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const message = "Nightgauge: permission requested (" + (permissionType || "unknown") + ")";
  runHook(
    bin,
    ["hook", "notify", "--event", "permission_prompt"],
    cwd,
    JSON.stringify({ message })
  );
}

// event handles the session lifecycle events this module cares about:
// session.created/session.updated (track parentage only, never recorded
// directly), session.compacted (a compaction just succeeded: record one
// "compaction" event), session.idle (the session went idle: record one
// "idle" event, then run `hook stop-verify` and record its verdict as a
// "stop_verify" event), and permission.asked — opencode 1.18.30's own bus
// event for a permission prompt; the plugin hook named "permission.ask" is
// never called on this binary (see permissionAsk's own comment), so this is
// the path AC5/AC6 actually run on. Every other event type is ignored. This
// function never sends a message into the session — it has no such
// capability — and never re-prompts the model: it only spawns read-only
// verification/notify verbs and records what they returned.
export async function event(ctx, input) {
  const evt = input && input.event;
  if (!evt || typeof evt.type !== "string") return;
  const props = evt.properties || {};

  if (evt.type === "session.created" || evt.type === "session.updated") {
    trackSessionParentage(props);
    return;
  }

  const sessionID = props.sessionID || "";

  if (evt.type === "session.compacted") {
    appendEvent("compaction", sessionID, isChildSession(sessionID), {});
    return;
  }

  if (evt.type === "session.idle") {
    appendEvent("idle", sessionID, isChildSession(sessionID), {});
    const bin = resolveNightgaugeBin();
    let verdict = "no_bin";
    if (bin) {
      const cwd = resolveCwd(ctx);
      const { ok, stdout } = runHook(bin, ["hook", "stop-verify", "--workdir", cwd], cwd, null);
      // EvaluateStopHookOutput's own contract (internal/hooks/stop.go):
      // silent stdout means every task is complete; a non-empty
      // {"decision":"block",...} means it is not. Only the verdict code is
      // ever recorded, never Reason (which can hold plan-derived text).
      verdict = !ok ? "error" : stdout === "" ? "complete" : "blocked";
    }
    appendEvent("stop_verify", sessionID, isChildSession(sessionID), { verdict });
    return;
  }

  if (evt.type === "permission.asked") {
    // 1.18.30's permission.asked properties: {id, sessionID, permission,
    // patterns, metadata, always, tool}. Only `permission` (the permission
    // *type*, e.g. "bash") and sessionID are ever recorded: `patterns` and
    // `metadata` carry the model-authored command text the permission asks
    // about (e.g. a literal shell command), which the events file's own
    // retention contract (never transcript or prompt text) forbids.
    const permissionType = typeof props.permission === "string" ? props.permission : "";
    appendEvent(
      "permission_ask",
      sessionID,
      isChildSession(sessionID),
      permissionType ? { permission_type: permissionType } : {}
    );
    notifyOnce(ctx, sessionID, permissionType);
  }
}

// toolExecuteBefore is this module's own tool.execute.before contribution:
// skill-usage telemetry for the native "skill" tool. nightgauge.js's shared
// tool.execute.before handler calls this AFTER gates.js's mandatory
// toolExecuteBefore, so a throw here would still leave the careful gate's
// own decision already applied — but this never throws regardless (fail-
// open, telemetry only: technical notes, "asynchronous and fail-open").
// Every other tool is untouched.
//
// tool.execute.before fires on the model's raw arguments, before opencode's
// own skill tool decodes its schema — so a confused or adversarial model can
// put anything there, including nested objects or free-text sentences (see
// the events file's own retention contract: no transcript or prompt text,
// ever). `name` is read first (the skill tool's own schema field on 1.18.30;
// `skill`/`id` are read only as a fallback for an older or divergent
// shape), and is accepted only when it is a string matching SKILL_ID_RE — an
// opaque identifier, never a sentence. Anything else records
// {skill_invalid:true} instead of the raw value, and never reaches `hook
// skill-usage`'s stdin either.
export async function toolExecuteBefore(ctx, input, output) {
  if (!input || input.tool !== "skill") return;
  const args = (output && output.args) || {};
  const sessionID = input.sessionID || "";
  const child = isChildSession(sessionID);
  const raw = args.name !== undefined ? args.name : args.skill !== undefined ? args.skill : args.id;
  if (raw === undefined || raw === null || raw === "") return;

  if (typeof raw !== "string" || !SKILL_ID_RE.test(raw)) {
    appendEvent("skill", sessionID, child, { skill_invalid: true });
    return;
  }
  const skill = raw;

  appendEvent("skill", sessionID, child, { skill });

  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const payload = JSON.stringify({ tool_name: "Skill", cwd, tool_input: { skill } });
  runHook(bin, ["hook", "skill-usage"], cwd, payload);
}
