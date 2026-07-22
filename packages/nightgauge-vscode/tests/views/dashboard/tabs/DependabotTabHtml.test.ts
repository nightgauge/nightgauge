import { describe, it, expect } from "vitest";
import {
  getDependabotTabHtml,
  getDependabotTabScript,
  getDependabotTabStyles,
} from "../../../../src/views/dashboard/tabs/DependabotTabHtml";
import type { DependabotPRData, DependabotPR } from "../../../../src/services/DependabotPRService";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makePR(overrides: Partial<DependabotPR> = {}): DependabotPR {
  return {
    nodeId: "PR_1",
    number: 1,
    title: "Bump lodash from 4.17.20 to 4.17.21",
    state: "OPEN",
    headRef: "dependabot/npm_and_yarn/lodash-4.17.21",
    baseRef: "main",
    repo: "acme/myrepo",
    url: "https://github.com/acme/myrepo/pull/1",
    isDraft: false,
    labels: ["dependencies"],
    prType: "dependency",
    staleDays: 3,
    isStale: false,
    ...overrides,
  };
}

function makeData(overrides: Partial<DependabotPRData> = {}): DependabotPRData {
  return {
    prs: [],
    staleCount: 0,
    securityCount: 0,
    fetchedAt: "2026-05-16T00:00:00Z",
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("getDependabotTabHtml", () => {
  it("renders loading state when data is undefined", () => {
    const html = getDependabotTabHtml(undefined);
    expect(html).toContain("Loading dependabot");
  });

  it("renders empty state when data is null", () => {
    const html = getDependabotTabHtml(null);
    expect(html).toContain("No open dependabot PRs");
  });

  it("renders empty state when data.prs is empty", () => {
    const html = getDependabotTabHtml(makeData());
    expect(html).toContain("No open dependabot PRs");
  });

  it("renders PR count in summary card", () => {
    const data = makeData({ prs: [makePR(), makePR({ nodeId: "PR_2", number: 2 })] });
    const html = getDependabotTabHtml(data);
    expect(html).toContain(">2<");
  });

  it("renders stale count in summary card", () => {
    const data = makeData({
      prs: [makePR({ isStale: true, staleDays: 9 })],
      staleCount: 1,
    });
    const html = getDependabotTabHtml(data);
    expect(html).toContain(">1<");
  });

  it("renders PR title and link", () => {
    const pr = makePR({ title: "Bump <lodash> version", url: "https://github.com/pr/1" });
    const data = makeData({ prs: [pr] });
    const html = getDependabotTabHtml(data);
    expect(html).toContain("Bump &lt;lodash&gt; version");
    expect(html).toContain('href="https://github.com/pr/1"');
  });

  it("renders security badge for security PRs", () => {
    const data = makeData({
      prs: [makePR({ prType: "security" })],
      securityCount: 1,
    });
    const html = getDependabotTabHtml(data);
    expect(html).toContain("badge--security");
    expect(html).toContain("security");
  });

  it("renders merge button for each PR", () => {
    const data = makeData({ prs: [makePR()] });
    const html = getDependabotTabHtml(data);
    expect(html).toContain("mergeDependabotPR");
    expect(html).toContain("Merge");
  });

  it("marks stale age cell for stale PRs", () => {
    const data = makeData({ prs: [makePR({ isStale: true, staleDays: 10 })], staleCount: 1 });
    const html = getDependabotTabHtml(data);
    expect(html).toContain("stale-age");
    expect(html).toContain("10d");
  });

  it("renders CI passing badge for SUCCESS checkStatus", () => {
    const data = makeData({ prs: [makePR({ checkStatus: "SUCCESS" })] });
    const html = getDependabotTabHtml(data);
    expect(html).toContain("passing");
  });

  it("renders CI failing badge for FAILURE checkStatus", () => {
    const data = makeData({ prs: [makePR({ checkStatus: "FAILURE" })] });
    const html = getDependabotTabHtml(data);
    expect(html).toContain("failing");
  });

  it("escapes XSS in PR title", () => {
    const data = makeData({ prs: [makePR({ title: '<script>alert("xss")</script>' })] });
    const html = getDependabotTabHtml(data);
    expect(html).not.toContain("<script>");
    expect(html).toContain("&lt;script&gt;");
  });
});

describe("getDependabotTabScript", () => {
  it("returns non-empty script with expected message types", () => {
    const script = getDependabotTabScript();
    expect(script.length).toBeGreaterThan(0);
    expect(script).toContain("mergeDependabotPR");
    expect(script).toContain("dependabotRefresh");
  });
});

describe("getDependabotTabStyles", () => {
  it("returns non-empty CSS string with key selectors", () => {
    const styles = getDependabotTabStyles();
    expect(styles.length).toBeGreaterThan(0);
    expect(styles).toContain(".dependabot-tab");
    expect(styles).toContain(".merge-btn");
    expect(styles).toContain(".dependabot-table");
  });
});
