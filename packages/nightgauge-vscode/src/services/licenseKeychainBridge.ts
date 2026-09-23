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
 *
 * Source of truth: the shared keychain entry. SecretStorage is the copy the
 * extension can read, and it is trusted only while its fingerprint matches
 * the CLI's (`auth license status --json`). The fingerprint of the key last
 * confirmed in the keychain is recorded, so a mismatch can be told apart: if
 * the CLI still holds that recorded key, the extension's newer write never
 * reached it and is retried; otherwise the key was changed outside VS Code,
 * the CLI's key wins, and the stale SecretStorage copy is dropped so the
 * daemon the extension spawns is never handed it.
 *
 * The CLI contract (subcommands and JSON fields) is pinned on both sides by
 * cmd/nightgauge/testdata/auth-license-contract.json.
 */

import { spawn as nodeSpawn } from "child_process";
import { createHash } from "crypto";
import * as vscode from "vscode";
import { BinaryResolver } from "./BinaryResolver";

/** The keychain entry the binary writes. Pinned by tests on both sides. */
export const LICENSE_KEYCHAIN_SERVICE = "nightgauge";
export const LICENSE_KEYCHAIN_ACCOUNT = "platform.license_key";

export const LICENSE_COMMAND: readonly string[] = ["auth", "license"];
export const LICENSE_SET_ARGS: readonly string[] = [...LICENSE_COMMAND, "set", "--json"];
export const LICENSE_STATUS_ARGS: readonly string[] = [...LICENSE_COMMAND, "status", "--json"];
export const LICENSE_CLEAR_ARGS: readonly string[] = [...LICENSE_COMMAND, "clear", "--json"];

/** What a user runs by hand when the extension could not. */
export const MANUAL_LICENSE_SET_COMMAND = "nightgauge auth license set";
export const MANUAL_LICENSE_CLEAR_COMMAND = "nightgauge auth license clear";

/** SecretStorage key recording the fingerprint last confirmed in the keychain. */
export const LICENSE_SYNCED_FINGERPRINT_SECRET = "nightgauge.platform.licenseKeySyncedFingerprint";

const DEFAULT_TIMEOUT_MS = 15_000;
const MAX_OUTPUT = 8_192;

/** A fingerprint as the binary prints it: exactly 12 lowercase hex characters. */
export function isWellFormedFingerprint(value: unknown): value is string {
  return typeof value === "string" && /^[0-9a-f]{12}$/.test(value);
}

/** Non-reversible key identifier; the same function as Go's keychain.Fingerprint. */
export function licenseKeyFingerprint(key: string): string {
  return key ? createHash("sha256").update(key).digest("hex").slice(0, 12) : "";
}

/** Where the binary put the key: the keychain, or the 0600 fallback file. */
export type LicenseStoreSource = "keychain" | "machine-file";

export type LicenseStoreOutcome =
  { ok: true; source: LicenseStoreSource } | { ok: false; reason: string };

export interface LicenseStatus {
  source: "env" | "keychain" | "machine-file" | "none";
  keychainAvailable: boolean;
  fingerprint?: string;
}

export interface LicenseClearResult {
  keychainCleared: boolean;
  fileCleared: boolean;
  localCleared: boolean;
  /** The host has no keychain service, so nothing can remain there. */
  noKeychain: boolean;
}

export interface LicenseKeychainBridgeDeps {
  /** Resolves the nightgauge binary, or null when there is none. */
  resolveBinary: () => Promise<string | null>;
  /** User-facing warning. Called at most once per bridge. */
  warn: (message: string) => void;
  /** User-facing information message. Called at most once per bridge. */
  inform?: (message: string) => void;
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

/** A failure whose cure is a newer binary, not a manual command. */
const OUTDATED = "outdated";

function parseJSON(stdout: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(stdout.trim());
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

export class LicenseKeychainBridge {
  private warned = false;
  private informed = false;

  constructor(private readonly deps: LicenseKeychainBridgeDeps) {}

  /**
   * Store the key where the CLI and daemon read it, and confirm (by
   * fingerprint) that the binary stored this key. On failure the caller's
   * SecretStorage copy is untouched and one warning says what to do.
   */
  async store(key: string): Promise<LicenseStoreOutcome> {
    const result = await this.run(LICENSE_SET_ARGS, key);
    if ("error" in result) {
      return this.fail("set", result.error);
    }
    const parsed = parseJSON(result.stdout);
    if (!parsed || typeof parsed.source !== "string") {
      return this.fail("set", unparsedReason(result));
    }
    if (result.code !== 0) {
      return this.fail("set", describeExit(result));
    }
    if (parsed.fingerprint !== licenseKeyFingerprint(key)) {
      return this.fail("set", "the binary did not confirm the key it stored");
    }
    if (parsed.source === "machine-file") {
      this.informOnce(
        "Nightgauge: this machine has no OS keychain, so the CLI keeps the license key in its machine config file (readable only by your user). Install a keychain service (for example gnome-keyring) to keep it out of files."
      );
      return { ok: true, source: "machine-file" };
    }
    if (parsed.source === "keychain") {
      return { ok: true, source: "keychain" };
    }
    return this.fail("set", OUTDATED);
  }

  /**
   * Remove every copy the CLI stores. Returns null unless the binary reported
   * what it removed and exited zero: it exits non-zero only when a keychain
   * entry may remain (a timeout or an unexpected error), not on a host that
   * has no keychain at all.
   */
  async clear(): Promise<LicenseClearResult | null> {
    const result = await this.run(LICENSE_CLEAR_ARGS);
    if ("error" in result) {
      this.fail("clear", result.error);
      return null;
    }
    const parsed = parseJSON(result.stdout);
    if (!parsed || typeof parsed.keychainCleared !== "boolean") {
      this.fail("clear", unparsedReason(result));
      return null;
    }
    if (result.code !== 0) {
      this.fail(
        "clear",
        typeof parsed.keychainError === "string" ? parsed.keychainError : describeExit(result)
      );
      return null;
    }
    return {
      keychainCleared: parsed.keychainCleared,
      fileCleared: parsed.fileCleared === true,
      localCleared: parsed.localCleared === true,
      noKeychain: parsed.noKeychain === true,
    };
  }

  /** Where the CLI would read the key from, ignoring the environment variable. */
  async status(): Promise<LicenseStatus | null> {
    const result = await this.run(LICENSE_STATUS_ARGS);
    if ("error" in result || result.code !== 0) {
      return null;
    }
    const parsed = parseJSON(result.stdout);
    const source = parsed?.source;
    if (
      source === "env" ||
      source === "keychain" ||
      source === "machine-file" ||
      source === "none"
    ) {
      return {
        source,
        keychainAvailable: parsed?.keychainAvailable === true,
        fingerprint: isWellFormedFingerprint(parsed?.fingerprint) ? parsed.fingerprint : undefined,
      };
    }
    return null;
  }

  /** Warn (once per bridge) that the binary cannot take part in the sync. */
  reportOutdated(): void {
    this.fail("set", OUTDATED);
  }

  /** Show one information message per bridge. */
  informOnce(message: string): void {
    if (!this.informed && this.deps.inform) {
      this.informed = true;
      this.deps.inform(message);
    }
  }

  private fail(action: "set" | "clear", reason: string): LicenseStoreOutcome {
    const shown =
      reason === OUTDATED ? "the nightgauge binary has no `auth license` command" : reason;
    this.deps.log?.(`[licenseKeychainBridge] auth license ${action} failed: ${shown}`);
    if (!this.warned) {
      this.warned = true;
      let message: string;
      if (reason === OUTDATED) {
        message =
          "Nightgauge: your nightgauge binary is too old to share the license key with the CLI and daemon (it has no `auth license` command). Update the nightgauge binary; until then the CLI outside VS Code can't see the key.";
      } else if (action === "set") {
        message = `Nightgauge: the license key is saved in VS Code, but the CLI and daemon outside VS Code can't see it (${reason}). Run \`${MANUAL_LICENSE_SET_COMMAND}\` in a terminal and paste the key.`;
      } else {
        message = `Nightgauge: the license key was removed from VS Code, but the CLI's stored copy may remain (${reason}). Run \`${MANUAL_LICENSE_CLEAR_COMMAND}\` in a terminal.`;
      }
      this.deps.warn(message);
    }
    return { ok: false, reason: shown };
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
      let timer: NodeJS.Timeout | undefined;
      const settle = (value: RunResult | { error: string }) => {
        if (!settled) {
          settled = true;
          if (timer) clearTimeout(timer);
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
        settle({ error: `could not start the nightgauge binary: ${errorMessage(err)}` });
        return;
      }
      timer = setTimeout(() => {
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

/**
 * Why a run printed no parseable result. A binary that predates
 * `auth license` prints cobra help (exit 0) or an unknown-command usage error;
 * both mean "update the binary", not "run a command".
 */
function unparsedReason(result: RunResult): string {
  const out = `${result.stdout}\n${result.stderr}`;
  if (
    result.code === 0 ||
    /unknown (command|flag)|Available Commands:|for more information about a command/i.test(out)
  ) {
    return OUTDATED;
  }
  return describeExit(result);
}

function describeExit(result: RunResult): string {
  const detail = result.stderr.trim().split("\n").pop()?.slice(0, 200);
  return detail ? `exit ${result.code}: ${detail}` : `exit ${result.code}`;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// Reconciliation and startup migration
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

export interface LicenseSecrets {
  getSecret: (key: string) => Promise<string | undefined>;
  setSecret: (key: string, value: string) => Promise<void>;
  deleteSecret: (key: string) => Promise<void>;
}

type BridgeLike = Pick<LicenseKeychainBridge, "store" | "status" | "informOnce" | "reportOutdated">;

export interface LicenseMigrationDeps {
  fs: {
    existsSync: (path: string) => boolean;
    readFileSync: (path: string, encoding: "utf-8") => string;
    writeFileSync: (path: string, data: string, encoding: "utf-8") => void;
  };
  secrets: LicenseSecrets;
  bridge: BridgeLike;
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

/** Remove the license_key line from path as it reads now, if it holds key. */
function removeLicenseLine(fs: LicenseMigrationDeps["fs"], path: string, key: string): void {
  const current = readLicenseLine(fs, path);
  if (!current || current.key !== key) return;
  const lines = current.raw.split("\n");
  lines.splice(current.lineIndex, 1);
  fs.writeFileSync(path, lines.join("\n"), "utf-8");
}

/**
 * Store a key the user gave the extension: SecretStorage, then the CLI's
 * store. On success the key's fingerprint is recorded as the one the keychain
 * holds; on failure the SecretStorage copy stays and the record does not
 * move, so the next startup retries the write.
 */
export async function persistLicenseKey(
  secrets: Pick<LicenseSecrets, "setSecret">,
  secretKey: string,
  key: string,
  bridge: Pick<LicenseKeychainBridge, "store"> = vscodeLicenseKeychainBridge()
): Promise<LicenseStoreOutcome> {
  await secrets.setSecret(secretKey, key);
  const outcome = await bridge.store(key);
  if (outcome.ok) {
    await secrets.setSecret(LICENSE_SYNCED_FINGERPRINT_SECRET, licenseKeyFingerprint(key));
  }
  return outcome;
}

/** Clear the key everywhere: SecretStorage, the sync record, and the CLI's store. */
export async function forgetLicenseKey(
  secrets: Pick<LicenseSecrets, "deleteSecret">,
  secretKey: string,
  bridge: Pick<LicenseKeychainBridge, "clear"> = vscodeLicenseKeychainBridge()
): Promise<boolean> {
  await secrets.deleteSecret(secretKey);
  await secrets.deleteSecret(LICENSE_SYNCED_FINGERPRINT_SECRET);
  return (await bridge.clear()) !== null;
}

/**
 * Bring SecretStorage and the CLI's store into agreement. Returns the key the
 * extension may use (and hand the daemon), or undefined when it has none it
 * can trust. The rule is in the module comment.
 *
 * The SecretStorage copy is deleted only on positive evidence of rotation: a
 * well-formed fingerprint from the CLI that differs from both the extension's
 * key and the fingerprint recorded at the last confirmed sync. Anything less
 * (no binary, a failed or unparseable status, a missing or malformed
 * fingerprint, no recorded sync) keeps the key.
 */
export async function reconcileLicenseKey(
  deps: Pick<LicenseMigrationDeps, "secrets" | "bridge" | "secretKey" | "log">
): Promise<string | undefined> {
  const { secrets, bridge, secretKey } = deps;
  const ext = await secrets.getSecret(secretKey);
  if (!ext) return undefined;
  const status = await bridge.status();
  if (!status) return ext; // no usable binary: nothing to compare against
  const fp = licenseKeyFingerprint(ext);
  const push = () => persistLicenseKey(secrets, secretKey, ext, bridge);

  if (status.source === "none") {
    await push();
    return ext;
  }
  if (!isWellFormedFingerprint(status.fingerprint)) {
    // A binary that reports a stored key but no usable fingerprint cannot
    // take part in the comparison. Keep the key; ask for a newer binary.
    deps.log?.(
      "[licenseKeychainBridge] status reported no usable fingerprint; keeping the VS Code key"
    );
    bridge.reportOutdated();
    return ext;
  }
  if (status.fingerprint === fp) {
    if (status.source === "machine-file" && status.keychainAvailable) {
      await push(); // move it into the keychain; the CLI drops the plaintext copy
    } else if ((await secrets.getSecret(LICENSE_SYNCED_FINGERPRINT_SECRET)) !== fp) {
      await secrets.setSecret(LICENSE_SYNCED_FINGERPRINT_SECRET, fp);
    }
    return ext;
  }

  const synced = await secrets.getSecret(LICENSE_SYNCED_FINGERPRINT_SECRET);
  if (synced === status.fingerprint || (synced === undefined && status.source === "machine-file")) {
    // The CLI still holds the key last synced (or a pre-keychain plaintext
    // copy): the extension's newer key never reached it. Retry the write.
    deps.log?.("[licenseKeychainBridge] the CLI holds an older license key; updating it");
    await push();
    return ext;
  }
  if (!isWellFormedFingerprint(synced)) {
    // The keys differ but there is no record of a confirmed sync, so there
    // is no evidence which side changed. Keep both; let the user pick.
    deps.log?.(
      "[licenseKeychainBridge] the CLI's license key differs and no sync is recorded; keeping both"
    );
    bridge.informOnce(
      "Nightgauge: the license key in VS Code differs from the one the CLI uses. Run 'Nightgauge: Activate License' with the current key to use one key everywhere."
    );
    return ext;
  }

  // Positive evidence: the CLI's key differs from the one last confirmed in
  // sync, so it was changed outside VS Code. The shared entry wins.
  deps.log?.(
    "[licenseKeychainBridge] the license key was changed outside VS Code; dropping the VS Code copy"
  );
  await secrets.deleteSecret(secretKey);
  await secrets.deleteSecret(LICENSE_SYNCED_FINGERPRINT_SECRET);
  bridge.informOnce(
    "Nightgauge: the license key was changed outside VS Code (nightgauge auth license set), so VS Code stopped using its older copy. Run 'Nightgauge: Activate License' with the current key to use it in VS Code too."
  );
  return undefined;
}

/**
 * Startup license-key migration and reconciliation. Returns the key the
 * extension should use.
 *
 *  1. A key in the committed project config is stripped from the file and
 *     stored in SecretStorage and the CLI's store.
 *  2. A key only in the machine-tier file is stored in SecretStorage and the
 *     CLI's store. The binary removes the plaintext line once the keychain
 *     holds the key; a failed write leaves the file untouched, so the CLI
 *     never ends up with no key.
 *  3. Otherwise SecretStorage and the CLI are reconciled.
 */
export async function migrateLicenseKeyAtStartup(
  deps: LicenseMigrationDeps
): Promise<string | undefined> {
  const { fs, secrets, bridge, secretKey } = deps;

  if (deps.projectConfigPath) {
    const project = readLicenseLine(fs, deps.projectConfigPath);
    if (project?.key) {
      removeLicenseLine(fs, deps.projectConfigPath, project.key);
      await persistLicenseKey(secrets, secretKey, project.key, bridge);
      return project.key;
    }
  }

  if (!(await secrets.getSecret(secretKey))) {
    const machine = readLicenseLine(fs, deps.machineConfigPath);
    if (!machine?.key) return undefined;
    const outcome = await persistLicenseKey(secrets, secretKey, machine.key, bridge);
    if (outcome.ok && outcome.source === "keychain") {
      // The binary already removed it; this covers a file it could not edit.
      removeLicenseLine(fs, deps.machineConfigPath, machine.key);
    }
    return machine.key;
  }

  return reconcileLicenseKey(deps);
}

// ---------------------------------------------------------------------------
// Startup ordering
// ---------------------------------------------------------------------------

let reconciliation: Promise<unknown> = Promise.resolve();

/** Record the startup reconciliation so the daemon spawn can wait for it. */
export function setLicenseReconciliation(p: Promise<unknown>): void {
  reconciliation = p.catch(() => undefined);
}

/**
 * Wait (bounded) for the startup reconciliation, so the daemon is not spawned
 * with a SecretStorage key that reconciliation is about to drop.
 */
export async function whenLicenseReconciled(timeoutMs = 5_000): Promise<void> {
  let timer: NodeJS.Timeout | undefined;
  await Promise.race([
    reconciliation,
    new Promise<void>((resolve) => {
      timer = setTimeout(resolve, timeoutMs);
    }),
  ]);
  if (timer) clearTimeout(timer);
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

/** The shared bridge wired to the resolved binary and VS Code messages. */
export function vscodeLicenseKeychainBridge(): LicenseKeychainBridge {
  return getLicenseKeychainBridge(() => ({
    resolveBinary: () => BinaryResolver.fromVSCode().resolve(),
    warn: (message) => void vscode.window.showWarningMessage(message),
    inform: (message) => void vscode.window.showInformationMessage(message),
    log: (message) => console.warn(message),
  }));
}
