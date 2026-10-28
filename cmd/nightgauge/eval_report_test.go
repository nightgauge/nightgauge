package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEvalReportArgvValidation(t *testing.T) {
	ok := evalReportFlags{model: "m", tolerance: 5, minSamples: 3}
	cases := []struct {
		name string
		mut  func(*evalReportFlags)
		want string
	}{
		{"model required", func(f *evalReportFlags) { f.model = " " }, "--model is required"},
		{"negative tolerance", func(f *evalReportFlags) { f.tolerance = -1 }, "--tolerance must be >= 0"},
		{"zero min samples", func(f *evalReportFlags) { f.minSamples = 0 }, "--min-samples must be >= 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ok
			tc.mut(&f)
			if _, err := evalReportArgv(f, func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEvalReportArgvForwardsFlags(t *testing.T) {
	f := evalReportFlags{model: "m", controlHash: "abc", recordsDir: "r", tolerance: 2.5, minSamples: 1,
		json: true, emitPatch: "p.patch"}
	argv, err := evalReportArgv(f, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"npx", "tsx", "scripts/eval-report.ts", "--model", "m", "--control-hash", "abc",
		"--records-dir", "r", "--tolerance", "2.5", "--min-samples", "1", "--json", "--emit-patch", "p.patch"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %v\nwant %v", argv, want)
	}
	// Defaults are the script's: unchanged flags are not forwarded.
	argv, _ = evalReportArgv(evalReportFlags{model: "m", tolerance: 5, minSamples: 3}, func(string) bool { return false })
	if want := []string{"npx", "tsx", "scripts/eval-report.ts", "--model", "m"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %v\nwant %v", argv, want)
	}
}

func TestEvalReportCmdExecsScriptAndPropagatesExitCode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, evalSweepScript), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var gotArgv []string
	orig := evalRunScript
	t.Cleanup(func() { evalRunScript = orig })
	evalRunScript = func(_ string, argv []string) (int, error) {
		gotArgv = argv
		return 1, nil
	}

	cmd := evalCmd()
	cmd.SetArgs([]string{"report", "--model", "m", "--json"})
	err := cmd.Execute()
	var ve verdictExit
	if !asVerdict(err, &ve) || ve.code != 1 {
		t.Fatalf("err = %v, want verdictExit{1}", err)
	}
	if strings.Join(gotArgv, " ") != "npx tsx scripts/eval-report.ts --model m --json" {
		t.Fatalf("argv = %v", gotArgv)
	}
}
