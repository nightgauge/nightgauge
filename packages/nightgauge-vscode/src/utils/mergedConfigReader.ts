/**
 * Merged Config Reader — the single synchronous entry point for reading the
 * EFFECTIVE Nightgauge configuration as YAML text.
 *
 * Historically every synchronous config consumer (utils/resolvers/*, the
 * skillRunner auto-accept loader, ApprovalDialog, …) read ONLY the
 * project-tier file (.nightgauge/config.yaml) and silently ignored the
 * machine tier (~/.nightgauge/config.yaml), the local tier
 * (.nightgauge/config.local.yaml), and NIGHTGAUGE_* env overrides.
 * Whether a tier applied therefore depended on WHICH resolver happened to
 * read a key — the fragmentation documented in docs/SETTINGS_ARCHITECTURE.md.
 *
 * This module materializes the merged tiers back into YAML text so the
 * existing line/regex parsers keep working unchanged:
 *
 *   global (machine)  →  project (team)  →  local  →  env (NIGHTGAUGE_*)
 *
 * matching the Go binary's LoadMerged (internal/config/merge.go) and the
 * async configMergeEngine precedence. Two tiers are intentionally NOT merged
 * here:
 *
 * - runtime (VSCode memento): lives in extension state and is applied by
 *   configMergeEngine/ConfigBridge. File-level readers also run against
 *   worktree roots where extension state must not leak in.
 * - CLI flags: per-invocation, handled by the CLI layer.
 *
 * Merge semantics match configMergeEngine.deepMerge (and Go): objects
 * deep-merge, arrays and scalars replace, explicit null overrides, undefined
 * is skipped.
 *
 * Deliberate non-consumers (do NOT route through this reader):
 * - authResolver.getGitHubUser — implements custom per-tier identity rules
 *   (`github_user` is repo-scoped by design and must never be inherited from
 *   the machine config in multi-account workspaces, #2487).
 * - Any read-to-WRITE path (customStageModels write/clear, IncrediYamlService
 *   writers) — writes must edit the raw tier file, never the merged view,
 *   or machine/local values would be materialized into committed files.
 *
 * Robustness: if the project tier fails to parse as YAML the RAW project
 * text is returned (exactly the pre-merge behavior); an unparseable global
 * or local tier is skipped with a console warning.
 */

// NOTE — deliberately minimal imports: dozens of resolver test files mock
// the fs module and/or the configPathResolver module. This reader must (a)
// see the same mocked fs the resolver under test sees, and (b) not call
// through a mocked-away configPathResolver — so the sibling tier paths are
// derived from the project path directly, and the only configPathResolver
// import is a type.
//
// The codebase imports fs under BOTH specifiers ("node:fs" in the resolvers
// and configMerger, bare "fs" in skillRunner/customStageModels/…), and
// vitest mocks them independently. readFileAny/statAny below try both
// registrations with exception fallthrough, so whichever one a test mocked
// is the one that answers; in production both resolve to the same module
// and the first succeeds.
import * as nodeFs from "node:fs";
import * as bareFs from "fs";
import * as path from "path";
import { parse as parseYaml, stringify as stringifyYaml } from "yaml";
import type { ConfigPathResult } from "./configPathResolver";
import { resolveGlobalConfigPathSync } from "./globalConfigResolver";
import { deepMerge } from "../config/configMergeEngine";
import { resolveEnvVars } from "../config/envVarResolver";

/** Local-tier file name — mirrors configPathResolver.LOCAL_CONFIG_FILE_NAME. */
const LOCAL_CONFIG_FILE_NAME = "config.local.yaml";

// Warn-once registry: a corrupt tier file would otherwise emit one warning
// per uncached read — in production that floods the console for the session's
// lifetime, and under vitest (where suites mock fs to return non-YAML for
// every path, including config paths) it buries test output in hundreds of
// identical lines. One warning per (site, path, content) is the signal;
// repetition is noise. Keyed on content so an EDITED-but-still-broken file
// re-warns — "still corrupt after my fix" must stay visible.
const warnedForContent = new Map<string, string>();

function warnOncePerContent(key: string, content: string, message: string, err?: unknown): void {
  if (warnedForContent.get(key) === content) return;
  warnedForContent.set(key, content);
  if (err !== undefined) {
    console.warn(message, err);
  } else {
    console.warn(message);
  }
}

const FS_CANDIDATES = [nodeFs, bareFs] as const;

/**
 * Look up a function export defensively. Vitest module mocks are proxies
 * that THROW on access to an export the mock does not define — so even
 * `typeof mod.statSync` must be guarded.
 */
function getFsFn<K extends "readFileSync" | "statSync">(
  mod: (typeof FS_CANDIDATES)[number],
  name: K
): (typeof nodeFs)[K] | undefined {
  try {
    const fn = mod[name];
    return typeof fn === "function" ? fn : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Read a file as UTF-8 through whichever fs registration answers.
 * Returns undefined when the file is absent in every registration (or a
 * mocked readFileSync yields a non-string).
 */
function readFileAny(filePath: string): string | undefined {
  for (const f of FS_CANDIDATES) {
    const readFileSync = getFsFn(f, "readFileSync");
    if (!readFileSync) continue;
    try {
      const out = readFileSync(filePath, "utf-8");
      if (typeof out === "string") return out;
    } catch {
      // Absent under this registration — try the next.
    }
  }
  return undefined;
}

/**
 * True when both fs registrations resolve to the SAME module — the
 * production condition. When a test mocks either specifier the identities
 * diverge, and the mtime cache must be bypassed entirely (a mocked
 * readFileSync can change its fixture without any stat changing).
 */
function fsIsReal(): boolean {
  return (nodeFs as unknown) === (bareFs as unknown);
}

interface MergedCacheEntry {
  /** Concatenated stat fingerprints of the three tier files. */
  fingerprint: string;
  /** Merged global→project→local YAML text (no env overlay). */
  baseText: string;
  /**
   * Parsed merged object for the env overlay, or null when the project tier
   * was unparseable and baseText is the raw project text.
   */
  baseObj: Record<string, unknown> | null;
}

/** Cache keyed by the resolved project config path. */
const cache = new Map<string, MergedCacheEntry>();

/** Test hook — clears the mtime-fingerprint cache between test cases. */
export function clearMergedConfigCacheForTests(): void {
  cache.clear();
}

/**
 * Stat-based change fingerprint for one tier file.
 *
 * Returns `<path>:absent` when the file does not exist (so creation/deletion
 * invalidates the cache) and `null` when stat itself is unavailable — the
 * signature of a test environment that mocked `fs` without `statSync`. A
 * null anywhere makes the read uncacheable (see readEffectiveConfigTextSync).
 */
function statFingerprint(filePath: string): string | null {
  for (const f of FS_CANDIDATES) {
    const statSync = getFsFn(f, "statSync");
    if (!statSync) continue;
    try {
      const s = statSync(filePath);
      return `${filePath}:${s.mtimeMs}:${s.size}:${s.ctimeMs}`;
    } catch (err) {
      if ((err as NodeJS.ErrnoException)?.code === "ENOENT") {
        return `${filePath}:absent`;
      }
    }
  }
  return null;
}

/**
 * Parse one tier file into a mapping object.
 *
 * Returns:
 * - the parsed mapping (empty object for an empty file)
 * - `undefined` when the file does not exist (tier skipped silently)
 * - `null` when the file exists but is not parseable as a YAML mapping
 *   (tier skipped with a warning; for the project tier the caller falls
 *   back to raw text instead)
 */
function parseTierFile(
  filePath: string,
  label: string
): Record<string, unknown> | null | undefined {
  const raw = readFileAny(filePath);
  if (raw === undefined) {
    return undefined;
  }
  try {
    const parsed: unknown = parseYaml(raw);
    if (parsed === null || parsed === undefined) return {};
    if (typeof parsed !== "object" || Array.isArray(parsed)) {
      warnOncePerContent(
        `mapping:${filePath}`,
        raw,
        `[mergedConfigReader] ${label} config at ${filePath} is not a YAML mapping — skipping tier`
      );
      return null;
    }
    return parsed as Record<string, unknown>;
  } catch (err) {
    warnOncePerContent(
      `parse:${filePath}`,
      raw,
      `[mergedConfigReader] Skipping unparseable ${label} config at ${filePath}:`,
      err
    );
    return null;
  }
}

function buildEntry(
  projectPath: string,
  localPath: string,
  globalPath: string,
  fingerprint: string
): MergedCacheEntry {
  // Project file absent → empty string; the merge proceeds with the other tiers.
  const projectRaw = readFileAny(projectPath) ?? "";

  let projectObj: Record<string, unknown> | null = {};
  if (projectRaw.trim() !== "") {
    try {
      const parsed: unknown = parseYaml(projectRaw);
      projectObj =
        parsed && typeof parsed === "object" && !Array.isArray(parsed)
          ? (parsed as Record<string, unknown>)
          : null;
    } catch {
      projectObj = null;
    }
  }

  if (projectObj === null) {
    // Unparseable project config — preserve the exact pre-merge behavior so
    // the regex parsers see what they always saw.
    warnOncePerContent(
      `project:${projectPath}`,
      projectRaw,
      `[mergedConfigReader] Project config at ${projectPath} is not parseable YAML — ` +
        `machine/local/env tiers are NOT merged for this read`
    );
    return { fingerprint, baseText: projectRaw, baseObj: null };
  }

  const globalObj = parseTierFile(globalPath, "machine") ?? {};
  const localObj = parseTierFile(localPath, "local") ?? {};

  const merged = deepMerge(deepMerge(globalObj ?? {}, projectObj), localObj ?? {});
  if (Object.keys(merged).length === 0) {
    // Nothing in any tier — emit empty text, not "{}", so downstream
    // line parsers see the same shape an empty file always had.
    return { fingerprint, baseText: "", baseObj: merged };
  }
  try {
    // lineWidth: 0 disables folding so long scalar values stay on one line
    // for the line-oriented parsers downstream.
    const text = stringifyYaml(merged, { lineWidth: 0 });
    return { fingerprint, baseText: text, baseObj: merged };
  } catch {
    return { fingerprint, baseText: projectRaw, baseObj: null };
  }
}

/**
 * Read the effective (tier-merged) config as YAML text.
 *
 * Drop-in replacement for `fs.readFileSync(pathResult.path, "utf-8")` at
 * resolver read sites: takes the ConfigPathResult from
 * `resolveConfigPathSync(root)` and returns merged YAML text instead of the
 * raw project file. The workspace root is derived from the project path
 * (`<root>/.nightgauge/config.yaml`), so worktree invocations merge the
 * WORKTREE's project+local files with the machine tier — identical to the Go
 * binary's `config.Load(worktreeDir)`.
 *
 * The global→project→local merge is cached per project path and invalidated
 * by mtime/size changes of any tier file. The env overlay is applied per
 * call so env-var changes are always live.
 */
export function readEffectiveConfigTextSync(pathResult: ConfigPathResult): string {
  const projectPath = pathResult.path;
  // Sibling local tier lives next to the project file — derived directly so
  // a mocked configPathResolver in tests cannot break the reader.
  const localPath = path.join(path.dirname(projectPath), LOCAL_CONFIG_FILE_NAME);
  const globalPath = resolveGlobalConfigPathSync().path;

  const parts = [
    statFingerprint(globalPath),
    statFingerprint(projectPath),
    statFingerprint(localPath),
  ];
  // Uncacheable whenever either fs registration is mocked (identities
  // diverge) or a stat is unavailable: compute fresh each call so per-test
  // mockReturnValue changes are always observed. In production fs is real,
  // stats are available, and the mtime cache serves hot reads.
  const cacheable = fsIsReal() && parts.every((p) => p !== null);
  const fingerprint = parts.join("|");

  let entry = cacheable ? cache.get(projectPath) : undefined;
  if (!entry || entry.fingerprint !== fingerprint) {
    entry = buildEntry(projectPath, localPath, globalPath, fingerprint);
    if (cacheable) {
      cache.set(projectPath, entry);
    }
  }

  // Env overlay (tier 6) — skipped when the project tier was unparseable
  // (raw-text fallback) or when no NIGHTGAUGE_* override is active.
  if (entry.baseObj) {
    const envResult = resolveEnvVars();
    if (envResult.appliedVars.length > 0) {
      try {
        const withEnv = deepMerge(entry.baseObj, envResult.config as Record<string, unknown>);
        return stringifyYaml(withEnv, { lineWidth: 0 });
      } catch {
        return entry.baseText;
      }
    }
  }
  return entry.baseText;
}
