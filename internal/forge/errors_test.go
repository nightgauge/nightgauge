package forge_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

func TestSentinels_AreDistinct(t *testing.T) {
	sentinels := []error{
		forge.ErrNotFound,
		forge.ErrRateLimited,
		forge.ErrPermissionDenied,
		forge.ErrUnauthorized,
		forge.ErrUnsupported,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("sentinels[%d] (%v) and sentinels[%d] (%v) compare equal under errors.Is — must be distinct", i, a, j, b)
			}
		}
	}
}

func TestSentinels_SurviveWrapWith_w(t *testing.T) {
	cases := []struct {
		name     string
		sentinel error
	}{
		{"not_found", forge.ErrNotFound},
		{"rate_limited", forge.ErrRateLimited},
		{"permission_denied", forge.ErrPermissionDenied},
		{"unauthorized", forge.ErrUnauthorized},
		{"unsupported", forge.ErrUnsupported},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wrapped := fmt.Errorf("adapter: %w", c.sentinel)
			doubleWrapped := fmt.Errorf("caller: %w", wrapped)
			if !errors.Is(wrapped, c.sentinel) {
				t.Errorf("errors.Is fails through single %%w wrap for %v", c.sentinel)
			}
			if !errors.Is(doubleWrapped, c.sentinel) {
				t.Errorf("errors.Is fails through double %%w wrap for %v", c.sentinel)
			}
		})
	}
}

func TestSentinels_HumanReadableMessage(t *testing.T) {
	if got := forge.ErrNotFound.Error(); got == "" {
		t.Error("ErrNotFound has empty message")
	}
	if got := forge.ErrUnsupported.Error(); got == "" {
		t.Error("ErrUnsupported has empty message")
	}
}
