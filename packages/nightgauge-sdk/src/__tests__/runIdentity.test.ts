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
import { RunAttemptSchema, RunStateSchema } from "../context/schemas/run-state.js";

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

  // All four legal variant nibbles, not just the one the hand-built fixture
  // happens to carry. The legal set is the authority's business — this file
  // deliberately does not restate the character class, because a comment
  // carrying the shape is the transcription-rot vector the repo-wide walk in
  // internal/runstate/identity_crosslang_test.go exists to catch.
  it("every legal variant nibble", () => {
    for (const variant of ["8", "9", "a", "b"]) {
      const id = `019fe6f3-fcfe-7b6f-${variant}a7c-be0f444b6610`;
      expect(isRunIdentity(id), id).toBe(true);
    }
  });
});

/**
 * THE REFUSAL TABLE — mirroring `internal/state/run_identity_test.go`'s
 * non-canonical cases case-for-case.
 *
 * Declared ONCE and driven through every TypeScript site that claims to gate a
 * run identity, so a site that loosens on its own goes red here rather than in
 * production (#468). It is deliberately a table of DATA, never of patterns: the
 * character sequence of the shape itself lives in exactly one place
 * (`context/runIdentity.ts`) and is pinned to Go — see the repo-wide walk in
 * `internal/runstate/identity_crosslang_test.go`.
 *
 * Each row is expanded into ONE arm per site: a table asserted in a single
 * `expect` loop fails on its first row and hides the rest.
 */
const NON_CANONICAL: ReadonlyArray<{ label: string; value: string; why: string }> = [
  {
    label: "a UUIDv4 — right length, wrong version nibble",
    value: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
    why:
      "zod's `.uuid()` accepts every RFC 9562 version, so this is the headline " +
      "TS-accepts / Go-refuses divergence (#468).",
  },
  {
    label: "a ULID with a prefix",
    value: "run_01H8XGJWBWBAQ4ZZY1N1V9PJ0M",
    why: "a competing id encoding — length and alphabet both wrong.",
  },
  {
    label: "the canonical shape in UPPERCASE",
    value: "019FE6F3-FCFE-7B6F-8A7C-BE0F444B6610",
    why:
      "the identity is canonical LOWERCASE. An `i` flag on the pattern would " +
      "make this pass here and still fail Go's `IdentityRegexp` — drift with an " +
      "identical-looking body, which is why the pin checks flags too. zod's " +
      "`.uuid()` is case-insensitive, which is the second half of #468.",
  },
  {
    label: "the canonical shape with ONE uppercased component",
    value: "019fe6f3-fcfe-7b6f-8a7c-BE0F444B6610",
    why:
      "full-uppercase is not the only case widening: a `[0-9a-fA-F]` class in " +
      "one component, or a `.toLowerCase()` on the last group only, leaves the " +
      "row above red-free while accepting this.",
  },
  {
    label: "a fullwidth-confusable canonical id",
    value: "０１９ｆｅ６ｆ３－ｆｃｆｅ－７ｂ６ｆ－８ａ７ｃ－ｂｅ０ｆ４４４ｂ６６１０",
    why:
      "every codepoint NFKC-folds to the canonical id, and Go's RE2 does no " +
      "normalization whatsoever — so accepting it is precisely the F16 " +
      "`run_id_invalid` divergence: the extension mints it, the server refuses " +
      "to key on it, every progress call for that run is discarded. It is also " +
      "the BODY the cross-language pin cannot see: the pin byte-compares the " +
      'pattern LITERAL, so a `.test(value.normalize("NFKC"))` mutation leaves ' +
      "the pin and every other row green.",
  },
  {
    label: "a canonical id with a trailing space",
    value: `${CANONICAL} `,
    why: "a `.test(value.trim())` mutation leaves the pin green and this red.",
  },
  {
    label: "a canonical id with a leading space",
    value: ` ${CANONICAL}`,
    why: "the leading half of the same trim mutation.",
  },
  {
    label: "a canonical id with a trailing newline",
    value: `${CANONICAL}\n`,
    why: "JS `$` matches before a final newline in some engines' dialects; RE2 does not.",
  },
  {
    label: "two canonical ids separated by an embedded newline",
    value: `aaaaaaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee\n${CANONICAL}`,
    why:
      "without the `m` flag `^…$` bracket the WHOLE string and this is refused; " +
      "with `m` they bracket each line and both halves match, so a " +
      "multiline-flag mutation is caught HERE even though every single-line row " +
      "still behaves.",
  },
  {
    label: "a wrong variant nibble (c)",
    value: "019fe6f3-fcfe-7b6f-ca7c-be0f444b6610",
    why: "the variant nibble is a closed set; `c` is outside it.",
  },
  { label: "the empty string", value: "", why: "the degenerate case." },
  {
    label: "a path traversal",
    value: "../../etc/passwd",
    why:
      "the security case: the identity becomes a filename component on a socket " +
      'ADR-015 documents as unauthenticated, so a "/" or ".." bearing value ' +
      "is an arbitrary-path write. Twin of Go's traversal refusal.",
  },
];

describe("isRunIdentity — refuses", () => {
  for (const { label, value, why } of NON_CANONICAL) {
    it(label, () => {
      expect(isRunIdentity(value), why).toBe(false);
    });
  }
});

/**
 * The SECOND gate on the same shape: the `run-state.json` READ-BACK schema
 * (#468).
 *
 * `isRunIdentity` guards values the extension is about to put on the wire. It
 * says nothing about values that arrive FROM DISK — and until #468 the on-disk
 * schema had its own, laxer opinion: `z.string().uuid()`, which accepts every
 * UUID version and uppercase hex, i.e. exactly the set Go's authority refuses
 * with `ErrNoRunIdentity`. An id only the TypeScript side accepted could be
 * read back out of `run-state.json` and handed to IPC params and snapshot
 * filename components the Go scanner will not parse — the F16 `run_id_invalid`
 * family, where every progress call for that run is silently discarded.
 *
 * Both sites are driven from the ONE table above, and each arm asserts BOTH of
 * them, so a future loosening of one site cannot pass while the other holds.
 */
describe("run-state.json schema — refuses the same set", () => {
  const attempt = (runId: string) => ({
    run_id: runId,
    attempt_number: 1,
    started_at: "2026-01-01T00:00:00.000Z",
  });

  const wholeState = (runId: string) => ({
    schema_version: "1.0",
    issue_number: 1,
    state: "running" as const,
    run_id: runId,
    attempt_number: 1,
    completed_stages: [],
    branch: "feat/acme-platform-widget",
    created_at: "2026-01-01T00:00:00.000Z",
    updated_at: "2026-01-01T00:00:00.000Z",
    attempts: [attempt(CANONICAL)],
  });

  for (const { label, value, why } of NON_CANONICAL) {
    it(`${label} — at RunAttemptSchema.run_id`, () => {
      expect(RunAttemptSchema.safeParse(attempt(value)).success, why).toBe(false);
    });

    it(`${label} — at RunStateSchema.run_id`, () => {
      expect(RunStateSchema.safeParse(wholeState(value)).success, why).toBe(false);
    });
  }

  it("accepts what the minter actually produces, at both sites", () => {
    for (let i = 0; i < 8; i++) {
      const id = uuidV7();
      expect(RunAttemptSchema.safeParse(attempt(id)).success, id).toBe(true);
      expect(RunStateSchema.safeParse(wholeState(id)).success, id).toBe(true);
    }
  });

  // The refusal must NAME the offending id, because the only human who ever
  // sees it is an operator whose resume just failed on a file they did not
  // write. A bare "invalid string" sends them reading the schema; the id sends
  // them to the file.
  it("names the offending id in the message", () => {
    const bad = "3f2504e0-4f89-41d3-9a0c-0305e82c3301";
    const result = RunAttemptSchema.safeParse(attempt(bad));
    expect(result.success).toBe(false);
    expect(result.success === false && JSON.stringify(result.error.issues)).toContain(bad);
  });
});

// The `typeof value === "string"` half of the guard: the parameter is `unknown`
// because these values arrive from JSON and untyped call sites, and
// `RegExp.test` COERCES — `test(null)` reads "null", `test(42)` reads "42" —
// so dropping the typeof check would not throw, it would silently answer about
// a stringified non-string.
//
// The four inert arms below DOCUMENT that intent but do not enforce it: null,
// undefined, 42 and {} stringify to "null"/"undefined"/"42"/"[object Object]",
// values the pattern refuses on their own, so all four stay green with
// `typeof value === "string" &&` DELETED. The three arms after them are the ones
// that flip false→true under that deletion — they are what makes the guard
// load-bearing, and the Go pin cannot see any of this because the literal is
// untouched.
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

  // `Array.prototype.toString` of a ONE-element array IS that element, so the
  // coercion `RegExp.test` performs hands the pattern a canonical id.
  it("a one-element array whose element is a canonical id", () => {
    expect(isRunIdentity([CANONICAL])).toBe(false);
  });

  // Any object can name itself a canonical id. This is the shape that arrives
  // from a JSON body an attacker controls plus a reviver, or from a Proxy.
  it("an object whose toString is a canonical id", () => {
    expect(isRunIdentity({ toString: () => CANONICAL })).toBe(false);
  });

  // `typeof new String(x) === "object"`. The declared return type is
  // `value is string`, so accepting this would make the type predicate a lie:
  // downstream code treats it as a primitive string and it is not one (it is a
  // Go map key and a filename component two hops later).
  it('a boxed String — typeof "object", and the predicate would be a lie', () => {
    expect(isRunIdentity(new String(CANONICAL))).toBe(false);
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

  // The fragment's whole purpose is embedding in a larger pattern, and the two
  // arms below are the EMBEDDING CONTRACT documented on the export.
  //
  // They replace an earlier arm that built `^runtime-(\d+)(?:-${SHAPE})?\.json$`
  // and asserted `exec(...)[1] === "370"`. That arm could not fail: `(\d+)`
  // opens before the interpolation, so group 1 is structurally immovable no
  // matter what the fragment does — it stayed green with a capture group added
  // to the fragment AND with a top-level alternation added to it.

  // (A) NO CAPTURE GROUPS. `exec().length` is 1 + the group count, so an
  // anchored match over the fragment alone reads the count directly: 1 means
  // zero groups. This is what lets `runtime-(\d+)` keep group 1 in
  // ANY_RUNTIME_FILE (extension-side) — and it transitively covers a future
  // backreference, which would need a group to point at.
  it("contributes no capture groups when embedded", () => {
    expect(new RegExp(`^${RUN_IDENTITY_SHAPE}$`).exec(CANONICAL)!.length).toBe(1);
  });

  // (B) SELF-CONTAINED: no top-level alternation. The surrounding literal text is
  // REQUIRED on both sides — which is only true while the fragment has no bare
  // `|` in it. Add one and `^PRE-A|B-POST$` re-associates into
  // `(^PRE-A)|(B-POST$)`: each anchor binds one branch, `PRE-<id>` alone starts
  // matching, and this arm goes red.
  //
  // The interpolation here is deliberately BARE, unlike the two production
  // embedders (which wrap, per the contract on the export). That is the point:
  // the wrap makes the embed sites safe REGARDLESS, so it would also hide the
  // change. This arm is the tripwire that fires on the day the fragment stops
  // being self-contained — the day the wrap starts doing real work.
  it("is self-contained — surrounding literals are required even interpolated bare", () => {
    const bare = new RegExp(`^PRE-${RUN_IDENTITY_SHAPE}-POST$`);
    expect(bare.test(`PRE-${CANONICAL}-POST`)).toBe(true);
    expect(bare.test(`PRE-${CANONICAL}`)).toBe(false);
    expect(bare.test(`${CANONICAL}-POST`)).toBe(false);
  });

  // And the fragment still refuses a non-identity in the position the two
  // production embedders put it in.
  it("refuses a non-identity in the filename position", () => {
    const anyRuntimeFile = new RegExp(`^runtime-(\\d+)(?:-(?:${RUN_IDENTITY_SHAPE}))?\\.json$`);
    expect(anyRuntimeFile.exec(`runtime-370-${CANONICAL}.json`)?.[1]).toBe("370");
    expect(anyRuntimeFile.exec("runtime-370.json")?.[1]).toBe("370");
    expect(anyRuntimeFile.exec("runtime-370-not-an-identity.json")).toBeNull();
  });
});
