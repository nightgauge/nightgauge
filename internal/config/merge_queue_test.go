package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestResolveMergeQueueWaitTimeout(t *testing.T) {
	var nilCfg *PipelineConfig
	if got := nilCfg.ResolveMergeQueueWaitTimeout(); got != DefaultMergeQueueWaitTimeout {
		t.Errorf("nil config: got %s, want %s", got, DefaultMergeQueueWaitTimeout)
	}
	if DefaultMergeQueueWaitTimeout != 90*time.Minute {
		t.Errorf("default = %s, want 90m", DefaultMergeQueueWaitTimeout)
	}
	var p PipelineConfig
	if err := yaml.Unmarshal([]byte("merge_queue:\n  wait_timeout: 2h\n"), &p); err != nil {
		t.Fatal(err)
	}
	if got := p.ResolveMergeQueueWaitTimeout(); got != 2*time.Hour {
		t.Errorf("configured: got %s, want 2h", got)
	}
}
