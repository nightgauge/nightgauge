/**
 * Structure tests for the Adapter Doctor webview renderer (Issue #4031).
 * Asserts the report renders per-adapter + per-stage tables, surfaces remediation
 * and the binary-unavailable warning, and escapes interpolated values.
 */

import { describe, it, expect } from "vitest";
import {
  renderAdapterDoctorHtml,
  type AdapterDoctorReport,
} from "../../../src/views/doctor/AdapterDoctorHtml";

const SECURITY = { cspSource: "vscode-resource://test", nonce: "test-nonce-123" };

/** Render with a stub CSP/nonce so structure tests don't repeat the security arg. */
function render(report: AdapterDoctorReport): string {
  return renderAdapterDoctorHtml(report, SECURITY);
}

function baseReport(overrides: Partial<AdapterDoctorReport> = {}): AdapterDoctorReport {
  return {
    generatedAt: "2026-06-17 10:00",
    binaryResolved: true,
    notes: [],
    rows: [
      {
        sdkAdapter: "codex",
        displayName: "Codex",
        kind: "cli",
        binary: "codex",
        installed: true,
        path: "/usr/local/bin/codex",
        version: "0.112.0",
        versionOk: true,
        minVersion: "0.111.0",
        mcp: { configPath: "/h/.codex/config.toml", configPresent: true, managedBlock: true },
        authOk: true,
        remediations: [],
        ok: true,
      },
    ],
    stages: [
      {
        stage: "feature-dev",
        adapter: "codex",
        sdkAdapter: "codex",
        source: "stage-config",
        model: "opus",
        codexModel: "gpt-5.5",
        status: "ok",
      },
    ],
    ...overrides,
  };
}

describe("renderAdapterDoctorHtml (#4031)", () => {
  it("renders both tables and the refresh control", () => {
    const html = render(baseReport());
    expect(html).toContain('id="adapter-table"');
    expect(html).toContain('id="stage-table"');
    expect(html).toContain('id="refresh"');
    expect(html).toContain("Adapter Doctor");
    expect(html).toContain('data-adapter="codex"');
    expect(html).toContain('data-stage="feature-dev"');
    expect(html).toContain("gpt-5.5"); // codex tier resolution surfaced
  });

  it("shows remediation hints for an unhealthy adapter", () => {
    const report = baseReport({
      rows: [
        {
          sdkAdapter: "codex",
          displayName: "Codex",
          kind: "cli",
          binary: "codex",
          installed: false,
          versionOk: false,
          authOk: false,
          authReason: "not logged in",
          remediations: ["Run `codex login`.", "Install the codex CLI and ensure it is on PATH."],
          ok: false,
        },
      ],
    });
    const html = render(report);
    expect(html).toContain("Run `codex login`.");
    expect(html).toContain("Install the codex CLI and ensure it is on PATH.");
    expect(html).toContain("not authenticated");
  });

  it("renders the binary-unavailable warning when binaryResolved is false", () => {
    const html = render(baseReport({ binaryResolved: false }));
    expect(html).toContain("Go binary could not be resolved");
    // version cell falls back to 'unknown' rather than a false 'not on PATH'
    expect(html).toContain("unknown");
  });

  it("renders an empty-state when no adapters/stages resolved", () => {
    const html = render(baseReport({ rows: [], stages: [] }));
    expect(html).toContain("No adapters configured");
    expect(html).toContain("No pipeline stages resolved");
  });

  it("escapes interpolated values to prevent HTML injection", () => {
    const report = baseReport({
      notes: ['<img src=x onerror="alert(1)">'],
    });
    const html = render(report);
    expect(html).not.toContain('<img src=x onerror="alert(1)">');
    expect(html).toContain("&lt;img src=x");
  });

  it("emits a strict CSP and nonces the inline script (webview hardening)", () => {
    const html = render(baseReport());
    expect(html).toContain('http-equiv="Content-Security-Policy"');
    expect(html).toContain("default-src 'none'");
    expect(html).toContain("script-src 'nonce-test-nonce-123'");
    expect(html).toContain("style-src vscode-resource://test 'unsafe-inline'");
    expect(html).toContain('<script nonce="test-nonce-123">');
    // No unnonced inline <script> should remain.
    expect(html).not.toMatch(/<script>\s/);
  });

  it("renders warn and unknown stage statuses", () => {
    const report = baseReport({
      stages: [
        {
          stage: "feature-dev",
          adapter: "codex",
          sdkAdapter: "codex",
          source: "stage-config",
          model: "opus",
          status: "warn",
        },
        {
          stage: "pr-merge",
          adapter: "gemini",
          sdkAdapter: "gemini",
          source: "default",
          model: "(auto / router)",
          status: "unknown",
        },
      ],
    });
    const html = render(report);
    expect(html).toMatch(/<span class="status warn">warn<\/span>/);
    expect(html).toMatch(/<span class="status unknown">unknown<\/span>/);
  });
});
