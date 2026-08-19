/**
 * Workspace Repositories section rendering (#705).
 *
 * These assertions are about what the operator can and cannot do from the
 * panel: a candidate with no board must be unaddable and say why, a
 * routing-referenced repo must be visibly referenced before removal, and a
 * manifest/resolver disagreement must not be hidden behind one number.
 */

import { describe, it, expect } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { DEFAULT_CONFIG } from "../../../src/views/settings/types";
import { EMPTY_WORKSPACE_REPO_STATE } from "../../../src/services/WorkspaceRepoSettingsService";

const webview = { cspSource: "test" } as Parameters<typeof getSettingsHtml>[0];

function render(workspaceRepos: unknown): string {
  return getSettingsHtml(webview, DEFAULT_CONFIG, undefined, undefined, undefined, {
    workspaceRepos,
  } as never);
}

describe("Workspace Repositories section", () => {
  it("explains single-repo mode when no manifest exists", () => {
    const html = render({ ...EMPTY_WORKSPACE_REPO_STATE, unmanaged: true });
    expect(html).toContain("single-repository mode");
    expect(html).toContain("no window reload");
  });

  it("lists configured repositories with their board and path", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      manifestPath: "/ws/.vscode/nightgauge-workspace.yaml",
      configured: [
        {
          name: "alpha",
          path: ".",
          role: "primary",
          projectNumber: 3,
          resolvedProject: 3,
          projectTitle: "",
          exists: true,
          routingRefs: null,
        },
      ],
    });
    expect(html).toContain("alpha");
    expect(html).toContain("Board #3");
    expect(html).toContain("workspace-repo-remove");
  });

  // The manifest's number and the resolver's answer disagreeing is the drift
  // `nightgauge doctor` fails on. Collapsing them would hide it here.
  it("shows manifest-vs-resolver drift rather than one number", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      configured: [
        {
          name: "alpha",
          path: ".",
          role: "primary",
          projectNumber: 3,
          resolvedProject: 7,
          projectTitle: "",
          exists: true,
          routingRefs: null,
        },
      ],
    });
    expect(html).toContain("manifest #3 vs resolved #7");
  });

  it("flags a configured repo whose directory is gone", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      configured: [
        {
          name: "ghost",
          path: "../ghost",
          role: "primary",
          projectNumber: 3,
          resolvedProject: 3,
          projectTitle: "",
          exists: false,
          routingRefs: null,
        },
      ],
    });
    expect(html).toContain("directory missing");
  });

  it("marks a routing-referenced repo before it can be removed", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      configured: [
        {
          name: "gamma",
          path: "../gamma",
          role: "secondary",
          projectNumber: 5,
          resolvedProject: 5,
          projectTitle: "",
          exists: true,
          routingRefs: ["routing.patterns[web].preferred_repo"],
        },
      ],
    });
    expect(html).toContain("referenced by routing");
    expect(html).toContain("routing.patterns[web].preferred_repo");
  });

  it("offers unlisted folders as one-click adds with the board pre-resolved", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      candidates: [
        {
          name: "delta",
          spec: "acme/delta",
          path: "../delta",
          suggestedProject: 9,
          projectTitle: "Delta Board",
          boardUnavailable: "",
        },
      ],
    });
    expect(html).toContain("delta");
    expect(html).toContain("Board #9");
    expect(html).toContain("Delta Board");
    expect(html).toContain('data-project="9"');
    expect(html).not.toContain("workspace-repo-add\n                  disabled");
  });

  // THE criterion that keeps project_number: 0 unreachable from the UI: no
  // board means the button is disabled and the reason is stated, rather than
  // the entry being silently accepted.
  it("disables the add button and states why when no board resolves", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      candidates: [
        {
          name: "boardless",
          spec: "acme/boardless",
          path: "../boardless",
          suggestedProject: 0,
          projectTitle: "",
          boardUnavailable:
            "No project board resolves for this repository. Provision a board first.",
        },
      ],
    });
    expect(html).toContain("No project board resolves for this repository");
    expect(html).toMatch(/workspace-repo-add[\s\S]{0,220}disabled/);
  });

  it("surfaces an error verbatim", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      error: 'repository "delta" is already in the manifest',
    });
    expect(html).toContain("already in the manifest");
  });

  it("surfaces the board-sync note after a successful add", () => {
    const html = render({
      ...EMPTY_WORKSPACE_REPO_STATE,
      notice: "Run `nightgauge workspace provision-board-sync --write`",
    });
    expect(html).toContain("provision-board-sync");
  });
});
