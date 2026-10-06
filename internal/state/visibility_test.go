package state

import (
	"encoding/json"
	"testing"
)

func TestParseVisibility(t *testing.T) {
	for in, want := range map[string]string{"": "", "team": "", "private": VisibilityPrivate} {
		got, err := ParseVisibility(in)
		if err != nil || got != want {
			t.Errorf("ParseVisibility(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	for _, in := range []string{"public", "Private", " private"} {
		if _, err := ParseVisibility(in); err == nil {
			t.Errorf("ParseVisibility(%q) accepted a value that is neither team nor private", in)
		}
	}
}

func TestRuntimeVisibilityIsRaisedNeverLowered(t *testing.T) {
	rs := NewRuntimeState("o/r", 1, "", "01900309-0000-7000-8000-000000000001")
	if got := rs.RunVisibility(); got != "" {
		t.Fatalf("new run visibility = %q, want team (empty)", got)
	}
	rs.SetVisibility(VisibilityTeam)
	rs.SetVisibility("public")
	if got := rs.RunVisibility(); got != "" {
		t.Errorf("visibility after non-private values = %q, want team (empty)", got)
	}
	rs.SetVisibility(VisibilityPrivate)
	rs.SetVisibility(VisibilityTeam)
	rs.SetVisibility("")
	if got := rs.RunVisibility(); got != VisibilityPrivate {
		t.Errorf("visibility after a lowering attempt = %q, want private", got)
	}
	// The snapshot carries it, so a run rehydrated after a restart keeps it.
	snap := rs.Snapshot()
	if snap.Visibility != VisibilityPrivate {
		t.Errorf("snapshot visibility = %q, want private", snap.Visibility)
	}
	b, _ := json.Marshal(snap)
	var back RuntimeState
	if err := json.Unmarshal(b, &back); err != nil || back.Visibility != VisibilityPrivate {
		t.Errorf("persisted snapshot visibility = %q (%v), want private", back.Visibility, err)
	}
	var nilRS *RuntimeState
	if got := nilRS.RunVisibility(); got != "" {
		t.Errorf("nil runtime visibility = %q, want team (empty)", got)
	}
}
