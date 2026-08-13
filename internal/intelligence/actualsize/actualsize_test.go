package actualsize

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFiveBucketMatchesOutcomeServiceBoundaries(t *testing.T) {
	tests := []struct {
		lines int
		want  string
	}{
		{0, "XS"}, {75, "XS"}, {76, "S"}, {250, "S"},
		{251, "M"}, {750, "M"}, {751, "L"}, {1750, "L"},
		{1751, "XL"},
	}
	for _, tc := range tests {
		if got := FiveBucket(tc.lines, nil); got != tc.want {
			t.Errorf("FiveBucket(%d) = %q, want %q", tc.lines, got, tc.want)
		}
	}
}

func TestLearningBucketUsesLearnedThresholdsAndScoreVocabulary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".nightgauge", "complexity-model.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	model := []byte("size_calibration:\n  XS:\n    expected_lines: 10\n  S:\n    expected_lines: 20\n  M:\n    expected_lines: 30\n  L:\n    expected_lines: 40\n")
	if err := os.WriteFile(path, model, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lines int
		want  string
	}{{10, "small"}, {30, "small"}, {31, "medium"}, {41, "large"}} {
		if got := LearningBucket(root, tc.lines); got != tc.want {
			t.Errorf("LearningBucket(%d) = %q, want %q", tc.lines, got, tc.want)
		}
	}
}

func TestMeasureLinesDistinguishesZeroFromFailure(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	if got, err := MeasureLines(root, "main"); err != nil || got != 0 {
		t.Fatalf("clean MeasureLines = %d, %v; want measured zero", got, err)
	}
	if err := os.WriteFile(path, []byte("base\none\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := MeasureLines(root, "main"); err != nil || got != 2 {
		t.Fatalf("changed MeasureLines = %d, %v; want 2", got, err)
	}
	if _, err := MeasureLines(root, "missing"); err == nil {
		t.Fatal("MeasureLines against a missing base returned nil error")
	}
}

func TestMeasureLinesPrefersRemoteTrackingBaseOverStaleLocalBase(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	run("checkout", "-b", "upstream")
	if err := os.WriteFile(path, []byte("base\nupstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "-am", "upstream")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("checkout", "main")
	run("checkout", "-b", "feature", "origin/main")
	if err := os.WriteFile(path, []byte("base\nupstream\nfeature\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, err := MeasureLines(root, "main"); err != nil || got != 1 {
		t.Fatalf("MeasureLines against preferred origin/main = %d, %v; want only the 1 feature line", got, err)
	}
}

func TestMeasureLinesExcludesChangesAddedOnlyToAnAdvancedBase(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "base")
	run("checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "feature")

	// Advance origin/main on a sibling history after the feature fork. A direct
	// `git diff origin/main` sees both feature.txt and the missing upstream.txt;
	// the PR changeset contains only feature.txt.
	run("checkout", "main")
	if err := os.WriteFile(filepath.Join(root, "upstream.txt"), []byte("upstream\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "upstream")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("checkout", "feature")

	if got, err := MeasureLines(root, "main"); err != nil || got != 1 {
		t.Fatalf("MeasureLines with advanced origin/main = %d, %v; want only the 1 feature line", got, err)
	}
}
