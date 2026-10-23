/**
 * Workspace repository management for the Settings panel (#705).
 *
 * Every mutation goes through the Go writer over IPC. This service does not
 * parse, serialize or edit `.vscode/nightgauge-workspace.yaml` — the manifest
 * carries load-bearing comments (the `project_number: 0` footgun is documented
 * only there) and a second writer in TypeScript would reflow them.
 *
 * Validation failures are surfaced verbatim rather than reworded: the binary is
 * the authority on why a write was refused, and a paraphrase in the panel would
 * drift from the rule actually enforced.
 */

import { IpcClient } from "./IpcClient";
import type {
  WorkspaceRepoDescriptor,
  WorkspaceRepoCandidate,
  WorkspaceRepoListResult,
} from "./IpcClientBase";

export type { WorkspaceRepoDescriptor, WorkspaceRepoCandidate };

/** Panel-facing state for the Workspace Repositories section. */
export interface WorkspaceRepoState {
  /** True while a list or mutation is in flight. */
  loading: boolean;
  /** No manifest exists — the workspace is in single-repo mode. */
  unmanaged: boolean;
  manifestPath: string;
  configured: WorkspaceRepoDescriptor[];
  /** Git checkouts in the workspace that the manifest does not list. */
  candidates: WorkspaceRepoCandidate[];
  /** Verbatim error from the last failed operation, or "". */
  error: string;
  /** Transient success note (e.g. the board-sync reminder), or "". */
  notice: string;
}

export const EMPTY_WORKSPACE_REPO_STATE: WorkspaceRepoState = {
  loading: false,
  unmanaged: false,
  manifestPath: "",
  configured: [],
  candidates: [],
  error: "",
  notice: "",
};

export class WorkspaceRepoSettingsService {
  /**
   * Read the configured repositories and the unlisted candidates.
   *
   * `folders` are the VSCode workspace's own folder paths. The daemon's sibling
   * scan finds checkouts beside the workspace root, which misses a workspace
   * folder added from elsewhere on disk — and such a folder is exactly as
   * unwatched as any other uncovered repo.
   *
   * A transport failure yields an error state rather than an empty list:
   * rendering "no repositories" when the daemon is unreachable would tell the
   * operator their workspace is empty, which is a different and wrong fact.
   */
  async load(folders: string[] = []): Promise<WorkspaceRepoState> {
    try {
      const res: WorkspaceRepoListResult = await IpcClient.getInstance().workspaceRepoList(folders);
      return {
        ...EMPTY_WORKSPACE_REPO_STATE,
        unmanaged: Boolean(res.unmanaged),
        manifestPath: res.manifestPath ?? "",
        configured: res.configured ?? [],
        candidates: res.candidates ?? [],
      };
    } catch (err) {
      return { ...EMPTY_WORKSPACE_REPO_STATE, error: describeError(err) };
    }
  }

  /**
   * Add a repository. `project` must be a positive board number — the caller is
   * expected to have required an explicit selection, and the daemon refuses
   * anything else regardless.
   *
   * Returns the board-sync note on success so the panel can surface it.
   */
  async add(input: {
    name: string;
    path: string;
    role: string;
    project: number;
  }): Promise<{ ok: boolean; error: string; notice: string }> {
    if (!Number.isInteger(input.project) || input.project <= 0) {
      return {
        ok: false,
        error:
          "A project board must be selected. An entry with no project number resolves to project 0 and silently misroutes every issue the repository produces.",
        notice: "",
      };
    }
    try {
      const res = await IpcClient.getInstance().workspaceRepoAdd(
        input.name,
        input.path,
        input.role,
        input.project
      );
      return { ok: Boolean(res?.ok), error: "", notice: res?.boardSyncNote ?? "" };
    } catch (err) {
      return { ok: false, error: describeError(err), notice: "" };
    }
  }

  /** Remove a repository. `force` proceeds despite routing references. */
  async remove(name: string, force: boolean): Promise<{ ok: boolean; error: string }> {
    try {
      const res = await IpcClient.getInstance().workspaceRepoRemove(name, force);
      return { ok: Boolean(res?.ok), error: "" };
    } catch (err) {
      return { ok: false, error: describeError(err) };
    }
  }
}

/**
 * The binary's message, unmodified. The panel must never report success for a
 * write the binary rejected, and must not soften the reason it gave.
 */
function describeError(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  return String(err);
}
