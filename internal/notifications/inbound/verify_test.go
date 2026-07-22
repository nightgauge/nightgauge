package inbound

import (
	"testing"
	"time"
)

func TestVerifyToken(t *testing.T) {
	cases := []struct {
		name      string
		presented []byte
		expected  []byte
		want      bool
	}{
		{"equal tokens", []byte("super-secret"), []byte("super-secret"), true},
		{"single byte difference", []byte("super-secret"), []byte("super-sxcret"), false},
		{"length mismatch shorter presented", []byte("short"), []byte("longer-token"), false},
		{"length mismatch longer presented", []byte("longer-token"), []byte("short"), false},
		{"both empty rejected", []byte(""), []byte(""), false},
		{"empty presented vs nonempty expected", []byte(""), []byte("secret"), false},
		{"nonempty presented vs empty expected", []byte("secret"), []byte(""), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyToken(tc.presented, tc.expected)
			if got != tc.want {
				t.Fatalf("verifyToken(%q, %q) = %v, want %v",
					tc.presented, tc.expected, got, tc.want)
			}
		})
	}
}

func TestParseTriggerTimestamp(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	ms := now.UnixMilli()

	cases := []struct {
		name      string
		triggerID string
		wantTime  time.Time
		wantErr   bool
	}{
		{
			name:      "valid",
			triggerID: "req-abc.1234567890123",
			wantTime:  time.UnixMilli(1234567890123),
		},
		{
			name:      "valid with current time",
			triggerID: "req-xyz." + formatInt(ms),
			wantTime:  now,
		},
		{
			name:      "valid with multiple dots — uses last",
			triggerID: "req.with.dots.987654321000",
			wantTime:  time.UnixMilli(987654321000),
		},
		{name: "empty", triggerID: "", wantErr: true},
		{name: "no dot", triggerID: "noDotHere", wantErr: true},
		{name: "trailing dot", triggerID: "req.", wantErr: true},
		{name: "non-numeric suffix", triggerID: "req.abc123", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseTriggerTimestamp(tc.triggerID)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (time=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(tc.wantTime) {
				t.Fatalf("got %v, want %v", got, tc.wantTime)
			}
		})
	}
}

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		t      time.Time
		maxAge time.Duration
		want   bool
	}{
		{"zero time", time.Time{}, 5 * time.Minute, true},
		{"current time", now, 5 * time.Minute, false},
		{"1m old", now.Add(-1 * time.Minute), 5 * time.Minute, false},
		{"5m old (boundary)", now.Add(-5 * time.Minute), 5 * time.Minute, false},
		{"5m1s old", now.Add(-5*time.Minute - time.Second), 5 * time.Minute, true},
		{"1h old", now.Add(-1 * time.Hour), 5 * time.Minute, true},
		{"future 1m", now.Add(1 * time.Minute), 5 * time.Minute, false},
		{"future 1h", now.Add(1 * time.Hour), 5 * time.Minute, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStale(tc.t, now, tc.maxAge); got != tc.want {
				t.Fatalf("isStale(%v, %v, %v) = %v, want %v", tc.t, now, tc.maxAge, got, tc.want)
			}
		})
	}
}

// formatInt is a tiny helper to avoid pulling in strconv at import time.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
