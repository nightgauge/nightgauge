/**
 * Tree tooltips must not trust issue-derived Markdown (#3369).
 *
 * (a) Each of the seven items that used to set `isTrusted = true` is built
 *     with a hostile title/branch and must render it inert.
 * (b) A guard fails if any file under `src/views/` assigns `isTrusted` anything but `false`.
 */
import { describe, it, expect } from "vitest";
import * as fs from "fs";
import * as path from "path";
import { IssueTreeItem } from "../../../src/views/items/IssueTreeItem";
import { FailedIssueTreeItem } from "../../../src/views/items/FailedIssueTreeItem";
import { CompletedIssueTreeItem } from "../../../src/views/items/CompletedIssueTreeItem";
import { DependencyTreeItem } from "../../../src/views/items/DependencyTreeItem";
import { DependencySectionTreeItem } from "../../../src/views/items/DependencySectionTreeItem";
import { ReadyIssueTreeItem } from "../../../src/views/items/ReadyIssueTreeItem";
import { EpicGroupTreeItem } from "../../../src/views/items/EpicGroupTreeItem";
import { createMockReadyIssue, createMockBlockingIssue } from "../../mocks/github-api";

const HOSTILE = "[pwn](command:workbench.action.terminal.new) <img src=x onerror=1> `tick`";

function tooltipOf(item: { tooltip?: unknown }): { value: string; isTrusted?: boolean } {
  const t = item.tooltip as { value?: string; isTrusted?: boolean } | undefined;
  return { value: t?.value ?? "", isTrusted: t?.isTrusted };
}

/** Strip code spans (their content is literal), then look for live markup. */
function assertInert(item: { tooltip?: unknown }): void {
  const { value, isTrusted } = tooltipOf(item);
  expect(isTrusted).not.toBe(true);
  expect(value).toContain("pwn");
  const prose = value.replace(/\\`/g, "").replace(/(`+)(?!`)[\s\S]*?(?<!`)\1(?!`)/g, "");
  expect(prose).not.toMatch(/(?<!\\)\[pwn(?<!\\)\]\(/);
  expect(prose).not.toMatch(/(?<!\\)<img/);
}

const blocker = createMockBlockingIssue({ number: 7, state: "OPEN", title: HOSTILE });

describe("tree tooltips render issue-derived text inert (#3369)", () => {
  it("IssueTreeItem", () => {
    assertInert(
      new IssueTreeItem({ number: 1, title: HOSTILE, branch: HOSTILE, labels: [HOSTILE] })
    );
  });
  it("FailedIssueTreeItem", () => {
    assertInert(
      new FailedIssueTreeItem({
        issue_number: 1,
        title: HOSTILE,
        branch: HOSTILE,
        timestamp: "2026-11-16T00:00:00Z",
        failed_stage: HOSTILE,
        error: HOSTILE,
        retry_count: 0,
      })
    );
  });
  it("CompletedIssueTreeItem", () => {
    assertInert(
      new CompletedIssueTreeItem({
        issue_number: 1,
        title: HOSTILE,
        branch: HOSTILE,
        timestamp: "2026-11-16T00:00:00Z",
      })
    );
  });
  it("DependencyTreeItem", () => {
    assertInert(new DependencyTreeItem(blocker));
  });
  it("DependencySectionTreeItem", () => {
    assertInert(new DependencySectionTreeItem("blockedBy", [blocker]));
  });
  it("ReadyIssueTreeItem", () => {
    assertInert(
      new ReadyIssueTreeItem(createMockReadyIssue({ title: HOSTILE, blockedBy: [blocker] }))
    );
  });
  it("EpicGroupTreeItem", () => {
    const sub = createMockReadyIssue({ number: 2, title: HOSTILE });
    assertInert(
      new EpicGroupTreeItem({ number: 1, title: HOSTILE, url: "https://x", blockedBy: [blocker] }, [
        sub,
      ])
    );
  });
});

describe("assertInert self-check", () => {
  it("throws on raw hostile markup", () => {
    expect(() => assertInert({ tooltip: { value: HOSTILE } })).toThrow();
  });
});

describe("no views tooltip is trusted (#3369 guard)", () => {
  it("no file under src/views assigns isTrusted other than false", () => {
    const root = path.resolve(__dirname, "../../../src/views");
    const offenders: string[] = [];
    const walk = (dir: string): void => {
      for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
        const full = path.join(dir, entry.name);
        if (entry.isDirectory()) walk(full);
        else if (entry.name.endsWith(".ts")) {
          const src = fs.readFileSync(full, "utf8");
          if (/isTrusted\s*[=:](?!\s*false\b)/.test(src)) offenders.push(path.relative(root, full));
        }
      }
    };
    walk(root);
    expect(offenders).toEqual([]);
  });
});
