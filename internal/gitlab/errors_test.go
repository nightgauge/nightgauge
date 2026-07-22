package gitlab

import (
	"errors"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

func TestMapStatus_SentinelMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"401 → ErrUnauthorized", 401, forge.ErrUnauthorized},
		{"403 → ErrPermissionDenied", 403, forge.ErrPermissionDenied},
		{"404 → ErrNotFound", 404, forge.ErrNotFound},
		{"429 → ErrRateLimited", 429, forge.ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapStatus("op", tc.status, "")
			if err == nil {
				t.Fatalf("mapStatus(%d): expected error", tc.status)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("mapStatus(%d): want errors.Is(%v), got %v", tc.status, tc.want, err)
			}
		})
	}
}

func TestMapStatus_OtherErrorIncludesSnippet(t *testing.T) {
	err := mapStatus("get issue", 500, `{"message":"server exploded"}`)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if msg := err.Error(); !contains(msg, "HTTP 500") || !contains(msg, "server exploded") {
		t.Errorf("missing detail in %q", msg)
	}
}

func TestMapStatus_2xxReturnsNil(t *testing.T) {
	for _, s := range []int{200, 201, 204, 299} {
		if err := mapStatus("op", s, ""); err != nil {
			t.Errorf("mapStatus(%d): expected nil, got %v", s, err)
		}
	}
}

func TestMapStatus_LongSnippetTruncated(t *testing.T) {
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	err := mapStatus("op", 500, string(long))
	if err == nil {
		t.Fatal("expected error")
	}
	// Should include ellipsis indicating truncation.
	if !contains(err.Error(), "…") {
		t.Errorf("expected truncation marker in %q", err.Error())
	}
}

func TestAsEditionError_WrapsUnsupportedOnEdition(t *testing.T) {
	cause := errors.New("HTTP 400: approvals_before_merge is unavailable on CE")
	err := asEditionError("update MR", "approvals_before_merge", cause)
	if !errors.Is(err, forge.ErrUnsupportedOnEdition) {
		t.Errorf("expected ErrUnsupportedOnEdition, got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
