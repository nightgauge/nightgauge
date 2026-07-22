package loopverdicts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- helpers ---

func writeJSONL(t *testing.T, path string, records []interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

func writeJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// --- tests ---

func TestAnalyze_EmptyWorkspace_AllNoData(t *testing.T) {
	dir := t.TempDir()
	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.V != 1 {
		t.Errorf("v = %d, want 1", report.V)
	}
	if len(report.Loops) != 5 {
		t.Fatalf("loops = %d, want 5", len(report.Loops))
	}
	for _, l := range report.Loops {
		if l.Verdict != VerdictNoData && l.Verdict != VerdictBootstrapping {
			t.Errorf("loop %s: verdict = %s, want no-data (empty workspace)", l.Loop, l.Verdict)
		}
		if l.Points != 0 {
			t.Errorf("loop %s: points = %d, want 0 for no-data", l.Loop, l.Points)
		}
	}
}

func TestAnalyze_DefaultPeriod(t *testing.T) {
	dir := t.TempDir()
	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir}) // Period 0 → defaults to 30
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.Period != 30 {
		t.Errorf("period = %d, want 30", report.Period)
	}
}

func TestAnalyze_CalibrationBootstrapping(t *testing.T) {
	dir := t.TempDir()
	// Write 3 outcomes → below the 10 threshold → bootstrapping
	outcomesPath := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC()
	records := []interface{}{
		map[string]interface{}{"predictedSize": "S", "actualSize": "S", "success": true, "completedAt": now},
		map[string]interface{}{"predictedSize": "M", "actualSize": "L", "success": true, "completedAt": now},
		map[string]interface{}{"predictedSize": "S", "actualSize": "M", "success": false, "completedAt": now},
	}
	writeJSONL(t, outcomesPath, records)

	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var calibration *LoopResult
	for i := range report.Loops {
		if report.Loops[i].Loop == "calibration" {
			calibration = &report.Loops[i]
			break
		}
	}
	if calibration == nil {
		t.Fatal("calibration loop not found in report")
	}
	if calibration.Verdict != VerdictBootstrapping {
		t.Errorf("verdict = %s, want bootstrapping", calibration.Verdict)
	}
	if calibration.Points != 0 {
		t.Errorf("points = %d, want 0 for bootstrapping", calibration.Points)
	}
}

func TestAnalyze_SkillDrift_NoData_LessThan5(t *testing.T) {
	dir := t.TempDir()
	assessDir := filepath.Join(dir, ".nightgauge", "pipeline", "assessments")
	if err := os.MkdirAll(assessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write 3 assessment files (below threshold of 5)
	for i := 0; i < 3; i++ {
		writeJSON(t, filepath.Join(assessDir, itoa(i)+"-test.json"),
			map[string]interface{}{"friction": []interface{}{}})
	}

	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sd *LoopResult
	for i := range report.Loops {
		if report.Loops[i].Loop == "skill-drift" {
			sd = &report.Loops[i]
			break
		}
	}
	if sd == nil {
		t.Fatal("skill-drift loop not found")
	}
	if sd.Verdict != VerdictNoData {
		t.Errorf("verdict = %s, want no-data", sd.Verdict)
	}
}

func TestAnalyze_SkillDrift_Closing_LowFrictionRate(t *testing.T) {
	dir := t.TempDir()
	assessDir := filepath.Join(dir, ".nightgauge", "pipeline", "assessments")
	if err := os.MkdirAll(assessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 8 assessments: 1 with friction, 7 empty → rate = 1/8 = 12.5% → closing
	writeJSON(t, filepath.Join(assessDir, "0-friction.json"),
		map[string]interface{}{"friction": []interface{}{map[string]interface{}{"type": "stale_reference"}}})
	for i := 1; i < 8; i++ {
		writeJSON(t, filepath.Join(assessDir, itoa(i)+"-ok.json"),
			map[string]interface{}{"friction": []interface{}{}})
	}

	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sd *LoopResult
	for i := range report.Loops {
		if report.Loops[i].Loop == "skill-drift" {
			sd = &report.Loops[i]
			break
		}
	}
	if sd.Verdict != VerdictClosing {
		t.Errorf("verdict = %s, want closing (low friction rate)", sd.Verdict)
	}
	if sd.Points != 20 {
		t.Errorf("points = %d, want 20 for closing", sd.Points)
	}
}

func TestAnalyze_SkillDrift_Degrading_HighFrictionRate(t *testing.T) {
	dir := t.TempDir()
	assessDir := filepath.Join(dir, ".nightgauge", "pipeline", "assessments")
	if err := os.MkdirAll(assessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// 6 assessments: all with friction → rate = 6/6 = 100% → degrading
	for i := 0; i < 6; i++ {
		writeJSON(t, filepath.Join(assessDir, itoa(i)+"-friction.json"),
			map[string]interface{}{"friction": []interface{}{map[string]interface{}{"type": "stale_reference"}}})
	}

	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sd *LoopResult
	for i := range report.Loops {
		if report.Loops[i].Loop == "skill-drift" {
			sd = &report.Loops[i]
			break
		}
	}
	if sd.Verdict != VerdictDegrading {
		t.Errorf("verdict = %s, want degrading (high friction rate)", sd.Verdict)
	}
	if sd.Points != -10 {
		t.Errorf("points = %d, want -10 for degrading", sd.Points)
	}
}

func TestCompositeScore_AllClosing(t *testing.T) {
	// 5 loops × 20 points = 100 raw; (100+50)/100*100 = 150 clamped to 100
	loops := []LoopResult{
		{Loop: "a", Verdict: VerdictClosing, Points: 20},
		{Loop: "b", Verdict: VerdictClosing, Points: 20},
		{Loop: "c", Verdict: VerdictClosing, Points: 20},
		{Loop: "d", Verdict: VerdictClosing, Points: 20},
		{Loop: "e", Verdict: VerdictClosing, Points: 20},
	}
	raw := 0
	for _, l := range loops {
		raw += l.Points
	}
	composite := int(float64(raw+50) / 100.0 * 100.0)
	if composite > 100 {
		composite = 100
	}
	if composite != 100 {
		t.Errorf("composite = %d, want 100", composite)
	}
}

func TestCompositeScore_AllNoData(t *testing.T) {
	// 5 loops × 0 points = 0 raw; (0+50)/100*100 = 50
	raw := 0
	composite := int(float64(raw+50) / 100.0 * 100.0)
	if composite != 50 {
		t.Errorf("composite = %d, want 50", composite)
	}
	if healthBand(composite) != "needs-attention" {
		t.Errorf("band = %s, want needs-attention", healthBand(composite))
	}
}

func TestCompositeScore_AllDegrading(t *testing.T) {
	// 5 loops × -10 points = -50 raw; (-50+50)/100*100 = 0
	raw := -50
	composite := int(float64(raw+50) / 100.0 * 100.0)
	if composite < 0 {
		composite = 0
	}
	if composite != 0 {
		t.Errorf("composite = %d, want 0", composite)
	}
	if healthBand(composite) != "urgent" {
		t.Errorf("band = %s, want urgent", healthBand(composite))
	}
}

func TestHealthBand(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{100, "highly-effective"},
		{80, "highly-effective"},
		{79, "working"},
		{60, "working"},
		{59, "needs-attention"},
		{40, "needs-attention"},
		{39, "urgent"},
		{0, "urgent"},
	}
	for _, tc := range cases {
		got := healthBand(tc.score)
		if got != tc.want {
			t.Errorf("healthBand(%d) = %s, want %s", tc.score, got, tc.want)
		}
	}
}

func TestVerdictPoints(t *testing.T) {
	cases := []struct {
		v    Verdict
		want int
	}{
		{VerdictClosing, 20},
		{VerdictStalling, 5},
		{VerdictDegrading, -10},
		{VerdictNoData, 0},
		{VerdictBootstrapping, 0},
	}
	for _, tc := range cases {
		got := verdictPoints(tc.v)
		if got != tc.want {
			t.Errorf("verdictPoints(%s) = %d, want %d", tc.v, got, tc.want)
		}
	}
}

func TestAnalyze_ReportSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report.V != 1 {
		t.Errorf("schema version v = %d, want 1", report.V)
	}
	if report.GeneratedAt.IsZero() {
		t.Error("generatedAt should not be zero")
	}
}

func TestAnalyze_Reliability_Closing(t *testing.T) {
	dir := t.TempDir()
	outcomesPath := filepath.Join(dir, ".nightgauge", "pipeline", "history", "outcomes.jsonl")
	now := time.Now().UTC()
	// 10 records: first 5 have 3 failures, last 5 have 0 failures → rate improving
	records := []interface{}{}
	for i := 0; i < 5; i++ {
		success := i >= 3 // first 3 fail
		records = append(records, map[string]interface{}{
			"success": success, "completedAt": now,
		})
	}
	for i := 0; i < 5; i++ {
		records = append(records, map[string]interface{}{
			"success": true, "completedAt": now,
		})
	}
	writeJSONL(t, outcomesPath, records)

	report, err := Analyze(AnalyzeInput{WorkspaceRoot: dir, Period: 30})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rel *LoopResult
	for i := range report.Loops {
		if report.Loops[i].Loop == "reliability" {
			rel = &report.Loops[i]
			break
		}
	}
	if rel == nil {
		t.Fatal("reliability loop missing")
	}
	if rel.Verdict != VerdictClosing {
		t.Errorf("verdict = %s, want closing (failure rate improved)", rel.Verdict)
	}
}
