import { describe, expect, it } from "vitest";
import {
  detectStreamQuotaRejection,
  formatQuotaRejectionMarker,
  isHardRateLimit,
  isQuotaPressureSignal,
} from "../../src/utils/tokenParser";
import { inferProcessError, stripBenignStderr } from "../../src/utils/skillRunner";

const TRUST_WARNING =
  "Ignoring 12 permissions.allow entries from .claude/settings.json: the workspace has not been trusted";

function rejected(type: string, status = "rejected"): string {
  return JSON.stringify({
    type: "rate_limit_event",
    rate_limit_info: {
      status,
      resetsAt: 1795233600,
      rateLimitType: type,
      overageStatus: "rejected",
    },
  });
}
const synthetic = JSON.stringify({
  type: "assistant",
  message: { model: "<synthetic>", content: [{ type: "text", text: "limit reached" }] },
});
const real = JSON.stringify({ type: "assistant", message: { model: "claude-opus-5-5" } });
const NOW = 1795233600 - 3600;
const MARKER =
  "[rate-limit-quota-exhausted] seven-day usage limit reached; resumes at 2026-11-21T04:00:00Z " +
  "(rate_limit_event status=rejected; seven_day bucket; resetsAt=1795233600)";
const detect = (lines: string[]) => detectStreamQuotaRejection(lines.join("\n"), NOW);
const result = JSON.stringify({ type: "result", is_error: false, usage: { input_tokens: 0 } });

describe("detectStreamQuotaRejection (#3465)", () => {
  it("detects the issue's seven_day rejection with a synthetic reply", () => {
    const r = detect([rejected("seven_day"), synthetic, result]);
    expect(r).toEqual({ status: "rejected", rateLimitType: "seven_day", resetsAt: 1795233600 });
    // Same literal as the Go replay test (failure_handler_test.go).
    expect(formatQuotaRejectionMarker(r!)).toBe(MARKER);
  });

  it.each([
    ["five_hour", "five-hour usage limit reached"],
    ["seven_day_overage_included", "seven-day-overage-included usage limit reached"],
  ])("names the %s bucket", (type, text) => {
    const r = detect([rejected(type), synthetic]);
    expect(formatQuotaRejectionMarker(r!)).toContain(text);
  });

  it("ignores a rejection followed by a served request", () => {
    expect(detect([rejected("seven_day"), real])).toBeUndefined();
  });

  it.each(["allowed", "allowed_warning"])("ignores status %s", (status) => {
    expect(detect([rejected("seven_day", status), synthetic])).toBeUndefined();
  });

  it("detects a limited status, the flat event shape and skips malformed lines", () => {
    const flat = JSON.stringify({
      type: "rate_limit_event",
      status: "limited",
      rateLimitType: "five_hour",
      resetsAt: 1795233600,
    });
    expect(detect(["not json", "{broken", flat, synthetic])?.status).toBe("limited");
  });

  it("detects a rejection that follows an earlier served request", () => {
    expect(detect([real, rejected("seven_day"), synthetic])?.rateLimitType).toBe("seven_day");
  });

  it("keeps the rejection when an assistant message has no model", () => {
    const noModel = JSON.stringify({ type: "assistant", message: {} });
    expect(detect([rejected("seven_day"), noModel])).toBeDefined();
  });

  it("drops an implausible resetsAt and an unsafe bucket name", () => {
    const ev = JSON.stringify({
      type: "rate_limit_event",
      rate_limit_info: { status: "rejected", resetsAt: 99999999999, rateLimitType: "x resetsAt=1" },
    });
    const r = detect([ev]);
    expect(r).toEqual({ status: "rejected", rateLimitType: "unknown", resetsAt: undefined });
    expect(formatQuotaRejectionMarker(r!)).toContain("resetsAt=unknown");
  });

  it("omits the resume time when resetsAt is absent", () => {
    const marker = formatQuotaRejectionMarker({ status: "rejected", rateLimitType: "five_hour" });
    expect(marker).not.toContain("resumes at");
    expect(marker).toContain("resetsAt=unknown");
  });

  it("treats rejected as a hard limit and a pressure signal", () => {
    expect(isHardRateLimit("rejected")).toBe(true);
    expect(isQuotaPressureSignal("rejected")).toBe(true);
  });
});

describe("trust warning is never the reported error (#3465)", () => {
  it("falls through to the exit-code error when stderr is only the warning", () => {
    const err = inferProcessError(false, TRUST_WARNING, "", 1);
    expect(err?.message).not.toContain("permissions.allow");
    expect(err?.message).toContain("Process exited with code 1");
  });

  it("keeps a real stderr error and drops the warning", () => {
    const err = inferProcessError(false, `${TRUST_WARNING}\nfatal: boom`, "", 1);
    expect(err?.message).toBe("fatal: boom");
    expect(stripBenignStderr(TRUST_WARNING)).toBe("");
    expect(stripBenignStderr("note: permissions.allow is set")).toBe(
      "note: permissions.allow is set"
    );
  });
});
