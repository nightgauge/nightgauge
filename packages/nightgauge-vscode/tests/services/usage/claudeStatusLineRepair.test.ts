/**
 * Re-pointing a status line whose binary an extension update deleted (#807).
 *
 * The wiring writes an absolute, version-stamped bundle path. Updates install a
 * new directory and delete the old one, so the command outlives the binary it
 * names and every Claude Code render since the update has invoked a dead path —
 * silently, because `readStatusLineState` decides `wired` from the verb
 * substring and never asks whether the path still runs.
 *
 * Every case here fails against the pre-#807 code, which had no notion of the
 * path going stale.
 */

import { describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import {
  buildStatusLineCommand,
  parseStatusLineBinary,
  quoteDelegate,
  readClaudeSettings,
  readStatusLineState,
  repairStatusLineBinary,
  STATUS_LINE_VERB,
} from "../../../src/services/usage/claudeStatusLineSetup";
import { repairStaleClaudeStatusLine } from "../../../src/services/usage/claudeStatusLineRepair";

/** The path shape the bug report captured, verbatim. */
const DEAD =
  "/Users/x/.vscode/extensions/nightgauge.nightgauge-vscode-0.1.1787175119/dist/bin/nightgauge";
const LIVE =
  "/Users/x/.vscode/extensions/nightgauge.nightgauge-vscode-0.1.1787399523/dist/bin/nightgauge";

function wired(command: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { model: "opus", statusLine: { type: "command", command, ...extra } };
}

/** Only LIVE exists — exactly the post-update filesystem. */
const onlyLiveExists = async (p: string) => p === LIVE;

function deps(overrides: Partial<Parameters<typeof repairStatusLineBinary>[1]> = {}) {
  return {
    isExecutable: onlyLiveExists,
    resolveBinary: async () => LIVE,
    ...overrides,
  };
}

describe("parseStatusLineBinary", () => {
  it("reads the quoted binary out of a command this module wrote", () => {
    expect(parseStatusLineBinary(buildStatusLineCommand(DEAD, null))).toBe(DEAD);
  });

  it("is not confused by quotes inside a --delegate payload", () => {
    const command = buildStatusLineCommand(DEAD, `echo "it's fine" | tr a-z A-Z`);
    expect(parseStatusLineBinary(command)).toBe(DEAD);
  });

  it("reads a path containing a single quote", () => {
    const odd = "/Users/o'brien/bin/nightgauge";
    expect(parseStatusLineBinary(buildStatusLineCommand(odd, null))).toBe(odd);
  });

  it("accepts a bare unquoted path, which an operator may have written by hand", () => {
    expect(parseStatusLineBinary(`/usr/local/bin/nightgauge ${STATUS_LINE_VERB}`)).toBe(
      "/usr/local/bin/nightgauge"
    );
  });

  it("refuses a wrapped invocation rather than guessing which token is the binary", () => {
    expect(parseStatusLineBinary(`/usr/bin/env FOO=1 '${DEAD}' ${STATUS_LINE_VERB}`)).toBeNull();
  });

  it("returns null for a command that is not ours at all", () => {
    expect(parseStatusLineBinary("~/bin/my-status-line --fancy")).toBeNull();
  });
});

describe("repairStatusLineBinary", () => {
  it("re-points a stale path at the currently resolvable binary", async () => {
    const settings = wired(buildStatusLineCommand(DEAD, null));

    const result = await repairStatusLineBinary(settings, deps());

    expect(result.outcome).toBe("repaired");
    expect(result.staleBinary).toBe(DEAD);
    expect(result.binary).toBe(LIVE);
    expect(parseStatusLineBinary(readStatusLineState(result.settings).command!)).toBe(LIVE);
    expect(result.changed).toBe(true);
  });

  it("carries a --delegate payload across the rewrite byte for byte", async () => {
    // Deliberately nasty: spaces, a pipe, a subshell and an embedded quote —
    // the shapes that break a naive re-quote.
    const delegate = `~/bin/ctx.sh --dir "$(pwd)" | sed "s/it's/its/"`;
    const settings = wired(buildStatusLineCommand(DEAD, delegate));

    const result = await repairStatusLineBinary(settings, deps());

    expect(result.outcome).toBe("repaired");
    const next = readStatusLineState(result.settings);
    expect(next.delegate).toBe(delegate);
    expect(next.command).toBe(
      `${quoteDelegate(LIVE)} ${STATUS_LINE_VERB} --delegate ${quoteDelegate(delegate)}`
    );
    // One invocation, never a nightgauge nested inside a nightgauge.
    expect(next.command!.split(STATUS_LINE_VERB).length - 1).toBe(1);
  });

  it("leaves the rest of the settings document, and the rest of statusLine, alone", async () => {
    const settings = wired(buildStatusLineCommand(DEAD, null), { padding: 0 });

    const result = await repairStatusLineBinary(settings, deps());

    expect(result.settings.model).toBe("opus");
    expect((result.settings.statusLine as Record<string, unknown>).padding).toBe(0);
    expect((result.settings.statusLine as Record<string, unknown>).type).toBe("command");
  });

  it("does nothing when the wired binary is still there", async () => {
    const settings = wired(buildStatusLineCommand(LIVE, null));

    const result = await repairStatusLineBinary(settings, deps());

    expect(result.outcome).toBe("healthy");
    expect(result.changed).toBe(false);
    expect(result.settings).toBe(settings);
  });

  it("never touches a status line that is not nightgauge's", async () => {
    const settings = wired("~/bin/my-own-status-line --fancy");

    const result = await repairStatusLineBinary(settings, deps());

    expect(result.outcome).toBe("not-wired");
    expect(result.settings).toBe(settings);
  });

  it("leaves a wrapped nightgauge invocation exactly as it is", async () => {
    const settings = wired(`/usr/bin/env FOO=1 '${DEAD}' ${STATUS_LINE_VERB}`);

    const result = await repairStatusLineBinary(settings, deps());

    expect(result.outcome).toBe("unrecognized");
    expect(result.changed).toBe(false);
    expect(result.settings).toBe(settings);
  });

  it("leaves the stale command alone when no binary can be resolved at all", async () => {
    const settings = wired(buildStatusLineCommand(DEAD, null));

    const result = await repairStatusLineBinary(
      settings,
      deps({ resolveBinary: async () => null })
    );

    expect(result.outcome).toBe("unresolvable");
    expect(result.changed).toBe(false);
    expect(result.settings).toBe(settings);
    // A broken command replaced by a differently broken one is worse than a
    // broken command left alone.
    expect(readStatusLineState(result.settings).command).toContain(DEAD);
  });

  it("refuses to rewrite a command to itself when the resolver returns the dead path", async () => {
    const settings = wired(buildStatusLineCommand(DEAD, null));

    const result = await repairStatusLineBinary(
      settings,
      deps({ resolveBinary: async () => DEAD })
    );

    expect(result.outcome).toBe("unresolvable");
    expect(result.changed).toBe(false);
  });

  it("is idempotent — the second pass finds it healthy", async () => {
    const first = await repairStatusLineBinary(wired(buildStatusLineCommand(DEAD, null)), deps());
    expect(first.outcome).toBe("repaired");

    const second = await repairStatusLineBinary(first.settings, deps());

    expect(second.outcome).toBe("healthy");
    expect(second.changed).toBe(false);
    expect(second.settings).toBe(first.settings);
  });

  it("does not resolve a binary at all when nothing is wired", async () => {
    let resolves = 0;
    const result = await repairStatusLineBinary(
      { model: "opus" },
      deps({
        resolveBinary: async () => {
          resolves += 1;
          return LIVE;
        },
      })
    );

    expect(result.outcome).toBe("not-wired");
    expect(resolves).toBe(0);
  });
});

describe("repairStaleClaudeStatusLine — the activation path, against a real file", () => {
  async function withSettings(
    contents: string,
    run: (settingsPath: string) => Promise<void>
  ): Promise<void> {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-807-"));
    const settingsPath = path.join(dir, "settings.json");
    await fs.writeFile(settingsPath, contents, "utf8");
    await run(settingsPath);
  }

  it("rewrites the file, and only the statusLine key", async () => {
    const original = {
      model: "opus",
      permissions: { allow: ["Bash(ls:*)"] },
      statusLine: { type: "command", command: buildStatusLineCommand(DEAD, "~/bin/ctx.sh") },
    };
    await withSettings(`${JSON.stringify(original, null, 2)}\n`, async (settingsPath) => {
      const outcome = await repairStaleClaudeStatusLine({
        settingsPath,
        resolveBinary: async () => LIVE,
      });

      expect(outcome).toBe("repaired");
      const after = await readClaudeSettings(settingsPath);
      expect(after!.model).toBe("opus");
      expect(after!.permissions).toEqual({ allow: ["Bash(ls:*)"] });
      const state = readStatusLineState(after!);
      expect(parseStatusLineBinary(state.command!)).toBe(LIVE);
      expect(state.delegate).toBe("~/bin/ctx.sh");
    });
  });

  it("follows a symlinked settings file rather than replacing the link", async () => {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-807-link-"));
    const real = path.join(dir, "dotfiles-settings.json");
    const link = path.join(dir, "settings.json");
    await fs.writeFile(
      real,
      `${JSON.stringify(wired(buildStatusLineCommand(DEAD, null)), null, 2)}\n`,
      "utf8"
    );
    await fs.symlink(real, link);

    const outcome = await repairStaleClaudeStatusLine({
      settingsPath: link,
      resolveBinary: async () => LIVE,
    });

    expect(outcome).toBe("repaired");
    // The link must still BE a link — a rename over it would have made it a
    // regular file and silently detached the operator's managed config.
    expect((await fs.lstat(link)).isSymbolicLink()).toBe(true);
    expect(
      parseStatusLineBinary(readStatusLineState((await readClaudeSettings(real))!).command!)
    ).toBe(LIVE);
  });

  it("leaves a settings file it cannot parse completely untouched", async () => {
    const garbage = "{ this is not json";
    await withSettings(garbage, async (settingsPath) => {
      const outcome = await repairStaleClaudeStatusLine({
        settingsPath,
        resolveBinary: async () => LIVE,
      });

      expect(outcome).toBe("error");
      expect(await fs.readFile(settingsPath, "utf8")).toBe(garbage);
    });
  });

  it("does not create a settings file when there is none", async () => {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "ng-807-absent-"));
    const settingsPath = path.join(dir, "settings.json");

    const outcome = await repairStaleClaudeStatusLine({
      settingsPath,
      resolveBinary: async () => LIVE,
    });

    expect(outcome).toBe("not-wired");
    await expect(fs.access(settingsPath)).rejects.toThrow();
  });

  it("reports, and does not throw, when the settings file cannot be read at all", async () => {
    const outcome = await repairStaleClaudeStatusLine({
      // A directory, not a file: readFile fails in a way JSON parsing cannot.
      settingsPath: await fs.mkdtemp(path.join(os.tmpdir(), "ng-807-dir-")),
      resolveBinary: async () => LIVE,
    });

    expect(["error", "not-wired"]).toContain(outcome);
  });

  it("logs the unresolvable case rather than failing silently", async () => {
    const warnings: string[] = [];
    await withSettings(
      `${JSON.stringify(wired(buildStatusLineCommand(DEAD, null)), null, 2)}\n`,
      async (settingsPath) => {
        const outcome = await repairStaleClaudeStatusLine({
          settingsPath,
          resolveBinary: async () => null,
          logger: { info: () => undefined, warn: (m) => warnings.push(m) },
        });

        expect(outcome).toBe("unresolvable");
        expect(warnings.join("\n")).toContain(DEAD);
        // Untouched on disk.
        const after = await readClaudeSettings(settingsPath);
        expect(readStatusLineState(after!).command).toContain(DEAD);
      }
    );
  });
});
