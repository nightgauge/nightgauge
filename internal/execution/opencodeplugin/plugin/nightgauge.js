// nightgauge.js is the Nightgauge OpenCode plugin's entry module (#1635).
// Embedded in the Go binary (internal/execution/opencodeplugin/plugin.go)
// and written into the run's per-run OpenCode plugin directory at spawn —
// never installed from npm or Bun — so opencode 1.18.30 loads it as a local
// plugin file named directly in the per-run config's `plugin` array.
//
// It registers every hook once and delegates: `tool.execute.before` to
// ./nightgauge/gates.js, MANDATORY and imported statically, so a broken or
// missing gates.js fails this module's own import. That failure aborts
// plugin init entirely: opencode then runs with the Nightgauge plugin not
// loaded at all, no handshake sentinel is ever written, and the manager's
// handshake check (internal/execution/manager.go) fails the stage closed as
// adapter_incompatible instead of running ungated. `command.execute.before`
// likewise runs gates.js's commandExecuteBefore (#1640's sanitize-prompt
// gate) MANDATORILY, before any optional session.js delegate — a review
// finding this round: this hook used to only delegate to ./nightgauge/
// session.js (#1641's file), so gates.js's sanitize-prompt gate, though
// fully implemented and unit-tested, never actually ran on a real dispatch.
//
// The remaining hooks delegate to ./nightgauge/session.js and
// ./nightgauge/edit.js, which sibling tickets (#1641/#1642) create. A hook
// with no such module installed yet is a pure no-op: optionalHooks() imports
// it dynamically and swallows the failure.
import fs from "node:fs";
import {
  toolExecuteBefore,
  commandExecuteBefore as gatesCommandExecuteBefore,
} from "./nightgauge/gates.js";

// NIGHTGAUGE_PLUGIN_VERSION MUST match opencodeplugin.PluginVersion
// (plugin.go) byte for byte — TestPluginVersionAndHooksMatchGo compares
// them, reading this file's source text with a regex rather than importing
// it, specifically so this constant stays UNEXPORTED: opencode 1.18.30's
// loader accepts a module whose default export is a function, but on any
// other export that is neither a function nor a {server} object it throws
// "Plugin export is not a function" before calling anything in this file —
// exporting this constant (or NIGHTGAUGE_HOOK_NAMES below) reproduces that
// failure on the real binary even though the Node harness (which imports
// this module directly and never goes through the loader) does not catch
// it. See the #1635 fix round and internal/execution/opencodeplugin's new
// opencode_integration test, TestPluginLoadsOnRealOpenCode.
const NIGHTGAUGE_PLUGIN_VERSION = "1";

// NIGHTGAUGE_HOOK_NAMES are every hook this module registers, in
// registration order: the handshake sentinel's "hooks" field, and the value
// opencodeplugin.HookNames (plugin.go) is compared against. Unexported for
// the same reason as NIGHTGAUGE_PLUGIN_VERSION above.
const NIGHTGAUGE_HOOK_NAMES = [
  "tool.execute.before",
  "tool.execute.after",
  "command.execute.before",
  "permission.ask",
  "event",
  "experimental.session.compacting",
  "experimental.compaction.autocontinue",
];

// optionalHooks loads a sibling module that may not exist yet. A missing or
// broken module is a pure no-op for every hook it would have supplied —
// never a load failure of this plugin.
async function optionalHooks(specifier) {
  try {
    const mod = await import(specifier);
    return mod && typeof mod === "object" ? mod : {};
  } catch {
    return {};
  }
}

// writeHandshakeSentinel writes the handshake sentinel the manager verifies
// (opencodeplugin.VerifyLoaded/VerifyNotLate): the run's own nonce and this
// plugin's version, at the path opencode.go computed and exported as
// NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL — a single Go-side formula, read back
// here rather than recomputed, so the two languages can never disagree on
// where the file goes. Outside a Nightgauge run (no sentinel path or nonce
// exported) this is a no-op: nothing here writes a handshake nobody asked
// for.
function writeHandshakeSentinel() {
  const sentinelPath = process.env.NIGHTGAUGE_OPENCODE_PLUGIN_SENTINEL;
  const nonce = process.env.NIGHTGAUGE_OPENCODE_PLUGIN_NONCE;
  if (!sentinelPath || !nonce) return;
  const sentinel = {
    nonce,
    plugin_version: NIGHTGAUGE_PLUGIN_VERSION,
    hooks: NIGHTGAUGE_HOOK_NAMES,
  };
  fs.writeFileSync(sentinelPath, JSON.stringify(sentinel), { mode: 0o600 });
}

export const NightgaugePlugin = async (ctx) => {
  const session = await optionalHooks("./nightgauge/session.js");
  const edit = await optionalHooks("./nightgauge/edit.js");

  writeHandshakeSentinel();

  return {
    "tool.execute.before": async (input, output) => {
      await toolExecuteBefore(ctx, input, output);
      if (typeof session.toolExecuteBefore === "function") {
        await session.toolExecuteBefore(ctx, input, output);
      }
    },
    "tool.execute.after": async (input, output) => {
      if (typeof edit.toolExecuteAfter === "function") {
        await edit.toolExecuteAfter(ctx, input, output);
      }
    },
    "command.execute.before": async (input, output) => {
      await gatesCommandExecuteBefore(ctx, input, output);
      if (typeof session.commandExecuteBefore === "function") {
        await session.commandExecuteBefore(ctx, input, output);
      }
    },
    "permission.ask": async (input, output) => {
      if (typeof session.permissionAsk === "function") {
        await session.permissionAsk(ctx, input, output);
      }
    },
    event: async (input) => {
      if (typeof session.event === "function") {
        await session.event(ctx, input);
      }
    },
    "experimental.session.compacting": async (input, output) => {
      if (typeof session.sessionCompacting === "function") {
        await session.sessionCompacting(ctx, input, output);
      }
    },
    "experimental.compaction.autocontinue": async (input, output) => {
      if (typeof session.compactionAutocontinue === "function") {
        await session.compactionAutocontinue(ctx, input, output);
      }
    },
  };
};

export default NightgaugePlugin;
