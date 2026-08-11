package runstate

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Cross-language pin for the run-identity shape — ADR-017 Decision 1, #422.
//
// identity.go says there is exactly ONE definition of the identity shape "in
// the Go tree". TypeScript cannot import Go, so there is a second one:
// RUN_IDENTITY_PATTERN in packages/nightgauge-sdk/src/context/runIdentity.ts —
// and since #424 there is exactly one of it on that side too, next to uuidV7,
// the minter that produces the shape (the extension's IPC params, snapshot
// resolver and stub sweep all derive from it rather than transcribing it).
// The two sides are character-identical by hand and nothing but this test
// enforces it. ADR-017 step 4 turns `run_id_invalid` into a hard wire error, at
// which point drift is not cosmetic: the extension mints an identity the server
// refuses, and every progress call for that run is silently thrown away (F16's
// shape).
//
// This test is the enforcement. It reads the TypeScript file, extracts the
// regex literal, and requires it to be byte-identical to the anchored Go
// constant. It never skips: a TS file that cannot be read, or a const that has
// been renamed or rewritten as `new RegExp("…")`, is a FAILURE, because a pin
// that quietly stops checking is worse than no pin.

// tsIdentitySourcePath is the TypeScript-side twin, relative to this package
// directory (go test runs with cwd = the package dir). It is the SDK module
// (#424), not the extension: the extension depends on the SDK, never the
// reverse, so the one definition belongs at the bottom of that dependency edge
// where every TypeScript consumer can reach it.
var tsIdentitySourcePath = filepath.Join(
	"..", "..", "packages", "nightgauge-sdk", "src", "context", "runIdentity.ts")

// tsIdentityLiteralRegexp lifts `export const RUN_IDENTITY_PATTERN = /…/flags`
// out of the TypeScript source. The whitespace around `=` is elastic because
// prettier reflows that assignment whenever the line length changes. The body
// group accepts any run of non-slash, non-newline characters plus backslash
// escapes, so the first UNescaped `/` closes the literal exactly as the
// TypeScript lexer would close it. The `(?m)^` anchor requires the declaration
// at column 0 — a module-scope `export const` always is — so a `//`- or
// ` * `-prefixed COPY of the declaration in a comment can never satisfy the
// pin: a "moved to another module, left the old line commented out" refactor
// must land in the zero-match failure below, not silently pass against the
// comment while the real pattern drifts elsewhere.
//
// Captures: 1 = the regex body (between the slashes), 2 = the flag letters.
var tsIdentityLiteralRegexp = regexp.MustCompile(
	`(?m)^export const RUN_IDENTITY_PATTERN\s*=\s*/((?:[^/\\\n]|\\.)*)/([a-zA-Z]*)`)

// realignHint is appended to every failure: the fixer needs to know that the
// two sites are peers and that the deadline for drift is step 4.
const realignHint = "re-align the two definition sites: internal/runstate/identity.go " +
	"(IdentityPattern / IdentityRegexp) and " +
	"packages/nightgauge-sdk/src/context/runIdentity.ts (RUN_IDENTITY_PATTERN). " +
	"They must stay character-identical: after ADR-017 step 4 makes run_id_invalid a hard " +
	"wire error, any drift means the extension mints run identities the server refuses at " +
	"the IPC boundary."

func TestIdentityPatternPinnedToTypeScriptTwin(t *testing.T) {
	// want is what BOTH anchored validators must be. Derived from the Go
	// constant so this test cannot drift from identity.go on its own.
	want := "^" + IdentityPattern + "$"

	// The Go-side validator is re-derived here rather than string-compared to
	// a literal: if someone rebuilds IdentityRegexp from a different
	// expression, the TS comparison below would still pass against the
	// constant while the actual wire validator had moved.
	if got := IdentityRegexp.String(); got != want {
		t.Errorf("IdentityRegexp is not IdentityPattern anchored:\n"+
			"  IdentityRegexp.String() = %q\n"+
			"  \"^\"+IdentityPattern+\"$\" = %q\n"+
			"IdentityRegexp is the single validator for \"is this a run identity?\"; "+
			"deriving it from anything but IdentityPattern unpins the whole chain. %s",
			got, want, realignHint)
	}

	source, err := os.ReadFile(tsIdentitySourcePath)
	if err != nil {
		t.Fatalf("cannot read the TypeScript twin at %s: %v\n"+
			"This pin is path-coupled: if runIdentity.ts moved or was renamed, move "+
			"tsIdentitySourcePath in this test with it — do NOT delete the pin. %s",
			tsIdentitySourcePath, err, realignHint)
	}

	matches := tsIdentityLiteralRegexp.FindAllStringSubmatch(string(source), -1)
	switch len(matches) {
	case 1:
		// The pinned shape. Fall through to the comparisons below.
	case 0:
		t.Fatalf("no `export const RUN_IDENTITY_PATTERN = /…/` regex literal found in %s.\n"+
			"The const was renamed, deleted, or rewritten in a form this pin cannot read "+
			"(e.g. `new RegExp(\"…\")` — a string form re-introduces escaping questions the "+
			"literal form does not have, so keep the literal). The literal must be a "+
			"top-level declaration in THIS file — a commented-out copy or a re-export from "+
			"another module does not count; if the definition moved, move this pin's path "+
			"and extractor with it. A missing definition is a FAILURE, never a skip: a pin "+
			"that stops checking hides exactly the drift it exists to catch. %s",
			tsIdentitySourcePath, realignHint)
	default:
		t.Fatalf("found %d `export const RUN_IDENTITY_PATTERN = /…/` literals in %s; "+
			"expected exactly 1.\n"+
			"Ambiguous: this pin cannot tell which one the extension actually uses. There "+
			"must be a single identity definition per side. %s",
			len(matches), tsIdentitySourcePath, realignHint)
	}

	body, flags := matches[0][1], matches[0][2]

	// Flags are checked before the body: `i` would accept the uppercase hex
	// the Go side rejects, and `m` would let a newline-bearing string satisfy
	// ^…$ — both are silent semantic drift with an identical-looking body.
	if flags != "" {
		t.Fatalf("RUN_IDENTITY_PATTERN carries regex flags %q; it must carry none.\n"+
			"Go's regexp has no flag suffix, so any flag here is a semantic difference the "+
			"body comparison cannot see: `i` accepts uppercase hex the Go side rejects, "+
			"`m` lets ^ and $ match around an embedded newline. %s",
			flags, realignHint)
	}

	// Byte equality, not semantic equivalence: the two sides are maintained by
	// copy, so "equivalent but differently written" is the state this pin is
	// meant to prevent. This also covers anchoring — want carries ^ and $.
	if body != want {
		t.Errorf("run-identity shape has DRIFTED between Go and TypeScript:\n"+
			"  TypeScript RUN_IDENTITY_PATTERN = /%s/\n"+
			"  Go \"^\"+IdentityPattern+\"$\"    = %s\n"+
			"(%s vs internal/runstate/identity.go)\n%s",
			body, want, tsIdentitySourcePath, realignHint)
	}
}
