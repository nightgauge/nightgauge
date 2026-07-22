import { describe, it, expect, vi, beforeEach } from "vitest";

const activeEditorState: { uri?: { scheme: string; fsPath: string } } = {};
const workspaceFolderResolver = { fn: (_uri: any) => undefined as any };

vi.mock("vscode", () => ({
  window: {
    get activeTextEditor() {
      return activeEditorState.uri ? { document: { uri: activeEditorState.uri } } : undefined;
    },
  },
  workspace: {
    getWorkspaceFolder: (uri: any) => workspaceFolderResolver.fn(uri),
  },
  Uri: { file: (p: string) => ({ scheme: "file", fsPath: p }) },
}));

import { resolveActiveRepository } from "../../src/utils/resolveActiveRepository";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { Repository } from "../../src/models/Repository";

const mkRepo = (name: string, path: string, role?: string): Repository =>
  ({ name, path, role }) as unknown as Repository;

const mkManager = (repos: Repository[]): WorkspaceManager =>
  ({
    getAllRepositories: () => repos,
  }) as unknown as WorkspaceManager;

describe("resolveActiveRepository", () => {
  beforeEach(() => {
    activeEditorState.uri = undefined;
    workspaceFolderResolver.fn = () => undefined;
  });

  it("returns null when the manager is undefined", () => {
    expect(resolveActiveRepository(undefined)).toBeNull();
  });

  it("returns null when the manager has no repos", () => {
    expect(resolveActiveRepository(mkManager([]))).toBeNull();
  });

  it("returns the sole repo when only one is loaded", () => {
    const r = mkRepo("only", "/repos/only");
    expect(resolveActiveRepository(mkManager([r]))).toBe(r);
  });

  it("prefers the repo whose path contains the active editor's file", () => {
    const a = mkRepo("a", "/repos/a");
    const b = mkRepo("b", "/repos/b");
    activeEditorState.uri = { scheme: "file", fsPath: "/repos/b/src/x.ts" };
    expect(resolveActiveRepository(mkManager([a, b]))).toBe(b);
  });

  it("picks the longest matching path prefix for nested repos", () => {
    const outer = mkRepo("outer", "/repos/outer");
    const inner = mkRepo("inner", "/repos/outer/packages/inner");
    activeEditorState.uri = {
      scheme: "file",
      fsPath: "/repos/outer/packages/inner/src/y.ts",
    };
    expect(resolveActiveRepository(mkManager([outer, inner]))).toBe(inner);
  });

  it("falls back to the primary role when no editor match is found", () => {
    const a = mkRepo("a", "/repos/a", "secondary");
    const b = mkRepo("b", "/repos/b", "primary");
    expect(resolveActiveRepository(mkManager([a, b]))).toBe(b);
  });

  it("falls back to the first repo when no primary and no editor match", () => {
    const a = mkRepo("a", "/repos/a");
    const b = mkRepo("b", "/repos/b");
    expect(resolveActiveRepository(mkManager([a, b]))).toBe(a);
  });

  it("tries the workspace folder when the file isn't inside a repo path directly", () => {
    const a = mkRepo("a", "/repos/a");
    const b = mkRepo("b", "/repos/b");
    // Non-file-scheme URIs should skip the path-prefix match.
    activeEditorState.uri = { scheme: "untitled", fsPath: "/unrelated/path" };
    expect(resolveActiveRepository(mkManager([a, b]))).toBe(a);
  });
});
