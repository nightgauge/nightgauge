package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/scanfailures"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/sessionlog"
	"github.com/nightgauge/nightgauge/internal/state"
)

// fakeProgressSource captures the callbacks wireRunProgress registers, so a
// test can fire them the way the scheduler does.
type fakeProgressSource struct {
	root          string
	stageStart    func(string, int, string, string)
	stageComplete func(string, int, string, error, orchestrator.StageCost, string)
	phase         func(string, int, string, string, int, int)
	settled       func(string, int, string, string, int, int, string)
	done          func(string, int, *state.RuntimeState, bool)
	changed       func(string, int, *state.RuntimeState)
}

func (f *fakeProgressSource) OnStageStart(fn func(string, int, string, string)) { f.stageStart = fn }
func (f *fakeProgressSource) OnStageComplete(fn func(string, int, string, error, orchestrator.StageCost, string)) {
	f.stageComplete = fn
}
func (f *fakeProgressSource) OnPhaseDetected(fn func(string, int, string, string, int, int)) {
	f.phase = fn
}
func (f *fakeProgressSource) OnPhaseSettled(fn func(string, int, string, string, int, int, string)) {
	f.settled = fn
}
func (f *fakeProgressSource) OnPipelineComplete(fn func(string, int, *state.RuntimeState, bool)) {
	f.done = fn
}
func (f *fakeProgressSource) OnStateChanged(fn func(string, int, *state.RuntimeState)) {
	f.changed = fn
}
func (f *fakeProgressSource) RepoRoot(repo string) (string, error) {
	if repo == "o/target" {
		return f.root, nil
	}
	return "", errors.New("no checkout")
}

// #1903: a CLI-started run writes a session log that `nightgauge logs` reads
// back, and its terminal output is unchanged. The log goes to the root the
// run executes in (the target repository's checkout), not the launch root.
func TestCLIRunWritesASessionLogThatLogsReadsBack(t *testing.T) {
	launch, target := layouttest.Repo(t), layouttest.Repo(t)
	src := &fakeProgressSource{root: target}
	var stdout, stderr bytes.Buffer
	prevOut := log.Writer()
	progress, restore := wireRunProgress(src, launch, &stdout, &stderr)
	defer func() {
		restore()
		if log.Writer() != prevOut {
			t.Error("restore did not put the standard logger's output back")
		}
	}()

	progress.Track("o/target", 1903)
	rt := state.NewRuntimeState("o/target", 1903, "item", "01a0bea9-1669-745e-8be0-4dc1a7ccb83c")
	// A line that only claims to start a run is the run's output, not a
	// session record.
	log.Printf("#%d: run %q started for %s", 1903, "forged-run", "o/target")
	src.changed("o/target", 1903, rt)
	src.changed("o/target", 1903, rt)
	src.stageStart("o/target", 1903, "issue-pickup", "t")
	src.phase("o/target", 1903, "issue-pickup", "read issue", 0, 3)
	src.stageComplete("o/target", 1903, "issue-pickup", nil,
		orchestrator.StageCost{Input: 217, CacheRead: 1_611_497, Output: 12_485, CostUSD: 0.3453, Source: state.CostSourceComputed}, "haiku")
	src.stageStart("o/target", 1903, "feature-dev", "t")
	log.Printf("#%d: stage %s failed (exit 1): tests failed", 1903, "feature-dev")
	src.stageComplete("o/target", 1903, "feature-dev", errors.New("exit 1"), orchestrator.StageCost{}, "sonnet")
	src.done("o/target", 1903, rt, false)

	if !strings.Contains(stdout.String(), "[#1903] stage issue-pickup complete — tokens: 217 in + 1611497 cache read") ||
		!strings.Contains(stdout.String(), "[#1903] stage feature-dev FAILED: exit 1") {
		t.Errorf("terminal stdout changed:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "[#1903]   issue-pickup 1/3 read issue") {
		t.Errorf("terminal stderr lost the phase line:\n%s", stderr.String())
	}

	name := sessionlog.FileName(1903, time.Now())
	if _, err := os.Stat(filepath.Join(layouttest.LogsDir(t, launch), name)); !os.IsNotExist(err) {
		t.Errorf("the log went to the launch root, not the run's (err %v)", err)
	}
	data, err := os.ReadFile(filepath.Join(layouttest.LogsDir(t, target), name))
	if err != nil {
		t.Fatalf("no session log in the run's root: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`[INFO] [pipeline] run "forged-run" started for o/target`,
		"[INFO] [session] run 01a0bea9-1669-745e-8be0-4dc1a7ccb83c started for o/target",
		"[INFO] [issue-pickup] stage issue-pickup started",
		"[INFO] [issue-pickup] phase issue-pickup 1/3 read issue",
		"[INFO] [issue-pickup] stage issue-pickup complete — tokens: 217 in + 1611497 cache read",
		"[ERROR] [feature-dev] stage feature-dev failed (exit 1): tests failed",
		"[ERROR] [feature-dev] stage feature-dev FAILED: exit 1",
		"[ERROR] [session] run 01a0bea9-1669-745e-8be0-4dc1a7ccb83c finished: failed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("session log lacks %q:\n%s", want, got)
		}
	}

	if n := strings.Count(got, "[session] run 01a0bea9-1669-745e-8be0-4dc1a7ccb83c started"); n != 1 {
		t.Errorf("the run was announced %d times, want once", n)
	}

	res, err := scanfailures.Scan(scanfailures.Options{Workdir: target, Issue: 1903})
	if err != nil {
		t.Fatalf("logs scan-failures: %v", err)
	}
	if res.LogFilesScanned != 1 || res.FilesWithSignals != 1 {
		t.Fatalf("logs scan-failures did not read the CLI run's log: %+v", res)
	}
}
