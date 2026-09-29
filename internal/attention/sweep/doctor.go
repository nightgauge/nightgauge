package sweep

// Workspace producer: doctor findings as standing cards (ADR-025 § 7).
//
// The Action Center is where an operator looks for "what needs me", and the
// doctor is what knows whether the pipeline can run. This producer runs the
// doctor registry once per sweep and raises one standing card per blocker or
// warning finding, keyed by the finding's fingerprint. Housekeeping and info
// never raise a card: they do not degrade the pipeline, and a card for each
// leaked worktree would bury the ones that do.
//
// It replaces two producers that re-implemented doctor checks with their own
// thresholds: the API-budget producer (the `github_api_budget` check, NGD008)
// and the board-reachability half of stranded-ready-items (the
// `board_population` and `project_mapping` checks, NGD012 and NGD013). Each
// condition now has one threshold, the doctor's.
//
// Remedies become the card's options. An auto or confirm remedy is an option
// on doctor.applyRemedy carrying only the fingerprint and the remedy ID; the
// remedy engine re-runs the check, applies the registered Go verb and
// verifies it. A manual remedy becomes the body's steps and links. Every card
// also offers doctor.recheck, which resolves it once the check stops
// reporting the fingerprint.
//
// Invariant 1 applies per check as well as per producer. A scan that could
// not run is an error, which leaves every doctor card untouched. A check that
// timed out or was skipped behind a failed dependency did not observe its
// condition either, so its open cards are carried forward rather than
// retracted.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/doctor"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// ProducerDoctor is the stable producer id. It is half of the sticky
// (producer, idempotency_key) identity, so it must never change.
const ProducerDoctor = attention.DoctorProducer

// Doctor raises one standing card per blocker or warning doctor finding.
type Doctor struct {
	// Scan is a seam for tests. Nil runs the doctor registry for the
	// workspace root, as `nightgauge doctor` does.
	Scan func(ctx context.Context, workspaceRoot string) ([]doctor.CheckResult, error)
	// PrimaryRepo is a seam for tests. Nil reads the primary repository from
	// the workspace config.
	PrimaryRepo func(workspaceRoot string) string
}

func init() { Default.RegisterWorkspace(&Doctor{}) }

// Name implements WorkspaceProducer.
func (p *Doctor) Name() string { return ProducerDoctor }

// Evaluate implements WorkspaceProducer.
func (p *Doctor) Evaluate(ctx context.Context, in WorkspaceInput) ([]attention.DecisionRequest, error) {
	root := strings.TrimSpace(in.WorkspaceRoot)
	if root == "" {
		return nil, fmt.Errorf("doctor: no workspace root, so the doctor cannot run; that is not a clean report")
	}
	repo := p.cardRepo(root, in.ConfiguredRepos)
	if repo == "" {
		return nil, fmt.Errorf("doctor: no repository to scope the workspace's cards to")
	}
	scan := p.Scan
	if scan == nil {
		scan = doctorScan
	}
	results, err := scan(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("doctor: scan failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		// Checks cut short by the sweep deadline report NGD000 instead of their
		// own findings; reconciling that would retract every real card.
		return nil, fmt.Errorf("doctor: scan cut short: %w", err)
	}

	var out []attention.DecisionRequest
	seen := map[string]bool{}
	incomplete := map[string]bool{}
	for _, r := range results {
		if r.Status == doctor.StatusTimeout || r.Status == doctor.StatusSkipped {
			incomplete[r.ID] = true
		}
		for _, f := range r.Findings {
			if f.Severity != doctor.SeverityBlocker && f.Severity != doctor.SeverityWarning {
				continue
			}
			card, ok := doctorCard(f, repo)
			if !ok || seen[card.IdempotencyKey] {
				continue
			}
			seen[card.IdempotencyKey] = true
			out = append(out, card)
		}
	}

	// A check that did not complete observed nothing: keep its open cards.
	for _, e := range in.Existing {
		if e.Producer != ProducerDoctor || e.Lifecycle.State.IsTerminal() || seen[e.IdempotencyKey] {
			continue
		}
		check, _, ok := attention.ParseDoctorCardKey(e.IdempotencyKey)
		if !ok || !incomplete[check] {
			continue
		}
		carried := e
		carried.Lifecycle = attention.Lifecycle{}
		seen[carried.IdempotencyKey] = true
		out = append(out, carried)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].IdempotencyKey < out[j].IdempotencyKey })
	return out, nil
}

// cardRepo is the repository every doctor card is scoped to: the workspace's
// primary repository, else the first configured one. The doctor reports on
// the workspace, but a card needs a repository scope to be reconciled.
func (p *Doctor) cardRepo(root string, configured []string) string {
	primary := p.PrimaryRepo
	if primary == nil {
		primary = primaryRepo
	}
	if r := strings.TrimSpace(primary(root)); r != "" {
		return r
	}
	for _, r := range configured {
		if r = strings.TrimSpace(r); r != "" {
			return r
		}
	}
	return ""
}

func primaryRepo(root string) string {
	cfg, err := config.Load(root)
	if err != nil || cfg == nil || cfg.Owner == "" || cfg.DefaultRepo == "" {
		return ""
	}
	return cfg.Owner + "/" + cfg.DefaultRepo
}

// newDoctorFixer builds the remedy engine for a workspace root the way
// `nightgauge doctor` builds it for the current directory. A config that
// fails to load is the config check's finding, not a scan failure, and a
// credential that cannot be resolved leaves the GitHub checks to report it.
func newDoctorFixer(root string) *doctor.Fixer {
	cfg, cfgErr := config.Load(root)
	var client *gh.Client
	if cfgErr == nil {
		owner := ""
		if cfg != nil {
			owner = cfg.Owner
		}
		if c, err := gh.NewClientFromConfig(cfg, owner, ""); err == nil {
			client = c
		}
	}
	// NewFixer's error only says the fix log is unavailable; the Fixer works.
	fx, _ := doctor.NewFixer(cfg, cfgErr, client, nil)
	fx.Env.Cwd = root
	return fx
}

func scanDoctor(ctx context.Context, root string) ([]doctor.CheckResult, error) {
	return newDoctorFixer(root).Scan(ctx), nil
}

// doctorScan is the registered producer's scan. Only SwapDoctorScanForTest
// changes it.
var doctorScan = scanDoctor

// SwapDoctorScanForTest replaces the scan the registered doctor producer runs
// and returns the restore function. A package whose tests drive the default
// sweep registry calls it from TestMain, so no test runs the real doctor
// against the host (its binaries, credentials and GitHub).
func SwapDoctorScanForTest(scan func(ctx context.Context, workspaceRoot string) ([]doctor.CheckResult, error)) func() {
	prev := doctorScan
	doctorScan = scan
	return func() { doctorScan = prev }
}

// doctorCard builds the standing card for one blocker or warning finding.
// Everything operator-facing is redacted first. ok is false for a finding
// whose check or fingerprint cannot form a card key (the verbs would refuse
// it, so the card would have no working option).
func doctorCard(raw doctor.Finding, repo string) (attention.DecisionRequest, bool) {
	f := doctor.RedactFinding(raw)
	key := attention.DoctorCardKey(f.Check, f.Fingerprint)
	if _, _, ok := attention.ParseDoctorCardKey(key); !ok {
		return attention.DecisionRequest{}, false
	}

	severity := attention.SeverityFYI
	if f.Severity == doctor.SeverityBlocker {
		severity = attention.SeverityBlockingFleet
	}

	var options []attention.Option
	var manual []doctor.Remedy
	var link string
	for _, r := range f.Remedies {
		switch r.Kind {
		case doctor.RemedyAuto, doctor.RemedyConfirm:
			if r.Verb == "" {
				continue
			}
			style := attention.StyleDefault
			if len(options) == 0 {
				style = attention.StylePrimary
			}
			label := r.Summary
			if r.Kind == doctor.RemedyConfirm {
				label += " (changes something others can see)"
			}
			options = append(options, attention.Option{
				ID:    "apply-" + r.ID,
				Label: label,
				Verb:  attention.VerbDoctorApplyRemedy,
				Args: map[string]any{
					attention.DoctorOptionArgFingerprint: f.Fingerprint,
					attention.DoctorOptionArgRemedyID:    r.ID,
				},
				Style: style,
			})
		case doctor.RemedyManual:
			manual = append(manual, r)
			for _, l := range r.Links {
				if link == "" && allowedDoctorLink(l) {
					link = l
				}
			}
		}
	}
	options = append(options, attention.Option{
		ID:    "recheck",
		Label: "Re-check: I fixed it",
		Verb:  attention.VerbDoctorRecheck,
		Args:  map[string]any{attention.DoctorOptionArgFingerprint: f.Fingerprint},
		Style: attention.StyleDefault,
	})

	return attention.DecisionRequest{
		IdempotencyKey: key,
		Kind:           attention.KindUnblock,
		Severity:       severity,
		Title:          fmt.Sprintf("%s: %s", f.Code, f.Title),
		Body:           doctorCardBody(f, manual),
		// The finding's fingerprint names the object; the severity is appended
		// so a warning that becomes a blocker alerts again.
		Fingerprint: f.Fingerprint + ":" + string(f.Severity),
		Context: attention.Context{
			Repo:    repo,
			Blocker: fmt.Sprintf("doctor %s (%s)", f.Code, f.Check),
			URL:     link,
		},
		Options:       options,
		DefaultAction: attention.ExpireNoop,
	}, true
}

func doctorCardBody(f doctor.Finding, manual []doctor.Remedy) string {
	var b strings.Builder
	cause := strings.TrimSpace(f.Cause)
	if cause != "" {
		b.WriteString(cause)
		b.WriteString("\n")
	}
	if len(f.Evidence) > 0 {
		keys := make([]string, 0, len(f.Evidence))
		for k := range f.Evidence {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\nEvidence:\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, f.Evidence[k])
		}
	}
	for _, r := range f.Remedies {
		if (r.Kind == doctor.RemedyAuto || r.Kind == doctor.RemedyConfirm) && r.Verb != "" && r.Preview != "" {
			fmt.Fprintf(&b, "\n%s (%s): %s\n", r.Summary, r.Kind, r.Preview)
		}
	}
	for _, r := range manual {
		fmt.Fprintf(&b, "\n%s:\n", r.Summary)
		for i, s := range r.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
		}
		for _, l := range r.Links {
			if allowedDoctorLink(l) {
				fmt.Fprintf(&b, "  %s\n", l)
			} else {
				fmt.Fprintf(&b, "  %s (not a link: host not allowlisted)\n", l)
			}
		}
	}
	fmt.Fprintf(&b, "\nCheck %s, code %s. Reference: %s", f.Check, f.Code, f.Docs)
	return b.String()
}

// allowedDoctorLink applies ADR-025 § 8's link allowlist: https on
// github.com, docs.github.com or nightgauge.dev. Finding text can derive from
// repository content, so any other link is shown as text and never offered as
// the card's URL.
func allowedDoctorLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	switch u.Hostname() {
	case "github.com", "docs.github.com", "nightgauge.dev":
		return u.Port() == ""
	}
	return false
}

// DoctorRemedies implements attention.DoctorRemedyRunner over the doctor
// remedy engine for one workspace root. It is what the doctor card verbs
// execute through, on the CLI and in the daemon alike.
type DoctorRemedies struct {
	WorkspaceRoot string
	// NewFixer is a seam for tests. Nil builds the engine the way the doctor
	// producer's scan does.
	NewFixer func(workspaceRoot string) *doctor.Fixer
}

var _ attention.DoctorRemedyRunner = DoctorRemedies{}

func (d DoctorRemedies) fixer() (*doctor.Fixer, error) {
	root := strings.TrimSpace(d.WorkspaceRoot)
	if root == "" {
		return nil, fmt.Errorf("no workspace root to run the doctor in")
	}
	if d.NewFixer != nil {
		return d.NewFixer(root), nil
	}
	return newDoctorFixer(root), nil
}

// ApplyRemedy implements attention.DoctorRemedyRunner. Choosing the card
// option is the operator's consent, so a confirm remedy is applied.
func (d DoctorRemedies) ApplyRemedy(ctx context.Context, check, fingerprint, remedyID string) (attention.DoctorRemedyOutcome, error) {
	fx, err := d.fixer()
	if err != nil {
		return attention.DoctorRemedyOutcome{}, err
	}
	res := fx.ApplyFingerprint(ctx, check, fingerprint, remedyID, true)
	return attention.DoctorRemedyOutcome{Outcome: string(res.Outcome), Detail: res.Detail}, nil
}

// Recheck implements attention.DoctorRemedyRunner.
func (d DoctorRemedies) Recheck(ctx context.Context, check, fingerprint string) (bool, error) {
	fx, err := d.fixer()
	if err != nil {
		return false, err
	}
	res, ok := fx.Recheck(ctx, check)
	if !ok {
		return false, fmt.Errorf("check %q is not registered", check)
	}
	if res.Status == doctor.StatusTimeout || res.Status == doctor.StatusSkipped {
		return false, fmt.Errorf("check %q did not complete (%s)", check, res.Status)
	}
	for _, f := range res.Findings {
		if f.Fingerprint == fingerprint {
			return true, nil
		}
	}
	return false, nil
}
