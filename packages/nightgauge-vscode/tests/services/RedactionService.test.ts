/**
 * Tests for RedactionService (Issue #3326).
 *
 * Covers:
 * 1. Pass-through — clean payload returned unchanged with zero counters
 * 2. _debug_ prefix drop
 * 3. Secret-pattern drops (api_key, apiKey, token, password, ssh_key,
 *    secret, credential, auth) — anchored regex
 * 4. Nested drop with counter increment
 * 5. String truncation past maxStringLength
 * 6. Array recursion
 * 7. Depth cap terminates safely
 * 8. eventType / timestamp pass-through
 * 9. Hard guarantee — synthetic-PII fixtures never appear in serialized output
 *    (the issue's mandated `describe('redaction').it('blocks user code paths')`).
 *
 * @see src/services/RedactionService.ts
 */

import { describe, it, expect } from "vitest";
import { RedactionService } from "../../src/services/RedactionService";
import type { AnalyticsEvent } from "../../src/platform/types";

function makeEvent(
  payload: Record<string, unknown> | undefined,
  overrides: Partial<AnalyticsEvent> = {}
): AnalyticsEvent {
  return {
    eventType: overrides.eventType ?? "pipeline_execution_completed",
    payload,
    timestamp: overrides.timestamp ?? "2026-05-08T12:00:00.000Z",
  };
}

describe("RedactionService.redact — pass-through", () => {
  it("returns clean payload unchanged with zero counters", () => {
    const svc = new RedactionService();
    const event = makeEvent({
      duration_ms: 1234,
      stage_count: 6,
      outcome: "success",
      model_used: "claude-opus-4-7",
    });

    const result = svc.redact(event);

    expect(result.event.payload).toEqual({
      duration_ms: 1234,
      stage_count: 6,
      outcome: "success",
      model_used: "claude-opus-4-7",
    });
    expect(result.fieldsRemoved).toBe(0);
    expect(result.fieldsRedacted).toBe(0);
  });

  it("preserves undefined payload", () => {
    const svc = new RedactionService();
    const result = svc.redact(makeEvent(undefined));
    expect(result.event.payload).toBeUndefined();
    expect(result.fieldsRemoved).toBe(0);
    expect(result.fieldsRedacted).toBe(0);
  });
});

describe("RedactionService.redact — debug prefix drop", () => {
  it("drops _debug_-prefixed fields and increments fieldsRemoved", () => {
    const svc = new RedactionService();
    const result = svc.redact(
      makeEvent({
        duration_ms: 100,
        _debug_prompt: "internal prompt content",
        _debug_capture: { whatever: 1 },
      })
    );

    expect(result.event.payload).toEqual({ duration_ms: 100 });
    expect(result.fieldsRemoved).toBe(2);
    expect(result.fieldsRedacted).toBe(0);
  });
});

describe("RedactionService.redact — secret-pattern drop (anchored)", () => {
  const canonicalSecretKeys = [
    "api_key",
    "apiKey",
    "ApiKey",
    "API_KEY",
    "apikey",
    "token",
    "TOKEN",
    "password",
    "ssh_key",
    "sshkey",
    "secret",
    "credential",
    "auth",
  ];

  for (const key of canonicalSecretKeys) {
    it(`drops top-level field "${key}"`, () => {
      const svc = new RedactionService();
      const payload: Record<string, unknown> = { duration_ms: 1 };
      payload[key] = "some-sensitive-value-12345";
      const result = svc.redact(makeEvent(payload));

      expect(result.event.payload).toEqual({ duration_ms: 1 });
      expect(result.event.payload).not.toHaveProperty(key);
      expect(result.fieldsRemoved).toBe(1);
    });
  }

  it("does NOT drop fields whose name only contains a secret token (anchored regex)", () => {
    const svc = new RedactionService();
    const result = svc.redact(
      makeEvent({
        auth_attempts: 5,
        api_key_count: 3,
        token_bucket_size: 100,
        keypress_count: 12,
      })
    );

    expect(result.event.payload).toEqual({
      auth_attempts: 5,
      api_key_count: 3,
      token_bucket_size: 100,
      keypress_count: 12,
    });
    expect(result.fieldsRemoved).toBe(0);
  });
});

describe("RedactionService.redact — nested drop", () => {
  it("drops secret keys at depth and counts each drop once", () => {
    const svc = new RedactionService();
    const result = svc.redact(
      makeEvent({
        outer: {
          token: "should-be-dropped",
          value: 1,
          inner: {
            password: "also-dropped",
            keep: "kept",
          },
        },
      })
    );

    expect(result.event.payload).toEqual({
      outer: {
        value: 1,
        inner: { keep: "kept" },
      },
    });
    expect(result.fieldsRemoved).toBe(2);
  });
});

describe("RedactionService.redact — string truncation", () => {
  it("truncates strings longer than maxStringLength and increments fieldsRedacted", () => {
    const svc = new RedactionService({ maxStringLength: 10 });
    const result = svc.redact(makeEvent({ note: "a".repeat(50) }));

    expect(result.event.payload?.note).toBe("aaaaaaaaaa…");
    expect(result.fieldsRedacted).toBe(1);
    expect(result.fieldsRemoved).toBe(0);
  });

  it("uses default cap of 200 chars when no option provided", () => {
    const svc = new RedactionService();
    const result = svc.redact(makeEvent({ note: "x".repeat(1024) }));

    expect((result.event.payload?.note as string).length).toBe(201); // 200 chars + ellipsis
    expect(result.event.payload?.note).toMatch(/…$/);
    expect(result.fieldsRedacted).toBe(1);
  });

  it("does not truncate strings at or under the cap", () => {
    const svc = new RedactionService({ maxStringLength: 10 });
    const result = svc.redact(makeEvent({ note: "abcdefghij" }));

    expect(result.event.payload?.note).toBe("abcdefghij");
    expect(result.fieldsRedacted).toBe(0);
  });
});

describe("RedactionService.redact — array recursion", () => {
  it("scrubs secrets nested inside arrays of objects", () => {
    const svc = new RedactionService();
    const result = svc.redact(
      makeEvent({
        items: [
          { name: "alpha", token: "drop1" },
          { name: "beta", password: "drop2", value: 7 },
        ],
      })
    );

    expect(result.event.payload).toEqual({
      items: [{ name: "alpha" }, { name: "beta", value: 7 }],
    });
    expect(result.fieldsRemoved).toBe(2);
  });
});

describe("RedactionService.redact — depth cap", () => {
  it("terminates safely on deeply nested objects", () => {
    const svc = new RedactionService();
    // Build an 11-deep nested object: { a: { a: { a: ... { a: 1 } ... }}}
    let nested: Record<string, unknown> = { a: 1 };
    for (let i = 0; i < 11; i++) {
      nested = { a: nested };
    }

    expect(() => svc.redact(makeEvent(nested))).not.toThrow();
    const result = svc.redact(makeEvent(nested));
    const serialized = JSON.stringify(result.event.payload);
    expect(serialized).toContain("[DEPTH_LIMIT]");
  });
});

describe("RedactionService.redact — eventType and timestamp pass-through", () => {
  it("preserves eventType and timestamp verbatim", () => {
    const svc = new RedactionService();
    const result = svc.redact({
      eventType: "custom_event_type",
      payload: { token: "drop", keep: 1 },
      timestamp: "2026-05-08T13:00:00.000Z",
    });

    expect(result.event.eventType).toBe("custom_event_type");
    expect(result.event.timestamp).toBe("2026-05-08T13:00:00.000Z");
    expect(result.event.payload).toEqual({ keep: 1 });
  });
});

describe("redaction", () => {
  it("blocks user code paths", () => {
    // The issue's mandated hard-guarantee test: seed an event with synthetic
    // PII fixtures and assert that the fixture strings never appear in the
    // serialized output of the redacted event.
    const USER_CODE_FIXTURE = "function secret() { return 'PASSWORD123'; }";
    const EMAIL_FIXTURE = "user@example.com";
    const API_KEY_FIXTURE = "sk-abcdef0123456789";
    const FILE_CONTENT_FIXTURE =
      "import { readFileSync } from 'fs'; const data = readFileSync('/etc/passwd');";

    const svc = new RedactionService();
    const result = svc.redact(
      makeEvent({
        duration_ms: 100,
        _debug_capture: USER_CODE_FIXTURE,
        _debug_email: EMAIL_FIXTURE,
        api_key: API_KEY_FIXTURE,
        token: API_KEY_FIXTURE,
        password: API_KEY_FIXTURE,
        nested: {
          api_key: API_KEY_FIXTURE,
          token: API_KEY_FIXTURE,
          _debug_file: FILE_CONTENT_FIXTURE,
          contents: [{ token: API_KEY_FIXTURE }, { _debug_email: EMAIL_FIXTURE }],
        },
      })
    );

    const serialized = JSON.stringify(result.event);

    expect(serialized).not.toContain(USER_CODE_FIXTURE);
    expect(serialized).not.toContain(EMAIL_FIXTURE);
    expect(serialized).not.toContain(API_KEY_FIXTURE);
    expect(serialized).not.toContain(FILE_CONTENT_FIXTURE);
    expect(serialized).not.toContain("PASSWORD123");
    expect(serialized).not.toContain("sk-abcdef");
    expect(serialized).not.toContain("user@example.com");

    // Sanity: the surviving non-sensitive metric is still there.
    expect(result.event.payload?.duration_ms).toBe(100);
  });
});
