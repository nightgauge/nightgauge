/**
 * Install Nightgauge skills into an agent home directory (~/.codex/skills,
 * ~/.grok/skills). Every future session of that agent, in every project,
 * loads what lands there, so both the source and the writes are constrained.
 *
 * - The default source is the VSIX bundle and nothing else. The VSIX ships
 *   the plugin skills under dist/claude-plugins/nightgauge/skills (short
 *   names) and a pipeline subset under dist/skills (canonical names). A
 *   workspace `skills/` tree is never consulted implicitly: it is reachable
 *   only through installWorkspaceSkills(), a development command that
 *   refuses untrusted workspaces and names the absolute source and
 *   destination paths in a modal confirmation.
 * - copySkillsTree() writes only SKILL.md directories and `_shared/`. It
 *   copies a linked file only when the link resolves inside the source tree,
 *   never follows a linked directory, refuses a destination that overlaps the
 *   source, and replaces an existing destination directory only when it
 *   holds the install marker Nightgauge writes.
 */

import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";

const NAMESPACE = "nightgauge-";
const UNPREFIXED = new Set(["_shared", "smart-setup", "update-docs", "pr-preflight"]);

/**
 * Written into every directory copySkillsTree installs. A name, generic
 * (`update-docs`, `_shared`) or namespaced (`nightgauge-*`), proves nothing
 * about who put a directory there; this marker is the only evidence that
 * Nightgauge did. scripts/install-agent-skills.sh writes no marker, so a
 * directory it installed is left untouched.
 */
export const INSTALL_MARKER = ".nightgauge-installed";
const INSTALL_MARKER_TEXT =
  "Installed by Nightgauge, which replaces this directory when it installs its skills again.\n";

/** Receives one line per source or destination entry the install skipped. */
export type SkillCopyWarn = (message: string) => void;

export function resolveBundledSkillsDir(extensionPath: string): string | null {
  const pluginSkills = path.join(extensionPath, "dist", "claude-plugins", "nightgauge", "skills");
  if (fs.existsSync(pluginSkills)) {
    return pluginSkills;
  }
  const distSkills = path.join(extensionPath, "dist", "skills");
  if (fs.existsSync(distSkills)) {
    return distSkills;
  }
  return null;
}

function destSkillName(dirName: string, restorePrefix: boolean): string {
  if (!restorePrefix) {
    return dirName;
  }
  if (UNPREFIXED.has(dirName) || dirName.startsWith(NAMESPACE)) {
    return dirName;
  }
  return `${NAMESPACE}${dirName}`;
}

/**
 * The names Nightgauge installs under: its `nightgauge-` namespace and the
 * unprefixed names it ships. Every other name in an agent's skills
 * directory belongs to the operator or to another tool.
 */
function isNightgaugeSkillName(name: string): boolean {
  return name.startsWith(NAMESPACE) || UNPREFIXED.has(name);
}

function isWithin(root: string, candidate: string): boolean {
  const rel = path.relative(root, candidate);
  return rel === "" || (rel !== ".." && !rel.startsWith(`..${path.sep}`) && !path.isAbsolute(rel));
}

function isMissing(error: unknown): boolean {
  return (error as NodeJS.ErrnoException).code === "ENOENT";
}

/**
 * The real path of `p`. When `p` does not exist yet, the real path of its
 * nearest existing ancestor joined with the rest, which is where a write to
 * `p` would land.
 */
async function realpathNearest(p: string): Promise<string> {
  const rest: string[] = [];
  let current = path.resolve(p);
  for (;;) {
    try {
      return path.join(await fs.promises.realpath(current), ...rest);
    } catch (error) {
      const parent = path.dirname(current);
      if (!isMissing(error) || parent === current) {
        throw error;
      }
      rest.unshift(path.basename(current));
      current = parent;
    }
  }
}

/**
 * Resolve both ends of an install and refuse when one contains the other:
 * replacing a destination directory would then delete the source it is
 * about to be copied from (for example an agent's skills directory
 * symlinked to a checkout's `skills/`).
 */
async function resolveInstallPaths(
  sourceDir: string,
  destDir: string
): Promise<{ root: string; realDest: string }> {
  const root = await fs.promises.realpath(sourceDir);
  const realDest = await realpathNearest(destDir);
  if (isWithin(root, realDest) || isWithin(realDest, root)) {
    throw new Error(
      `Nightgauge will not install skills from ${root} into ${realDest}: ` +
        "one directory contains the other."
    );
  }
  return { root, realDest };
}

interface ResolvedEntry {
  realPath: string;
  stats: fs.Stats;
}

/**
 * Stat a source entry and return it only when it is a real directory, a
 * regular file, or a link to a regular file inside `root`. A missing entry
 * returns null silently. Anything else is skipped with a warning: a link
 * that escapes the source tree or dangles, and every link to a directory,
 * so the copy walks only the real tree (no loops, no fan-out through links).
 */
async function resolveContained(
  entryPath: string,
  root: string,
  warn: SkillCopyWarn
): Promise<ResolvedEntry | null> {
  let stats: fs.Stats;
  try {
    stats = await fs.promises.lstat(entryPath);
  } catch (error) {
    if (isMissing(error)) {
      return null;
    }
    throw error;
  }
  let realPath = entryPath;
  if (stats.isSymbolicLink()) {
    const target = await fs.promises.readlink(entryPath);
    const resolved = await fs.promises.realpath(entryPath).catch(() => null);
    if (resolved === null || !isWithin(root, resolved)) {
      warn(`Skipped symlink ${entryPath} -> ${target}: it does not resolve inside ${root}.`);
      return null;
    }
    stats = await fs.promises.stat(resolved);
    if (stats.isDirectory()) {
      warn(
        `Skipped symlink ${entryPath} -> ${target}: ` +
          "Nightgauge copies linked files, never linked directories."
      );
      return null;
    }
    realPath = resolved;
  }
  if (!stats.isFile() && !stats.isDirectory()) {
    warn(`Skipped ${entryPath}: it is not a regular file or directory.`);
    return null;
  }
  return { realPath, stats };
}

/**
 * True when copySkillsTree may replace `target`: nothing is there, or it is
 * a real directory holding the install marker. The name proves nothing, not
 * even in the `nightgauge-` namespace: an agent's skills directory linked to
 * a checkout's `skills/` holds `nightgauge-` directories with uncommitted
 * work. A symlink or a file is always the operator's.
 */
async function mayReplace(target: string): Promise<boolean> {
  let stats: fs.Stats;
  try {
    stats = await fs.promises.lstat(target);
  } catch (error) {
    if (isMissing(error)) {
      return true;
    }
    throw error;
  }
  if (!stats.isDirectory()) {
    return false;
  }
  const marker = await fs.promises.lstat(path.join(target, INSTALL_MARKER)).catch(() => null);
  return marker?.isFile() ?? false;
}

/**
 * Copy a real directory's contents; links to files inside `root` land as
 * regular files. The marker name belongs to copySkillsTree, so a source
 * entry by that name is never copied over it.
 */
async function copyContained(
  sourceDir: string,
  targetDir: string,
  root: string,
  warn: SkillCopyWarn
): Promise<void> {
  await fs.promises.mkdir(targetDir, { recursive: true });
  for (const name of await fs.promises.readdir(sourceDir)) {
    if (name === INSTALL_MARKER) {
      continue;
    }
    const entry = await resolveContained(path.join(sourceDir, name), root, warn);
    if (!entry) {
      continue;
    }
    const target = path.join(targetDir, name);
    if (entry.stats.isDirectory()) {
      await copyContained(entry.realPath, target, root, warn);
    } else {
      await fs.promises.copyFile(entry.realPath, target);
    }
  }
}

/**
 * Copy each SKILL.md directory (and `_shared/`) from sourceDir into destDir.
 * When the source is the plugin mirror (short names like `issue-create`),
 * restore the `nightgauge-` prefix so Codex/Grok invocations match the
 * canonical directory names used by `install-agent-skills.sh`.
 *
 * Refuses (throws) when the source and destination overlap. A skill is
 * installed only under a name Nightgauge owns and only where mayReplace()
 * allows, so every other destination entry is left untouched (with a
 * warning). Each installed directory gets the install marker before its
 * content, so even a partial install is recognised as Nightgauge's next
 * time. Returns the number of directories installed.
 */
export async function copySkillsTree(
  sourceDir: string,
  destDir: string,
  warn: SkillCopyWarn
): Promise<number> {
  const { root } = await resolveInstallPaths(sourceDir, destDir);
  const restorePrefix =
    !fs.existsSync(path.join(root, "nightgauge-issue-create")) &&
    fs.existsSync(path.join(root, "issue-create"));

  await fs.promises.mkdir(destDir, { recursive: true });
  let count = 0;
  for (const name of await fs.promises.readdir(root)) {
    const entryPath = path.join(root, name);
    const entry = await resolveContained(entryPath, root, warn);
    if (!entry?.stats.isDirectory()) {
      continue;
    }
    if (name !== "_shared") {
      const skillFile = await resolveContained(path.join(entryPath, "SKILL.md"), root, warn);
      if (!skillFile?.stats.isFile()) {
        continue;
      }
    }
    const destName = destSkillName(name, restorePrefix);
    const target = path.join(destDir, destName);
    if (!isNightgaugeSkillName(destName)) {
      warn(
        `Skipped ${entryPath}: ${destName} is not a Nightgauge skill name; ${target} is untouched.`
      );
      continue;
    }
    if (!(await mayReplace(target))) {
      warn(
        `Skipped ${entryPath}: ${target} is not a directory Nightgauge installed, so it is ` +
          `left untouched. Remove it to let Nightgauge install ${destName}.`
      );
      continue;
    }
    await fs.promises.rm(target, { recursive: true, force: true });
    await fs.promises.mkdir(target);
    await fs.promises.writeFile(path.join(target, INSTALL_MARKER), INSTALL_MARKER_TEXT);
    await copyContained(entryPath, target, root, warn);
    count += 1;
  }
  return count;
}

const INSTALL_FROM_WORKSPACE = "Install from Workspace";

/**
 * Development only: install skills from the first workspace folder's
 * `skills/` into `destDir`. Refuses in an untrusted workspace or when the
 * source and destination overlap, and copies only after a modal
 * confirmation that names both real paths (so a symlinked `skills/` or
 * agent skills directory shows where it actually points).
 */
export async function installWorkspaceSkills(
  agent: string,
  destDir: string,
  output: vscode.OutputChannel
): Promise<void> {
  if (!vscode.workspace.isTrusted) {
    vscode.window.showErrorMessage(
      `Nightgauge will not install ${agent} skills from an untrusted workspace. ` +
        "Trust the workspace first."
    );
    return;
  }
  const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  const skillsDir = workspaceRoot ? path.join(workspaceRoot, "skills") : undefined;
  const hasSkillsDir = skillsDir
    ? await fs.promises.stat(skillsDir).then(
        (stats) => stats.isDirectory(),
        () => false
      )
    : false;
  if (!skillsDir || !hasSkillsDir) {
    vscode.window.showErrorMessage(
      `Nightgauge found no skills/ directory in this workspace to install ${agent} skills from.`
    );
    return;
  }
  let source: string;
  let realDest: string;
  try {
    ({ root: source, realDest } = await resolveInstallPaths(skillsDir, destDir));
  } catch (error) {
    vscode.window.showErrorMessage(error instanceof Error ? error.message : String(error));
    return;
  }
  const choice = await vscode.window.showWarningMessage(
    `Install ${agent} skills from ${source}?`,
    {
      modal: true,
      detail:
        `The skills in ${source} replace the Nightgauge skills in ${realDest}, which every ` +
        `future ${agent} session loads in every project. Use this only while developing ` +
        "Nightgauge skills in this checkout.",
    },
    INSTALL_FROM_WORKSPACE
  );
  if (choice !== INSTALL_FROM_WORKSPACE) {
    return;
  }

  output.show(true);
  try {
    const count = await copySkillsTree(source, destDir, (message) =>
      output.appendLine(`WARNING: ${message}`)
    );
    output.appendLine(`✓ Copied ${count} skill directories from ${source} to ${realDest}`);
    vscode.window.showInformationMessage(
      `Installed ${count} ${agent} skill directories from ${source}.`
    );
  } catch (error) {
    const errorMessage = error instanceof Error ? error.message : String(error);
    output.appendLine(`✗ Installation from ${source} failed: ${errorMessage}`);
    vscode.window.showErrorMessage(
      `Failed to install ${agent} skills from the workspace: ${errorMessage}`
    );
  }
}
