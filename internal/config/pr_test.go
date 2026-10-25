package config

import (
	"testing"
	"time"
)

func TestPRConfigResolveAutoFixMaxAttempts(t *testing.T) {
	t.Setenv("NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS", "")
	var nilCfg *PRConfig
	if got := nilCfg.ResolveAutoFixMaxAttempts(); got != DefaultAutoFixMaxAttempts {
		t.Fatalf("nil: got %d", got)
	}
	cfg := &PRConfig{AutoFixMaxAttempts: 4}
	if got := cfg.ResolveAutoFixMaxAttempts(); got != 4 {
		t.Fatalf("config: got %d", got)
	}
	t.Setenv("NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS", "1")
	if got := cfg.ResolveAutoFixMaxAttempts(); got != 1 {
		t.Fatalf("env: got %d", got)
	}
	t.Setenv("NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS", "nope")
	if got := cfg.ResolveAutoFixMaxAttempts(); got != 4 {
		t.Fatalf("bad env: got %d", got)
	}
}

func TestPRConfigResolveCICheckTimeout(t *testing.T) {
	t.Setenv("NIGHTGAUGE_PR_CI_CHECK_TIMEOUT", "")
	var nilCfg *PRConfig
	if got := nilCfg.ResolveCICheckTimeout(); got != 0 {
		t.Fatalf("nil: got %v", got)
	}
	cfg := &PRConfig{CICheckTimeout: 600}
	if got := cfg.ResolveCICheckTimeout(); got != 10*time.Minute {
		t.Fatalf("config: got %v", got)
	}
	t.Setenv("NIGHTGAUGE_PR_CI_CHECK_TIMEOUT", "120")
	if got := cfg.ResolveCICheckTimeout(); got != 2*time.Minute {
		t.Fatalf("env: got %v", got)
	}
}
