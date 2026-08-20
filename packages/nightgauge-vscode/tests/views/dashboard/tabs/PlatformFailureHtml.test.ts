/**
 * Tests for PlatformFailureHtml (#748) — the shared renderer every
 * platform-backed tab uses to turn a classified `PlatformFailure` into
 * copy. This is the single source of truth for the "never assert a cause
 * the code hasn't established" rule, so it gets its own direct coverage in
 * addition to each tab's integration tests.
 */

import { describe, it, expect } from "vitest";
import {
  renderPlatformFailure,
  getPlatformRetryButtonHtml,
  getPlatformSignInButtonHtml,
  getPlatformFailureScript,
} from "../../../../src/views/dashboard/tabs/PlatformFailureHtml";
import type { PlatformFailure } from "../../../../src/services/platformResult";

function makeFailure(overrides: Partial<PlatformFailure> = {}): PlatformFailure {
  return {
    ok: false,
    kind: "server_error",
    endpoint: "platform.getAnalyticsHealth",
    message: "get analytics health: server returned 500",
    ...overrides,
  };
}

describe("renderPlatformFailure", () => {
  it("unauthorized → sign-in copy, no role/plan claim, no retry", () => {
    const rendered = renderPlatformFailure(makeFailure({ kind: "unauthorized", status: 401 }));
    expect(rendered.title).toBe("Sign-in required");
    expect(rendered.showSignIn).toBe(true);
    expect(rendered.showRetry).toBe(false);
    expect(rendered.hintHtml.toLowerCase()).not.toContain("role");
    expect(rendered.hintHtml.toLowerCase()).not.toContain("upgrade");
    expect(rendered.hintHtml).toContain("401");
  });

  it("forbidden → the ONLY kind whose copy may mention role/plan, quoting the real message", () => {
    const rendered = renderPlatformFailure(
      makeFailure({
        kind: "forbidden",
        status: 403,
        message: "get analytics health: server returned 403",
      })
    );
    expect(rendered.title).toBe("Access denied");
    expect(rendered.hintHtml).toContain("role or plan");
    expect(rendered.hintHtml).toContain("server returned 403");
    expect(rendered.showSignIn).toBe(false);
    expect(rendered.showRetry).toBe(true);
  });

  it("server_error → retry is meaningful, copy names the endpoint and status", () => {
    const rendered = renderPlatformFailure(
      makeFailure({ kind: "server_error", status: 500, endpoint: "platform.getCostAnalytics" })
    );
    expect(rendered.title).toBe("Platform error");
    expect(rendered.hintHtml).toContain("platform.getCostAnalytics");
    expect(rendered.hintHtml).toContain("500");
    expect(rendered.showRetry).toBe(true);
    expect(rendered.showSignIn).toBe(false);
  });

  it("offline → unreachable copy, no status claim (there was no HTTP response)", () => {
    const rendered = renderPlatformFailure(makeFailure({ kind: "offline", status: undefined }));
    expect(rendered.title).toBe("Platform unreachable");
    expect(rendered.showRetry).toBe(true);
  });

  it("not_configured → not-connected copy, offers sign-in not retry", () => {
    const rendered = renderPlatformFailure(makeFailure({ kind: "not_configured" }));
    expect(rendered.title).toBe("Not connected");
    expect(rendered.showSignIn).toBe(true);
    expect(rendered.showRetry).toBe(false);
  });

  it("unrecognized kind → neutral message naming endpoint and status, never invents a reason", () => {
    const rendered = renderPlatformFailure(
      makeFailure({
        // @ts-expect-error — deliberately outside the known union to exercise the fallback
        kind: "totally_unknown_kind",
        status: 599,
        endpoint: "platform.someNewEndpoint",
        message: "raw message text",
      })
    );
    expect(rendered.title).toBe("Unable to load data");
    expect(rendered.hintHtml).toContain("platform.someNewEndpoint");
    expect(rendered.hintHtml).toContain("599");
    expect(rendered.hintHtml).toContain("raw message text");
    expect(rendered.hintHtml.toLowerCase()).not.toContain("role");
    expect(rendered.hintHtml.toLowerCase()).not.toContain("upgrade");
    expect(rendered.hintHtml.toLowerCase()).not.toContain("temporary");
  });

  it("every kind renders genuinely distinct copy — no two causes share a title", () => {
    const kinds: PlatformFailure["kind"][] = [
      "unauthorized",
      "forbidden",
      "server_error",
      "offline",
      "not_configured",
    ];
    const titles = kinds.map((kind) => renderPlatformFailure(makeFailure({ kind })).title);
    expect(new Set(titles).size).toBe(titles.length);
  });

  it("only forbidden's copy ever contains 'role' or 'plan' — no other kind leaks that language", () => {
    const kinds: PlatformFailure["kind"][] = [
      "unauthorized",
      "server_error",
      "offline",
      "not_configured",
    ];
    for (const kind of kinds) {
      const rendered = renderPlatformFailure(makeFailure({ kind }));
      expect(rendered.hintHtml.toLowerCase()).not.toContain("role");
      expect(rendered.hintHtml.toLowerCase()).not.toContain("plan");
    }
  });

  it("no kind other than server_error/forbidden calls a failure 'temporary' unless it is", () => {
    // Verified live (#748): Health told a permanent failure "likely a
    // temporary issue — retry shortly." offline/unauthorized/not_configured
    // must never claim a condition is temporary.
    const kinds: PlatformFailure["kind"][] = ["unauthorized", "offline", "not_configured"];
    for (const kind of kinds) {
      const rendered = renderPlatformFailure(makeFailure({ kind }));
      expect(rendered.hintHtml.toLowerCase()).not.toContain("temporary");
    }
  });
});

describe("getPlatformRetryButtonHtml", () => {
  it("embeds the retry message as escaped JSON for the shared delegated handler", () => {
    const html = getPlatformRetryButtonHtml("runsRetryBtn", { type: "runsRefresh" });
    expect(html).toContain('id="runsRetryBtn"');
    expect(html).toContain('data-action="platform-retry"');
    expect(html).toContain("runsRefresh");
  });

  it("escapes HTML-significant characters in the id", () => {
    const html = getPlatformRetryButtonHtml('"><script>', { type: "x" });
    expect(html).not.toContain("<script>");
  });
});

describe("getPlatformSignInButtonHtml", () => {
  it("wires data-action=platform-sign-in", () => {
    const html = getPlatformSignInButtonHtml("healthSignInBtn");
    expect(html).toContain('id="healthSignInBtn"');
    expect(html).toContain('data-action="platform-sign-in"');
  });
});

describe("getPlatformFailureScript", () => {
  it("posts the parsed retry message and the signInWithPlatform message", () => {
    const script = getPlatformFailureScript();
    expect(script).toContain("platform-retry");
    expect(script).toContain("platform-sign-in");
    expect(script).toContain("signInWithPlatform");
    expect(script).toContain("JSON.parse");
  });
});
