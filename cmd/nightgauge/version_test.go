package main

import (
	"runtime/debug"
	"testing"
)

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

func TestDevVersionFromVCS(t *testing.T) {
	if got := devVersionFromVCS(nil); got != "dev" {
		t.Fatalf("no VCS info = %q, want dev", got)
	}
	got := devVersionFromVCS([]debug.BuildSetting{
		{Key: "vcs.revision", Value: "57f00c5e0123456789abcdef"},
		{Key: "vcs.modified", Value: "true"},
	})
	if got != "dev+57f00c5e0123-dirty" {
		t.Fatalf("devVersionFromVCS = %q", got)
	}
}
