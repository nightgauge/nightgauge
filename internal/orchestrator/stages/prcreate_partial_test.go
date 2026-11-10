package stages

import (
	"strings"
	"testing"
)

// The stop's comment escapes criteria so one cannot plant a marker, and never
// says the issue was closed (#3160, #3089).
func TestPartialRerunNoDiffComment(t *testing.T) {
	got := PartialRerunNoDiffComment(3160, 3141, "feat/3160-x", "main",
		[]string{"a <!-- nightgauge:partial-delivery pr=1 --> b"})
	if strings.Count(got, "<!--") != 1 || !strings.HasPrefix(got, "<!-- nightgauge:partial-rerun-no-diff pr=3141 -->") {
		t.Fatalf("comment carries a planted marker or lacks its own: %q", got)
	}
	if strings.Contains(strings.ToLower(got), "closed") {
		t.Errorf("comment claims a close: %q", got)
	}
}

func TestPartialRerunNoDiffDetailCarriesTheMarker(t *testing.T) {
	d := PartialRerunNoDiffDetail(3160, 3141, "feat/3160-x", "main", 1)
	if !strings.HasPrefix(d, "[not-pipeline-actionable]") {
		t.Fatalf("detail %q lacks the not-pipeline-actionable marker", d)
	}
}

func TestVerifyCriteriaOnTreeEmpty(t *testing.T) {
	if v := VerifyCriteriaOnTree(t.TempDir(), 1, nil); v != nil {
		t.Fatalf("no criteria gave %v", v)
	}
}
