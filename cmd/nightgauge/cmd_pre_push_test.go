package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

func TestResolvePrePushTimeout_Precedence(t *testing.T) {
	configured := t.TempDir()
	if err := os.MkdirAll(filepath.Join(configured, ".nightgauge"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := "owner: acme\nhooks:\n  pre_push:\n    timeout_seconds: 2400\n"
	if err := os.WriteFile(filepath.Join(configured, ".nightgauge", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()

	for _, tc := range []struct {
		name    string
		flagSet bool
		flag    int
		dir     string
		want    int
	}{
		{"flag beats config", true, 60, configured, 60},
		{"flag zero means no limit", true, 0, configured, 0},
		{"config beats default", false, 0, configured, 2400},
		{"default", false, 0, empty, config.DefaultPrePushTimeoutSeconds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolvePrePushTimeout(tc.flagSet, tc.flag, tc.dir); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPrePushJSONExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result hooks.PrePushResult
		want   int
	}{
		{"pass", hooks.PrePushResult{Decision: "allow"}, 0},
		{"block", hooks.PrePushResult{Decision: "block"}, 1},
		{"timeout", hooks.PrePushResult{Decision: "block", FailureKind: hooks.FailureKindTimeout}, exitPrePushTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(io.Discard, nil, prePushJSONExit(tc.result)); got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPrePushTimeoutExit(t *testing.T) {
	timedOut := prePushTimeoutExit(hooks.PrePushResult{Decision: "block", FailureKind: hooks.FailureKindTimeout})
	if got := exitCodeFor(io.Discard, nil, timedOut); got != exitPrePushTimeout {
		t.Errorf("timeout exit = %d, want %d", got, exitPrePushTimeout)
	}
	if err := prePushTimeoutExit(hooks.PrePushResult{Decision: "block"}); err != nil {
		t.Errorf("a plain block must keep its exit path, got %v", err)
	}
	var ve verdictExit
	if errors.As(timedOut, &ve) && ve.code == 1 {
		t.Error("timeout must not share a test failure's exit code")
	}
}
