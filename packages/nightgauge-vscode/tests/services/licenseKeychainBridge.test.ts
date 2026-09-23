/**
 * licenseKeychainBridge (#2027): the extension stores the license key in the
 * Go binary's shared OS-keychain entry by piping it to
 * `nightgauge auth license set`, so the CLI and a terminal-started daemon keep
 * working after the extension moves the key out of YAML.
 */

import { describe, it, expect, vi } from "vitest";
import { EventEmitter } from "events";
import * as fs from "fs";
import * as path from "path";
import type { spawn } from "child_process";
import {
  LicenseKeychainBridge,
  LICENSE_CLEAR_ARGS,
  LICENSE_KEYCHAIN_ACCOUNT,
  LICENSE_KEYCHAIN_SERVICE,
  LICENSE_SET_ARGS,
  MANUAL_LICENSE_SET_COMMAND,
  migrateLicenseKeyAtStartup,
  type LicenseMigrationDeps,
} from "../../src/services/licenseKeychainBridge";

const KEY = "ib_live_bridge_test_key";
const SECRET_KEY = "nightgauge.platform.licenseKey";
const MACHINE = "/machine/config.yaml";
const PROJECT = "/workspace/.nightgauge/config.yaml";

interface SpawnCall {
  binary: string;
  args: string[];
  env: NodeJS.ProcessEnv;
  stdin: string;
}

/** A spawn double: records each call and answers with the scripted reply. */
function fakeSpawn(
  reply: (args: string[]) => { stdout?: string; stderr?: string; code?: number; error?: Error }
): { spawnImpl: typeof spawn; calls: SpawnCall[] } {
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
      const r = reply(call.args);
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

function bridgeWith(reply: Parameters<typeof fakeSpawn>[0]) {
  const { spawnImpl, calls } = fakeSpawn(reply);
  const warn = vi.fn();
  const log = vi.fn();
  const bridge = new LicenseKeychainBridge({
    resolveBinary: async () => "/bin/nightgauge",
    spawnImpl,
    warn,
    log,
    env: { PATH: "/bin", NIGHTGAUGE_LICENSE_KEY: "ib_live_from_env" },
  });
  return { bridge, calls, warn, log };
}

const keychainReply = (args: string[]) =>
  args.includes("status")
    ? { stdout: '{"source":"none","keychainAvailable":true}\n' }
    : { stdout: '{"source":"keychain"}\n' };

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

function memSecrets(initial?: string) {
  let value = initial;
  return {
    getSecret: vi.fn(async () => value),
    setSecret: vi.fn(async (_k: string, v: string) => {
      value = v;
    }),
    get value() {
      return value;
    },
  };
}

const machineYaml = `platform:\n  api_url: https://example.test\n  license_key: ${KEY}\n`;

describe("entry contract", () => {
  // The Go side owns the entry; these strings must match its constants.
  it("matches the Go keychain package's service and account", () => {
    expect(LICENSE_KEYCHAIN_SERVICE).toBe("nightgauge");
    expect(LICENSE_KEYCHAIN_ACCOUNT).toBe("platform.license_key");
    const goSource = fs.readFileSync(
      path.resolve(__dirname, "../../../../internal/keychain/keychain.go"),
      "utf-8"
    );
    expect(goSource).toMatch(new RegExp(`Service\\s*=\\s*"${LICENSE_KEYCHAIN_SERVICE}"`));
    expect(goSource).toMatch(
      new RegExp(`AccountLicenseKey\\s*=\\s*"${LICENSE_KEYCHAIN_ACCOUNT.replace(".", "\\.")}"`)
    );
  });
});

describe("LicenseKeychainBridge.store", () => {
  it("spawns `auth license set` with the key on stdin only", async () => {
    const { bridge, calls, warn } = bridgeWith(keychainReply);

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

  it("reports the machine-file fallback on a host with no keychain", async () => {
    const { bridge } = bridgeWith(() => ({
      stdout: '{"source":"machine-file","path":"/m/config.yaml","keychainError":"no dbus"}\n',
    }));
    expect(await bridge.store(KEY)).toEqual({ ok: true, source: "machine-file" });
  });

  it("warns once, naming the manual command, and never the key", async () => {
    const { bridge, warn, log } = bridgeWith(() => ({ code: 1, stderr: "Error: boom\n" }));

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

  it("fails cleanly when no binary resolves", async () => {
    const warn = vi.fn();
    const bridge = new LicenseKeychainBridge({ resolveBinary: async () => null, warn });
    const outcome = await bridge.store(KEY);
    expect(outcome.ok).toBe(false);
    expect(warn).toHaveBeenCalledTimes(1);
  });
});

describe("LicenseKeychainBridge.clear", () => {
  it("spawns `auth license clear` with nothing on stdin", async () => {
    const { bridge, calls } = bridgeWith(() => ({ stdout: "Removed.\n" }));
    expect(await bridge.clear()).toBe(true);
    expect(calls[0].args).toEqual([...LICENSE_CLEAR_ARGS]);
    expect(calls[0].stdin).toBe("");
  });
});

describe("migrateLicenseKeyAtStartup", () => {
  function deps(
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
      projectConfigPath: PROJECT,
      machineConfigPath: MACHINE,
    };
    return { d, store: mem.store };
  }

  it("moves a machine-config key into SecretStorage and the keychain, then deletes the YAML line", async () => {
    const { bridge, calls } = bridgeWith(keychainReply);
    const secrets = memSecrets();
    const { d, store } = deps({ [MACHINE]: machineYaml }, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(secrets.value).toBe(KEY);
    expect(calls.map((c) => c.args)).toEqual([[...LICENSE_SET_ARGS]]);
    expect(store.get(MACHINE)).not.toContain("license_key");
    expect(store.get(MACHINE)).toContain("api_url");
  });

  // Moving the YAML delete ahead of the spawn turns this red.
  it("leaves the machine YAML untouched and warns once when the spawn fails", async () => {
    const { bridge, warn } = bridgeWith(() => ({ error: new Error("spawn ENOENT") }));
    const secrets = memSecrets();
    const { d, store } = deps({ [MACHINE]: machineYaml }, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(store.get(MACHINE)).toBe(machineYaml);
    expect(secrets.value).toBe(KEY); // the SecretStorage copy is kept
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it("keeps the machine YAML when the CLI could only use its file fallback", async () => {
    const { bridge } = bridgeWith(() => ({ stdout: '{"source":"machine-file"}\n' }));
    const { d, store } = deps({ [MACHINE]: machineYaml }, memSecrets(), bridge);

    await migrateLicenseKeyAtStartup(d);

    expect(store.get(MACHINE)).toBe(machineYaml);
  });

  it("strips a committed project key and stores it in both places", async () => {
    const { bridge, calls } = bridgeWith(keychainReply);
    const secrets = memSecrets();
    const { d, store } = deps({ [PROJECT]: `platform:\n  license_key: ${KEY}\n` }, secrets, bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(store.get(PROJECT)).not.toContain(KEY);
    expect(secrets.value).toBe(KEY);
    expect(calls[0].stdin).toBe(KEY);
  });

  it("copies an already-migrated SecretStorage key to the keychain when the CLI has none", async () => {
    const { bridge, calls } = bridgeWith(keychainReply);
    const { d } = deps({}, memSecrets(KEY), bridge);

    expect(await migrateLicenseKeyAtStartup(d)).toBe(KEY);

    expect(calls.map((c) => c.args[2])).toEqual(["status", "set"]);
    expect(calls[1].stdin).toBe(KEY);
  });

  it("does not rewrite the keychain when the CLI already has a stored key", async () => {
    const { bridge, calls } = bridgeWith(() => ({
      stdout: '{"source":"keychain","keychainAvailable":true}\n',
    }));
    const { d } = deps({}, memSecrets(KEY), bridge);

    await migrateLicenseKeyAtStartup(d);

    expect(calls.map((c) => c.args[2])).toEqual(["status"]);
  });
});
