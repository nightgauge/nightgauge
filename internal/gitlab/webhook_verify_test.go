package gitlab_test

import (
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gitlab"
)

func TestVerifyToken(t *testing.T) {
	cases := []struct {
		name      string
		presented string
		expected  string
		want      bool
	}{
		{"matching tokens", "secret123", "secret123", true},
		{"wrong token", "wrong", "secret123", false},
		{"empty presented", "", "secret123", false},
		{"empty expected", "secret123", "", false},
		{"both empty", "", "", false},
		{"different lengths", "short", "secret123", false},
		{"same length wrong", "XXXXXXXXX", "secret123", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gitlab.VerifyToken(tc.presented, tc.expected)
			if got != tc.want {
				t.Errorf("VerifyToken(%q, %q) = %v; want %v", tc.presented, tc.expected, got, tc.want)
			}
		})
	}
}

func TestIsStale(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name       string
		occurredAt time.Time
		maxAge     time.Duration
		want       bool
	}{
		{"fresh event", now.Add(-1 * time.Minute), 5 * time.Minute, false},
		{"exactly at boundary", now.Add(-5 * time.Minute), 5 * time.Minute, true},
		{"stale event", now.Add(-10 * time.Minute), 5 * time.Minute, true},
		{"future event within window", now.Add(1 * time.Minute), 5 * time.Minute, false},
		{"future event outside window", now.Add(10 * time.Minute), 5 * time.Minute, true},
		{"zero time", time.Time{}, 5 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gitlab.IsStale(tc.occurredAt, tc.maxAge)
			if got != tc.want {
				t.Errorf("IsStale(%v, %v) = %v; want %v", tc.occurredAt, tc.maxAge, got, tc.want)
			}
		})
	}
}
