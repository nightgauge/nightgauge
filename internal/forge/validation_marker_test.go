package forge

import "testing"

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
