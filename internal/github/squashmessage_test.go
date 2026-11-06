package github

import (
	"strings"
	"testing"
)

func TestSquashCommitMessage(t *testing.T) {
	wip := "wip(feature-dev): preserve work in progress\n\nRefs: #7\n\nNightgauge-WIP: feature-dev"
	cases := []struct {
		name     string
		msgs     []string
		wantBody string
	}{
		{"sole WIP commit", []string{wip}, ""},
		{"single real commit", []string{"fix: a\n\nwhy a"}, "why a"},
		{"WIP dropped among real", []string{"fix: a\n\nwhy a", wip, "test: b"}, "* fix: a\n\nwhy a\n\n* test: b"},
		{"no commits", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, b := SquashCommitMessage("Fix the thing", 7, tc.msgs)
			if h != "Fix the thing (#7)" {
				t.Errorf("headline = %q", h)
			}
			if b != tc.wantBody {
				t.Errorf("body = %q, want %q", b, tc.wantBody)
			}
			if strings.Contains(b, WIPCommitTrailer) {
				t.Errorf("body carries the WIP trailer: %q", b)
			}
		})
	}
}

func TestIsWIPPreservationMessage(t *testing.T) {
	if !IsWIPPreservationMessage("wip: x\n\nNightgauge-WIP: feature-dev") {
		t.Error("WIP commit not detected")
	}
	if IsWIPPreservationMessage("fix: mention Nightgauge-WIP trailer handling") {
		t.Error("headline mention misread as trailer")
	}
}
