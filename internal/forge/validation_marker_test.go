package forge

import (
	"strings"
	"testing"
)

func TestValidationMarker_Format(t *testing.T) {
	got := ValidationMarker("abc123")
	want := "<!-- nightgauge:validated sha=abc123 -->"
	if got != want {
		t.Fatalf("ValidationMarker = %q, want %q", got, want)
	}
	if got := ValidationMarker(""); got != "<!-- nightgauge:validated sha=- -->" {
		t.Fatalf("ValidationMarker(\"\") = %q", got)
	}
}

func TestValidationMarkerComment_CarriesMarker(t *testing.T) {
	body := ValidationMarkerComment("main", "abc123")
	if !HasValidationMarker(body) {
		t.Fatalf("comment %q does not carry the marker", body)
	}
	if HasValidationMarker("Post-merge: main is green") {
		t.Fatal("HasValidationMarker matched a body without the marker")
	}
	if HasValidationMarker(PipelineMarker("pr-create", 1, "r")) {
		t.Fatal("HasValidationMarker matched the pipeline marker")
	}
}

func TestHasValidationMarker_Legacy(t *testing.T) {
	if !HasValidationMarker("<!-- opencode-program:validated -->") {
		t.Fatal("legacy bypass marker not accepted")
	}
	if !HasValidationMarker("Validated.\n<!-- opencode-program:validated sha=abc -->") {
		t.Fatal("legacy marker with a payload not accepted")
	}
	for _, near := range []string{
		"opencode-program:validated",
		"<!-- opencode-program:validating -->",
		"<!-- nightgauge:validated-->",
	} {
		if HasValidationMarker(near) {
			t.Errorf("HasValidationMarker(%q) = true, want false", near)
		}
	}
}

func TestValidationMarker_NeverWritesLegacy(t *testing.T) {
	for _, out := range []string{ValidationMarker("abc"), ValidationMarkerComment("main", "abc")} {
		if strings.Contains(out, LegacyValidationMarker) {
			t.Errorf("writer emitted the legacy marker: %q", out)
		}
	}
}
