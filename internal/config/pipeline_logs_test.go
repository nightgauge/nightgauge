package config

import "testing"

// TestPipelineConfig_ResolveHistoryRetentionDays pins the default-application
// rules for pipeline.logs.history_retention_days (#674): a nil receiver, a
// nil Logs block, and a non-positive value all fall back to
// DefaultHistoryRetentionDays; a positive configured value passes through
// unchanged.
func TestPipelineConfig_ResolveHistoryRetentionDays(t *testing.T) {
	tests := []struct {
		name string
		p    *PipelineConfig
		want int
	}{
		{"nil receiver", nil, DefaultHistoryRetentionDays},
		{"nil logs block", &PipelineConfig{}, DefaultHistoryRetentionDays},
		{"zero value", &PipelineConfig{Logs: &PipelineLogsConfig{HistoryRetentionDays: 0}}, DefaultHistoryRetentionDays},
		{"negative value", &PipelineConfig{Logs: &PipelineLogsConfig{HistoryRetentionDays: -5}}, DefaultHistoryRetentionDays},
		{"configured value", &PipelineConfig{Logs: &PipelineLogsConfig{HistoryRetentionDays: 30}}, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.ResolveHistoryRetentionDays(); got != tt.want {
				t.Errorf("ResolveHistoryRetentionDays() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestLoadMergedHistoryRetentionDays pins that pipeline.logs.history_retention_days
// actually reaches the typed Config through the tier merge — not just that the
// resolver applies defaults correctly in isolation (#674).
func TestLoadMergedHistoryRetentionDays(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
pipeline:
  logs:
    history_retention_days: 45
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if cfg.Pipeline == nil || cfg.Pipeline.Logs == nil {
		t.Fatalf("expected pipeline.logs to be populated, got Pipeline=%+v", cfg.Pipeline)
	}
	if cfg.Pipeline.Logs.HistoryRetentionDays != 45 {
		t.Errorf("HistoryRetentionDays = %d, want 45", cfg.Pipeline.Logs.HistoryRetentionDays)
	}
	if got := cfg.Pipeline.ResolveHistoryRetentionDays(); got != 45 {
		t.Errorf("ResolveHistoryRetentionDays() = %d, want 45", got)
	}
}

// TestLoadMergedHistoryRetentionDaysUnset confirms an absent pipeline.logs
// block resolves to the default rather than zero-value crashing or defaulting
// to something else silently.
func TestLoadMergedHistoryRetentionDaysUnset(t *testing.T) {
	withNoMachineConfig(t)
	dir := t.TempDir()
	writeProjectYAML(t, dir, `
schema_version: "2"
owner: nightgauge
`)

	cfg, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if got := cfg.Pipeline.ResolveHistoryRetentionDays(); got != DefaultHistoryRetentionDays {
		t.Errorf("ResolveHistoryRetentionDays() = %d, want default %d", got, DefaultHistoryRetentionDays)
	}
}
