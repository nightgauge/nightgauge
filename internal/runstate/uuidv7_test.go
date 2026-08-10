package runstate

import (
	"strings"
	"testing"
	"time"
)

// UUIDv7Millis exists for exactly one rule — ADR-017 Decision 9's claim-token
// age — and its failure mode is a confident wrong answer, so its round trip
// against the production minter is pinned rather than assumed.
func TestUUIDv7Millis_RoundTripsTheMinter(t *testing.T) {
	before := time.Now().UnixMilli()
	id, err := NewRunID()
	if err != nil {
		t.Fatalf("NewRunID: %v", err)
	}
	after := time.Now().UnixMilli()

	ms, err := UUIDv7Millis(id)
	if err != nil {
		t.Fatalf("UUIDv7Millis(%q): %v", id, err)
	}
	if ms < before || ms > after {
		t.Fatalf("decoded %d, want within [%d, %d] — the 48-bit prefix is not being read big-endian", ms, before, after)
	}
}

func TestUUIDv7Millis_DecodesAKnownPrefix(t *testing.T) {
	// 0x018f_0000_0000 ms, with the version and variant nibbles of a real id.
	const id = "018f0000-0000-7abc-8def-0123456789ab"
	ms, err := UUIDv7Millis(id)
	if err != nil {
		t.Fatalf("UUIDv7Millis: %v", err)
	}
	if want := int64(0x018f00000000); ms != want {
		t.Fatalf("decoded %d, want %d", ms, want)
	}
}

func TestUUIDv7Millis_RefusesAnythingThatIsNotAV7Identity(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"not a uuid", "not-a-uuid"},
		// Version nibble 4: a v4 UUID's first six bytes are RANDOM, so reading
		// them as a time is the confident wrong answer the release rule must
		// never produce.
		{"uuid v4", "018f0000-0000-4abc-8def-0123456789ab"},
		{"bad variant", "018f0000-0000-7abc-cdef-0123456789ab"},
		{"uppercase", "018F0000-0000-7ABC-8DEF-0123456789AB"},
		{"truncated", "018f0000-0000-7abc-8def-0123456789a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := UUIDv7Millis(tc.id); err == nil {
				t.Fatalf("UUIDv7Millis(%q) accepted a non-v7 value", tc.id)
			}
		})
	}
}

// The claim artifact's name and the reader that releases it are built from one
// regex family, so a composed name must round-trip through the parser.
func TestResumingArtifactName_RoundTripsTheParser(t *testing.T) {
	runID, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	token, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}

	name := ResumingArtifactName(742, runID, token)
	if !strings.HasPrefix(name, "resuming-742-") || !strings.HasSuffix(name, ".json") {
		t.Fatalf("composed %q", name)
	}
	issue, gotRun, gotToken, ok := ParseResumingArtifactName(name)
	if !ok {
		t.Fatalf("the parser rejected the composer's own output: %q", name)
	}
	if issue != "742" || gotRun != runID || gotToken != token {
		t.Fatalf("round trip = (%q, %q, %q), want (742, %q, %q)", issue, gotRun, gotToken, runID, token)
	}
}
