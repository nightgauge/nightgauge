/**
 * runIdentity — the ONE TypeScript definition of the run-identity shape.
 *
 * SOURCE OF TRUTH IS GO: `internal/runstate/identity.go` (`IdentityPattern`,
 * `IdentityRegexp`, `IsIdentity`) — ADR-017 Decision 1. TypeScript cannot
 * import Go, so this module is the second definition site, and there is
 * exactly one of it: every TypeScript consumer — the IPC notify params, the
 * snapshot-filename resolver, the runtime-stub sweep, the minter's own tests —
 * derives from here rather than transcribing the character sequence again. An
 * id one side accepts and the other refuses is a `run_id_invalid` refusal at
 * ADR-017 step 4: the extension mints an identity the server will not key on,
 * and every progress call for that run is silently discarded (F16's shape).
 *
 * THIS FILE IS PINNED FROM GO. `internal/runstate/identity_crosslang_test.go`
 * (`TestIdentityPatternPinnedToTypeScriptTwin`) reads THIS path, lifts the
 * `RUN_IDENTITY_PATTERN` declaration out of the source text, and requires it to
 * be byte-identical to the anchored Go constant. That makes the FORM of the
 * declaration below load-bearing, not merely stylistic:
 *
 *  - a regex LITERAL, never `new RegExp("…")` (a string form re-introduces the
 *    escaping questions the literal form does not have);
 *  - NO flags (`i` would accept the uppercase hex Go refuses, `m` would let a
 *    newline-bearing string satisfy `^…$`);
 *  - at column 0, as a top-level `export const`, exactly ONE declaration in
 *    the file — the extractor requires exactly one match and rejects
 *    commented-out copies and re-exports from other modules.
 *
 * If this declaration is renamed or moved to another module, MOVE THE PIN WITH
 * IT — `tsIdentitySourcePath` and the extractor in that test — do not delete
 * the pin and do not leave a stray column-0 copy behind. The pin's not-found
 * arm fails loudly rather than skipping, because a pin that quietly stops
 * checking hides exactly the drift it exists to catch.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decision 1
 * @see internal/runstate/identity.go — the authority this mirrors
 */

/**
 * Canonical run-identity shape — lowercase UUIDv7, ANCHORED. The single
 * TypeScript answer to "is this string a run identity?".
 *
 * Version nibble 7, variant nibble in `[89ab]`, lowercase hex only. Twin of
 * Go's `IdentityRegexp` (`"^" + IdentityPattern + "$"`).
 */
export const RUN_IDENTITY_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

/**
 * The UNANCHORED identity fragment, for embedding in larger patterns —
 * `runtime-{issue}-{runId}.json` and friends. Twin of Go's bare
 * `IdentityPattern`, which is unanchored for the same reason.
 *
 * DERIVED from {@link RUN_IDENTITY_PATTERN} by stripping its `^`/`$`, never
 * transcribed: a second hand-written copy of the character sequence is the
 * drift this module exists to remove. The fragment adds no capture groups, so
 * interpolating it never displaces a surrounding pattern's group numbers.
 */
export const RUN_IDENTITY_SHAPE = RUN_IDENTITY_PATTERN.source.slice(1, -1);

/**
 * True when `value` is a canonical lowercase UUIDv7 run identity.
 *
 * Twin of `runstate.IsIdentity` (internal/runstate/identity.go). Called BEFORE
 * a value is installed on a service or put on the wire: the value ends up as a
 * Go map key and a `runtime-{issue}-{runId}.json` filename component, so a
 * string containing "/" or ".." is an arbitrary-path write on a socket ADR-015
 * documents as unauthenticated.
 */
export function isRunIdentity(value: unknown): value is string {
  return typeof value === "string" && RUN_IDENTITY_PATTERN.test(value);
}
