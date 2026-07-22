package main

import "testing"

func TestEffectiveVersionPrefersLinkerValue(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = "0.2.0"
	if got := effectiveVersion(); got != "0.2.0" {
		t.Fatalf("effectiveVersion() = %q, want %q", got, "0.2.0")
	}
}

func TestEffectiveVersionDevelopmentFallback(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	version = "dev"
	if got := effectiveVersion(); got != "dev" {
		t.Fatalf("effectiveVersion() = %q, want dev for a local test build", got)
	}
}
