/**
 * licenseKeychainBridge — keeps the Go CLI and daemon's copy of the license
 * key in step with the extension's.
 *
 * The extension keeps the license key in VS Code SecretStorage, which nothing
 * outside VS Code can read. The CLI and a daemon started from a terminal read
 * the OS-keychain entry the Go binary owns (service `nightgauge`, account
 * `platform.license_key`; docs/GO_BINARY.md § Platform license key). So every
 * place the extension stores or clears the key also runs
 * `nightgauge auth license set` / `clear`, with the key on stdin only. There
 * is one writer for that entry — the binary — and no keychain code here.
 *
 * The key never appears in argv, in the child's environment, or in any log
 * or message this module writes.
 */

import { spawn as nodeSpawn } from "child_process";
import * as vscode from "vscode";
import { BinaryResolver } from "./BinaryResolver";

/** The keychain entry the binary writes. Pinned by tests on both sides. */
export const LICENSE_KEYCHAIN_SERVICE = "nightgauge";
export const LICENSE_KEYCHAIN_ACCOUNT = "platform.license_key";

export const LICENSE_SET_ARGS: readonly string[] = ["auth", "license", "set", "--json"];
export const LICENSE_STATUS_ARGS: readonly string[] = ["auth", "license", "status", "--json"];
export const LICENSE_CLEAR_ARGS: readonly string[] = ["auth", "license", "clear"];

/** What a user runs by hand when the extension could not. */
export const MANUAL_LICENSE_SET_COMMAND = "nightgauge auth license set";
export const MANUAL_LICENSE_CLEAR_COMMAND = "nightgauge auth license clear";

const DEFAULT_TIMEOUT_MS = 15_000;
const MAX_OUTPUT = 8_192;

/** Where the binary put the key: the keychain, or the 0600 fallback file. */
export type LicenseStoreSource = "keychain" | "machine-file";

export type LicenseStoreOutcome =
  { ok: true; source: LicenseStoreSource } | { ok: false; reason: string };

export interface LicenseStatus {
  source: "env" | "keychain" | "machine-file" | "none";
  keychainAvailable: boolean;
}

export interface LicenseKeychainBridgeDeps {
  /** Resolves the nightgauge binary, or null when there is none. */
  resolveBinary: () => Promise<string | null>;
  /** User-facing warning. Called at most once per bridge. */
  warn: (message: string) => void;
  /** Diagnostic log line. Never receives the key. */
  log?: (message: string) => void;
  spawnImpl?: typeof nodeSpawn;
  env?: NodeJS.ProcessEnv;
  timeoutMs?: number;
}

interface RunResult {
  code: number | null;
  stdout: string;
  stderr: string;
}

export class LicenseKeychainBridge {
  private warned = false;

  constructor(private readonly deps: LicenseKeychainBridgeDeps) {}

  /**
   * Store the key where the CLI and daemon read it. On failure the caller's
   * SecretStorage copy is untouched and one warning names the manual command.
   */
  async store(key: string): Promise<LicenseStoreOutcome> {
    const result = await this.run(LICENSE_SET_ARGS, key);
    if ("error" in result) {
      return this.fail("set", result.error);
    }
    if (result.code !== 0) {
      return this.fail("set", describeExit(result));
    }
    try {
      const parsed = JSON.parse(result.stdout.trim()) as { source?: string };
      if (parsed.source === "keychain" || parsed.source === "machine-file") {
        if (parsed.source === "machine-file") {
          this.deps.log?.(
            "[licenseKeychainBridge] no OS keychain on this host; the CLI keeps the key in its machine-tier file"
          );
        }
        return { ok: true, source: parsed.source };
      }
    } catch {
      // fall through
    }
    return this.fail("set", "unexpected output from the nightgauge binary");
  }

  /** Remove the CLI's stored copies. Returns whether the command succeeded. */
  async clear(): Promise<boolean> {
    const result = await this.run(LICENSE_CLEAR_ARGS);
    if ("error" in result) {
      this.fail("clear", result.error);
      return false;
    }
    if (result.code !== 0) {
      this.fail("clear", describeExit(result));
      return false;
    }
    return true;
  }

  /** Where the CLI would read the key from, ignoring the environment variable. */
  async status(): Promise<LicenseStatus | null> {
    const result = await this.run(LICENSE_STATUS_ARGS);
    if ("error" in result || result.code !== 0) {
      return null;
    }
    try {
      const parsed = JSON.parse(result.stdout.trim()) as Partial<LicenseStatus>;
      if (
        parsed.source === "env" ||
        parsed.source === "keychain" ||
        parsed.source === "machine-file" ||
        parsed.source === "none"
      ) {
        return { source: parsed.source, keychainAvailable: parsed.keychainAvailable === true };
      }
    } catch {
      // fall through
    }
    return null;
  }

  private fail(action: "set" | "clear", reason: string): LicenseStoreOutcome {
    this.deps.log?.(`[licenseKeychainBridge] auth license ${action} failed: ${reason}`);
    if (!this.warned) {
      this.warned = true;
      const command = action === "set" ? MANUAL_LICENSE_SET_COMMAND : MANUAL_LICENSE_CLEAR_COMMAND;
      this.deps.warn(
        action === "set"
          ? `Nightgauge: the license key is saved in VS Code, but the CLI and daemon outside VS Code can't see it (${reason}). Run \`${command}\` in a terminal and paste the key.`
          : `Nightgauge: the license key was removed from VS Code, but the CLI's stored copy could not be removed (${reason}). Run \`${command}\` in a terminal.`
      );
    }
    return { ok: false, reason };
  }

  private async run(
    args: readonly string[],
    stdin?: string
  ): Promise<RunResult | { error: string }> {
    let binary: string | null;
    try {
      binary = await this.deps.resolveBinary();
    } catch {
      binary = null;
    }
    if (!binary) {
      return { error: "the nightgauge binary could not be found" };
    }
    // The child must not inherit a license key through its environment: the
    // key travels on stdin only, and `status` must report the stored copy.
    const env = { ...(this.deps.env ?? process.env) };
    delete env.NIGHTGAUGE_LICENSE_KEY;
    const spawnImpl = this.deps.spawnImpl ?? nodeSpawn;
    const timeoutMs = this.deps.timeoutMs ?? DEFAULT_TIMEOUT_MS;

    return new Promise((resolve) => {
      let settled = false;
      const settle = (value: RunResult | { error: string }) => {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          resolve(value);
        }
      };
      let proc: ReturnType<typeof nodeSpawn>;
      try {
        proc = spawnImpl(binary, [...args], {
          shell: false,
          env,
          stdio: ["pipe", "pipe", "pipe"],
          windowsHide: true,
        });
      } catch (err) {
        resolve({ error: `could not start the nightgauge binary: ${errorMessage(err)}` });
        return;
      }
      const timer = setTimeout(() => {
        proc.kill();
        settle({ error: `the nightgauge binary did not finish within ${timeoutMs / 1000}s` });
      }, timeoutMs);
      let stdout = "";
      let stderr = "";
      proc.stdout?.on("data", (d: Buffer) => {
        if (stdout.length < MAX_OUTPUT) stdout += d.toString();
      });
      proc.stderr?.on("data", (d: Buffer) => {
        if (stderr.length < MAX_OUTPUT) stderr += d.toString();
      });
      proc.on("error", (err: Error) =>
        settle({ error: `could not start the nightgauge binary: ${err.message}` })
      );
      proc.on("close", (code: number | null) => settle({ code, stdout, stderr }));
      // A child that exits before reading stdin raises EPIPE on the stream;
      // the close handler reports the exit, so the stream error is dropped.
      proc.stdin?.on("error", () => undefined);
      proc.stdin?.end(stdin ?? "");
    });
  }
}

function describeExit(result: RunResult): string {
  const detail = result.stderr.trim().split("\n").pop()?.slice(0, 200);
  return detail ? `exit ${result.code}: ${detail}` : `exit ${result.code}`;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// Startup migration
// ---------------------------------------------------------------------------

/** Extract platform.license_key from a YAML file via a line scan (no parser). */
export function extractLicenseKeyLine(raw: string): { key?: string; lineIndex: number } {
  const lines = raw.split("\n");
  let inPlatform = false;
  for (let i = 0; i < lines.length; i++) {
    const trimmed = lines[i].trim();
    if (trimmed === "platform:") {
      inPlatform = true;
      continue;
    }
    if (
      inPlatform &&
      trimmed &&
      !trimmed.startsWith("#") &&
      /^[a-z_]+:/.test(trimmed) &&
      !lines[i].startsWith(" ") &&
      !lines[i].startsWith("\t")
    ) {
      inPlatform = false;
      continue;
    }
    if (inPlatform) {
      const m = trimmed.match(/^license_key:\s*(.+)$/);
      if (m) {
        return { key: m[1].replace(/^['"]|['"]$/g, "").trim(), lineIndex: i };
      }
    }
  }
  return { lineIndex: -1 };
}

export interface LicenseMigrationDeps {
  fs: {
    existsSync: (path: string) => boolean;
    readFileSync: (path: string, encoding: "utf-8") => string;
    writeFileSync: (path: string, data: string, encoding: "utf-8") => void;
  };
  secrets: {
    getSecret: (key: string) => Promise<string | undefined>;
    setSecret: (key: string, value: string) => Promise<void>;
  };
  bridge: Pick<LicenseKeychainBridge, "store" | "status">;
  /** SecretStorage key of the license key. */
  secretKey: string;
  /** The workspace's committed .nightgauge/config.yaml, when there is a workspace. */
  projectConfigPath?: string;
  /** The machine-tier config file. */
  machineConfigPath: string;
  log?: (message: string) => void;
}

function readLicenseLine(
  fs: LicenseMigrationDeps["fs"],
  path: string
): { raw: string; key?: string; lineIndex: number } | null {
  if (!fs.existsSync(path)) return null;
  const raw = fs.readFileSync(path, "utf-8");
  return { raw, ...extractLicenseKeyLine(raw) };
}

function removeLine(fs: LicenseMigrationDeps["fs"], path: string, raw: string, index: number) {
  const lines = raw.split("\n");
  lines.splice(index, 1);
  fs.writeFileSync(path, lines.join("\n"), "utf-8");
}

/**
 * Startup license-key migration. Returns the key the extension should use.
 *
 *  1. A key in the committed project config is moved to SecretStorage and the
 *     CLI's store, and always stripped from the file.
 *  2. A key only in the machine-tier file is copied to SecretStorage and the
 *     CLI's store; the file line is removed only once the keychain holds it,
 *     so a failed write never leaves the CLI with no key.
 *  3. A key already in SecretStorage (moved there before the CLI had a
 *     keychain store) is copied to the CLI's store when the CLI has none.
 */
export async function migrateLicenseKeyAtStartup(
  deps: LicenseMigrationDeps
): Promise<string | undefined> {
  const { fs, secrets, bridge, secretKey } = deps;

  if (deps.projectConfigPath) {
    const project = readLicenseLine(fs, deps.projectConfigPath);
    if (project?.key) {
      await secrets.setSecret(secretKey, project.key);
      removeLine(fs, deps.projectConfigPath, project.raw, project.lineIndex);
      await bridge.store(project.key);
      return project.key;
    }
  }

  const machine = readLicenseLine(fs, deps.machineConfigPath);
  const existing = await secrets.getSecret(secretKey);

  if (!existing) {
    if (!machine?.key) return undefined;
    await secrets.setSecret(secretKey, machine.key);
    const outcome = await bridge.store(machine.key);
    if (outcome.ok && outcome.source === "keychain") {
      removeLine(fs, deps.machineConfigPath, machine.raw, machine.lineIndex);
    }
    return machine.key;
  }

  const status = await bridge.status();
  if (!status) return existing;
  if (status.source === "none") {
    await bridge.store(existing);
  } else if (status.source === "machine-file" && status.keychainAvailable) {
    if (machine?.key === existing) {
      const outcome = await bridge.store(existing);
      if (outcome.ok && outcome.source === "keychain") {
        removeLine(fs, deps.machineConfigPath, machine.raw, machine.lineIndex);
      }
    } else {
      deps.log?.(
        "[licenseKeychainBridge] the machine-tier license key differs from the one in VS Code; leaving both in place"
      );
    }
  }
  return existing;
}

// ---------------------------------------------------------------------------
// Shared instance
// ---------------------------------------------------------------------------

let shared: LicenseKeychainBridge | null = null;

/**
 * The bridge every extension flow shares, so a failing binary surfaces one
 * warning per session rather than one per flow.
 */
export function getLicenseKeychainBridge(
  factory: () => LicenseKeychainBridgeDeps
): LicenseKeychainBridge {
  if (!shared) shared = new LicenseKeychainBridge(factory());
  return shared;
}

/**
 * Test hook: install the shared instance (null drops it). tests/setup.ts
 * installs one that resolves no binary, so no unit test spawns the real
 * binary and writes the operator's keychain.
 */
export function setLicenseKeychainBridgeForTest(bridge: LicenseKeychainBridge | null): void {
  shared = bridge;
}

/** The shared bridge wired to the resolved binary and a VS Code warning. */
export function vscodeLicenseKeychainBridge(): LicenseKeychainBridge {
  return getLicenseKeychainBridge(() => ({
    resolveBinary: () => BinaryResolver.fromVSCode().resolve(),
    warn: (message) => void vscode.window.showWarningMessage(message),
    log: (message) => console.warn(message),
  }));
}

/**
 * Store a license key the user just activated: SecretStorage for the
 * extension, then the CLI's store. A failed CLI write keeps the SecretStorage
 * copy and warns once.
 */
export async function persistLicenseKey(
  secrets: { setSecret: (key: string, value: string) => Promise<void> },
  secretKey: string,
  key: string,
  bridge: Pick<LicenseKeychainBridge, "store"> = vscodeLicenseKeychainBridge()
): Promise<LicenseStoreOutcome> {
  await secrets.setSecret(secretKey, key);
  return bridge.store(key);
}
