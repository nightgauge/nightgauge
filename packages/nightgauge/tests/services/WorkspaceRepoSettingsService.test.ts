/**
 * WorkspaceRepoSettingsService (#705).
 *
 * The properties that matter are about honesty rather than mechanics: a
 * transport failure must not read as "no repositories", a rejected write must
 * never report success, and the binary's refusal must reach the operator
 * unedited.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const mockRepoList = vi.fn();
const mockRepoAdd = vi.fn();
const mockRepoRemove = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      workspaceRepoList: mockRepoList,
      workspaceRepoAdd: mockRepoAdd,
      workspaceRepoRemove: mockRepoRemove,
    }),
  },
}));

import { WorkspaceRepoSettingsService } from "../../src/services/WorkspaceRepoSettingsService";

describe("WorkspaceRepoSettingsService", () => {
  let svc: WorkspaceRepoSettingsService;

  beforeEach(() => {
    vi.clearAllMocks();
    svc = new WorkspaceRepoSettingsService();
  });

  describe("load", () => {
    it("maps the daemon's list into panel state", async () => {
      mockRepoList.mockResolvedValue({
        manifestPath: "/ws/.vscode/nightgauge-workspace.yaml",
        configured: [{ name: "alpha", path: ".", role: "primary", projectNumber: 3 }],
        candidates: [{ name: "delta", spec: "acme/delta", path: "../delta", suggestedProject: 9 }],
        unmanaged: false,
      });

      const state = await svc.load();

      expect(state.configured).toHaveLength(1);
      expect(state.candidates[0].name).toBe("delta");
      expect(state.manifestPath).toBe("/ws/.vscode/nightgauge-workspace.yaml");
      expect(state.error).toBe("");
    });

    it("reports a transport failure as an error, not an empty workspace", async () => {
      mockRepoList.mockRejectedValue(new Error("daemon unreachable"));

      const state = await svc.load();

      // "No repositories" and "we could not ask" are different facts, and only
      // one of them means the operator should go edit something.
      expect(state.error).toContain("daemon unreachable");
      expect(state.configured).toEqual([]);
    });

    it("passes the VSCode workspace folders through to the daemon", async () => {
      mockRepoList.mockResolvedValue({ manifestPath: "/ws/x.yaml", unmanaged: false });
      await svc.load(["/a/one", "/b/two"]);
      // The daemon's sibling scan cannot reach a folder added from elsewhere on
      // disk, so the caller's list is what makes it discoverable.
      expect(mockRepoList).toHaveBeenCalledWith(["/a/one", "/b/two"]);
    });

    it("passes through the unmanaged (single-repo) state", async () => {
      mockRepoList.mockResolvedValue({ manifestPath: "/ws/x.yaml", unmanaged: true });
      const state = await svc.load();
      expect(state.unmanaged).toBe(true);
    });

    it("tolerates null collections from the wire", async () => {
      mockRepoList.mockResolvedValue({
        manifestPath: "/ws/x.yaml",
        configured: null,
        candidates: null,
        unmanaged: false,
      });
      const state = await svc.load();
      expect(state.configured).toEqual([]);
      expect(state.candidates).toEqual([]);
    });
  });

  describe("add", () => {
    it("refuses a non-positive project without calling the daemon", async () => {
      for (const project of [0, -1, 1.5, NaN]) {
        const res = await svc.add({ name: "delta", path: "../delta", role: "primary", project });
        expect(res.ok).toBe(false);
        expect(res.error).toMatch(/project board must be selected/i);
      }
      // project_number: 0 must be unreachable from this surface entirely.
      expect(mockRepoAdd).not.toHaveBeenCalled();
    });

    it("returns the board-sync note on success", async () => {
      mockRepoAdd.mockResolvedValue({ ok: true, boardSyncNote: "Run provision-board-sync" });

      const res = await svc.add({ name: "delta", path: "../delta", role: "primary", project: 9 });

      expect(res.ok).toBe(true);
      expect(res.notice).toContain("provision-board-sync");
      expect(mockRepoAdd).toHaveBeenCalledWith("delta", "../delta", "primary", 9);
    });

    it("surfaces the binary's refusal verbatim and never reports success", async () => {
      mockRepoAdd.mockRejectedValue(
        new Error('repository "delta" is already in the manifest — names must be unique')
      );

      const res = await svc.add({ name: "delta", path: "../delta", role: "primary", project: 9 });

      expect(res.ok).toBe(false);
      // Not reworded: the binary is the authority on the rule it enforced.
      expect(res.error).toBe(
        'repository "delta" is already in the manifest — names must be unique'
      );
    });
  });

  describe("remove", () => {
    it("passes force through", async () => {
      mockRepoRemove.mockResolvedValue({ ok: true });
      await svc.remove("gamma", true);
      expect(mockRepoRemove).toHaveBeenCalledWith("gamma", true);
    });

    it("surfaces the routing-reference refusal verbatim", async () => {
      mockRepoRemove.mockRejectedValue(
        new Error('repository "gamma" is still referenced by routing.patterns[web].preferred_repo')
      );

      const res = await svc.remove("gamma", false);

      expect(res.ok).toBe(false);
      expect(res.error).toContain("routing.patterns[web].preferred_repo");
    });

    it("does not report success when the daemon returns ok:false", async () => {
      mockRepoRemove.mockResolvedValue({ ok: false });
      const res = await svc.remove("gamma", false);
      expect(res.ok).toBe(false);
    });
  });
});
