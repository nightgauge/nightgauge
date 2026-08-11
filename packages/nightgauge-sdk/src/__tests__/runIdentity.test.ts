/**
 * runIdentity tests (#424) — the TypeScript half of ADR-017 Decision 1.
 *
 * `isRunIdentity` is the gate every TypeScript caller passes a run identity
 * through before it becomes a service field, a wire value, or (Go-side) a
 * `runtime-{issue}-{runId}.json` filename component. Until now it had NO direct
 * behavioral coverage on this side: the cross-language pin
 * (internal/runstate/identity_crosslang_test.go) compares the pattern's SOURCE
 * TEXT to Go's constant, and `PipelineStateService.identityIsNotAmbient` proves
 * the production wiring calls it — but nothing asserted what the function
 * actually accepts and refuses. A mutation like `.test(value.trim())` left both
 * of those green.
 *
 * This file is that refusal table, mirroring
 * `internal/state/run_identity_test.go`'s non-canonical cases case-for-case, so
 * the two languages are shown refusing the same set rather than assumed to.
 * One arm per case: a table that fails on its first row hides the rest.
 */
import { describe, it, expect } from "vitest";
import { RUN_IDENTITY_PATTERN, RUN_IDENTITY_SHAPE, isRunIdentity } from "../context/runIdentity.js";
import { uuidV7 } from "../context/RunStateManager.js";

/** A hand-built canonical identity: version nibble 7, variant nibble 8. */
const CANONICAL = "019fe6f3-fcfe-7b6f-8a7c-be0f444b6610";

describe("isRunIdentity — accepts", () => {
  it("a canonical lowercase UUIDv7", () => {
    expect(isRunIdentity(CANONICAL)).toBe(true);
  });

  // The minter and the validator must agree, or the pipeline mints ids its own
  // gate refuses. This is the one arm that exercises the REAL producer.
  it("what uuidV7 actually mints", () => {
    for (let i = 0; i < 32; i++) {
      const id = uuidV7();
      expect(isRunIdentity(id), `uuidV7 minted a refused identity: ${id}`).toBe(true);
    }
  });

  // Variant nibble is [89ab] — all four, not just the 8 the hand-built fixture
  // happens to carry.
  it("every legal variant nibble", () => {
    for (const variant of ["8", "9", "a", "b"]) {
      const id = `019fe6f3-fcfe-7b6f-${variant}a7c-be0f444b6610`;
      expect(isRunIdentity(id), id).toBe(true);
    }
  });
});

describe("isRunIdentity — refuses", () => {
  it("a UUIDv4 — right length, wrong version nibble", () => {
    expect(isRunIdentity("3f2504e0-4f89-41d3-9a0c-0305e82c3301")).toBe(false);
  });

  it("a ULID with a prefix", () => {
    expect(isRunIdentity("run_01H8XGJWBWBAQ4ZZY1N1V9PJ0M")).toBe(false);
  });

  // The identity is canonical LOWERCASE. An `i` flag on the pattern would make
  // this pass here and still fail Go's `IdentityRegexp` — drift with an
  // identical-looking body, which is why the pin checks flags too.
  it("the canonical shape in UPPERCASE", () => {
    expect(isRunIdentity("019FE6F3-FCFE-7B6F-8A7C-BE0F444B6610")).toBe(false);
  });

  it("a canonical id with a trailing space", () => {
    expect(isRunIdentity(`${CANONICAL} `)).toBe(false);
  });

  it("a canonical id with a leading space", () => {
    expect(isRunIdentity(` ${CANONICAL}`)).toBe(false);
  });

  it("a canonical id with a trailing newline", () => {
    expect(isRunIdentity(`${CANONICAL}\n`)).toBe(false);
  });

  // Two canonical-shaped lines in one string. Without the `m` flag `^…$` bracket
  // the WHOLE string and this is refused; with `m` they bracket each line and
  // both halves match, so a multiline-flag mutation is caught HERE even though
  // every single-line case above still behaves.
  it("two canonical ids separated by an embedded newline", () => {
    expect(isRunIdentity(`aaaaaaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee\n${CANONICAL}`)).toBe(false);
  });

  it("a wrong variant nibble (c)", () => {
    expect(isRunIdentity("019fe6f3-fcfe-7b6f-ca7c-be0f444b6610")).toBe(false);
  });

  it("the empty string", () => {
    expect(isRunIdentity("")).toBe(false);
  });

  // The security case: the identity becomes a filename component on a socket
  // ADR-015 documents as unauthenticated, so a "/" or ".." bearing value is an
  // arbitrary-path write. Twin of Go's traversal refusal.
  it("a path traversal", () => {
    expect(isRunIdentity("../../etc/passwd")).toBe(false);
  });
});

// The `typeof value === "string"` half of the guard: the parameter is `unknown`
// because these values arrive from JSON and untyped call sites, and
// `RegExp.test` COERCES — `test(null)` reads "null", `test(42)` reads "42" —
// so dropping the typeof check would not throw, it would silently answer about
// a stringified non-string.
describe("isRunIdentity — refuses non-strings", () => {
  it("null", () => {
    expect(isRunIdentity(null)).toBe(false);
  });

  it("undefined", () => {
    expect(isRunIdentity(undefined)).toBe(false);
  });

  it("a number", () => {
    expect(isRunIdentity(42)).toBe(false);
  });

  it("an object", () => {
    expect(isRunIdentity({})).toBe(false);
  });
});

// RUN_IDENTITY_SHAPE is DERIVED from RUN_IDENTITY_PATTERN so the two can never
// disagree. These arms pin the derivation itself, plus the two properties the
// Go cross-language pin reads out of this module's source text.
describe("RUN_IDENTITY_SHAPE derivation", () => {
  it("the pattern carries no flags", () => {
    expect(RUN_IDENTITY_PATTERN.flags).toBe("");
  });

  it("the pattern is the shape, anchored", () => {
    expect(RUN_IDENTITY_PATTERN.source).toBe(`^${RUN_IDENTITY_SHAPE}$`);
  });

  it("the shape itself is unanchored", () => {
    expect(RUN_IDENTITY_SHAPE.startsWith("^")).toBe(false);
    expect(RUN_IDENTITY_SHAPE.endsWith("$")).toBe(false);
  });

  // The fragment's whole purpose: embedding in a larger pattern. It contributes
  // NO capture groups, so the surrounding pattern's group numbering is
  // untouched — `runtime-(\d+)` stays capture 1, which every caller of
  // ANY_RUNTIME_FILE (extension-side) depends on for the issue number.
  it("embeds in a filename pattern without displacing capture groups", () => {
    const anyRuntimeFile = new RegExp(`^runtime-(\\d+)(?:-${RUN_IDENTITY_SHAPE})?\\.json$`);
    expect(anyRuntimeFile.exec(`runtime-370-${CANONICAL}.json`)?.[1]).toBe("370");
    expect(anyRuntimeFile.exec("runtime-370.json")?.[1]).toBe("370");
    expect(anyRuntimeFile.exec("runtime-370-not-an-identity.json")).toBeNull();
  });
});
