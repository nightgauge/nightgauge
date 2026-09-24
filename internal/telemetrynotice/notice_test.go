package telemetrynotice

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintsOnceForTheDefaultOnPopulation(t *testing.T) {
	root := t.TempDir()
	n := New(root)

	var buf bytes.Buffer
	printed, err := n.MaybePrint(&buf, true, false)
	if err != nil {
		t.Fatalf("MaybePrint: %v", err)
	}
	if !printed {
		t.Fatal("first call must print for an operator who never configured telemetry")
	}
	if !strings.Contains(buf.String(), "enabled: false") {
		t.Error("notice must show the operator how to turn telemetry off")
	}

	buf.Reset()
	printed, err = n.MaybePrint(&buf, true, false)
	if err != nil {
		t.Fatalf("second MaybePrint: %v", err)
	}
	if printed || buf.Len() != 0 {
		t.Error("notice must not repeat once the marker is recorded")
	}
}

func TestSilentWhenTelemetryIsOff(t *testing.T) {
	var buf bytes.Buffer
	printed, err := New(t.TempDir()).MaybePrint(&buf, false, false)
	if err != nil {
		t.Fatalf("MaybePrint: %v", err)
	}
	if printed {
		t.Error("an operator with telemetry off has nothing to be informed about")
	}
}

func TestSilentWhenOperatorChoseItThemselves(t *testing.T) {
	var buf bytes.Buffer
	printed, err := New(t.TempDir()).MaybePrint(&buf, true, true)
	if err != nil {
		t.Fatalf("MaybePrint: %v", err)
	}
	if printed {
		t.Error("an explicit enabled: true needs no disclosure — they wrote it")
	}
}

// A marker left by a prior run must suppress the notice even though this
// Notifier has never printed anything itself: the state is per-machine, not
// per-process.
func TestExistingMarkerSuppresses(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, markerName)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(marker, []byte(""), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	var buf bytes.Buffer
	printed, _ := New(root).MaybePrint(&buf, true, false)
	if printed {
		t.Error("a marker from a previous process must suppress the notice")
	}
}

// The notice is only ever delivered once, so a failure to record the marker
// must not fail the caller's command — but it must be reported, so the caller
// can log it rather than discover the repeat by surprise.
func TestMarkerWriteFailureStillPrints(t *testing.T) {
	root := t.TempDir()
	// Occupy the marker's parent path with a file so MkdirAll cannot succeed.
	blocker := filepath.Join(root, "state")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	var buf bytes.Buffer
	printed, err := New(blocker).MaybePrint(&buf, true, false)
	if !printed {
		t.Error("the notice must still reach the operator when the marker cannot be saved")
	}
	if err == nil {
		t.Error("a marker write failure must be reported so the caller can log it")
	}
	if buf.Len() == 0 {
		t.Error("notice text must have been written")
	}
}

func TestForAccountUsesStateHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("NIGHTGAUGE_STATE_HOME", state)
	n, err := ForAccount()
	if err != nil {
		t.Fatalf("ForAccount: %v", err)
	}
	if want := filepath.Join(state, markerName); n.markerPath != want {
		t.Errorf("marker path %q, want %q", n.markerPath, want)
	}
}

// An operator who saw the notice under a release that kept the marker in
// ~/.nightgauge must not see it again after the upgrade (ADR-024 § 8).
func TestForAccountMovesLegacyMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("NIGHTGAUGE_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	legacy := filepath.Join(home, ".nightgauge", markerName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := ForAccount()
	if err != nil {
		t.Fatalf("ForAccount: %v", err)
	}
	var buf bytes.Buffer
	if printed, _ := n.MaybePrint(&buf, true, false); printed {
		t.Error("the notice was shown again after the legacy marker was moved")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy marker still present: %v", err)
	}
}
