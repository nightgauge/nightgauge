package followup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/filing"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// EmptyCaptureReason is the Result.Reason of a run with no records (#2638):
// a silent no-op cannot be told apart from a materializer that never ran.
const EmptyCaptureReason = "empty capture: no discovered-work records"

// RunEndTimeout bounds MaterializeOnRunEnd's forge calls on both terminal
// paths, so a slow forge cannot hold a run's terminal bookkeeping open.
const RunEndTimeout = 2 * time.Minute

// ErrNoForgeClient is ForgeFilerFactory's file-mode error when no client or
// config is wired.
var ErrNoForgeClient = errors.New("no forge client configured for filing")

// FilerFactory builds the Filer for one materialize run under gate, and
// reports whether the source issue's author is trusted. A nil Filer is valid
// in report mode: Materialize then skips the parent-body read.
type FilerFactory func(ctx context.Context, gate filing.Gate) (f Filer, trusted bool, err error)

// RunMaterialize is the whole followup-materialize sequence (#3685), shared by
// the CLI and both terminal paths of a run that ends without success: load the
// capture in runDir, return an empty-capture Result without building a filer,
// otherwise Materialize and consume what was decided. dryRun keeps the
// capture. A cap refusal is returned after consuming; IsCapRefusal names it.
func RunMaterialize(ctx context.Context, runDir string, issue int, gate filing.Gate, newFiler FilerFactory, dryRun bool) (*Result, error) {
	capture, err := discoveredwork.Load(runDir, issue)
	if err != nil {
		return nil, err
	}
	if capture == nil || len(capture.Records) == 0 {
		return &Result{Issue: issue, Reason: EmptyCaptureReason, Items: []Item{}, Empty: true}, nil
	}
	filer, trusted, err := newFiler(ctx, gate)
	if err != nil {
		return nil, err
	}
	track := LoadTracking(runDir, issue)
	res, matErr := Materialize(ctx, capture, gate, trusted, track, filer)
	keep := func(r discoveredwork.Record) bool { return res == nil || !res.Consumed(r.ID) }
	if matErr != nil && !IsCapRefusal(matErr) {
		// Consume what was decided before the error so a retry does not redo
		// it; undecided records stay.
		if res != nil {
			if err := discoveredwork.Consume(runDir, issue, keep); err != nil {
				return res, errors.Join(matErr, fmt.Errorf("consume capture: %w", err))
			}
		}
		return res, matErr
	}
	if !dryRun {
		if err := discoveredwork.Consume(runDir, issue, keep); err != nil {
			return res, fmt.Errorf("consume capture: %w", err)
		}
	}
	return res, matErr
}

// MaterializeOnRunEnd materializes the discovered work of a run that ended
// without success (#3685), under workdir's follow_up_filing gate, so its
// records do not sit on disk as an NGD050 orphan until an operator runs
// `nightgauge followup materialize`. File mode files them; report mode logs
// them as would-file and removes them from the capture. It never fails the
// caller: an error, a cap refusal included, is logged with the issue number,
// and the records it left undecided stay for the next attempt.
func MaterializeOnRunEnd(ctx context.Context, runDir, workdir string, issue int, newFiler FilerFactory) {
	res, err := RunMaterialize(ctx, runDir, issue, filing.ResolveGate(workdir), newFiler, false)
	if err == nil && res.Empty {
		return
	}
	if err != nil {
		log.Printf("#%d: followup-materialize on unsuccessful run end failed: %v — records left for the next attempt", issue, err)
		return
	}
	mode := "file"
	if res.ReportOnly {
		mode = "report-only: " + res.Reason
	}
	log.Printf("#%d: followup-materialize on unsuccessful run end (%s): %d record(s) decided", issue, mode, len(res.Items))
}

// ForgeFilerFactory resolves the live GitHubFiler for repo ("owner/name";
// empty means cfg's default repository) and issue's author trust. resolve
// picks the per-repository client (#2837): a nil resolver or (nil, nil) keeps
// client. In report mode the forge is only read, for the parent's body, so an
// unresolved project, a resolver error or a malformed repo degrade instead of
// failing; in file mode each is an error (filing under the wrong identity is
// the defect).
func ForgeFilerFactory(client *gh.Client, cfg *config.Config, resolve ClientResolver, workdir, repo string, issue int) FilerFactory {
	return func(ctx context.Context, gate filing.Gate) (Filer, bool, error) {
		if client == nil || cfg == nil {
			if gate.Files() {
				return nil, false, ErrNoForgeClient
			}
			return nil, false, nil
		}
		if repo == "" {
			repo = cfg.Owner + "/" + cfg.DefaultRepo
		}
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || owner == "" || name == "" {
			if !gate.Files() {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("repository %q is not owner/name", repo)
		}
		project, err := config.ResolveRepoProjectNumber(cfg, config.RepoProjectQuery{Owner: owner, Repo: name})
		if err != nil && gate.Files() {
			return nil, false, err
		}
		repoClient := client
		if resolve != nil {
			c, rerr := resolve(ctx, owner, name)
			switch {
			case rerr != nil && gate.Files():
				return nil, false, fmt.Errorf("resolve client for %s/%s: %w", owner, name, rerr)
			case rerr == nil && c != nil:
				repoClient = c
			}
		}
		// Report mode makes no board call, so an unresolved project (0) is fine.
		filer := NewGitHubFiler(repoClient, owner, name, project, gh.ParseOwnerType(cfg.OwnerType)).
			WithTrustedBots(TrustedBots(workdir))
		if !gate.Files() {
			return filer, false, nil
		}
		return filer, AuthorTrusted(ctx, repoClient, workdir, owner, name, issue), nil
	}
}
