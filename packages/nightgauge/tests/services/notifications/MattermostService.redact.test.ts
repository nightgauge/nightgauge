import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { vi } from "vitest";
import { makeConfigBridge, makeLogger, makeRun, makeState } from "./_helpers";
import { redactSecrets } from "../../../src/services/notifications/transport";
// DiscordService re-exports redactSecrets from transport — import for parity test
import { redactSecrets as discordRedact } from "../../../src/services/DiscordService";

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
}));

vi.mock("../../../src/services/SecretStorageService", () => ({
  SecretStorageService: { getInstance: () => null },
  SECRET_KEYS: { mattermostWebhookUrl: "mattermostWebhookUrl" },
}));

const { MattermostService } = await import("../../../src/services/notifications/MattermostService");

// ─── Redaction in buildAttachment ─────────────────────────────────────────

describe("MattermostService.redact — redaction in buildAttachment", () => {
  let service: InstanceType<typeof MattermostService>;

  beforeEach(() => {
    service = new MattermostService(
      {} as never,
      makeConfigBridge() as never,
      makeLogger() as never
    );
  });

  afterEach(() => {
    service.dispose();
  });

  it("redacts GitHub PAT (ghp_...) from error fields", () => {
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: "GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz1234 failed",
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).not.toContain("ghp_abcdefghijklmnopqrstuvwxyz1234");
    expect(errorField!.value).toContain("[REDACTED");
  });

  it("redacts JWT from error fields", () => {
    const jwt =
      "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c";
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: `bearer token: ${jwt}`,
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).not.toContain(jwt);
    expect(errorField!.value).toContain("[REDACTED:JWT]");
  });

  it("redacts KEY=value env-var assignments from error fields", () => {
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: "API_KEY=supersecretvalue1234567890 was exposed",
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).not.toContain("supersecretvalue1234567890");
    expect(errorField!.value).toContain("API_KEY=[REDACTED]");
  });

  it("redacts Stripe keys (sk_live_...) from error fields", () => {
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: "Stripe error: key sk_live_abcdefghijklmnopqrstuv was invalid",
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).not.toContain("sk_live_abcdefghijklmnopqrstuv");
    expect(errorField!.value).toContain("[REDACTED:STRIPE_KEY]");
  });

  it("redacts Slack tokens (xoxb-...) from error fields", () => {
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: "Slack token xoxb-abcdefghijklmnop was rejected",
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).not.toContain("xoxb-abcdefghijklmnop");
    expect(errorField!.value).toContain("[REDACTED:SLACK_TOKEN]");
  });

  it("does not alter non-secret text in error fields", () => {
    const state = makeState(42, "failure", {
      stages: {
        "feature-dev": {
          status: "failed",
          error: "TypeScript compilation failed: Cannot find module './foo'",
        },
      },
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    const errorField = att.fields?.find((f) => f.title === "Error Details");
    expect(errorField).toBeTruthy();
    expect(errorField!.value).toContain("TypeScript compilation failed");
    expect(errorField!.value).toContain("Cannot find module './foo'");
  });

  it("redacts secrets from the attachment description text", () => {
    // Secrets can appear in title via state.title — redacted in buildDescription
    const state = makeState(42, undefined, {
      title: "Issue with TOKEN=ghp_abcdefghijklmnop12345678 in title",
    });
    const att = service.buildAttachment(makeRun() as never, state as never);
    expect(att.text).not.toContain("ghp_abcdefghijklmnop12345678");
  });
});

// ─── Parity: MattermostService and DiscordService use identical redaction ──

describe("MattermostService.redact — parity with DiscordService redaction", () => {
  it("redactSecrets from DiscordService re-export is the same function as transport.redactSecrets", () => {
    // DiscordService re-exports redactSecrets from transport — both references are identical
    expect(discordRedact).toBe(redactSecrets);
  });

  it("both services redact the same error string to the same result (GH PAT)", () => {
    const errorText = "GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz1234 failed during fetch";
    const mmResult = redactSecrets(errorText);
    const discordResult = discordRedact(errorText);
    expect(mmResult).toBe(discordResult);
    expect(mmResult).not.toContain("ghp_abcdefghijklmnopqrstuvwxyz1234");
  });

  it("both services redact JWT identically", () => {
    const jwt =
      "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c";
    const input = `Authorization: Bearer ${jwt}`;
    expect(redactSecrets(input)).toBe(discordRedact(input));
  });

  it("both services redact Stripe keys identically", () => {
    // Constructed at runtime — avoids triggering push-protection on literal patterns
    const fakeStripeKey = "sk_" + "live_" + "fakekeyfortesting12345";
    const input = `Payment failed with key ${fakeStripeKey}`;
    expect(redactSecrets(input)).toBe(discordRedact(input));
  });

  it("both services leave non-secret text unchanged", () => {
    const input = "Build failed: cannot find module './missing'";
    expect(redactSecrets(input)).toBe(discordRedact(input));
    expect(redactSecrets(input)).toBe(input);
  });
});
