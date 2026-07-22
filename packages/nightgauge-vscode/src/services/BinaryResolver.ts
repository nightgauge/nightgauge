/**
 * BinaryResolver — Resolves the path to the nightgauge Go binary.
 *
 * Extracts the 4-tier resolution logic from IpcClientBase.resolveBinaryPath()
 * into a standalone, dependency-injectable class. This enables testing against
 * a real filesystem without requiring a live VSCode instance.
 *
 * Resolution order:
 *   1. VSCode setting: nightgauge.backend.binaryPath
 *   2. Environment variable: NIGHTGAUGE_GO_BINARY_PATH
 *   3. Extension-bundled binary: dist/bin/nightgauge-{platform}-{arch}
 *   4. System PATH: which/where nightgauge
 *
 * @see IpcClientBase.ts — Consumer (calls BinaryResolver.fromVSCode().resolve())
 * @see tests/deployment/binary-resolution.test.ts — Real-filesystem tests
 */

import { existsSync } from "fs";
import { join } from "path";
import * as vscode from "vscode";
import { resolveExtensionBundleRoot } from "../utils/extensionBundle";

// ---------------------------------------------------------------------------
// Public interface
// ---------------------------------------------------------------------------

export interface BinaryResolverDeps {
  /** fs.existsSync or a real-filesystem equivalent. Not mocked in tests. */
  existsSync: (path: string) => boolean;
  /** Returns nightgauge.backend.binaryPath setting value, or '' if unset. */
  getVSCodeSetting: () => string;
  /** Returns the extension's install path (extensionPath), or undefined if not in a VSCode host. */
  getExtensionPath: () => string | undefined;
  /** process.platform value */
  platform: string;
  /** process.arch value */
  arch: string;
  /** process.env */
  env: NodeJS.ProcessEnv;
  /** Resolves a binary name via PATH. Returns null if not found. */
  which: (binary: string) => Promise<string | null>;
}

// ---------------------------------------------------------------------------
// BinaryResolver
// ---------------------------------------------------------------------------

export class BinaryResolver {
  constructor(private readonly deps: BinaryResolverDeps) {}

  async resolve(): Promise<string | null> {
    // Tiers 1-3 are synchronous filesystem checks.
    const fsResolved = this.resolveSync();
    if (fsResolved) return fsResolved;

    // Tier 4: System PATH (global install via brew, etc.)
    return this.deps.which("nightgauge");
  }

  /**
   * Synchronous resolution covering the filesystem tiers only (1-3): VSCode
   * setting, NIGHTGAUGE_GO_BINARY_PATH, and the extension-bundled binary.
   *
   * Used by skillRunner (#4029) to export NIGHTGAUGE_BIN into the skill
   * subprocess environment without an async hop — runStageSkillHeadless is a
   * synchronous factory and must not become a Promise. Tier 4 (PATH lookup) is
   * intentionally omitted: when the binary is already on PATH, the skill's own
   * `command -v nightgauge` cascade resolves it, so no env injection is
   * needed in that case. Returns null when none of the filesystem tiers match.
   */
  resolveSync(): string | null {
    // Tier 1: VSCode setting (power user / development override)
    const configured = this.deps.getVSCodeSetting();
    if (configured && this.deps.existsSync(configured)) return configured;

    // Tier 2: Environment variable (CI / testing override)
    const envPath = this.deps.env.NIGHTGAUGE_GO_BINARY_PATH;
    if (envPath && this.deps.existsSync(envPath)) return envPath;

    // Tier 3: Extension-bundled binary (production — primary path)
    const extPath = this.deps.getExtensionPath();
    if (extPath) {
      const platform =
        this.deps.platform === "win32"
          ? "win"
          : this.deps.platform === "linux"
            ? "linux"
            : "darwin";
      const arch =
        this.deps.arch === "x64" ? "amd64" : this.deps.arch === "arm64" ? "arm64" : "amd64";
      const suffix = this.deps.platform === "win32" ? ".exe" : "";

      const bundledBin = join(extPath, "dist", "bin", `nightgauge-${platform}-${arch}${suffix}`);
      if (this.deps.existsSync(bundledBin)) return bundledBin;

      // Also check for a plain 'nightgauge' binary (single-platform build)
      const bundledPlain = join(extPath, "dist", "bin", `nightgauge${suffix}`);
      if (this.deps.existsSync(bundledPlain)) return bundledPlain;
    }

    return null;
  }

  /**
   * Factory for production use — wires live VSCode APIs and real filesystem.
   * Call this from IpcClientBase when running inside a VSCode extension host.
   */
  static fromVSCode(): BinaryResolver {
    return new BinaryResolver({
      existsSync,
      getVSCodeSetting: () => {
        const config = vscode.workspace.getConfiguration("nightgauge.backend");
        return config.get<string>("binaryPath", "");
      },
      getExtensionPath: () => {
        const ext = vscode.extensions.getExtension("nightgauge.nightgauge-vscode");
        // Self-heal past a garbage-collected extension dir (auto-update): if the
        // running extensionPath was removed, resolve to the newest surviving
        // sibling version so the bundled binary still resolves (#3883).
        return resolveExtensionBundleRoot(ext?.extensionPath);
      },
      platform: process.platform,
      arch: process.arch,
      env: process.env,
      which: async (binary: string) => {
        const { exec } = await import("child_process");
        const { promisify } = await import("util");
        const execAsync = promisify(exec);
        try {
          const cmd = process.platform === "win32" ? `where ${binary}` : `which ${binary}`;
          const { stdout } = await execAsync(cmd, { timeout: 5000 });
          const path = stdout.trim();
          return path || null;
        } catch {
          return null;
        }
      },
    });
  }
}
