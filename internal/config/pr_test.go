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

func TestResolvePRMergeCIWait(t *testing.T) {
	queue := func(d time.Duration) *Config {
		return &Config{Pipeline: &PipelineConfig{MergeQueue: &MergeQueueConfig{WaitTimeout: YAMLDuration(d)}}}
	}
	cases := []struct {
		name string
		cfg  *Config
		env  string
		want time.Duration
	}{
		{"nil config", nil, "", 100 * time.Minute},
		{"queue 30m", queue(30 * time.Minute), "", 40 * time.Minute},
		{"queue 5m hits floor", queue(5 * time.Minute), "", DefaultPRMergeCIWait},
		{"ci_check_timeout above", &Config{PR: &PRConfig{CICheckTimeout: 7200}}, "", 2 * time.Hour},
		{"env ci timeout", queue(5 * time.Minute), "3600", time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NIGHTGAUGE_PR_CI_CHECK_TIMEOUT", tc.env)
			if got := tc.cfg.ResolvePRMergeCIWait(); got != tc.want {
				t.Errorf("ResolvePRMergeCIWait() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolvePRMergeTimeout is #2835: the pr-merge CLI default covers one CI
// wait, the mergeability backoff and the queue wait, plus the margin.
func TestResolvePRMergeTimeout(t *testing.T) {
	queue := func(d time.Duration) *Config {
		return &Config{Pipeline: &PipelineConfig{MergeQueue: &MergeQueueConfig{WaitTimeout: YAMLDuration(d)}}}
	}
	backoff := 126 * time.Second
	cases := []struct {
		name string
		cfg  *Config
		env  string
		want time.Duration
	}{
		{"nil config", nil, "", 117*time.Minute + 6*time.Second},
		{"queue 30m", queue(30 * time.Minute), "", 15*time.Minute + backoff + 30*time.Minute + 10*time.Minute},
		{"queue 3h", queue(3 * time.Hour), "", 15*time.Minute + backoff + 3*time.Hour + 10*time.Minute},
		{"ci_check_timeout replaces CI term", &Config{PR: &PRConfig{CICheckTimeout: 1800}}, "", 30*time.Minute + backoff + 90*time.Minute + 10*time.Minute},
		{"env ci timeout", queue(5 * time.Minute), "60", time.Minute + backoff + 5*time.Minute + 10*time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NIGHTGAUGE_PR_CI_CHECK_TIMEOUT", tc.env)
			if got := tc.cfg.ResolvePRMergeTimeout(); got != tc.want {
				t.Errorf("ResolvePRMergeTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}
