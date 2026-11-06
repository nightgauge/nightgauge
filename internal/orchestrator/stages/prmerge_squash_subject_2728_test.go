package stages

import (
	"strings"
	"testing"
)

// A PR whose sole commit is a WIP-preservation commit is squash-merged with
// the PR title as --subject and a body without the trailer (#2728).
func TestParseSquashView_SoleWIPCommit_UsesPRTitle(t *testing.T) {
	out := []byte(`{"number":2716,"title":"fix: real change","commits":[{"messageHeadline":"wip(feature-dev): preserve work in progress","messageBody":"Refs: #2716\n\nNightgauge-WIP: feature-dev"}]}`)
	h, b, err := parseSquashView(out)
	if err != nil {
		t.Fatal(err)
	}
	if h != "fix: real change (#2716)" {
		t.Errorf("subject = %q, want the PR title", h)
	}
	if strings.Contains(b, "Nightgauge-WIP") || strings.Contains(b, "wip(") {
		t.Errorf("body = %q, want no WIP commit content", b)
	}
}

func TestParseSquashView_Malformed(t *testing.T) {
	if _, _, err := parseSquashView([]byte("not json")); err == nil {
		t.Error("want a parse error")
	}
}
