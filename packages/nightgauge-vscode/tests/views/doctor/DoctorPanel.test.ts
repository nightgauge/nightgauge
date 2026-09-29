/**
 * Doctor panel (ADR-025): the rendered cards, their buttons, escaping, and
 * the message boundary between the webview and the doctor.* IPC methods.
 */

import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import type {
  DoctorFinding,
  DoctorProgressEvent,
  DoctorRunResult,
} from "../../../src/services/IpcClientBase";

let messageHandler: ((msg: unknown) => Promise<void> | void) | null;
let createWebviewPanelMock: Mock;
let showWarningMessageMock: Mock;
let openExternalMock: Mock;

vi.mock("vscode", () => {
  class EventEmitter<T> {
    private listeners = new Set<(v: T) => void>();
    event = (listener: (v: T) => void) => {
      this.listeners.add(listener);
      return { dispose: () => this.listeners.delete(listener) };
    };
    fire(v: T) {
      for (const l of [...this.listeners]) l(v);
    }
    dispose() {
      this.listeners.clear();
    }
  }
  return {
    EventEmitter,
    ViewColumn: { One: 1 },
    Uri: { parse: (value: string) => ({ toString: () => value }) },
    env: { openExternal: vi.fn(async () => true) },
    window: {
      createWebviewPanel: vi.fn(),
      showWarningMessage: vi.fn(),
      showErrorMessage: vi.fn(),
    },
  };
});

function mockPanel() {
  messageHandler = null;
  return {
    webview: {
      html: "",
      cspSource: "vscode-resource:",
      onDidReceiveMessage: vi.fn((handler: (msg: unknown) => void) => {
        messageHandler = handler;
        return { dispose: vi.fn() };
      }),
      postMessage: vi.fn(async () => true),
    },
    reveal: vi.fn(),
    onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
    dispose: vi.fn(),
  };
}

const FP = {
  blocker: "1111111111111111",
  warning: "2222222222222222",
  housekeeping: "3333333333333333",
  info: "4444444444444444",
};

function finding(
  severity: DoctorFinding["severity"],
  over: Partial<DoctorFinding> = {}
): DoctorFinding {
  return {
    code: "NGD000",
    check: "check",
    severity,
    title: `${severity} title`,
    cause: `${severity} cause`,
    evidence: { key: "value" },
    docs: "docs/DOCTOR.md#ngd000",
    fingerprint: FP[severity],
    remedies: [],
    ...over,
  };
}

/** A v2 result with one finding of each severity. */
function fixture(): DoctorRunResult {
  const findings: DoctorFinding[] = [
    finding("blocker", {
      code: "NGD004",
      check: "github_auth",
      docs: "docs/DOCTOR.md#ngd004",
      remedies: [
        {
          id: "login",
          kind: "manual",
          summary: "Sign in to GitHub",
          preview: "",
          verb: "",
          reversible: false,
          verify: "github_auth",
          steps: ["Run the sign-in command"],
          links: ["https://docs.github.com/en/authentication", "https://evil.example/phish"],
        },
      ],
    }),
    finding("warning", {
      code: "NGD014",
      check: "complexity_model",
      remedies: [
        {
          id: "init",
          kind: "auto",
          summary: "Create the complexity model",
          preview: "outcome init",
          verb: "outcome_init",
          reversible: true,
          verify: "complexity_model",
        },
      ],
    }),
    finding("housekeeping", {
      code: "NGD018",
      check: "stranded_branches",
      remedies: [
        {
          id: "delete",
          kind: "confirm",
          summary: "Delete the merged branch",
          preview: "delete branch feat/1",
          verb: "delete_branch",
          reversible: false,
          verify: "stranded_branches",
        },
        {
          id: "prune",
          kind: "auto",
          summary: "Prune the local ref",
          preview: "prune feat/1",
          verb: "prune_ref",
          reversible: true,
          verify: "stranded_branches",
        },
      ],
    }),
    finding("info", { code: "NGD026", check: "survival_backlog" }),
  ];
  return {
    v: 2,
    findings,
    summary: { blocker: 1, warning: 1, housekeeping: 1, info: 1 },
    healthy: false,
    exit_code: 2,
    failed_checks: ["github_auth"],
    errors: [],
    warnings: [],
    install_instructions: "",
    adapters: [],
  };
}

function fakeIpc(result: DoctorRunResult) {
  const progressHandlers = new Set<(d: unknown) => void>();
  return {
    progressHandlers,
    doctorRun: vi.fn(async () => {
      const ev: DoctorProgressEvent = {
        index: 0,
        total: 1,
        check: "github_auth",
        title: "GitHub auth",
        phase: "finished",
        status: "failed",
        findings: 1,
      };
      for (const h of progressHandlers) h(ev);
      return result;
    }),
    doctorApplyRemedy: vi.fn(async () => ({
      outcome: "fixed" as const,
      action: "applied" as const,
      preview: "",
    })),
    doctorRecheck: vi.fn(async () => ({
      check: "check",
      title: "Check",
      status: "passed" as const,
      findings: [],
    })),
    on: vi.fn((event: string, handler: (d: unknown) => void) => {
      if (event === "doctor.progress") progressHandlers.add(handler);
      return { dispose: () => progressHandlers.delete(handler) };
    }),
  };
}

type Html = typeof import("../../../src/views/doctor/DoctorHtml");
let html: Html;
let DoctorPanel: typeof import("../../../src/views/doctor/DoctorPanel").DoctorPanel;
let DoctorService: typeof import("../../../src/views/doctor/DoctorService").DoctorService;

beforeEach(async () => {
  const vscode = await import("vscode");
  createWebviewPanelMock = vscode.window.createWebviewPanel as unknown as Mock;
  showWarningMessageMock = vscode.window.showWarningMessage as unknown as Mock;
  openExternalMock = vscode.env.openExternal as unknown as Mock;
  createWebviewPanelMock.mockReset();
  showWarningMessageMock.mockReset();
  openExternalMock.mockClear();
  html = await import("../../../src/views/doctor/DoctorHtml");
  ({ DoctorPanel } = await import("../../../src/views/doctor/DoctorPanel"));
  ({ DoctorService } = await import("../../../src/views/doctor/DoctorService"));
});

afterEach(() => {
  DoctorPanel.current?.dispose();
});

function render(result: DoctorRunResult): string {
  return html.renderDoctorHtml(
    { phase: "ready", result, progress: [] },
    { cspSource: "vscode-resource:", nonce: "abc123" }
  );
}

function cardOf(page: string, fingerprint: string): string {
  const start = page.indexOf(`<article class="card`, page.indexOf(`id="card-${fingerprint}"`) - 60);
  return page.slice(start, page.indexOf("</article>", start));
}

async function openPanel(result = fixture()) {
  const panel = mockPanel();
  createWebviewPanelMock.mockReturnValue(panel);
  const ipc = fakeIpc(result);
  const service = new DoctorService(ipc, () => ["claude-headless"]);
  DoctorPanel.show(service);
  await vi.waitFor(() => expect(panel.webview.html).toContain("<article"));
  return { panel, ipc, service };
}

describe("DoctorHtml", () => {
  it("renders four cards, one per severity, with the buttons their remedy kinds call for", () => {
    const page = render(fixture());
    expect(page.match(/<article class="card /g)).toHaveLength(4);

    const blocker = cardOf(page, FP.blocker);
    expect(blocker).toContain("Severity: </span>Blocker");
    expect(blocker).toContain('data-action="open"');
    expect(blocker).toContain('data-action="recheck"');
    expect(blocker).not.toContain('data-action="fix"');
    // The allowlisted link is listed; the other one is text, never opened.
    expect(blocker).toContain("not opened: host not allowlisted");

    const warning = cardOf(page, FP.warning);
    expect(warning).toMatch(/data-action="fix"[^>]*data-remedy="init"[^>]*>Fix<\/button>/);
    expect(warning).not.toContain('data-action="review"');

    const housekeeping = cardOf(page, FP.housekeeping);
    expect(housekeeping).toMatch(
      /data-action="review"[^>]*data-remedy="delete"[^>]*>Review &amp; fix</
    );
    expect(housekeeping).toMatch(/data-action="fix"[^>]*data-remedy="prune"/);

    const info = cardOf(page, FP.info);
    expect(info).toContain('data-action="recheck"');
    expect(info).not.toMatch(/data-action="(fix|review|open)"/);
  });

  it("links each code to its docs/DOCTOR.md anchor and names the severity as text", () => {
    const page = render(fixture());
    expect(page).toContain(
      'href="https://github.com/nightgauge/nightgauge/blob/main/docs/DOCTOR.md#ngd004"'
    );
    for (const label of ["Blocker", "Warning", "Housekeeping", "Info"]) {
      expect(page).toContain(`<span class="sr-only">Severity: </span>${label}`);
    }
  });

  it("collapses housekeeping behind a closed <details> and offers Fix all safe", () => {
    const page = render(fixture());
    expect(page).toMatch(/<details class="group-body" id="housekeeping-body">/);
    expect(page).not.toMatch(/<details class="group-body"[^>]*\bopen\b/);
    expect(page).toContain('data-action="fixAllSafe"');
    const body = page.slice(page.indexOf('id="housekeeping-body"'));
    expect(body.indexOf(`card-${FP.housekeeping}`)).toBeGreaterThan(0);
  });

  it("escapes finding text containing <script>", () => {
    const result = fixture();
    result.findings![0] = {
      ...result.findings![0],
      title: "<script>alert(1)</script>",
      cause: '<img src=x onerror="alert(2)">',
      evidence: { "<b>k</b>": "<script>alert(3)</script>" },
    };
    const page = render(result);
    expect(page).toContain("&lt;script&gt;alert(1)&lt;/script&gt;");
    expect(page).toContain("&lt;img src=x onerror=&quot;alert(2)&quot;&gt;");
    expect(page).toContain("&lt;script&gt;alert(3)&lt;/script&gt;");
    // The only <script> element is the nonce'd panel script.
    expect(page.match(/<script\b/g)).toHaveLength(1);
    expect(page).toContain('<script nonce="abc123">');
  });

  it("sets a strict CSP: default-src 'none', nonce-only script, no unsafe-eval, no remote source", () => {
    const page = render(fixture());
    const csp = /content="([^"]+)"/.exec(page.slice(page.indexOf("Content-Security-Policy")))![1];
    expect(csp).toContain("default-src 'none'");
    expect(csp).toContain("script-src 'nonce-abc123'");
    expect(csp).not.toContain("unsafe-eval");
    expect(csp).not.toContain("unsafe-inline");
    expect(csp).not.toMatch(/https?:/);
    expect(page).not.toMatch(/<(link|img|iframe)\b/);
    expect(page).toContain('aria-live="polite"');
  });

  it("allowlists only https links on the ADR-025 hosts", () => {
    expect(html.isAllowedLink("https://github.com/x")).toBe(true);
    expect(html.isAllowedLink("https://docs.github.com/x")).toBe(true);
    expect(html.isAllowedLink("https://nightgauge.dev/x")).toBe(true);
    expect(html.isAllowedLink("http://github.com/x")).toBe(false);
    expect(html.isAllowedLink("https://github.com.evil.example/x")).toBe(false);
    expect(html.isAllowedLink("https://user:pw@github.com/x")).toBe(false);
    expect(html.isAllowedLink("javascript:alert(1)")).toBe(false);
  });
});

describe("DoctorPanel", () => {
  it("runs doctor.run with the resolved adapters and renders progress while it runs", async () => {
    const { panel, ipc } = await openPanel();
    expect(ipc.doctorRun).toHaveBeenCalledWith(undefined, undefined, ["claude-headless"]);
    expect(panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "progress",
        event: expect.objectContaining({ check: "github_auth" }),
      })
    );
  });

  it("a fix message calls doctor.applyRemedy with the fingerprint and shows the outcome in place", async () => {
    const { panel, ipc } = await openPanel();
    const before = panel.webview.html;
    await messageHandler!({ type: "fix", fingerprint: FP.warning, remedyId: "init" });
    expect(ipc.doctorApplyRemedy).toHaveBeenCalledWith(FP.warning, "init", undefined, undefined);
    expect(panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "outcome", fingerprint: FP.warning, state: "resolved" })
    );
    // No full reload: the page itself was not re-rendered.
    expect(panel.webview.html).toBe(before);
  });

  it("shows a still-present outcome with the new evidence", async () => {
    const { panel, ipc } = await openPanel();
    ipc.doctorApplyRemedy.mockResolvedValueOnce({
      outcome: "still-present",
      action: "applied",
      preview: "",
      evidence: { path: "/tmp/new" },
    } as never);
    await messageHandler!({ type: "fix", fingerprint: FP.warning, remedyId: "init" });
    expect(panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "outcome",
        state: "present",
        evidence: [["path", "/tmp/new"]],
      })
    );
  });

  it("drops a message with an unknown type", async () => {
    const { panel, ipc } = await openPanel();
    const posted = panel.webview.postMessage.mock.calls.length;
    await messageHandler!({ type: "exec", fingerprint: FP.warning, remedyId: "init" });
    await messageHandler!({ type: "fix", fingerprint: FP.warning, remedyId: "init", verb: "rm" });
    await messageHandler!("fix");
    expect(ipc.doctorApplyRemedy).not.toHaveBeenCalled();
    expect(panel.webview.postMessage.mock.calls.length).toBe(posted);
  });

  it("refuses a fix whose remedy is not auto, or whose fingerprint is not in the scan", async () => {
    const { ipc } = await openPanel();
    await messageHandler!({ type: "fix", fingerprint: FP.housekeeping, remedyId: "delete" });
    await messageHandler!({ type: "fix", fingerprint: "ffffffffffffffff", remedyId: "init" });
    expect(ipc.doctorApplyRemedy).not.toHaveBeenCalled();
  });

  it("previews a confirm remedy in the card, then applies it only after the modal", async () => {
    const { panel, ipc } = await openPanel();
    ipc.doctorApplyRemedy.mockResolvedValueOnce({
      outcome: "",
      action: "previewed",
      preview: "delete branch feat/1",
    } as never);
    showWarningMessageMock.mockResolvedValueOnce("Fix");
    await messageHandler!({ type: "review", fingerprint: FP.housekeeping, remedyId: "delete" });
    expect(ipc.doctorApplyRemedy).toHaveBeenNthCalledWith(
      1,
      FP.housekeeping,
      "delete",
      undefined,
      true
    );
    expect(panel.webview.postMessage).toHaveBeenCalledWith({
      type: "preview",
      fingerprint: FP.housekeeping,
      preview: "delete branch feat/1",
    });
    expect(showWarningMessageMock).toHaveBeenCalledWith(
      expect.stringContaining("NGD018"),
      expect.objectContaining({ modal: true }),
      "Fix"
    );
    expect(ipc.doctorApplyRemedy).toHaveBeenNthCalledWith(
      2,
      FP.housekeeping,
      "delete",
      true,
      undefined
    );
  });

  it("does not apply a confirm remedy when the modal is dismissed", async () => {
    const { ipc } = await openPanel();
    ipc.doctorApplyRemedy.mockResolvedValueOnce({
      outcome: "",
      action: "previewed",
      preview: "p",
    } as never);
    showWarningMessageMock.mockResolvedValueOnce(undefined);
    await messageHandler!({ type: "review", fingerprint: FP.housekeeping, remedyId: "delete" });
    expect(ipc.doctorApplyRemedy).toHaveBeenCalledTimes(1);
  });

  it("Fix all safe applies only the housekeeping auto remedies", async () => {
    const { ipc } = await openPanel();
    await messageHandler!({ type: "fixAllSafe" });
    expect(ipc.doctorApplyRemedy).toHaveBeenCalledTimes(1);
    expect(ipc.doctorApplyRemedy).toHaveBeenCalledWith(
      FP.housekeeping,
      "prune",
      undefined,
      undefined
    );
  });

  it("opens only allowlisted links, through openExternal", async () => {
    await openPanel();
    await messageHandler!({ type: "open", fingerprint: FP.blocker, remedyId: "login" });
    await messageHandler!({ type: "openDocs", fingerprint: FP.blocker });
    expect(openExternalMock.mock.calls.map((c) => String(c[0]))).toEqual([
      "https://docs.github.com/en/authentication",
      "https://github.com/nightgauge/nightgauge/blob/main/docs/DOCTOR.md#ngd004",
    ]);
  });

  it("check again calls doctor.recheck with the finding's check", async () => {
    const { panel, ipc } = await openPanel();
    await messageHandler!({ type: "recheck", fingerprint: FP.info });
    expect(ipc.doctorRecheck).toHaveBeenCalledWith(undefined, "survival_backlog");
    expect(panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({ type: "outcome", fingerprint: FP.info, state: "resolved" })
    );
  });
});
