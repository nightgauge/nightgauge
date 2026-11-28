package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/sizeGate"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
)

// needsDecompositionLabel marks an issue whose plan was stopped as too large
// for it (#3684). An operator splits or relabels the issue, removes the label
// and runs clear-failures.
const needsDecompositionLabel = "needs-decomposition"

// needsDecompositionCommentMarker is the hidden marker that keeps the explanatory
// comment to one per issue across retries.
const needsDecompositionCommentMarker = "<!-- nightgauge:needs-decomposition -->"

// The label's description and colour, used when the label must be created.
const (
	needsDecompositionLabelDescription = "The plan is too large for the issue: split it or relabel its size"
	needsDecompositionLabelColor       = "d93f0b"
)

// needsDecompositionDetail is what FeaturePlanningGate's evidence records
// about an oversized plan; a field the text does not carry stays zero.
type needsDecompositionDetail struct {
	PlannedFiles    int
	MaxPlannedFiles int
	PlanSize        string
	IssueSize       string
}

// parseNeedsDecompositionDetail recovers the gate's key=value evidence lines
// (planned_files, max_planned_files, plan_size, issue_size) from the failure
// text, wherever they sit in it.
func parseNeedsDecompositionDetail(text string) needsDecompositionDetail {
	var d needsDecompositionDetail
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == ',' || r == ';' || r == '|' || r == '[' || r == ']'
	}) {
		key, val, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		switch key {
		case gates.EvidencePlannedFiles:
			if n, err := strconv.Atoi(val); err == nil {
				d.PlannedFiles = n
			}
		case gates.EvidenceMaxPlannedFiles:
			if n, err := strconv.Atoi(val); err == nil {
				d.MaxPlannedFiles = n
			}
		case gates.EvidencePlanSize:
			// Only a known size reaches the public comment: on the extension
			// path the text can be caller-supplied prose.
			d.PlanSize = sizeGate.NormalizeSize(val)
		case gates.EvidenceIssueSize:
			d.IssueSize = sizeGate.NormalizeSize(val)
		}
	}
	return d
}

// needsDecompositionComment is the one comment posted on an issue whose plan
// was stopped before feature-dev. It names the planned file count, the
// threshold and both sizes.
func needsDecompositionComment(issue int, d needsDecompositionDetail) string {
	orUnknown := func(s string) string {
		if s == "" {
			return "unknown"
		}
		return s
	}
	threshold := strconv.Itoa(d.MaxPlannedFiles)
	if d.MaxPlannedFiles == 0 {
		threshold = "0 (file-count check disabled)"
	}
	var b strings.Builder
	b.WriteString(needsDecompositionCommentMarker + "\n")
	fmt.Fprintf(&b, "The pipeline stopped #%d after planning, before feature-dev, because the plan is too large "+
		"for the issue (#3684):\n\n", issue)
	fmt.Fprintf(&b, "- Planned files: %d (`pipeline.size_gate.thresholds.max_planned_files`: %s)\n",
		d.PlannedFiles, threshold)
	fmt.Fprintf(&b, "- Plan size: %s\n", orUnknown(d.PlanSize))
	fmt.Fprintf(&b, "- Issue size label: %s\n\n", orUnknown(d.IssueSize))
	fmt.Fprintf(&b, "Split the issue into sub-issues or relabel its size, remove the `%s` label, then release it "+
		"with `nightgauge autonomous clear-failures <owner/repo#N>`.\n", needsDecompositionLabel)
	return b.String()
}

// needsDecompositionOps is the forge surface applyNeedsDecomposition uses.
type needsDecompositionOps interface {
	HasMarkerComment(ctx context.Context, owner, repo string, issue int, marker string) (bool, error)
	AddComment(ctx context.Context, owner, repo string, issue int, body string) error
	AddLabel(ctx context.Context, owner, repo string, issue int, label string) error
}

// applyNeedsDecomposition labels the issue needs-decomposition and posts the
// explanatory comment unless one carrying the marker already exists. A label
// failure does not withhold the comment; every error is returned joined.
func applyNeedsDecomposition(ctx context.Context, ops needsDecompositionOps, repo string, issue int, detail string) error {
	owner, name := splitOwnerRepo(repo)
	var errs []error
	if err := ops.AddLabel(ctx, owner, name, issue, needsDecompositionLabel); err != nil {
		errs = append(errs, fmt.Errorf("label: %w", err))
	}
	has, err := ops.HasMarkerComment(ctx, owner, name, issue, needsDecompositionCommentMarker)
	switch {
	case err != nil:
		// Without the read the dedupe cannot hold, so no comment is posted.
		errs = append(errs, fmt.Errorf("read comments: %w", err))
	case !has:
		body := needsDecompositionComment(issue, parseNeedsDecompositionDetail(detail))
		if err := ops.AddComment(ctx, owner, name, issue, body); err != nil {
			errs = append(errs, fmt.Errorf("comment: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("needs-decomposition %s#%d: %w", repo, issue, err)
	}
	return nil
}

// NeedsDecompositionClientResolver returns the GitHub client for owner/repo.
type NeedsDecompositionClientResolver func(ctx context.Context, owner, repo string) (*gh.Client, error)

// ghNeedsDecompositionOps is needsDecompositionOps over the GitHub client the
// caller resolves for the repository. It serves one issue: the client and the
// issue's node id are resolved once and reused.
type ghNeedsDecompositionOps struct {
	resolve NeedsDecompositionClientResolver
	c       *gh.Client
	nodeID  string
}

func (o *ghNeedsDecompositionOps) client(ctx context.Context, owner, repo string) (*gh.Client, error) {
	if o.c == nil {
		if o.resolve == nil {
			return nil, fmt.Errorf("no GitHub client resolver configured for %s/%s", owner, repo)
		}
		c, err := o.resolve(ctx, owner, repo)
		if err != nil {
			return nil, err
		}
		o.c = c
	}
	return o.c, nil
}

func (o *ghNeedsDecompositionOps) issueNodeID(ctx context.Context, c *gh.Client, owner, repo string, issue int) (string, error) {
	if o.nodeID != "" {
		return o.nodeID, nil
	}
	iss, err := gh.NewIssueService(c).GetIssue(ctx, owner, repo, issue)
	if err != nil {
		return "", fmt.Errorf("read %s/%s#%d: %w", owner, repo, issue, err)
	}
	if iss == nil || iss.NodeID == "" {
		return "", fmt.Errorf("%s/%s#%d has no node id", owner, repo, issue)
	}
	o.nodeID = iss.NodeID
	return o.nodeID, nil
}

func (o *ghNeedsDecompositionOps) HasMarkerComment(ctx context.Context, owner, repo string, issue int, marker string) (bool, error) {
	c, err := o.client(ctx, owner, repo)
	if err != nil {
		return false, err
	}
	comments, err := gh.NewTrustForge(c).IssueComments(ctx, owner, repo, issue)
	if err != nil {
		return false, err
	}
	for _, cm := range comments {
		if strings.Contains(cm.Body, marker) {
			return true, nil
		}
	}
	return false, nil
}

func (o *ghNeedsDecompositionOps) AddComment(ctx context.Context, owner, repo string, issue int, body string) error {
	c, err := o.client(ctx, owner, repo)
	if err != nil {
		return err
	}
	id, err := o.issueNodeID(ctx, c, owner, repo, issue)
	if err != nil {
		return err
	}
	return gh.NewIssueService(c).AddComment(ctx, id, body)
}

// AddLabel creates the label when the repository lacks it (LabelService.Create
// is idempotent) and applies it.
func (o *ghNeedsDecompositionOps) AddLabel(ctx context.Context, owner, repo string, issue int, label string) error {
	c, err := o.client(ctx, owner, repo)
	if err != nil {
		return err
	}
	l, err := gh.NewLabelService(c, owner, repo).Create(ctx, label,
		needsDecompositionLabelDescription, needsDecompositionLabelColor)
	if err != nil {
		return err
	}
	id, err := o.issueNodeID(ctx, c, owner, repo, issue)
	if err != nil {
		return err
	}
	return gh.NewIssueService(c).AddLabels(ctx, id, []string{l.ID})
}

// NeedsDecompositionTimeout bounds one label-and-comment action. It runs on a
// failure path and never changes the run's result.
const NeedsDecompositionTimeout = 2 * time.Minute

// ApplyNeedsDecompositionForge labels the issue needs-decomposition and posts
// the one explanatory comment through the client resolve returns (#3684).
// detail is the gate's failure text: its key=value evidence names the planned
// file count, the threshold and both sizes. Every terminal funnel that books a
// needs_decomposition run calls it: the Go scheduler's stage-failure path and
// the extension's pipeline.notifyComplete.
func ApplyNeedsDecompositionForge(ctx context.Context, resolve NeedsDecompositionClientResolver, repo string, issue int, detail string) error {
	return applyNeedsDecomposition(ctx, &ghNeedsDecompositionOps{resolve: resolve}, repo, issue, detail)
}

// labelIfNeedsDecomposition is the Scheduler funnel's action for a stage
// failure: when its resolved kind is needs_decomposition, the issue is labelled
// and commented. Best-effort: bounded by NeedsDecompositionTimeout and logged,
// never returned, so it never changes the run's result. It keeps the run's
// cancellation, so a shutdown cuts the wait short.
func (s *Scheduler) labelIfNeedsDecomposition(ctx context.Context, repo string, issue int, kind, detail string) {
	if kind != TerminalKindNeedsDecomposition {
		return
	}
	apply := s.applyNeedsDecompositionFn
	if apply == nil {
		apply = func(ctx context.Context, repo string, issue int, detail string) error {
			return ApplyNeedsDecompositionForge(ctx, s.repoClient, repo, issue, detail)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, NeedsDecompositionTimeout)
	defer cancel()
	if err := apply(ctx, repo, issue, detail); err != nil {
		log.Printf("scheduler: %v", err)
	}
}
