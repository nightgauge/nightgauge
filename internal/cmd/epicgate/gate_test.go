package epicgate

import "testing"

func TestClassify_PathA_SubIssuesPresent(t *testing.T) {
	if got := Classify("Some epic body", 3); got != ShapeA {
		t.Errorf("expected path_a, got %q", got)
	}
}

func TestClassify_PathA_CountAloneIsSufficient(t *testing.T) {
	if got := Classify("No markers here", 1); got != ShapeA {
		t.Errorf("expected path_a, got %q", got)
	}
}

func TestClassify_PathA_TakesPrecedenceOverB(t *testing.T) {
	body := "<!-- nightgauge:decompose-later -->"
	if got := Classify(body, 2); got != ShapeA {
		t.Errorf("expected path_a (count takes precedence over marker), got %q", got)
	}
}

func TestClassify_PathB_MarkerComment(t *testing.T) {
	body := "Some epic description\n<!-- nightgauge:decompose-later -->"
	if got := Classify(body, 0); got != ShapeB {
		t.Errorf("expected path_b, got %q", got)
	}
}

func TestClassify_PathB_ProsePhrase(t *testing.T) {
	body := "This is a placeholder, decompose later once we have more context."
	if got := Classify(body, 0); got != ShapeB {
		t.Errorf("expected path_b, got %q", got)
	}
}

func TestClassify_PathC_MarkerComment(t *testing.T) {
	body := "Epic body\n<!-- nightgauge:standalone-epic -->"
	if got := Classify(body, 0); got != ShapeC {
		t.Errorf("expected path_c, got %q", got)
	}
}

func TestClassify_PathC_ProsePhrase(t *testing.T) {
	body := "This is a standalone epic that ships as one PR."
	if got := Classify(body, 0); got != ShapeC {
		t.Errorf("expected path_c, got %q", got)
	}
}

func TestClassify_PathC_IntentionallyNoSubIssues(t *testing.T) {
	body := "This epic is intentionally no sub-issues required."
	if got := Classify(body, 0); got != ShapeC {
		t.Errorf("expected path_c, got %q", got)
	}
}

func TestClassify_Rejected_NeitherMarkerNorCount(t *testing.T) {
	body := "Just an epic body with no markers and no sub-issues planned."
	if got := Classify(body, 0); got != ShapeNone {
		t.Errorf("expected ShapeNone (rejected), got %q", got)
	}
}
