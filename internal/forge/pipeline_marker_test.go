package forge

import "testing"

func TestPipelineMarker_Format(t *testing.T) {
	got := PipelineMarker("pr-create", 1479, "run-abc")
	want := "<!-- nightgauge:pipeline stage=pr-create issue=1479 run=run-abc -->"
	if got != want {
		t.Fatalf("PipelineMarker = %q, want %q", got, want)
	}
	if !HasPipelineMarker("body\n\n" + got + "\n") {
		t.Fatal("HasPipelineMarker did not detect the rendered marker")
	}
	if HasPipelineMarker("## Summary\n\nCloses #1\n") {
		t.Fatal("HasPipelineMarker matched an unstamped body")
	}
}

func TestPipelineMarker_SanitizesValues(t *testing.T) {
	got := PipelineMarker("", 7, " a b--c ")
	want := "<!-- nightgauge:pipeline stage=- issue=7 run=a_b_c -->"
	if got != want {
		t.Fatalf("PipelineMarker = %q, want %q", got, want)
	}
}
