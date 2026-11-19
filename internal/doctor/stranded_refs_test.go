package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/reclaim"
)

func fixedStates(m map[int]string, err error) issueStateFactory {
	return func(string) func(int) (string, error) {
		return func(n int) (string, error) {
			if err != nil {
				return "", err
			}
			return m[n], nil
		}
	}
}

func pinStranded(r *leakRepo, issue int) (string, string) {
	sha := strings.TrimSpace(r.git("rev-parse", "main"))
	ref := reclaim.StrandedRefName(issue)
	r.git("update-ref", ref, sha)
	return ref, sha
}

func TestStrandedRefsClosedIssueGetsAutoPrune(t *testing.T) {
	r := newLeakRepo(t)
	ref, sha := pinStranded(r, 41)
	fs, _ := strandedRefFindings(r.dir, fixedStates(map[int]string{41: "CLOSED"}, nil))
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	f := fs[0]
	if !strings.Contains(f.Title, ref) || !strings.Contains(f.Title, "#41") || f.Evidence["ref"] != ref || f.Evidence["commit"] != sha {
		t.Errorf("finding does not name ref and issue: %q %v", f.Title, f.Evidence)
	}
	if f.Remedies[0].Kind != RemedyAuto || f.Remedies[0].Verb != verbStrandedRefPrune {
		t.Errorf("first remedy = %+v, want auto prune", f.Remedies[0])
	}
}

func TestStrandedRefsOpenIssueIsKept(t *testing.T) {
	r := newLeakRepo(t)
	pinStranded(r, 42)
	fs, detail := strandedRefFindings(r.dir, fixedStates(map[int]string{42: "OPEN"}, nil))
	if len(fs) != 0 {
		t.Fatalf("open issue produced findings: %v", fs)
	}
	if !strings.Contains(detail, "1 kept: issue open") {
		t.Errorf("detail = %q", detail)
	}
}

func TestStrandedRefsUnverifiableIsManualOnly(t *testing.T) {
	cases := map[string]issueStateFactory{
		"lookup error":  fixedStates(nil, errors.New("api down")),
		"nil lookup":    func(string) func(int) (string, error) { return nil },
		"nil factory":   nil,
		"unknown state": fixedStates(map[int]string{43: "MERGED"}, nil),
	}
	for name, states := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLeakRepo(t)
			ref, _ := pinStranded(r, 43)
			fs, _ := strandedRefFindings(r.dir, states)
			assertUnverifiable(t, fs, ref, "#43")
		})
	}
	t.Run("malformed suffix", func(t *testing.T) {
		r := newLeakRepo(t)
		sha := strings.TrimSpace(r.git("rev-parse", "main"))
		ref := reclaim.StrandedRefNamespace + "nope"
		r.git("update-ref", ref, sha)
		fs, _ := strandedRefFindings(r.dir, fixedStates(map[int]string{}, nil))
		assertUnverifiable(t, fs, ref, "unknown issue")
	})
}

func assertUnverifiable(t *testing.T, fs []Finding, ref, issue string) {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("findings = %d, want 1", len(fs))
	}
	f := fs[0]
	if f.Evidence["state"] != "unverifiable" || !strings.Contains(f.Title, ref) || !strings.Contains(f.Title, issue) {
		t.Errorf("finding = %q %v", f.Title, f.Evidence)
	}
	for _, rem := range f.Remedies {
		if rem.Kind != RemedyManual {
			t.Errorf("unverifiable finding offers a %s remedy", rem.Kind)
		}
	}
}

func strandedVerbs(states issueStateFactory) *verbs {
	return &verbs{env: &Env{}, deps: verbDeps{issueStates: states}}
}

func TestStrandedRefPruneVerb(t *testing.T) {
	r := newLeakRepo(t)
	ref, _ := pinStranded(r, 44)
	fs, _ := strandedRefFindings(r.dir, fixedStates(map[int]string{44: "CLOSED"}, nil))
	f := fs[0]
	ctx := context.Background()

	if err := strandedVerbs(fixedStates(map[int]string{44: "OPEN"}, nil)).strandedRefPrecondition(ctx, f); err == nil || !strings.Contains(err.Error(), "not confirmed closed") {
		t.Errorf("precondition on a reopened issue = %v, want not-confirmed-closed refusal", err)
	}
	if err := strandedVerbs(fixedStates(nil, errors.New("x"))).strandedRefApply(ctx, f); !errors.Is(err, ErrRemedyBlocked) {
		t.Errorf("apply on state error = %v, want ErrRemedyBlocked", err)
	}
	moved := f
	moved.Evidence = map[string]string{}
	for k, v := range f.Evidence {
		moved.Evidence[k] = v
	}
	moved.Evidence["commit"] = strings.Repeat("0", 39) + "1"
	closed := strandedVerbs(fixedStates(map[int]string{44: "CLOSED"}, nil))
	if err := closed.strandedRefPrecondition(ctx, moved); err == nil || !strings.Contains(err.Error(), "now points at") {
		t.Errorf("precondition on a moved ref = %v, want now-points-at refusal", err)
	}
	if err := closed.strandedRefApply(ctx, f); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got, _ := reclaim.ResolveStrandedRef(r.dir, ref); got != "" {
		t.Errorf("ref still present at %s", got)
	}
	if err := closed.strandedRefApply(ctx, f); !errors.Is(err, ErrRemedyBlocked) || !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("second apply = %v, want blocked: no longer exists", err)
	}
}

func TestStrandedRefsNoneAndNoRoots(t *testing.T) {
	r := newLeakRepo(t)
	if fs, detail := strandedRefFindings(r.dir, nil); len(fs) != 0 || detail != "no stranded fork refs" {
		t.Errorf("clean repo: %v %q", fs, detail)
	}
	fs, _ := strandedRefFindings(t.TempDir(), nil)
	if len(fs) != 1 || !strings.Contains(fs[0].Title, "unverifiable") {
		t.Errorf("no roots: %v, want one unverifiable finding", fs)
	}
}
