// session.js gives an OpenCode stage the same session-lifecycle coverage
// Claude Code's hooks give a stage (#1641), on the OpenCode 2.x plugin API
// (#3302): compaction context re-injection, end-of-run stop-verification,
// skill-usage telemetry, a rate-limited permission-ask notification and a
// streaming heartbeat. Every export here is OPTIONAL from nightgauge.js's
// point of view (optionalModule): a missing or throwing function degrades
// that one hook to a no-op, never breaks plugin load the way gates.js's
// mandatory import does — this module observes and records, it is never a
// gate.
//
// Every spawn below is bounded — by SPAWN_TIMEOUT_MS here, or, for the one
// verb spawned on a terminal event and deliberately let go of
// (runHookDetached), by that verb's own Go-side bound — and its result never
// changes what the tool call or model turn does: a hung, missing or failing
// verb degrades to "nothing recorded", never a throw. Model-authored content
// never reaches a spawned verb's argv; every payload travels as stdin JSON.
//
// runHook is async (node:child_process spawn, not spawnSync) on purpose
// (#1641): opencode is a single-threaded event loop process, and spawnSync
// blocks every other session's tool calls, timers and bus events for as long
// as the child runs.
//
// The #1810 un-awaited-hook workaround, revisited on 2.x (#3302):
//
//   - On 1.18.30 OpenCode did not await a hook's promise, so the tool-hook
//     verbs were left un-awaited and only .catch()-guarded. 2.x awaits
//     execute.before (spike #3197 § 4: each tool ran only after its
//     before-hook's 1.5 s delay ended), so toolExecuteBefore now awaits its
//     skill-usage spawn, which SPAWN_TIMEOUT_MS bounds. That part of the
//     workaround is removed. The permission notify stays un-awaited for a
//     different reason: events are handled one at a time (notifyOnce).
//
//   - runHookDetached is KEPT, for the stop-verify verb spawned on a
//     session's terminal event (session.execution.succeeded or .failed).
//     Events reach this module through an async-iterable subscription that
//     nothing awaits, and a one-shot `opencode run` shuts its location down
//     right after the last session ends (probe: the subscription's stream
//     closed ~200 ms after session.execution.succeeded). Whether or not a
//     hook is awaited, this process may be gone before a child it waits on
//     finishes, so the child is unref'd, writes its OWN result line into the
//     run's events file and enforces its OWN bound. See event().
import fs from "node:fs";
import path from "node:path";
import { spawn } from "node:child_process";
import { gateCwd } from "./gates.js";

const SPAWN_TIMEOUT_MS = 5000;

// EVENTS_MAX_BYTES mirrors opencodeplugin.eventsMaxBytes (events.go): once
// the events file reaches this size, one "truncated" line is appended and
// every further write this process makes is skipped.
const EVENTS_MAX_BYTES = 1024 * 1024; // 1 MiB

// PERMISSION_NOTIFY_INTERVAL_MS is the per-session notify throttle: at most
// one desktop notification per session per 60 s, however many times
// permission.asked fires in that window.
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
// non-empty parentID, learned from session.created events (observed on 2.2.7:
// a subagent's session.created data carries sessionID and parentID).
// Module-level for the same reason as eventsTruncated/lastNotifyAt: it must
// survive across every hook call this one opencode process makes. gates.js
// denies the `subagent` tool unconditionally (ADR-022), so no child session
// is known to reach any hook this plugin registers today; every appendEvent
// call below still reads this set rather than hard-coding false, so child
// reporting is correct the day that denial lifts.
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

// trackSessionParentage records session.created's parentage, when present,
// so later events for the same sessionID are tagged as a child session.
function trackSessionParentage(data) {
  if (data && typeof data.sessionID === "string" && data.sessionID && data.parentID) {
    childSessionIDs.add(data.sessionID);
  }
}

// resolveCwd is gates.js's gateCwd: the run's worktree. gates.js is the
// plugin's mandatory module, so importing it here adds no load dependency.
const resolveCwd = gateCwd;

// resolveNightgaugeBin returns NIGHTGAUGE_BIN only when it is an absolute
// path, the same closed-by-default rule gates.js's careful-gate uses: a
// relative or unset value means nothing here is ever spawned.
function resolveNightgaugeBin() {
  const bin = process.env.NIGHTGAUGE_BIN;
  if (!bin || bin[0] !== "/") return null;
  return bin;
}

// runHook spawns bin with args, feeding stdinPayload (a string, or null for
// no stdin) and resolves {ok, stdout} — ok is false on any spawn error,
// signal, timeout or non-zero exit, in which case stdout is always "". Every
// caller below treats a not-ok result as "nothing to record", never as a
// reason to throw: this module is telemetry, not a gate.
//
// The child is spawned detached (POSIX: its own process group, pgid ==
// child.pid) so that on timeout the WHOLE group is killed, not just the
// direct child: `hook notify`'s own osascript/notify-send grandchild (or any
// verb that shells out further) would otherwise survive its parent's SIGKILL
// and keep running past SPAWN_TIMEOUT_MS. Killing `-child.pid` reaches the
// group; killing child.pid alone would not.
function runHook(bin, args, cwd, stdinPayload) {
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(bin, args, {
        cwd,
        stdio: ["pipe", "pipe", "ignore"],
        shell: false,
        detached: true,
      });
    } catch {
      resolve({ ok: false, stdout: "" });
      return;
    }

    let stdout = "";
    let timedOut = false;
    let settled = false;

    const finish = (ok) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ ok, stdout: ok ? stdout.trim() : "" });
    };

    // killGroup reaps the child and every grandchild it spawned: SIGKILL to
    // -pid targets the process group detached:true created, not just the
    // one pid. If the group kill itself fails (e.g. the child already
    // exited between the timer firing and this running), falling back to
    // killing the child directly is still best-effort cleanup, never a
    // throw.
    const killGroup = () => {
      try {
        process.kill(-child.pid, "SIGKILL");
      } catch {
        try {
          child.kill("SIGKILL");
        } catch {
          // best effort: nothing left to reap
        }
      }
    };

    const timer = setTimeout(() => {
      timedOut = true;
      killGroup();
    }, SPAWN_TIMEOUT_MS);

    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    // child.stdout/child.stdin are their own EventEmitters: a pipe error on
    // either (e.g. the child exits or never spawns before this process
    // finishes writing/reading) surfaces as an 'error' event on that stream,
    // not on `child` itself. Node's default behaviour for an unhandled
    // 'error' event is to throw, which crashes this whole opencode process —
    // exactly the EPIPE observed when a child exits (or fails to spawn)
    // before its stdin is read (#1641 fixed forward, second pass). A no-op
    // listener here is enough: the close handler below still resolves this
    // promise from the child's own exit/signal/timeout, so nothing is lost
    // by swallowing the stream-level error itself.
    child.stdin.on("error", () => {});
    child.stdout.on("error", () => {});
    child.on("error", () => finish(false));
    child.on("close", (code, signal) => {
      if (timedOut || signal || code !== 0) {
        finish(false);
      } else {
        finish(true);
      }
    });

    try {
      if (stdinPayload == null) {
        child.stdin.end();
      } else {
        child.stdin.end(stdinPayload, "utf8");
      }
    } catch {
      // A write/end failure on stdin still lets the close handler above
      // resolve this promise from the child's own exit/signal/timeout.
    }
  });
}

// runHookDetached spawns bin with args and immediately lets go of it: its
// own process group (detached), no stdio at all, and unref()'d so this
// process's event loop is never held open by it and it is never killed when
// this process exits. It is for a verb spawned on a TERMINAL event, where
// this process is about to disappear (see runHook's own comment); such a
// verb records its own result and enforces its own bound, because nothing
// here can do either for it.
//
// It returns true when the child was started, and false when it provably was
// not — the ONE failure a caller can still act on synchronously. The
// executable check is deliberately synchronous (accessSync): spawn() reports
// ENOENT/EACCES on a LATER tick as an 'error' event, which on a terminal
// event is a tick this process may never reach, so a caller that needs to
// record "the verb could not run" has to learn it before returning.
function runHookDetached(bin, args, cwd) {
  try {
    fs.accessSync(bin, fs.constants.X_OK);
  } catch {
    return false;
  }
  let child;
  try {
    child = spawn(bin, args, { cwd, stdio: "ignore", shell: false, detached: true });
  } catch {
    return false;
  }
  // Nothing reads this child's outcome, but an unhandled 'error' event would
  // still throw and crash opencode (the #1641 EPIPE lesson): swallow it.
  child.on("error", () => {});
  child.unref();
  return true;
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

// STREAM_HEARTBEAT_FILE is touched in the run's OpenCode data directory
// (XDG_DATA_HOME/opencode, beside opencode.db) whenever a part streams
// (#2184). `opencode run --format json` prints a part only when it completes,
// so the manager's stall watchdog (#2176) could not tell a model slowly
// writing a long block from a dead request. The watchdog reads this file's
// mtime as progress. Mirrors execution.streamHeartbeatFile byte for byte.
export const STREAM_HEARTBEAT_FILE = "nightgauge-stream.heartbeat";

// STREAM_HEARTBEAT_INTERVAL_MS throttles the touch: one write per interval
// however fast tokens arrive.
const STREAM_HEARTBEAT_INTERVAL_MS = 5000;

let lastHeartbeatAt = 0;

// streamHeartbeat touches the heartbeat file, at most once per interval. A
// missing XDG_DATA_HOME or a write failure is swallowed: the watchdog then
// falls back to what it read before.
export function streamHeartbeat(now = Date.now()) {
  if (now - lastHeartbeatAt < STREAM_HEARTBEAT_INTERVAL_MS) return false;
  const dataHome = process.env.XDG_DATA_HOME;
  if (!dataHome || dataHome[0] !== "/") return false;
  lastHeartbeatAt = now;
  try {
    const dir = path.join(dataHome, "opencode");
    fs.mkdirSync(dir, { recursive: true });
    const target = path.join(dir, STREAM_HEARTBEAT_FILE);
    const t = new Date(now);
    try {
      fs.utimesSync(target, t, t);
    } catch {
      fs.writeFileSync(target, "");
      fs.utimesSync(target, t, t);
    }
    return true;
  } catch {
    return false;
  }
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

// sessionCompaction is the ctx.session.hook("compaction") contribution: it
// re-injects this stage's own context (branch, last commit, plan progress)
// into the compaction request via the same `hook inject-context` verb the
// Claude Code SessionStart hook runs (internal/hooks/context.go). It only
// ever appends one text part to arg.system (observed on 2.2.7: a part pushed
// there reaches the compaction request); the existing system parts and the
// messages are never touched. Any failure (no NIGHTGAUGE_BIN, a non-zero
// exit, a timeout, output over INJECT_CONTEXT_MAX_BYTES, output that is not
// JSON) is skipped silently: compaction proceeds with no added context.
//
// 1.x also capped how many times a session auto-continued after compacting
// (experimental.compaction.autocontinue, #2180). No 2.x hook exposes that
// decision (#3302 probe), so the cap is gone with the hook.
export async function sessionCompaction(ctx, arg) {
  if (!arg || !Array.isArray(arg.system)) return;
  const bin = resolveNightgaugeBin();
  if (!bin) return;
  const cwd = resolveCwd(ctx);
  const { ok, stdout } = await runHook(
    bin,
    ["hook", "inject-context", "--workdir", cwd],
    cwd,
    null
  );
  if (!ok || stdout === "" || stdout.length > INJECT_CONTEXT_MAX_BYTES) return;
  try {
    JSON.parse(stdout); // validate before injecting
  } catch {
    return;
  }
  arg.system.push({ type: "text", text: stdout });
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

// notifyOnce runs `hook notify` at most once per sessionID per 60 s,
// fire-and-forget. nightgauge.js handles subscribed events one at a time, so
// awaiting a notify (up to SPAWN_TIMEOUT_MS) would hold every event behind it,
// the session's terminal event included, and a one-shot run can exit before
// that event is handled. SPAWN_TIMEOUT_MS still bounds the child; the trailing
// .catch keeps a spawn error from becoming an unhandled rejection.
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
  ).catch(() => {});
}

// HEARTBEAT_EVENT_TYPES are the 2.x events that mean a model response is
// streaming, each observed on 2.2.7: a text delta, a step's stream opening,
// a tool call's input starting to stream, and a running tool's progress.
const HEARTBEAT_EVENT_TYPES = Object.freeze([
  "session.text.delta",
  "session.step.streamed",
  "session.tool.input.started",
  "session.tool.progress",
]);

// TERMINAL_EVENT_TYPES end a session's execution, each observed on 2.2.7:
// .succeeded on a normal end (once per session, a subagent's included) and
// .failed when the run ended on an error (a failed compaction, for one).
// 2.x has no session.idle; this is where 1.x's idle verification moved.
const TERMINAL_EVENT_TYPES = Object.freeze([
  "session.execution.succeeded",
  "session.execution.failed",
]);

// SUBSCRIBED_EVENT_TYPES is every event type event() reads, the `types`
// filter nightgauge.js subscribes with. Each was observed on 2.2.7 (#3302):
//
//   - session.created: a subagent's session carries parentID.
//   - session.compaction.started: a compaction began. 2.x also names
//     session.compaction.ended, but no probe produced a successful
//     compaction (the stub's summary never matched 2.x's template), so it
//     was never seen firing and a compaction is recorded when it starts.
//   - permission.asked: data {id, sessionID, action, resources, save,
//     source} on a rejected `ask`.
//   - HEARTBEAT_EVENT_TYPES and TERMINAL_EVENT_TYPES above.
const SESSION_CREATED = "session.created";
const COMPACTION_STARTED = "session.compaction.started";
const PERMISSION_ASKED = "permission.asked";
export const SUBSCRIBED_EVENT_TYPES = Object.freeze([
  SESSION_CREATED,
  COMPACTION_STARTED,
  PERMISSION_ASKED,
  ...HEARTBEAT_EVENT_TYPES,
  ...TERMINAL_EVENT_TYPES,
]);

// event handles one 2.x bus event, {id, created, type, location, data}:
// session.created tracks parentage only; session.compaction.started records
// one "compaction" event; a terminal event records one "idle" event, then
// hands a detached `hook stop-verify --emit-event` the job of recording its
// own verdict as a "stop_verify" event, since this process may not be alive
// to do it; permission.asked records the ask and notifies the operator; a
// heartbeat event touches the stream heartbeat. Every other event is ignored.
// This function never sends a message into the session and never re-prompts
// the model.
export async function event(ctx, evt) {
  if (!evt || typeof evt.type !== "string") return;
  const data = evt.data && typeof evt.data === "object" ? evt.data : {};

  if (HEARTBEAT_EVENT_TYPES.includes(evt.type)) {
    streamHeartbeat();
    return;
  }

  if (evt.type === SESSION_CREATED) {
    trackSessionParentage(data);
    return;
  }

  const sessionID = typeof data.sessionID === "string" ? data.sessionID : "";

  if (evt.type === COMPACTION_STARTED) {
    appendEvent("compaction", sessionID, isChildSession(sessionID), {});
    return;
  }

  if (TERMINAL_EVENT_TYPES.includes(evt.type)) {
    appendEvent("idle", sessionID, isChildSession(sessionID), {});
    const bin = resolveNightgaugeBin();
    if (!bin) {
      appendEvent("stop_verify", sessionID, isChildSession(sessionID), { verdict: "no_bin" });
      return;
    }
    const cwd = resolveCwd(ctx);
    const child = isChildSession(sessionID);
    // The verdict is not recorded from here (see the #1810 note at the top
    // of this file). The `hook stop-verify` child is handed --emit-event and
    // writes its OWN stop_verify line into this same events file (Go side:
    // events.go's AppendRunEvent, honouring the same 1 MiB cap and retention
    // contract), from a process that outlives opencode and bounds itself.
    //
    // The session id travels as argv, not stdin, because it is opencode's
    // own opaque id and never model-authored; the Go side re-checks its
    // shape before recording it regardless.
    const args = ["hook", "stop-verify", "--workdir", cwd, "--emit-event"];
    if (sessionID) args.push("--session-id", sessionID);
    if (child) args.push("--child");
    if (!runHookDetached(bin, args, cwd)) {
      // The one outcome this process can still determine synchronously:
      // the verb could not be started, so no child exists to write the line.
      appendEvent("stop_verify", sessionID, child, { verdict: "no_bin" });
    }
    return;
  }

  if (evt.type === PERMISSION_ASKED) {
    // Only `action` (the permission type, e.g. "shell") and sessionID are
    // recorded: `resources` and `save` carry the model-authored command text
    // the permission asks about, which the events file's retention contract
    // (never transcript or prompt text) forbids.
    const permissionType = typeof data.action === "string" ? data.action : "";
    appendEvent(
      "permission_ask",
      sessionID,
      isChildSession(sessionID),
      permissionType ? { permission_type: permissionType } : {}
    );
    notifyOnce(ctx, sessionID, permissionType);
  }
}

// toolExecuteBefore is this module's own execute.before contribution:
// skill-usage telemetry for the native "skill" tool. nightgauge.js calls it
// AFTER gates.js's mandatory toolExecuteBefore, so the gates' decision is
// already applied; this never throws regardless (fail-open, telemetry only).
// Every other tool is untouched.
//
// The hook fires on the model's raw arguments (arg.input), before OpenCode's
// own skill tool decodes its schema, so a confused or adversarial model can
// put anything there, including nested objects or free-text sentences.
// `name` is read first (`skill`/`id` only as a fallback), and is accepted
// only when it is a string matching SKILL_ID_RE. Anything else records
// {skill_invalid:true} instead of the raw value, and never reaches `hook
// skill-usage`'s stdin either.
//
// 2.x awaits this hook, so the skill-usage spawn is awaited too (the #1810
// note at the top of this file); SPAWN_TIMEOUT_MS bounds it, and a not-ok
// result is "nothing recorded".
export async function toolExecuteBefore(ctx, arg) {
  if (!arg || arg.tool !== "skill") return;
  const args = arg.input && typeof arg.input === "object" ? arg.input : {};
  const sessionID = typeof arg.sessionID === "string" ? arg.sessionID : "";
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
  await runHook(bin, ["hook", "skill-usage"], cwd, payload);
}
