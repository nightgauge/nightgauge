package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/reclaim"
)

// TestScenario_DiagnoseFixVerify is #2100's end-to-end repair scenario: a
// seeded workspace is diagnosed, repaired with `--fix --yes`, every remedy is
// verified by re-running its owning check, and a second pass is clean. The
// seeded credentials are token-shaped fakes, and no surface doctor writes (the
// human report, JSON v2, the fix report and the fix log) may contain them.
//
// The issue lists four seeds. Three are coded findings with an executable
// remedy: a leaked worktree (NGD017, auto worktree.sweep), a pipeline stash
// (NGD019, auto stash.sweep) and a missing complexity model (NGD033, confirm
// outcome.init). The fourth, a stale GitHub App token cache, has no code: the
// App probe discards a token minted under other permissions as a side effect
// of the scan (gh.SyncAppTokenPermissions from probeApp), before any finding
// exists, and a scan without an App configured never reads the cache. It is
// seeded here for the redaction proof only, and the test asserts that it
// produces no finding rather than counting it as a fourth.
func TestScenario_DiagnoseFixVerify(t *testing.T) {
	r := newLeakRepo(t) // isolates HOME and the machine-state root
	state := os.Getenv("NIGHTGAUGE_STATE_HOME")
	// Worktrees nest inside the checkout here (production keeps them beside
	// it) and must not dirty the tree; the model lands in the git dir.
	r.write(".git/info/exclude", ".worktrees/\n.nightgauge/\n")

	// Token-shaped fakes, assembled so no scanner reads them as real.
	stashToken := "ghp_" + strings.Repeat("Sc3nAr10", 5)
	cacheToken := "ghs_" + strings.Repeat("C4cheT0k", 5)
	patToken := "github_pat_" + strings.Repeat("Fak3Cred3nt1al", 4)
	secrets := []string{stashToken, cacheToken, patToken}

	// 1. A leaked worktree whose branch has landed.
	worktree := mergedWorktree(r, 2100)
	// 2. A pipeline stash a killed stage left behind. Its message is free
	// text: a token pasted into it reaches doctor's title, evidence and
	// preview, so the redactor must scrub it on every surface.
	r.write("README", "modified\n")
	r.git("stash", "push", "-m", reclaim.StashName(reclaim.StashBaseline, 2100, stashToken))
	// 3. A stale App token cache and machine credentials.
	writeFile(t, filepath.Join(state, "github-app-token-42.json"),
		`{"token":"`+cacheToken+`","expires_at":"2026-01-01T00:00:00Z","permissions_hash":"stale"}`)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".nightgauge", "config.yaml"),
		"github:\n  token: "+patToken+"\n")
	t.Setenv("GH_TOKEN", patToken)
	t.Setenv("GITHUB_TOKEN", patToken)
	// 4. No complexity model: none is written.
	model := layouttest.CheckoutPath(t, r.dir, layout.CheckoutComplexityModel)

	fx := hygieneFixer(t, r.dir, verbDeps{}, "worktree_leaks", "pipeline_stashes", "complexity_model")
	ctx := context.Background()

	// Diagnose.
	scan := fx.Scan(ctx)
	codes := map[string]int{}
	rawCarriesToken := false
	for _, res := range scan {
		for _, f := range res.Findings {
			codes[f.Code]++
			rawCarriesToken = rawCarriesToken || f.Evidence["stage"] == stashToken
		}
	}
	// The raw finding carries the token, so a clean surface below is the
	// redactor's work and not an absent value.
	if !rawCarriesToken {
		t.Fatal("the seeded stash token never reached the scan; the redaction proof would be vacuous")
	}
	want := map[string]int{"NGD017": 1, "NGD019": 1, codeComplexityModelMissing: 1}
	if len(codes) != len(want) {
		t.Fatalf("scan codes %v, want exactly %v (the token cache raises none)", codes, want)
	}
	for code, n := range want {
		if codes[code] != n {
			t.Fatalf("scan codes %v, want %s x%d", codes, code, n)
		}
	}
	before := fx.State(FixOptions{})
	var surfaces [][]byte
	surfaces = append(surfaces, renderHuman(before), mustJSON(t, before))

	// Repair with --fix --yes.
	rep := fx.Run(ctx, FixOptions{Yes: true})
	surfaces = append(surfaces, mustJSON(t, rep), renderHuman(rep.Doctor))
	if len(rep.Results) != 3 {
		t.Fatalf("fix pass decided %d findings, want 3: %+v", len(rep.Results), rep.Results)
	}
	for _, res := range rep.Results {
		if res.Action != ActionApplied || res.Outcome != OutcomeFixed || !strings.HasPrefix(res.Detail, "verified: ") {
			t.Errorf("%s: %s/%s (%s), want applied and fixed through verify-after-fix",
				res.Finding.Code, res.Action, res.Outcome, res.Detail)
		}
	}
	if rep.Counts.Fixed != 3 || rep.ExitCode != 0 {
		t.Errorf("counts %+v, exit %d; want 3 fixed and exit 0", rep.Counts, rep.ExitCode)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("the leaked worktree %s is still on disk", worktree)
	}
	if out := r.git("stash", "list"); strings.TrimSpace(out) != "" {
		t.Errorf("the pipeline stash was not restored: %q", out)
	}
	if info, err := os.Stat(model); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the complexity model was not written 0600: %v %v", info, err)
	}

	// The fix log records each remedy once, verified.
	entries, malformed, err := ReadFixLog(fx.Log.Path)
	if err != nil || malformed != 0 || len(entries) != 3 {
		t.Fatalf("fix log: %d entries, %d malformed, err %v; want 3 clean entries", len(entries), malformed, err)
	}
	for _, e := range entries {
		if e.Outcome != OutcomeFixed {
			t.Errorf("fix log %s: %s", e.Code, e.Outcome)
		}
	}
	logBytes, err := os.ReadFile(fx.Log.Path)
	if err != nil {
		t.Fatal(err)
	}
	surfaces = append(surfaces, logBytes)

	// A second run is clean and exits 0.
	again := fx.Run(ctx, FixOptions{Yes: true})
	surfaces = append(surfaces, mustJSON(t, again), renderHuman(again.Doctor))
	if again.ExitCode != 0 || len(again.Results) != 0 || len(again.Doctor.Findings) != 0 {
		t.Errorf("second run: exit %d, %d results, findings %v; want a clean exit 0",
			again.ExitCode, len(again.Results), again.Doctor.Findings)
	}

	// Redaction end to end: the seeded values reach no surface.
	for i, out := range surfaces {
		for _, s := range secrets {
			if bytes.Contains(out, []byte(s)) {
				t.Errorf("surface %d carries a seeded credential %s…:\n%s", i, s[:6], out)
			}
		}
	}
	if !bytes.Contains(surfaces[1], []byte("nightgauge:baseline:2100:")) {
		t.Errorf("the stash finding is missing from the JSON, so the redaction check proved nothing")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func renderHuman(res DoctorResult) []byte {
	var buf bytes.Buffer
	RenderHuman(&buf, res, RenderOptions{})
	return buf.Bytes()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
