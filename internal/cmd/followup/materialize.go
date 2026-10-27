// Package followup implements the followup-materialize stage (#480): it reads
// a run's discovered-work capture (internal/cmd/discoveredwork, ADR 027) and
// files each record as a follow-up issue under the Rule 5 controls shared
// with spike-materialize (internal/filing): closed enums, a fail-closed gate,
// report-only mode, a loud per-run cap and a provenance line.
//
// Nothing from a record selects a repository, board, label set or assignee,
// and nothing from a record is passed to a shell or used as a path. The
// forge is reached only through the Filer interface, so the decisions here
// are deterministic and tested with a fake.
package followup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/filing"
)

// Body bounds for record-supplied text.
const (
	maxEvidenceRender = 1000
	maxFilesRender    = 2000
)

// Filer is the forge surface the materializer needs.
type Filer interface {
	// FindByMarker returns an existing issue whose body carries marker, or 0.
	FindByMarker(ctx context.Context, marker string) (int, string, error)
	// FindOpenByTitle returns an open issue whose normalized title equals
	// title's, or 0.
	FindOpenByTitle(ctx context.Context, title string) (int, string, error)
	// HasComment reports whether issue number already carries a comment
	// containing marker.
	HasComment(ctx context.Context, number int, marker string) (bool, error)
	// AddComment comments on issue number.
	AddComment(ctx context.Context, number int, body string) error
	// CreateIssue creates the issue with the type:<typ> label, adds it to the
	// configured project and sets Priority, Size and Status=Backlog.
	CreateIssue(ctx context.Context, title, body, typ, priority, size string) (int, string, error)
}

// Outcome values for one record.
const (
	OutcomeFiled         = "filed"
	OutcomeAlreadyExists = "already-exists"
	OutcomeDeduped       = "deduped"
	OutcomeWouldFile     = "would-file"
	OutcomeRefused       = "refused"
	OutcomeInvalid       = "invalid"
)

// Item is the result for one record.
type Item struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Type        string `json:"type"`
	Priority    string `json:"priority"`
	Size        string `json:"size"`
	SourceStage string `json:"source_stage"`
	Outcome     string `json:"outcome"`
	IssueNumber int    `json:"issue_number,omitempty"`
	URL         string `json:"url,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// Result is the structured output of one run.
type Result struct {
	Issue      int         `json:"issue"`
	Gate       filing.Gate `json:"gate"`
	ReportOnly bool        `json:"report_only"`
	Reason     string      `json:"reason,omitempty"`
	Items      []Item      `json:"items"`
	Refused    int         `json:"refused,omitempty"`
}

// Consumed reports whether the record with id leaves the capture file:
// filed, already filed, deduped onto an open issue, or reported. Refused,
// invalid and undecided records (a run that errored part-way) stay for a
// later run.
func (r *Result) Consumed(id string) bool {
	for _, it := range r.Items {
		if it.ID == id {
			switch it.Outcome {
			case OutcomeFiled, OutcomeAlreadyExists, OutcomeDeduped, OutcomeWouldFile:
				return true
			}
			return false
		}
	}
	return false
}

// Marker is the idempotency marker for a record.
func Marker(rec discoveredwork.Record) string {
	return fmt.Sprintf("<!-- discovered-work: id=%s issue=#%d -->", rec.ID, rec.IssueNumber)
}

// NormalizeTitle is the dedup key: case-folded with whitespace collapsed.
func NormalizeTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Body renders the issue body for rec.
func Body(rec discoveredwork.Record) string {
	runID := ""
	if rec.RunID != nil {
		runID = *rec.RunID
	}
	var b strings.Builder
	b.WriteString(Marker(rec) + "\n\n")
	b.WriteString(filing.ProvenanceLine(rec.IssueNumber, runID, rec.SourceStage) + "\n\n")
	b.WriteString("## Summary\n\n" + filing.RenderUntrusted(rec.Summary, 0) + "\n\n")
	b.WriteString("## Evidence\n\n" + filing.RenderUntrusted(strings.Join(rec.Evidence, "\n\n"), maxEvidenceRender) + "\n")
	if len(rec.CandidateTargetFiles) > 0 {
		b.WriteString("\n## Candidate files\n\n" +
			filing.RenderUntrusted(strings.Join(rec.CandidateTargetFiles, "\n"), maxFilesRender) + "\n")
	}
	return b.String()
}

// CommentBody is the provenance comment left on an open duplicate.
func CommentBody(rec discoveredwork.Record) string {
	runID := ""
	if rec.RunID != nil {
		runID = *rec.RunID
	}
	return Marker(rec) + "\n\n" + filing.ProvenanceLine(rec.IssueNumber, runID, rec.SourceStage) +
		" — the same follow-up was discovered again.\n\n" + filing.RenderUntrusted(strings.Join(rec.Evidence, "\n\n"), maxEvidenceRender)
}

// Materialize decides and performs filing for capture. A nil or empty
// capture is a silent no-op. In report mode, or for an untrusted author
// without allow_untrusted_authors, every valid record is would-file and the
// Filer is never called. In file mode records already filed (marker) are
// skipped, records matching an open issue's title become a provenance
// comment, and at most gate.Cap issues are created: the rest are refused and
// a *filing.CapError is returned with the result.
func Materialize(ctx context.Context, capture *discoveredwork.Capture, gate filing.Gate, authorTrusted bool, f Filer) (*Result, error) {
	res := &Result{Gate: gate, Items: []Item{}}
	if capture == nil || len(capture.Records) == 0 {
		return res, nil
	}
	res.Issue = capture.IssueNumber

	reportOnly, reason := !gate.Files(), gate.Reason
	if !reportOnly && !authorTrusted && !gate.AllowUntrusted {
		reportOnly = true
		reason = "issue author is not trusted (follow_up_filing.allow_untrusted_authors is false)"
	}
	res.ReportOnly, res.Reason = reportOnly, reason

	var toCreate []int // indexes into res.Items
	recs := map[int]discoveredwork.Record{}
	for _, rec := range capture.Records {
		it := Item{ID: rec.ID, Title: rec.Summary, Type: rec.SuggestedType, Priority: rec.SuggestedPriority,
			Size: rec.SuggestedSize, SourceStage: rec.SourceStage}
		r := rec
		if err := discoveredwork.Validate(&r); err != nil {
			it.Outcome, it.Detail = OutcomeInvalid, err.Error()
			res.Items = append(res.Items, it)
			continue
		}
		if err := filing.ValidateFields(rec.SuggestedType, rec.SuggestedPriority, rec.SuggestedSize); err != nil {
			it.Outcome, it.Detail = OutcomeInvalid, err.Error()
			res.Items = append(res.Items, it)
			continue
		}
		if reportOnly {
			it.Outcome = OutcomeWouldFile
			res.Items = append(res.Items, it)
			continue
		}

		marker := Marker(rec)
		num, url, err := f.FindByMarker(ctx, marker)
		if err != nil {
			return res, fmt.Errorf("idempotency lookup for %s: %w", rec.ID, err)
		}
		if num != 0 {
			it.Outcome, it.IssueNumber, it.URL = OutcomeAlreadyExists, num, url
			res.Items = append(res.Items, it)
			continue
		}
		num, url, err = f.FindOpenByTitle(ctx, rec.Summary)
		if err != nil {
			return res, fmt.Errorf("dedup lookup for %s: %w", rec.ID, err)
		}
		if num != 0 {
			it.Outcome, it.IssueNumber, it.URL = OutcomeDeduped, num, url
			has, err := f.HasComment(ctx, num, marker)
			if err != nil {
				return res, fmt.Errorf("comment lookup on #%d for %s: %w", num, rec.ID, err)
			}
			if !has {
				if err := f.AddComment(ctx, num, CommentBody(rec)); err != nil {
					return res, fmt.Errorf("comment on #%d for %s: %w", num, rec.ID, err)
				}
			}
			res.Items = append(res.Items, it)
			continue
		}
		res.Items = append(res.Items, it)
		toCreate = append(toCreate, len(res.Items)-1)
		recs[len(res.Items)-1] = rec
	}

	accepted, capErr := filing.ApplyCap(len(toCreate), gate.Cap)
	for n, idx := range toCreate {
		it := &res.Items[idx]
		if n >= accepted {
			it.Outcome = OutcomeRefused
			res.Refused++
			continue
		}
		rec := recs[idx]
		num, url, err := f.CreateIssue(ctx, rec.Summary, Body(rec), rec.SuggestedType, rec.SuggestedPriority, rec.SuggestedSize)
		if err != nil {
			return res, fmt.Errorf("create issue for %s: %w", rec.ID, err)
		}
		it.Outcome, it.IssueNumber, it.URL = OutcomeFiled, num, url
	}
	if capErr != nil {
		return res, capErr
	}
	return res, nil
}

// IsCapRefusal reports whether err is a per-run cap refusal.
func IsCapRefusal(err error) bool {
	var ce *filing.CapError
	return errors.As(err, &ce)
}
