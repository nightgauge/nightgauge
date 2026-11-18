/**
 * webviewCspCoverage.test.ts (#3405)
 *
 * Every webview panel renders its Content-Security-Policy through the shared
 * `buildWebviewCsp` builder. This test enumerates every `createWebviewPanel`
 * site under src/, renders the HTML builder each one uses, and asserts the
 * policy carries the baseline directives and that every inline script (and,
 * for nonce-style panels, every style block) carries the render's nonce.
 *
 * A new panel fails the first test until its HTML builder is registered in
 * PANEL_BUILDERS below, which then holds it to the same assertions.
 */

import { describe, it, expect, vi } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

vi.mock("vscode", () => ({
  Uri: {
    joinPath: vi.fn((_base: unknown, ...parts: string[]) => ({ fsPath: parts.join("/") })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  workspace: {
    workspaceFolders: undefined,
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

import { getRecoveryDialogHtml } from "../../src/views/recovery/RecoveryDialogHtml";
import { getSettingsHtml } from "../../src/views/settings/SettingsHtml";
import { getDashboardHtml } from "../../src/views/dashboard/DashboardHtml";
import { getKnowledgeValueDashboardHtml } from "../../src/views/dashboard/KnowledgeValueDashboardHtml";
import { getPipelineSummaryHtml } from "../../src/views/summary/PipelineSummaryHtml";
import { getApprovalDialogHtml } from "../../src/views/approval/ApprovalDialogHtml";
import { getBrownfieldDashboardHtml } from "../../src/views/brownfield/BrownfieldDashboardHtml";
import { renderDoctorHtml } from "../../src/views/doctor/DoctorHtml";
import { renderGettingStartedHtml } from "../../src/views/onboarding/GettingStartedHtml";
import { renderConversationHtml } from "../../src/views/conversation/ConversationHtml";
import { getOutputWindowHtml } from "../../src/views/outputWindow/OutputWindowHtml";
import { renderNotifierSettingsHtml } from "../../src/views/notifier/NotifierSettingsPanel";
import { renderTelemetrySettingsHtml } from "../../src/views/telemetry/TelemetrySettingsPanel";
import { getDefaultConfig } from "../../src/config/schema";
import { makeEmptyAggregates } from "./dashboard/fixtures/aggregates";

const SRC = join(__dirname, "..", "..", "src");
const CSP_SOURCE = "vscode-webview://csp-test";
const NONCE = "cspcoveragenonce0123456789abcdef";

const webview = {
  cspSource: CSP_SOURCE,
  asWebviewUri: (uri: unknown) => uri,
} as never;
const extensionUri = {
  fsPath: "/extension",
  path: "/extension",
  toString: () => "/extension",
  with: (change: { path: string }) => ({ fsPath: change.path, toString: () => change.path }),
} as never;

interface PanelBuilder {
  /** Renders the panel's document. */
  render: () => string;
  /**
   * The nonce the render used. Builders that mint their own nonce report
   * `undefined`, and the test reads it back from `script-src`.
   */
  nonce?: string;
  /** The panel still allows inline style attributes. */
  inlineStyles?: boolean;
}

/** Keyed by the src/-relative file that calls `createWebviewPanel`. */
const PANEL_BUILDERS: Record<string, PanelBuilder> = {
  "views/recovery/RecoveryDialog.ts": {
    inlineStyles: true,
    render: () =>
      getRecoveryDialogHtml(webview, {
        issueNumber: 1,
        triggeringStage: "feature-dev",
        errorKind: "RUN_STATE_MISSING",
        errorDetail: "detail",
        runState: "none",
        availableActions: ["cancel"],
      } as never),
  },
  "views/settings/SettingsPanel.ts": {
    inlineStyles: true,
    render: () =>
      getSettingsHtml(webview, getDefaultConfig(), new Set(), {}, {
        currentTier: "project",
        defaultEditTier: "local",
        hasGlobalConfig: false,
        hasLocalConfig: false,
        hasProjectConfig: true,
        activeEnvVars: [],
      } as never),
  },
  "views/dashboard/Dashboard.ts": {
    inlineStyles: true,
    render: () =>
      getDashboardHtml(webview, null, [], makeEmptyAggregates(), {
        pipelineStart: 0,
        issuePickup: 5,
        featurePlanning: 30,
        featureDev: 120,
        featureValidate: 15,
        prCreate: 10,
        prMerge: 5,
        pipelineFinish: 0,
      }),
  },
  "views/dashboard/KnowledgeValueDashboard.ts": {
    render: () =>
      getKnowledgeValueDashboardHtml({
        windowDays: 7,
        current: null,
        prior: null,
        delta: null,
        loadedAt: 0,
        loading: false,
        error: null,
      }),
  },
  "views/summary/PipelineSummary.ts": {
    inlineStyles: true,
    render: () =>
      getPipelineSummaryHtml(webview, {
        issue_number: 1,
        title: "t",
        branch: "b",
        stages: {},
        started_at: "2026-01-01T00:00:00Z",
        updated_at: "2026-01-01T00:05:00Z",
      } as never),
  },
  "views/approval/ApprovalDialog.ts": {
    inlineStyles: true,
    render: () =>
      getApprovalDialogHtml(webview, extensionUri, "feature-planning" as never, 1, "# Plan"),
  },
  "views/brownfield/BrownfieldDashboard.ts": {
    render: () => getBrownfieldDashboardHtml(webview, { hasAnyData: false } as never, {} as never),
  },
  "views/doctor/DoctorPanel.ts": {
    nonce: NONCE,
    render: () =>
      renderDoctorHtml({ phase: "running", progress: [] } as never, {
        cspSource: CSP_SOURCE,
        nonce: NONCE,
      }),
  },
  "views/onboarding/GettingStartedPanel.ts": {
    nonce: NONCE,
    inlineStyles: true,
    render: () => renderGettingStartedHtml({ cspSource: CSP_SOURCE, nonce: NONCE } as never),
  },
  "views/conversation/ConversationPanel.ts": {
    nonce: NONCE,
    render: () => renderConversationHtml({ cspSource: CSP_SOURCE, nonce: NONCE }),
  },
  "views/outputWindow/OutputWindow.ts": {
    inlineStyles: true,
    render: () =>
      getOutputWindowHtml(
        webview,
        [],
        [],
        true,
        false,
        false,
        undefined,
        undefined,
        [],
        null,
        new Map(),
        new Map(),
        (name) => `webview:/ext/dist/vendor/${name}`
      ),
  },
  "views/notifier/NotifierSettingsPanel.ts": {
    nonce: NONCE,
    render: () => renderNotifierSettingsHtml({ cspSource: CSP_SOURCE, nonce: NONCE }),
  },
  "views/telemetry/TelemetrySettingsPanel.ts": {
    nonce: NONCE,
    render: () => renderTelemetrySettingsHtml({ cspSource: CSP_SOURCE, nonce: NONCE }),
  },
};

function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = join(dir, name);
    return statSync(full).isDirectory() ? walk(full) : full.endsWith(".ts") ? [full] : [];
  });
}

const sources = walk(SRC).map((file) => ({
  file: relative(SRC, file).split("\\").join("/"),
  body: readFileSync(file, "utf8"),
}));

function cspOf(html: string): string {
  const metas = [...html.matchAll(/<meta http-equiv="Content-Security-Policy" content="([^"]*)"/g)];
  expect(metas, "exactly one CSP meta tag").toHaveLength(1);
  return metas[0][1];
}

function directives(csp: string): Map<string, string> {
  return new Map(
    csp
      .split(";")
      .map((d) => d.trim())
      .filter(Boolean)
      .map((d) => {
        const [name, ...values] = d.split(/\s+/);
        return [name, values.join(" ")] as const;
      })
  );
}

describe("every webview panel renders the shared CSP (#3405)", () => {
  it("registers an HTML builder for every createWebviewPanel site", () => {
    const sites = sources
      .filter((s) => /\.createWebviewPanel\(/.test(s.body))
      .map((s) => s.file)
      .sort();
    expect(sites.length).toBeGreaterThan(0);
    expect(sites).toEqual(Object.keys(PANEL_BUILDERS).sort());
  });

  it("writes no Content-Security-Policy by hand", () => {
    const handWritten = sources
      .filter((s) => s.file !== "utils/webviewCsp.ts")
      .filter((s) => s.body.includes('http-equiv="Content-Security-Policy"'))
      .filter(
        (s) => !s.body.includes("buildWebviewCsp(") || /(?:=\s*`|["'])default-src/.test(s.body)
      )
      .map((s) => s.file);
    expect(handWritten).toEqual([]);
  });

  it.each(Object.entries(PANEL_BUILDERS))("%s renders the baseline policy", (_file, builder) => {
    const html = builder.render();
    const policy = directives(cspOf(html));

    expect(policy.get("default-src")).toBe("'none'");
    expect(policy.get("form-action")).toBe("'none'");
    expect(policy.get("base-uri")).toBe("'none'");

    const scriptSrc = policy.get("script-src") ?? "";
    expect(scriptSrc).toMatch(/^'nonce-[A-Za-z0-9+/_=-]+'$/);
    const nonce = /^'nonce-(.+)'$/.exec(scriptSrc)![1];
    if (builder.nonce) expect(nonce).toBe(builder.nonce);

    // Every script tag, inline or vendored, carries this render's nonce.
    const scripts = [...html.matchAll(/<script\b[^>]*>/gi)].map((m) => m[0]);
    expect(scripts.length).toBeGreaterThan(0);
    for (const tag of scripts) expect(tag).toContain(`nonce="${nonce}"`);

    const styleSrc = policy.get("style-src") ?? "";
    if (builder.inlineStyles) {
      expect(styleSrc).toContain("'unsafe-inline'");
    } else {
      expect(styleSrc).toContain(`'nonce-${nonce}'`);
      expect(styleSrc).not.toContain("'unsafe-inline'");
      for (const [tag] of html.matchAll(/<style\b[^>]*>/gi)) {
        expect(tag).toContain(`nonce="${nonce}"`);
      }
      expect(html).not.toMatch(/\sstyle="/);
    }
  });
});
