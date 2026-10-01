package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func finding(check, code string, sev Severity, title string) Finding {
	return Finding{Code: code, Check: check, Severity: sev, Title: title,
		Fingerprint: Fingerprint(code, check), Evidence: map[string]string{}}
}

// TestRegistryRejectsUnknownAndDuplicateChecks: a dependency must already be
// registered (keeping the graph acyclic) and an ID registers once.
func TestRegistryRejectsUnknownAndDuplicateChecks(t *testing.T) {
	reg := NewRegistry()
	run := func(context.Context, *Env) []Finding { return nil }
	if err := reg.Register(Check{ID: "b", DependsOn: []string{"a"}, Run: run}); err == nil {
		t.Fatal("unregistered dependency accepted")
	}
	if err := reg.Register(Check{ID: "a", Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(Check{ID: "a", Run: run}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

// TestRegistryDefaultRegistersEveryADRCheck: every ADR-025 check, including
// the three the old render list never printed, is registered with its code.
func TestRegistryDefaultRegistersEveryADRCheck(t *testing.T) {
	want := map[string]string{
		"binary": "NGD001", "skills": "NGD002", "gh": "NGD003", "github_auth": "NGD004",
		"api_user": "NGD005", "scopes": "NGD006", "rate_limit": "NGD007", "github_api_budget": "NGD008",
		"github_identity": "NGD009", "config": "NGD010", "project": "NGD011", "project_mapping": "NGD012",
		"board_population": "NGD013", "complexity_model": "NGD014", "ai_adapter": "NGD015",
		"compose_orphans": "NGD016", "worktree_leaks": "NGD017", "stranded_branches": "NGD018",
		"pipeline_stashes": "NGD019", "preserved_wip": "NGD020", "orphaned_processes": "NGD021",
		"serve_lease": "NGD022", "ledger_daemon_coverage": "NGD023", "tracked_secrets": "NGD024",
		"ci_machine_credentials": "NGD025", "survival_backlog": "NGD026", "survival_coverage": "NGD027",
		"corpus_calibration": "NGD028", "scheduled_automations": "NGD029",
		"log_retention": "NGD043", "platform_url": "NGD047",
	}
	got := map[string]string{}
	for _, c := range DefaultRegistry().Checks() {
		got[c.ID] = c.Code
	}
	for id, code := range want {
		if got[id] != code {
			t.Errorf("check %s: code %q, want %q", id, got[id], code)
		}
	}
}

// TestRunnerTimeoutYieldsNGD000: a check that overruns its deadline yields a
// warning NGD000 within timeout + 1s instead of hanging the command.
func TestRunnerTimeoutYieldsNGD000(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(Check{ID: "slow", Code: "NGD999", Timeout: 200 * time.Millisecond,
		Run: func(ctx context.Context, _ *Env) []Finding {
			select {
			case <-ctx.Done():
			case <-time.After(30 * time.Second):
			}
			return nil
		}})
	start := time.Now()
	results := Runner{}.Run(context.Background(), reg, &Env{})
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond+time.Second {
		t.Fatalf("runner took %s, want under timeout + 1s", elapsed)
	}
	r := results[0]
	if r.Status != StatusTimeout || len(r.Findings) != 1 {
		t.Fatalf("result = %+v, want one timeout finding", r)
	}
	if f := r.Findings[0]; f.Code != "NGD000" || f.Severity != SeverityWarning || f.Check != "slow" {
		t.Errorf("finding = %+v, want NGD000 warning for slow", f)
	}
}

// TestRunnerPanicDetailRenders: a check that panics yields NGD000 whose
// evidence carries the panic value, and the human report prints it.
func TestRunnerPanicDetailRenders(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(Check{ID: "crashy", Code: "NGD999",
		Run: func(context.Context, *Env) []Finding { panic("nil map in crashy") }})
	res := BuildResult(Runner{}.Run(context.Background(), reg, &Env{}))
	if len(res.Findings) != 1 || res.Findings[0].Code != "NGD000" {
		t.Fatalf("findings = %+v, want one NGD000", res.Findings)
	}
	if got := res.Findings[0].Evidence["detail"]; got != "nil map in crashy" {
		t.Errorf("evidence detail = %q, want the panic value", got)
	}
	var human bytes.Buffer
	RenderHuman(&human, res, RenderOptions{})
	if !strings.Contains(human.String(), "detail: nil map in crashy") {
		t.Errorf("human report omits the panic detail:\n%s", human.String())
	}
}

// TestRunnerRunsIndependentChecksConcurrently: two independent 1s checks
// finish in under 1.5s total.
func TestRunnerRunsIndependentChecksConcurrently(t *testing.T) {
	reg := NewRegistry()
	for _, id := range []string{"a", "b"} {
		reg.MustRegister(Check{ID: id, Code: "NGD999", Run: func(ctx context.Context, _ *Env) []Finding {
			time.Sleep(time.Second)
			return nil
		}})
	}
	start := time.Now()
	results := Runner{}.Run(context.Background(), reg, &Env{})
	if elapsed := time.Since(start); elapsed >= 1500*time.Millisecond {
		t.Fatalf("two independent 1s checks took %s", elapsed)
	}
	for _, r := range results {
		if r.Status != StatusPassed {
			t.Errorf("%s: status %s", r.ID, r.Status)
		}
	}
}

// TestRunnerWorkerPoolIsBounded: never more than Workers checks run at once.
func TestRunnerWorkerPoolIsBounded(t *testing.T) {
	reg := NewRegistry()
	var running, peak atomic.Int32
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		reg.MustRegister(Check{ID: id, Code: "NGD999", Run: func(ctx context.Context, _ *Env) []Finding {
			n := running.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
			return nil
		}})
	}
	Runner{Workers: 2}.Run(context.Background(), reg, &Env{})
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrency %d, want <= 2", p)
	}
}

// TestRunnerSkipsDependentOfFailedCheck: a dependent of a blocker is skipped
// with an info finding (never reported healthy) and never runs; the skip
// cascades; a dependent of a passing check runs.
func TestRunnerSkipsDependentOfFailedCheck(t *testing.T) {
	reg := NewRegistry()
	var ran atomic.Int32
	reg.MustRegister(Check{ID: "auth", Code: "NGD004", Run: func(context.Context, *Env) []Finding {
		return []Finding{finding("auth", "NGD004", SeverityBlocker, "no credential")}
	}})
	reg.MustRegister(Check{ID: "ok", Code: "NGD010", Run: func(context.Context, *Env) []Finding { return nil }})
	reg.MustRegister(Check{ID: "scopes", Code: "NGD006", DependsOn: []string{"auth"},
		Run: func(context.Context, *Env) []Finding { ran.Add(1); return nil }})
	reg.MustRegister(Check{ID: "deeper", Code: "NGD009", DependsOn: []string{"scopes"},
		Run: func(context.Context, *Env) []Finding { ran.Add(1); return nil }})
	reg.MustRegister(Check{ID: "after_ok", Code: "NGD011", DependsOn: []string{"ok"},
		Run: func(context.Context, *Env) []Finding { return nil }})

	results := Runner{}.Run(context.Background(), reg, &Env{})
	if ran.Load() != 0 {
		t.Fatal("a check whose dependency failed still ran")
	}
	byID := map[string]CheckResult{}
	for _, r := range results {
		byID[r.ID] = r
	}
	for _, id := range []string{"scopes", "deeper"} {
		r := byID[id]
		if r.Status != StatusSkipped || len(r.Findings) != 1 || r.Findings[0].Severity != SeverityInfo {
			t.Errorf("%s = %+v, want skipped with one info finding", id, r)
		}
	}
	if byID["after_ok"].Status != StatusPassed {
		t.Errorf("after_ok = %s, want passed", byID["after_ok"].Status)
	}
	res := BuildResult(results)
	if res.ExitCode != 2 || res.Summary.Info != 2 || res.Summary.Blocker != 1 {
		t.Errorf("exit %d summary %+v", res.ExitCode, res.Summary)
	}
}

// TestRunnerExitCodesFollowADR025: housekeeping and info never change the
// exit code; warnings give 1; a blocker gives 2.
func TestRunnerExitCodesFollowADR025(t *testing.T) {
	cases := []struct {
		sevs []Severity
		want int
	}{
		{nil, 0},
		{[]Severity{SeverityHousekeeping, SeverityInfo}, 0},
		{[]Severity{SeverityWarning, SeverityHousekeeping}, 1},
		{[]Severity{SeverityWarning, SeverityBlocker}, 2},
	}
	for _, c := range cases {
		var fs []Finding
		for _, s := range c.sevs {
			fs = append(fs, finding("x", "NGD999", s, string(s)))
		}
		res := BuildResult([]CheckResult{{ID: "x", Status: StatusFailed, Findings: fs}})
		if res.ExitCode != c.want || res.Healthy != (c.want < 2) {
			t.Errorf("%v: exit %d healthy %v, want %d", c.sevs, res.ExitCode, res.Healthy, c.want)
		}
		if c.want == 0 && (len(res.Warnings) != 0 || len(res.Errors) != 0) {
			t.Errorf("%v: housekeeping/info fed warnings/errors: %v %v", c.sevs, res.Warnings, res.Errors)
		}
	}
}

// TestFindingRedaction: a token-shaped evidence value never reaches the human
// or JSON output.
func TestFindingRedaction(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	f := finding("tracked_secrets", "NGD024", SeverityBlocker, "credential committed")
	f.Evidence = map[string]string{"path": ".nightgauge/config.yaml", "line": "token: " + token, "token": "opaque"}
	f.Cause = "found " + token
	res := BuildResult([]CheckResult{{ID: "tracked_secrets", Status: StatusFailed, Findings: []Finding{f}}})

	js, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var human bytes.Buffer
	RenderHuman(&human, res, RenderOptions{})
	for name, out := range map[string]string{"json": string(js), "human": human.String()} {
		if strings.Contains(out, token) || strings.Contains(out, "opaque") {
			t.Errorf("%s output leaks the credential:\n%s", name, out)
		}
		if !strings.Contains(out, "[REDACTED]") {
			t.Errorf("%s output has no redaction marker:\n%s", name, out)
		}
	}
}

// TestRegistryFingerprintIsStable: same code and identity, same fingerprint.
func TestRegistryFingerprintIsStable(t *testing.T) {
	a := Fingerprint("NGD017", "/tmp/wt")
	if a != Fingerprint("NGD017", "/tmp/wt") || len(a) != 16 {
		t.Fatalf("fingerprint %q unstable or wrong length", a)
	}
	if a == Fingerprint("NGD017", "/tmp/other") || a == Fingerprint("NGD018", "/tmp/wt") {
		t.Fatal("distinct objects share a fingerprint")
	}
}
