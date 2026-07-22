package hooks

import (
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/skills"
)

func TestLogSkillUsageWritesRecord(t *testing.T) {
	root := t.TempDir()
	input := fmt.Sprintf(`{"tool_name":"Skill","session_id":"sess-1","cwd":%q,"tool_input":{"skill":"nightgauge-security-audit","args":"."}}`, root)

	got := LogSkillUsage([]byte(input))
	if got.Decision != "allow" {
		t.Fatalf("telemetry hook must always allow, got %q", got.Decision)
	}

	recs, err := skills.ReadUsage(root)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if len(recs) != 1 || recs[0].Skill != "nightgauge-security-audit" || recs[0].Session != "sess-1" {
		t.Fatalf("unexpected records: %+v", recs)
	}
}

func TestLogSkillUsageFallbackNameField(t *testing.T) {
	root := t.TempDir()
	input := fmt.Sprintf(`{"tool_name":"Skill","cwd":%q,"tool_input":{"name":"smart-setup"}}`, root)
	if got := LogSkillUsage([]byte(input)); got.Decision != "allow" {
		t.Fatalf("want allow, got %q", got.Decision)
	}
	recs, _ := skills.ReadUsage(root)
	if len(recs) != 1 || recs[0].Skill != "smart-setup" {
		t.Fatalf("expected name fallback to log smart-setup, got %+v", recs)
	}
}

func TestLogSkillUsageIgnoresNonSkillTool(t *testing.T) {
	root := t.TempDir()
	input := fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":"ls"}}`, root)
	if got := LogSkillUsage([]byte(input)); got.Decision != "allow" {
		t.Fatalf("want allow, got %q", got.Decision)
	}
	recs, _ := skills.ReadUsage(root)
	if len(recs) != 0 {
		t.Fatalf("non-Skill tool must not be logged, got %+v", recs)
	}
}

func TestLogSkillUsageMalformedAllows(t *testing.T) {
	if got := LogSkillUsage([]byte("not json")); got.Decision != "allow" {
		t.Fatalf("malformed input must fail open (allow), got %q", got.Decision)
	}
}

func TestLogSkillUsageNoSkillNameNotLogged(t *testing.T) {
	root := t.TempDir()
	input := fmt.Sprintf(`{"tool_name":"Skill","cwd":%q,"tool_input":{}}`, root)
	LogSkillUsage([]byte(input))
	recs, _ := skills.ReadUsage(root)
	if len(recs) != 0 {
		t.Fatalf("missing skill name must not log, got %+v", recs)
	}
}
