/**
 * licenseKeychainBridge (#2027): the extension stores the license key in the
 * Go binary's shared OS-keychain entry by piping it to
 * `nightgauge auth license set`, so the CLI and a terminal-started daemon keep
 * working after the extension moves the key out of YAML. The shared entry is
 * the source of truth; SecretStorage is reconciled against it.
 */

import { describe, it, expect, vi } from "vitest";
import { EventEmitter } from "events";
import * as fs from "fs";
import * as path from "path";
import type { spawn } from "child_process";
import { scryptSync } from "crypto";
import {
  LicenseKeychainBridge,
  LICENSE_CLEAR_ARGS,
  LEGACY_LICENSE_SYNCED_FINGERPRINT_SECRET,
  LICENSE_FINGERPRINT_SALT,
  LICENSE_FINGERPRINT_SCRYPT,
  LICENSE_COMMAND,
  LICENSE_KEYCHAIN_ACCOUNT,
  LICENSE_KEYCHAIN_SERVICE,
  LICENSE_SET_ARGS,
  LICENSE_STATUS_ARGS,
  LICENSE_SYNCED_FINGERPRINT_SECRET,
  MANUAL_LICENSE_SET_COMMAND,
  forgetLicenseKey,
  licenseKeyFingerprint,
  migrateLicenseKeyAtStartup,
  persistLicenseKey,
  setLicenseReconciliation,
  whenLicenseReconciled,
  type LicenseMigrationDeps,
} from "../../src/services/licenseKeychainBridge";

const KEY = "ib_live_bridge_test_key";
const OLD_KEY = "ib_live_bridge_old_key";
const SECRET_KEY = "nightgauge.platform.licenseKey";
const MACHINE = "/machine/config.yaml";
// A synchronous copy of the KDF for building fixtures; the contract test
// below pins it and the production (async) function to the same vector.
const fpCache = new Map<string, string>();
const fp = (key: string): string => {
  if (!fpCache.has(key)) {
    fpCache.set(
      key,
      scryptSync(key, LICENSE_FINGERPRINT_SALT, 32, LICENSE_FINGERPRINT_SCRYPT)
        .toString("hex")
        .slice(0, 12)
    );
  }
  return fpCache.get(key)!;
};

interface SpawnCall {
  binary: string;
  args: string[];
  env: NodeJS.ProcessEnv;
  stdin: string;
}

type Reply = { stdout?: string; stderr?: string; code?: number; error?: Error };

/** A spawn double: records each call and answers with the scripted reply. */
function fakeSpawn(reply: (call: SpawnCall) => Reply): {
  spawnImpl: typeof spawn;
  calls: SpawnCall[];
} {
  const calls: SpawnCall[] = [];
  const spawnImpl = ((binary: string, args: string[], opts: { env: NodeJS.ProcessEnv }) => {
    const call: SpawnCall = { binary, args: [...args], env: opts.env, stdin: "" };
    calls.push(call);
    const proc = new EventEmitter() as EventEmitter & {
      stdout: EventEmitter;
      stderr: EventEmitter;
      stdin: EventEmitter & { end: (data?: string) => void };
      kill: () => void;
    };
    proc.stdout = new EventEmitter();
    proc.stderr = new EventEmitter();
    proc.kill = () => undefined;
    const stdin = new EventEmitter() as EventEmitter & { end: (data?: string) => void };
    stdin.end = (data?: string) => {
      call.stdin += data ?? "";
    };
    proc.stdin = stdin;
    setImmediate(() => {
      const r = reply(call);
      if (r.error) {
        proc.emit("error", r.error);
        return;
      }
      if (r.stdout) proc.stdout.emit("data", Buffer.from(r.stdout));
      if (r.stderr) proc.stderr.emit("data", Buffer.from(r.stderr));
      proc.emit("close", r.code ?? 0);
    });
    return proc;
  }) as unknown as typeof spawn;
  return { spawnImpl, calls };
}

function bridgeWith(reply: (call: SpawnCall) => Reply) {
  const { spawnImpl, calls } = fakeSpawn(reply);
  const warn = vi.fn();
  const inform = vi.fn();
  const log = vi.fn();
  const bridge = new LicenseKeychainBridge({
    resolveBinary: async () => "/bin/nightgauge",
    spawnImpl,
    warn,
    inform,
    log,
    env: { PATH: "/bin", NIGHTGAUGE_LICENSE_KEY: "ib_live_from_env" },
  });
  return { bridge, calls, warn, inform, log };
}

/**
 * A CLI double with a keychain: `set` stores the stdin key, `status` reports
 * its fingerprint, `clear` empties it.
 */
function cli(initial?: string) {
  const state = { key: initial };
  const reply = (call: SpawnCall): Reply => {
    switch (call.args[2]) {
      case "set":
        state.key = call.stdin;
        return { stdout: JSON.stringify({ source: "keychain", fingerprint: fp(call.stdin) }) };
      case "status":
        return {
          stdout: JSON.stringify(
            state.key
              ? { source: "keychain", keychainAvailable: true, fingerprint: fp(state.key) }
              : { source: "none", keychainAvailable: true }
          ),
        };
      default:
        state.key = undefined;
        return { stdout: JSON.stringify({ keychainCleared: true, fileCleared: false }) };
    }
  };
  return { state, reply };
}

/** An in-memory fs holding the given files. */
function memFs(files: Record<string, string>) {
  const store = new Map(Object.entries(files));
  return {
    store,
    fs: {
      existsSync: (p: string) => store.has(p),
      readFileSync: (p: string) => {
        const v = store.get(p);
        if (v === undefined) throw new Error(`ENOENT ${p}`);
        return v;
      },
      writeFileSync: (p: string, data: string) => {
        store.set(p, data);
      },
    } satisfies LicenseMigrationDeps["fs"],
  };
}

function memSecrets(initial: Record<string, string> = {}) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    getSecret: vi.fn(async (k: string) => map.get(k)),
    setSecret: vi.fn(async (k: string, v: string) => {
      map.set(k, v);
    }),
    deleteSecret: vi.fn(async (k: string) => {
      map.delete(k);
    }),
  };
}

const machineYaml = `platform:\n  api_url: https://example.test\n  license_key: ${KEY}\n`;

function migrationDeps(
  files: Record<string, string>,
  secrets: ReturnType<typeof memSecrets>,
  bridge: LicenseKeychainBridge
) {
  const mem = memFs(files);
  const d: LicenseMigrationDeps = {
    fs: mem.fs,
    secrets,
    bridge,
    secretKey: SECRET_KEY,
    machineConfigPath: MACHINE,
  };
  return { d, store: mem.store };
}

// ---------------------------------------------------------------------------

describe("CLI contract (cmd/nightgauge/testdata/auth-license-contract.json)", () => {
  const contract = JSON.parse(
    fs.readFileSync(
      path.resolve(__dirname, "../../../../cmd/nightgauge/testdata/auth-license-contract.json"),
      "utf-8"
    )
  ) as {
    keychain: { service: string; account: string };
    command: string[];
    subcommands: string[];
    fingerprint: { key: string; value: string };
    outputs: Record<"set" | "status" | "clear", Record<string, unknown>>;
  };

  it("uses the entry, subcommands and fingerprint the Go side pins", async () => {
    expect(LICENSE_KEYCHAIN_SERVICE).toBe(contract.keychain.service);
    expect(LICENSE_KEYCHAIN_ACCOUNT).toBe(contract.keychain.account);
    expect([...LICENSE_COMMAND]).toEqual(contract.command);
    const used = [LICENSE_SET_ARGS, LICENSE_STATUS_ARGS, LICENSE_CLEAR_ARGS].map((a) => a[2]);
    expect([...used].sort()).toEqual([...contract.subcommands].sort());
    for (const args of [LICENSE_SET_ARGS, LICENSE_STATUS_ARGS, LICENSE_CLEAR_ARGS]) {
      expect(args.slice(0, 2)).toEqual(contract.command);
      expect(args[3]).toBe("--json");
    }
    expect(await licenseKeyFingerprint(contract.fingerprint.key)).toBe(contract.fingerprint.value);
    expect(fp(contract.fingerprint.key)).toBe(contract.fingerprint.value);
    expect(await licenseKeyFingerprint("")).toBe("");
  });

  it("parses the set, status and clear samples", async () => {
    const out = (call: SpawnCall): Reply => ({
      stdout: JSON.stringify(contract.outputs[call.args[2] as "set" | "status" | "clear"]),
    });
    const { bridge, warn } = bridgeWith(out);

    expect(await bridge.store(contract.fingerprint.key)).toEqual({
      ok: true,
      source: contract.outputs.set.source,
    });
    expect(await bridge.status()).toEqual({
      source: contract.outputs.status.source,
      keychainAvailable: contract.outputs.status.keychainAvailable,
      fingerprint: contract.outputs.status.fingerprint,
    });
    expect(await bridge.clear()).toEqual({
      keychainCleared: contract.outputs.clear.keychainCleared,
      fileCleared: contract.outputs.clear.fileCleared,
      localCleared: contract.outputs.clear.localCleared,
      noKeychain: contract.outputs.clear.noKeychain,
    });
    expect(warn).not.toHaveBeenCalled();
  });
});

describe("LicenseKeychainBridge.store", () => {
  it("spawns `auth license set` with the key on stdin only", async () => {
    const { bridge, calls, warn } = bridgeWith(cli().reply);

    const outcome = await bridge.store(KEY);

    expect(outcome).toEqual({ ok: true, source: "keychain" });
    expect(calls).toHaveLength(1);
    expect(calls[0].binary).toBe("/bin/nightgauge");
    expect(calls[0].args).toEqual([...LICENSE_SET_ARGS]);
    expect(calls[0].args.slice(3)).toEqual(["--json"]); // nothing but the flag after `set`
    expect(calls[0].args.join(" ")).not.toContain(KEY);
    expect(calls[0].stdin).toBe(KEY);
    // The key never travels through the environment, not even one inherited.
    expect(calls[0].env.NIGHTGAUGE_LICENSE_KEY).toBeUndefined();
    expect(JSON.stringify(calls[0].env)).not.toContain(KEY);
    expect(warn).not.toHaveBeenCalled();
  });

  it("shows one information message when the key lands in the plaintext file", async () => {
    const { bridge, inform, warn } = bridgeWith(() => ({
      stdout: JSON.stringify({ source: "machine-file", fingerprint: fp(KEY), path: "/m" }),
    }));
    expect(await bridge.store(KEY)).toEqual({ ok: true, source: "machine-file" });
    expect(await bridge.store(KEY)).toEqual({ ok: true, source: "machine-file" });
    expect(inform).toHaveBeenCalledTimes(1);
    expect(String(inform.mock.calls[0][0])).not.toContain(KEY);
    expect(warn).not.toHaveBeenCalled();
  });

  it("fails when the binary does not confirm this key's fingerprint", async () => {
    const { bridge } = bridgeWith(() => ({
      stdout: JSON.stringify({ source: "keychain", fingerprint: fp(OLD_KEY) }),
    }));
    expect((await bridge.store(KEY)).ok).toBe(false);
  });

  it("warns once, naming the manual command, and never the key", async () => {
    const { bridge, warn, log } = bridgeWith(() => ({
      code: 1,
      stderr: "Error: store license key: the OS keychain did not respond within 3s\n",
    }));

    const first = await bridge.store(KEY);
    const second = await bridge.store(KEY);

    expect(first.ok).toBe(false);
    expect(second.ok).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
    const message = String(warn.mock.calls[0][0]);
    expect(message).toContain(MANUAL_LICENSE_SET_COMMAND);
    expect(message).not.toContain(KEY);
    for (const [line] of log.mock.calls) expect(String(line)).not.toContain(KEY);
  });

  it("tells the user to update a binary that has no `auth license` command", async () => {
    const { bridge, warn } = bridgeWith(() => ({
      code: 1,
      stderr:
        'Error: unknown command "license" for "nightgauge auth"\nRun \'nightgauge auth --help\' for usage.\n',
    }));
    expect((await bridge.store(KEY)).ok).toBe(false);
    const message = String(warn.mock.calls[0][0]);
    expect(message).toMatch(/update the nightgauge binary/i);
    expect(message).not.toContain(MANUAL_LICENSE_SET_COMMAND);
  });

  it("fails cleanly when no binary resolves", async () => {
    const warn = vi.fn();
    const bridge = new LicenseKeychainBridge({ resolveBinary: async () => null, warn });
    const outcome = await bridge.store(KEY);
    expect(outcome.ok).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
  });
});

describe("LicenseKeychainBridge.clear", () => {
  it("spawns `auth license clear --json` and returns what it removed", async () => {
    const { bridge, calls } = bridgeWith(() => ({
      stdout: JSON.stringify({ keychainCleared: true, fileCleared: true }),
    }));
    expect(await bridge.clear()).toEqual({
      keychainCleared: true,
      fileCleared: true,
      localCleared: false,
      noKeychain: false,
    });
    expect(calls[0].args).toEqual([...LICENSE_CLEAR_ARGS]);
    expect(calls[0].stdin).toBe("");
  });

  // An old binary prints `auth` help and exits 0: that is not a clear.
  it("treats a binary that printed help (exit 0) as a failure", async () => {
    const { bridge, warn } = bridgeWith(() => ({
      stdout:
        "Authentication operations\n\nUsage:\n  nightgauge auth [command]\n\nAvailable Commands:\n  check\n",
    }));
    expect(await bridge.clear()).toBeNull();
    expect(String(warn.mock.calls[0][0])).toMatch(/update the nightgauge binary/i);
  });

  // No keychain service at all (the host where `set` falls back to the
  // file): removing the file copy is a complete clear.
  it("succeeds on a host with no keychain once the file copy is removed", async () => {
    const { bridge, warn } = bridgeWith(() => ({
      stdout: JSON.stringify({
        keychainCleared: false,
        fileCleared: true,
        keychainError: "dbus: couldn't determine address of session bus",
        noKeychain: true,
      }),
    }));
    expect(await bridge.clear()).toEqual({
      keychainCleared: false,
      fileCleared: true,
      localCleared: false,
      noKeychain: true,
    });
    expect(warn).not.toHaveBeenCalled();
  });

  // A timeout or unexpected error may leave the entry: the CLI exits 1.
  it("fails when the keychain entry may remain", async () => {
    const { bridge, warn } = bridgeWith(() => ({
      code: 1,
      stdout: JSON.stringify({
        keychainCleared: false,
        fileCleared: true,
        keychainError: "no dbus",
      }),
    }));
    expect(await bridge.clear()).toBeNull();
    expect(String(warn.mock.calls[0][0])).toContain("no dbus");
  });

  it("forgetLicenseKey drops the SecretStorage copy and the sync record", async () => {
    const c = cli(KEY);
    const { bridge } = bridgeWith(c.reply);
    const secrets = memSecrets({ [SECRET_KEY]: KEY, [LICENSE_SYNCED_FINGERPRINT_SECRET]: fp(KEY) });
    expect(await forgetLicenseKey(secrets, SECRET_KEY, bridge)).toBe(true);
    expect(secrets.map.size).toBe(0);
    expect(c.state.key).toBeUndefined();
  });
});

describe("persistLicenseKey", () => {
  it("records the fingerprint only when the CLI confirmed the write", async () => {
    const ok = bridgeWith(cli().reply);
    const secrets = memSecrets();
    await persistLicenseKey(secrets, SECRET_KEY, KEY, ok.bridge);
    expect(secrets.map.get(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(fp(KEY));

    const failing = bridgeWith(() => ({ error: new Error("spawn ENOENT") }));
    await persistLicenseKey(secrets, SECRET_KEY, OLD_KEY, failing.bridge);
    expect(secrets.map.get(SECRET_KEY)).toBe(OLD_KEY); // SecretStorage kept
    expect(secrets.map.get(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(fp(KEY)); // record did not move
  });
});

describe("migrateLicenseKeyAtStartup", () => {
  it("moves a machine-config key into SecretStorage and the keychain, then deletes the YAML line", async () => {
    const c = cli();
    const { bridge, calls } = bridgeWith(c.reply);
    const secrets = memSecrets();
    const { d, store } = migrationDeps({ [MACHINE]: machineYaml }, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(secrets.map.get(SECRET_KEY)).toBe(KEY);
    expect(c.state.key).toBe(KEY);
    expect(calls.map((x) => x.args)).toEqual([[...LICENSE_SET_ARGS]]);
    expect(store.get(MACHINE)).not.toContain("license_key");
    expect(store.get(MACHINE)).toContain("api_url");
  });

  // Moving the YAML delete ahead of the spawn turns this red.
  it("leaves the machine YAML untouched and warns once when the spawn fails", async () => {
    const { bridge, warn } = bridgeWith(() => ({ error: new Error("spawn ENOENT") }));
    const secrets = memSecrets();
    const { d, store } = migrationDeps({ [MACHINE]: machineYaml }, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(store.get(MACHINE)).toBe(machineYaml);
    expect(secrets.map.get(SECRET_KEY)).toBe(KEY); // the SecretStorage copy is kept
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("keeps the machine YAML when the CLI could only use its file fallback", async () => {
    const { bridge } = bridgeWith(() => ({
      stdout: JSON.stringify({ source: "machine-file", fingerprint: fp(KEY) }),
    }));
    const { d, store } = migrationDeps({ [MACHINE]: machineYaml }, memSecrets(), bridge);

    await migrateLicenseKeyAtStartup(d);

    expect(store.get(MACHINE)).toBe(machineYaml);
  });

  // #2023: only the machine tier is a migration source; the startup code
  // warns about a project-tier key and never passes it here.
  it("never reads a project config, so a repository file can never supply the key", async () => {
    const project = "/workspace/.nightgauge/config.yaml";
    const projectYaml = `platform:\n  license_key: ${KEY}\n`;
    const { bridge, calls } = bridgeWith(cli().reply);
    const secrets = memSecrets();
    const { d, store } = migrationDeps({ [project]: projectYaml }, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBeUndefined();

    expect(secrets.setSecret).not.toHaveBeenCalled();
    expect(calls).toHaveLength(0);
    expect(store.get(project)).toBe(projectYaml);
  });

  it("copies an already-migrated SecretStorage key to the keychain when the CLI has none", async () => {
    const c = cli();
    const { bridge, calls } = bridgeWith(c.reply);
    const secrets = memSecrets({ [SECRET_KEY]: KEY });
    const { d } = migrationDeps({}, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(calls.map((x) => x.args[2])).toEqual(["status", "set"]);
    expect(c.state.key).toBe(KEY);
    expect(secrets.map.get(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(fp(KEY));
  });

  it("does not rewrite the keychain when the CLI already holds the same key", async () => {
    const { bridge, calls } = bridgeWith(cli(KEY).reply);
    const secrets = memSecrets({ [SECRET_KEY]: KEY });
    const { d } = migrationDeps({}, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(calls.map((x) => x.args[2])).toEqual(["status"]);
    expect(secrets.map.get(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(fp(KEY));
  });

  // Rotated from a terminal: the CLI no longer holds the key last synced, so
  // the shared entry wins and VS Code stops using (and handing the daemon)
  // its stale copy.
  it("drops the stale VS Code copy when the key was rotated from a terminal", async () => {
    const c = cli(KEY); // terminal ran `auth license set` with KEY
    const { bridge, calls, inform } = bridgeWith(c.reply);
    const secrets = memSecrets({
      [SECRET_KEY]: OLD_KEY,
      [LICENSE_SYNCED_FINGERPRINT_SECRET]: fp(OLD_KEY),
    });
    const { d } = migrationDeps({}, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBeUndefined();

    expect(secrets.map.has(SECRET_KEY)).toBe(false);
    expect(secrets.map.has(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(false);
    expect(c.state.key).toBe(KEY); // the terminal's key is untouched
    expect(calls.map((x) => x.args[2])).toEqual(["status"]);
    expect(inform).toHaveBeenCalledTimes(1);
    expect(String(inform.mock.calls[0][0])).not.toContain(KEY);
  });

  // The extension activated KEY but its keychain write failed: the CLI still
  // holds the key last synced, so the extension's newer key is pushed.
  it("retries the extension's newer key after a failed set", async () => {
    const c = cli(OLD_KEY);
    const { bridge, inform } = bridgeWith(c.reply);
    const secrets = memSecrets({
      [SECRET_KEY]: KEY,
      [LICENSE_SYNCED_FINGERPRINT_SECRET]: fp(OLD_KEY),
    });
    const { d } = migrationDeps({}, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(c.state.key).toBe(KEY);
    expect(secrets.map.get(LICENSE_SYNCED_FINGERPRINT_SECRET)).toBe(fp(KEY));
    expect(inform).not.toHaveBeenCalled();
  });
});

describe("reconcile never deletes the key without evidence of rotation", () => {
  const synced = { [SECRET_KEY]: OLD_KEY, [LICENSE_SYNCED_FINGERPRINT_SECRET]: fp(OLD_KEY) };

  for (const [name, fingerprint] of [
    ["missing", undefined],
    ["malformed", "not-a-fingerprint"],
    ["too short", "d34399"],
    ["uppercase", "D34399CF362C"],
  ] as const) {
    it(`keeps the key and asks for a newer binary when the fingerprint is ${name}`, async () => {
      const { bridge, calls, warn } = bridgeWith(() => ({
        stdout: JSON.stringify({ source: "keychain", keychainAvailable: true, fingerprint }),
      }));
      const secrets = memSecrets({ ...synced });
      const { d } = migrationDeps({}, secrets, bridge);

      expect(await migrateLicenseKeyAtStartup(d)).toBe(OLD_KEY);

      expect(secrets.deleteSecret).not.toHaveBeenCalled();
      expect(secrets.map.get(SECRET_KEY)).toBe(OLD_KEY);
      expect(calls.map((x) => x.args[2])).toEqual(["status"]); // no write either
      expect(warn).toHaveBeenCalledTimes(1);
      expect(String(warn.mock.calls[0][0])).toMatch(/update the nightgauge binary/i);
    });
  }

  it("keeps the key when status fails or prints nothing parseable", async () => {
    for (const reply of [
      { code: 1, stderr: "Error: boom" },
      { stdout: "not json" },
      { error: new Error("spawn ENOENT") },
    ]) {
      const { bridge } = bridgeWith(() => reply);
      const secrets = memSecrets({ ...synced });
      const { d } = migrationDeps({}, secrets, bridge);
      expect(await migrateLicenseKeyAtStartup(d)).toBe(OLD_KEY);
      expect(secrets.deleteSecret).not.toHaveBeenCalled();
    }
  });

  // A record written by the earlier SHA-256 scheme lives under the legacy
  // name and is ignored: that reads as "no recorded sync", never as evidence.
  it("ignores a sync record from the pre-scrypt scheme", async () => {
    const c = cli(KEY);
    const { bridge, inform } = bridgeWith(c.reply);
    const secrets = memSecrets({
      [SECRET_KEY]: OLD_KEY,
      [LEGACY_LICENSE_SYNCED_FINGERPRINT_SECRET]: "d34399cf362c",
    });
    const { d } = migrationDeps({}, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(OLD_KEY);

    expect(secrets.deleteSecret).not.toHaveBeenCalled();
    expect(c.state.key).toBe(KEY);
    expect(inform).toHaveBeenCalledTimes(1);
  });

  it("keeps both keys when they differ but no sync was ever recorded", async () => {
    const c = cli(KEY);
    const { bridge, calls, inform } = bridgeWith(c.reply);
    const secrets = memSecrets({ [SECRET_KEY]: OLD_KEY });
    const { d } = migrationDeps({}, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(OLD_KEY);

    expect(secrets.deleteSecret).not.toHaveBeenCalled();
    expect(c.state.key).toBe(KEY);
    expect(calls.map((x) => x.args[2])).toEqual(["status"]);
    expect(inform).toHaveBeenCalledTimes(1);
  });
});

describe("whenLicenseReconciled", () => {
  it("waits for the startup reconciliation, bounded", async () => {
    let done = false;
    setLicenseReconciliation(
      new Promise<void>((resolve) =>
        setTimeout(() => {
          done = true;
          resolve();
        }, 20)
      )
    );
    await whenLicenseReconciled(1_000);
    expect(done).toBe(true);

    setLicenseReconciliation(new Promise<void>(() => undefined)); // never settles
    const started = Date.now();
    await whenLicenseReconciled(30);
    expect(Date.now() - started).toBeLessThan(1_000);
    setLicenseReconciliation(Promise.resolve());
  });
});
