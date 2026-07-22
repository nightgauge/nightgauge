package diagnostics

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteStageExitRecord_WritesOneLine(t *testing.T) {
	root := t.TempDir()
	rec := StageExitRecord{
		Repo:    "nightgauge/nightgauge",
		Issue:   3591,
		Stage:   "feature-planning",
		Success: false,
	}
	if err := WriteStageExitRecord(root, rec); err != nil {
		t.Fatalf("WriteStageExitRecord: %v", err)
	}

	path := DailyFilePath(root, time.Now())
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	count := 0
	var read StageExitRecord
	for scanner.Scan() {
		count++
		if err := json.Unmarshal(scanner.Bytes(), &read); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("expected 1 line, got %d", count)
	}
	if read.Repo != "nightgauge/nightgauge" || read.Issue != 3591 || read.Stage != "feature-planning" || read.Success {
		t.Fatalf("read back wrong record: %+v", read)
	}
	if read.Timestamp == "" {
		t.Fatalf("expected auto-populated Timestamp")
	}
}

func TestWriteStageExitRecord_RoundTripsAllFields(t *testing.T) {
	root := t.TempDir()
	exitCode := 137
	lastBashExit := 1
	rec := StageExitRecord{
		Timestamp:                "2026-05-16T08:23:36.029Z",
		Repo:                     "nightgauge/nightgauge",
		Issue:                    3591,
		Stage:                    "feature-dev",
		SessionID:                "74534682-aaaa-bbbb-cccc-dddddddddddd",
		RunID:                    "abc123",
		Success:                  false,
		ExitCode:                 &exitCode,
		Signal:                   "SIGKILL",
		SignalSource:             "stall-kill",
		TerminalKind:             "stall_kill",
		ElapsedMs:                397123,
		IdleMsAtExit:             4521,
		Tokens:                   ExitRecordTokens{Input: 1000, Output: 200, CacheRead: 5000, CacheCreation: 80, CostUsd: 0.123},
		LastBashCommand:          "nightgauge project move-status 3591 in-progress",
		LastBashExit:             &lastBashExit,
		StopHookErrored:          true,
		StderrTail:               "[skillRunner] Stage exceeded stall idle threshold (20m without output) ...",
		RateLimitRemainingAtExit: 4172,
		ConcurrentPipelinesAtExit: []string{
			"acme/dashboard#414",
		},
	}
	if err := WriteStageExitRecord(root, rec); err != nil {
		t.Fatalf("WriteStageExitRecord: %v", err)
	}

	path := DailyFilePath(root, time.Now())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var got StageExitRecord
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SessionID != rec.SessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, rec.SessionID)
	}
	if got.RunID != rec.RunID {
		t.Errorf("RunID = %q, want %q", got.RunID, rec.RunID)
	}
	if got.ExitCode == nil || *got.ExitCode != 137 {
		t.Errorf("ExitCode = %v, want 137", got.ExitCode)
	}
	if got.Signal != "SIGKILL" {
		t.Errorf("Signal = %q, want SIGKILL", got.Signal)
	}
	if got.SignalSource != "stall-kill" {
		t.Errorf("SignalSource = %q, want stall-kill", got.SignalSource)
	}
	if got.TerminalKind != "stall_kill" {
		t.Errorf("TerminalKind = %q, want stall_kill", got.TerminalKind)
	}
	if got.IdleMsAtExit != 4521 {
		t.Errorf("IdleMsAtExit = %d, want 4521", got.IdleMsAtExit)
	}
	if got.Tokens.CostUsd != 0.123 {
		t.Errorf("Tokens.CostUsd = %v, want 0.123", got.Tokens.CostUsd)
	}
	if got.LastBashCommand != rec.LastBashCommand {
		t.Errorf("LastBashCommand mismatch")
	}
	if got.LastBashExit == nil || *got.LastBashExit != 1 {
		t.Errorf("LastBashExit = %v, want 1", got.LastBashExit)
	}
	if !got.StopHookErrored {
		t.Errorf("StopHookErrored = false, want true")
	}
	if !strings.Contains(got.StderrTail, "skillRunner") {
		t.Errorf("StderrTail did not round-trip")
	}
	if got.RateLimitRemainingAtExit != 4172 {
		t.Errorf("RateLimitRemainingAtExit = %d, want 4172", got.RateLimitRemainingAtExit)
	}
	if len(got.ConcurrentPipelinesAtExit) != 1 || got.ConcurrentPipelinesAtExit[0] != "acme/dashboard#414" {
		t.Errorf("ConcurrentPipelinesAtExit = %v", got.ConcurrentPipelinesAtExit)
	}
}

func TestWriteStageExitRecord_DailyFileRotation(t *testing.T) {
	root := t.TempDir()
	today := time.Now().UTC()
	yesterday := today.AddDate(0, 0, -1)

	rec := StageExitRecord{
		Timestamp: today.Format(time.RFC3339Nano),
		Repo:      "nightgauge/nightgauge",
		Issue:     1,
		Stage:     "issue-pickup",
		Success:   true,
	}
	if err := WriteStageExitRecord(root, rec); err != nil {
		t.Fatalf("write today: %v", err)
	}

	// Append a "yesterday" record directly via DailyFilePath to verify
	// the path helper produces a different file for a different day.
	yPath := DailyFilePath(root, yesterday)
	if err := os.MkdirAll(filepath.Dir(yPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yRec := []byte(`{"ts":"yesterday","repo":"r","issue":2,"stage":"feature-dev","success":true}` + "\n")
	if err := os.WriteFile(yPath, yRec, 0644); err != nil {
		t.Fatalf("write yesterday: %v", err)
	}

	if yPath == DailyFilePath(root, today) {
		t.Fatalf("daily file path collided across days")
	}
}

func TestWriteStageExitRecord_ConcurrentAppendsNotInterleaved(t *testing.T) {
	root := t.TempDir()
	const writers = 16
	const perWriter = 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				rec := StageExitRecord{
					Repo:    "nightgauge/nightgauge",
					Issue:   w*1000 + j,
					Stage:   "feature-dev",
					Success: j%2 == 0,
				}
				if err := WriteStageExitRecord(root, rec); err != nil {
					t.Errorf("write %d/%d: %v", w, j, err)
				}
			}
		}(i)
	}
	wg.Wait()

	path := DailyFilePath(root, time.Now())
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		var rec StageExitRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("line %d not valid JSON: %v (%q)", count, err, scanner.Text())
		}
		count++
	}
	if got, want := count, writers*perWriter; got != want {
		t.Fatalf("expected %d lines, got %d", want, got)
	}
}

func TestWriteStageExitRecord_EmptyRootFails(t *testing.T) {
	if err := WriteStageExitRecord("", StageExitRecord{}); err == nil {
		t.Fatalf("expected error for empty rootDir")
	}
}

func TestExitRecordsDir_RelativeToRoot(t *testing.T) {
	got := ExitRecordsDir("/some/workspace")
	want := "/some/workspace/.nightgauge/pipeline/exit-records"
	if got != want {
		t.Errorf("ExitRecordsDir = %q, want %q", got, want)
	}
}

func TestExitRecordsSubdir_ConstantSurfacesViaDir(t *testing.T) {
	// Defense against accidental rename of the on-disk path — every
	// external reader (CLI tooling, retro skill) relies on the path.
	got := ExitRecordsDir("root")
	if !strings.HasSuffix(got, exitRecordsSubdir) {
		t.Errorf("ExitRecordsDir = %q does not end with %q", got, exitRecordsSubdir)
	}
}
