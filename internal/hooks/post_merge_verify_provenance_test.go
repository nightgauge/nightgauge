package hooks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// provenReader is a MainCheckReader that can also tie the merge commit to its
// merged PR (#2055). Merge-commit reads come from the scripted frames; the PR
// head's checks are fixed.
type provenReader struct {
	scriptedChecks
	prov       *gh.MergeProvenance
	headChecks []forgetypes.CheckDetail
	headReads  int
}

func (p *provenReader) GetCommitChecks(ctx context.Context, owner, repo, ref string) ([]forgetypes.CheckDetail, error) {
	if p.prov != nil && ref == p.prov.HeadSHA {
		p.headReads++
		return p.headChecks, nil
	}
	return p.scriptedChecks.GetCommitChecks(ctx, owner, repo, ref)
}

func (p *provenReader) GetMergeProvenance(context.Context, string, string, string) (*gh.MergeProvenance, error) {
	return p.prov, nil
}

var provMergedAt = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func provWait(polls int, now time.Time) MainCheckWait {
	w := fastWait(polls, polls)
	w.Now = func() time.Time { return now }
	return w
}

func mergeProv(mergeTree, headTree string) *gh.MergeProvenance {
	return &gh.MergeProvenance{PRNumber: 7, MergeSHA: "abc1234", MergeTree: mergeTree, HeadSHA: "head999", HeadTree: headTree, BaseRef: "main", MergedAt: provMergedAt}
}

func TestVerifyMergeCommit_PRHeadIsTheGate(t *testing.T) {
	required := []string{"Go build & test", "lint"}
	headGreen := []forgetypes.CheckDetail{run("Go build & test", "COMPLETED", "SUCCESS"), run("lint", "COMPLETED", "SUCCESS")}
	headRed := []forgetypes.CheckDetail{run("Go build & test", "COMPLETED", "FAILURE"), run("lint", "COMPLETED", "SUCCESS")}
	late := provMergedAt.Add(time.Hour)

	t.Run("tree equal, head green, push green", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("CodeQL", "COMPLETED", "SUCCESS"), run("cache-warm", "COMPLETED", "SUCCESS")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(3, late))
		if res.Verdict != MainChecksGreen || res.TreesMatch == nil || !*res.TreesMatch || res.PRNumber != 7 {
			t.Fatalf("result = %+v, want green with matching trees for PR #7", res)
		}
	})

	t.Run("push job still running keeps the hook polling, then pending", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("publish", "IN_PROGRESS", "")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(4, late))
		if res.Verdict != MainChecksPending || res.Polls != 4 {
			t.Fatalf("result = %+v, want pending after all 4 polls", res)
		}
	})

	t.Run("push job goes green on a later poll", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{
				{run("publish", "IN_PROGRESS", "")},
				{run("publish", "COMPLETED", "SUCCESS")},
			}},
			prov: mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(4, late))
		if res.Verdict != MainChecksGreen || res.Polls != 2 {
			t.Fatalf("result = %+v, want green on poll 2", res)
		}
	})

	t.Run("tree equal, CodeQL still running: green on the first poll", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("Analyze (go)", "IN_PROGRESS", ""), run("CodeQL", "QUEUED", "")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(4, provMergedAt.Add(time.Minute)))
		if res.Verdict != MainChecksGreen || res.Polls != 1 {
			t.Fatalf("result = %+v, want green on poll 1: CodeQL is informational on the same tree", res)
		}
	})

	t.Run("tree equal, CodeQL failed: green, reported as info", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("Analyze (go)", "COMPLETED", "FAILURE"), run("CodeQL", "COMPLETED", "FAILURE")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(2, late))
		if res.Verdict != MainChecksGreen || len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "informational") {
			t.Fatalf("result = %+v, want green with an informational CodeQL reason", res)
		}
	})

	t.Run("tree differs, required CodeQL failed on the merge commit: red", func(t *testing.T) {
		req := append([]string{"CodeQL"}, required...)
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: req, frames: [][]forgetypes.CheckDetail{{run("Go build & test", "COMPLETED", "SUCCESS"), run("lint", "COMPLETED", "SUCCESS"), run("CodeQL", "COMPLETED", "FAILURE")}}},
			prov:           mergeProv("t1", "t2"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(2, late))
		if res.Verdict != MainChecksRed || len(res.Failing) != 1 || res.Failing[0].Name != "CodeQL" || !res.Failing[0].Required {
			t.Fatalf("result = %+v, want red naming the required CodeQL check", res)
		}
	})

	t.Run("PR head's required check red names it on the card", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("CodeQL", "COMPLETED", "FAILURE")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headRed,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(3, late))
		if res.Verdict != MainChecksRed || len(res.Failing) != 1 || res.Failing[0].Name != "Go build & test" || !res.Failing[0].Required {
			t.Fatalf("result = %+v, want red naming the required head check", res)
		}
	})

	t.Run("nothing runs on push: an empty merge commit is green after the grace", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(3, late))
		if res.Verdict != MainChecksGreen {
			t.Fatalf("result = %+v, want green", res)
		}
	})

	t.Run("tree differs inside the grace: pending", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("CodeQL", "COMPLETED", "SUCCESS")}}},
			prov:           mergeProv("t1", "t2"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(2, provMergedAt.Add(time.Minute)))
		if res.Verdict != MainChecksPending || res.TreesMatch == nil || *res.TreesMatch {
			t.Fatalf("result = %+v, want pending with trees not matching", res)
		}
		if r.headReads != 0 {
			t.Errorf("the PR head was read %d times; with different trees it is not evidence", r.headReads)
		}
	})

	t.Run("tree differs after the grace: red, and the card is raised", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("CodeQL", "COMPLETED", "SUCCESS")}}},
			prov:           mergeProv("t1", "t2"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(2, late))
		if res.Verdict != MainChecksRed || len(res.Failing) == 0 || !res.AnyRequiredFailing() {
			t.Fatalf("result = %+v, want red with a required failing entry", res)
		}
		card := BuildMainRedCard("o", "r", "main", 1, 7, res)
		if card.Severity != attention.SeverityBlockingFleet || card.Title == "" {
			t.Errorf("card = %+v, want a blocking card", card)
		}
		found := false
		for _, reason := range res.Reasons {
			found = found || reason == gh.UntestedTreeReason
		}
		if !found {
			t.Errorf("reasons %v do not name the remedy", res.Reasons)
		}
	})

	t.Run("a red cache-warm is informational", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{run("CodeQL", "COMPLETED", "SUCCESS"), run("cache-warm", "COMPLETED", "FAILURE")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(2, late))
		if res.Verdict != MainChecksGreen {
			t.Fatalf("result = %+v, want green: cache-warm tests nothing", res)
		}
	})

	t.Run("an unreadable required set is never green", func(t *testing.T) {
		r := &provenReader{
			scriptedChecks: scriptedChecks{reqErr: errors.New("403"), frames: [][]forgetypes.CheckDetail{{run("CodeQL", "COMPLETED", "SUCCESS")}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}
		res := VerifyMergeCommit(context.Background(), r, "o", "r", "main", "abc1234", provWait(2, late))
		if res.Verdict != MainChecksPending {
			t.Fatalf("result = %+v, want pending: the gate cannot be verified", res)
		}
	})
}

// pushAwareReader also reports whether any workflow can run on push (#2061).
type pushAwareReader struct {
	*provenReader
	canRun bool
	err    error
}

func (p pushAwareReader) PushWorkflowsCanRun(context.Context, string, string, string, string) (bool, error) {
	return p.canRun, p.err
}

func TestVerifyMergeCommit_NoPushWorkflows(t *testing.T) {
	required := []string{"Go build & test", "lint"}
	headGreen := []forgetypes.CheckDetail{run("Go build & test", "COMPLETED", "SUCCESS"), run("lint", "COMPLETED", "SUCCESS")}
	inGrace := provMergedAt.Add(time.Minute)
	reader := func(canRun bool, err error) pushAwareReader {
		return pushAwareReader{provenReader: &provenReader{
			scriptedChecks: scriptedChecks{required: required, frames: [][]forgetypes.CheckDetail{{}}},
			prov:           mergeProv("t1", "t1"), headChecks: headGreen,
		}, canRun: canRun, err: err}
	}

	if res := VerifyMergeCommit(context.Background(), reader(false, nil), "o", "r", "main", "abc1234", provWait(4, inGrace)); res.Verdict != MainChecksGreen || res.Polls != 1 {
		t.Errorf("nothing runs on push: result = %+v, want green on poll 1 inside the grace", res)
	}
	if res := VerifyMergeCommit(context.Background(), reader(true, nil), "o", "r", "main", "abc1234", provWait(4, inGrace)); res.Verdict == MainChecksGreen {
		t.Errorf("a push workflow exists: result = %+v, want the grace to hold", res)
	}
	if res := VerifyMergeCommit(context.Background(), reader(false, errors.New("500")), "o", "r", "main", "abc1234", provWait(4, inGrace)); res.Verdict == MainChecksGreen {
		t.Errorf("workflows unreadable: result = %+v, want the grace to hold", res)
	}
}
