import { describe, expect, it } from "vitest";
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";

const webview = { cspSource: "test-csp" } as any;

describe("repository-aware project settings", () => {
  it("renders repository assignments and hides the legacy scalar control", () => {
    const html = getSettingsHtml(webview, { project: { number: 99 } }, new Set(), {}, undefined, {
      repositoryProjects: {
        repositories: [
          { name: "core", owner: "nightgauge", repo: "nightgauge" },
          { name: "sdk", owner: "nightgauge", repo: "sdk" },
        ],
        selectedRepository: "core",
        assignments: [
          { name: "Community", number: 8, default: true, source: "team" },
          { name: "Engineering", number: 9, default: false, source: "local" },
        ],
        linkedProjects: [{ id: "PVT_8", owner: "nightgauge", number: 8, title: "Community" }],
        discovery: "ready",
      },
    });

    expect(html).toContain("Repository project routing");
    expect(html).toContain("nightgauge/nightgauge");
    expect(html).toContain("Community · #8");
    expect(html).toContain("Engineering · #9");
    expect(html).toContain("Make default");
    expect(html).not.toContain('id="project.number"');
    expect(html).toContain("Legacy project.number #99");
  });

  it("presents one inferred linked project without silently assigning it", () => {
    const html = getSettingsHtml(webview, {}, new Set(), {}, undefined, {
      repositoryProjects: {
        repositories: [{ name: "core", owner: "nightgauge", repo: "nightgauge" }],
        selectedRepository: "core",
        assignments: [],
        linkedProjects: [
          { id: "PVT_8", owner: "nightgauge", number: 8, title: "Community Roadmap" },
        ],
        discovery: "ready",
      },
    });
    expect(html).toContain("GitHub links one candidate: Community Roadmap (#8)");
    expect(html).toContain("Add linked project (1)");
  });

  it("requires explicit selection when several linked projects exist", () => {
    const html = getSettingsHtml(webview, {}, new Set(), {}, undefined, {
      repositoryProjects: {
        repositories: [{ name: "core", owner: "nightgauge", repo: "nightgauge" }],
        selectedRepository: "core",
        assignments: [],
        linkedProjects: [
          { id: "PVT_8", owner: "nightgauge", number: 8, title: "Community" },
          { id: "PVT_9", owner: "nightgauge", number: 9, title: "Engineering" },
        ],
        discovery: "ready",
      },
    });
    expect(html).toContain("Multiple linked projects found");
    expect(html).toContain("will not guess the default");
  });

  it("keeps manual assignment available when discovery is unavailable", () => {
    const html = getSettingsHtml(webview, {}, new Set(), {}, undefined, {
      repositoryProjects: {
        repositories: [{ name: "core", owner: "nightgauge", repo: "nightgauge" }],
        selectedRepository: "core",
        assignments: [],
        linkedProjects: [],
        discovery: "unavailable",
        error: "offline",
      },
    });
    expect(html).toContain("Linked-project discovery unavailable: offline");
    expect(html).toContain("Add by number");
  });
});
