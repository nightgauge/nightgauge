package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEvalSweepArgvValidation(t *testing.T) {
	ok := evalSweepFlags{model: "claude-opus-4-8", control: true, estimateUSD: 5, cellTimeoutMin: 90, mode: "stub"}
	none := func(string) bool { return false }
	cases := []struct {
		name string
		mut  func(*evalSweepFlags)
		want string
	}{
		{"control required", func(f *evalSweepFlags) { f.control = false }, "--control is required"},
		{"model required", func(f *evalSweepFlags) { f.model = " " }, "--model is required"},
		{"negative cap", func(f *evalSweepFlags) { f.maxUSD = -1 }, "--max-usd must be >= 0"},
		{"bad mode", func(f *evalSweepFlags) { f.mode = "prod" }, "--mode must be stub or live"},
		{"zero timeout", func(f *evalSweepFlags) { f.cellTimeoutMin = 0 }, "--cell-timeout-min"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ok
			tc.mut(&f)
			if _, err := evalSweepArgv(f, none); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEvalSweepArgvForwardsFlags(t *testing.T) {
	f := evalSweepFlags{model: "m", control: true, efforts: "low,high", maxUSD: 0.01, estimateUSD: 1,
		cellTimeoutMin: 30, mode: "live", out: "x.jsonl"}
	argv, err := evalSweepArgv(f, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"npx", "tsx", "scripts/eval-sweep.ts", "--model", "m", "--control", "--mode", "live",
		"--cell-timeout-min", "30", "--efforts", "low,high", "--max-usd", "0.01", "--estimate-usd", "1",
		"--out", "x.jsonl"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %v\nwant %v", argv, want)
	}
	// An unset cap is not forwarded: no flag means no cap, not a $0 cap.
	argv, _ = evalSweepArgv(f, func(string) bool { return false })
	for _, a := range argv {
		if a == "--max-usd" {
			t.Fatalf("unset --max-usd forwarded: %v", argv)
		}
	}
}

func TestEvalSweepCmdExecsScriptAndPropagatesExitCode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, evalSweepScript), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	var gotDir string
	var gotArgv []string
	orig := evalRunScript
	t.Cleanup(func() { evalRunScript = orig })
	evalRunScript = func(dir string, argv []string) (int, error) {
		gotDir, gotArgv = dir, argv
		return 1, nil
	}

	cmd := evalCmd()
	cmd.SetArgs([]string{"sweep", "--model", "m", "--control", "--max-usd", "0.01"})
	err := cmd.Execute()
	var ve verdictExit
	if !asVerdict(err, &ve) || ve.code != 1 {
		t.Fatalf("err = %v, want verdictExit{1}", err)
	}
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotRoot, _ := filepath.EvalSymlinks(gotDir)
	if gotRoot != wantRoot {
		t.Fatalf("ran in %s, want %s", gotDir, root)
	}
	if !strings.Contains(strings.Join(gotArgv, " "), "--max-usd 0.01") {
		t.Fatalf("argv = %v", gotArgv)
	}
}

func asVerdict(err error, ve *verdictExit) bool {
	v, ok := err.(verdictExit)
	if ok {
		*ve = v
	}
	return ok
}
