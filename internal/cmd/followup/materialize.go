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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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
	// OutcomeTrackedByParent is a review record restating one of the issue's
	// own acceptance criteria that this run's PR holds unmet: the parent
	// already tracks it, so no issue is filed (#3186).
	OutcomeTrackedByParent = "tracked-by-parent"
	// OutcomeDuplicate is a review record naming the gap another producer
	// recorded for the same issue and run (#3186).
	OutcomeDuplicate = "duplicate"
)

// reviewProducer is the independent review's source stage
// (internal/review). Only its records are suppressed.
const reviewProducer = "producer:independent-review"

// ACUnmetStatus is pr-create's verdict status for an unmet criterion
// (stages.ACUnmet); a test in stages pins the two together.
const ACUnmetStatus = "unmet"

// Tracking is what this run's pr-{N}.json says the parent issue already
// tracks: the run it was written by and the criteria its PR holds unmet, by
// NormalizeTitle'd text. The zero value tracks nothing, so every record files.
type Tracking struct {
	RunID string
	Unmet map[string]bool
}

// LoadTracking reads pr-{issue}.json from runDir. A missing, unreadable or
// foreign file tracks nothing. An unmet verdict is tracked by its own text and,
// when issue-{issue}.json in runDir lists the issue's criteria, by the text of
// the criterion at its index: a review record carries the issue's wording,
// which a verdict may paraphrase.
func LoadTracking(runDir string, issue int) Tracking {
	data, err := os.ReadFile(filepath.Join(runDir, "pr-"+strconv.Itoa(issue)+".json"))
	if err != nil {
		return Tracking{}
	}
	var pr struct {
		IssueNumber int    `json:"issue_number"`
		RunID       string `json:"run_id"`
		Criteria    []struct {
			Index  *int   `json:"index"`
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"acceptance_criteria"`
	}
	if json.Unmarshal(data, &pr) != nil || pr.IssueNumber != issue {
		return Tracking{}
	}
	issueCriteria := loadIssueCriteria(runDir, issue)
	t := Tracking{RunID: pr.RunID, Unmet: map[string]bool{}}
	for _, c := range pr.Criteria {
		if c.Status != ACUnmetStatus {
			continue
		}
		if strings.TrimSpace(c.Text) != "" {
			t.Unmet[NormalizeTitle(c.Text)] = true
		}
		if c.Index != nil && *c.Index >= 0 && *c.Index < len(issueCriteria) &&
			strings.TrimSpace(issueCriteria[*c.Index]) != "" {
			t.Unmet[NormalizeTitle(issueCriteria[*c.Index])] = true
		}
	}
	return t
}

// loadIssueCriteria is the acceptance criteria issue-{issue}.json in runDir
// lists, in order, or nil.
func loadIssueCriteria(runDir string, issue int) []string {
	data, err := os.ReadFile(filepath.Join(runDir, "issue-"+strconv.Itoa(issue)+".json"))
	if err != nil {
		return nil
	}
	var ic struct {
		IssueNumber  int `json:"issue_number"`
		Requirements struct {
			AcceptanceCriteria []string `json:"acceptance_criteria"`
		} `json:"requirements"`
	}
	if json.Unmarshal(data, &ic) != nil || (ic.IssueNumber != 0 && ic.IssueNumber != issue) {
		return nil
	}
	return ic.Requirements.AcceptanceCriteria
}

func runIDOf(rec discoveredwork.Record) string {
	if rec.RunID == nil {
		return ""
	}
	return *rec.RunID
}

// trackedByParent reports whether rec is a review record whose criterion this
// run's PR holds unmet. Absent run ids match each other.
func trackedByParent(rec discoveredwork.Record, t Tracking) bool {
	return rec.SourceStage == reviewProducer && rec.Criterion != "" &&
		runIDOf(rec) == t.RunID && t.Unmet[NormalizeTitle(rec.Criterion)]
}

// Code identifiers (#3186): a backticked span, a path:line token, or a
// letter-led token with an internal '_' or '.' (max_tested, opencode.json).
// Bare numbers (404) and ordinals (2nd) cannot match; e.g. and i.e. are
// stop-listed.
var (
	backtickRe = regexp.MustCompile("`([^`\n]+)`")
	pathLineRe = regexp.MustCompile(`[\w./-]*[A-Za-z][\w./-]*:\d+`)
	bareRe     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*(?:[_.][A-Za-z0-9]+)+`)
	stopIdents = map[string]bool{"e.g": true, "i.e": true}
)

// identifiers is the code identifiers in texts, less those found in exclude.
func identifiers(texts []string, exclude map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, t := range texts {
		for _, m := range backtickRe.FindAllStringSubmatch(t, -1) {
			out[strings.TrimSpace(m[1])] = true
		}
		for _, m := range pathLineRe.FindAllString(t, -1) {
			out[m] = true
		}
		for _, m := range bareRe.FindAllString(t, -1) {
			out[m] = true
		}
	}
	for id := range out {
		if id == "" || stopIdents[strings.ToLower(id)] || exclude[id] {
			delete(out, id)
		}
	}
	return out
}

// duplicateOf returns the id of a record from another producer that rec, a
// review record, restates, or "". The match: same issue, same run (absent
// matches absent), a shared candidate file, and at least one shared code
// identifier in the summaries and evidence once the shared files' paths, and
// the identifiers inside them, are left out. A location alone never matches.
func duplicateOf(rec discoveredwork.Record, others []discoveredwork.Record) string {
	if rec.SourceStage != reviewProducer {
		return ""
	}
	files := map[string]bool{}
	for _, f := range rec.CandidateTargetFiles {
		files[path.Clean(f)] = true
	}
	for _, o := range others {
		if o.SourceStage == rec.SourceStage || o.IssueNumber != rec.IssueNumber || runIDOf(o) != runIDOf(rec) {
			continue
		}
		exclude := map[string]bool{}
		for _, f := range o.CandidateTargetFiles {
			f = path.Clean(f)
			if !files[f] {
				continue
			}
			exclude[f] = true
			for id := range identifiers([]string{f}, nil) {
				exclude[id] = true
			}
		}
		if len(exclude) == 0 {
			continue
		}
		mine := identifiers(append([]string{rec.Summary}, rec.Evidence...), exclude)
		for id := range identifiers(append([]string{o.Summary}, o.Evidence...), exclude) {
			if mine[id] {
				return o.ID
			}
		}
	}
	return ""
}

// valid reports whether rec would be filed rather than refused as invalid.
func valid(rec discoveredwork.Record) bool {
	r := rec
	return discoveredwork.Validate(&r) == nil &&
		filing.ValidateFields(rec.SuggestedType, rec.SuggestedPriority, rec.SuggestedSize) == nil
}

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
// filed, already filed, deduped onto an open issue, reported, tracked by the
// parent or a duplicate of another producer's record. Refused,
// invalid and undecided records (a run that errored part-way) stay for a
// later run.
func (r *Result) Consumed(id string) bool {
	for _, it := range r.Items {
		if it.ID == id {
			switch it.Outcome {
			case OutcomeFiled, OutcomeAlreadyExists, OutcomeDeduped, OutcomeWouldFile,
				OutcomeTrackedByParent, OutcomeDuplicate:
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
//
// Before any of that, in either mode, a review record is not filed when track
// says this run's PR holds its criterion unmet (tracked-by-parent), or when it
// restates a valid record another producer wrote for the same issue and run
// (duplicate). Both are reported and consumed, never dropped silently (#3186).
func Materialize(ctx context.Context, capture *discoveredwork.Capture, gate filing.Gate, authorTrusted bool, track Tracking, f Filer) (*Result, error) {
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

	var others []discoveredwork.Record // valid records a review record may duplicate
	for _, rec := range capture.Records {
		if rec.SourceStage != reviewProducer && valid(rec) {
			others = append(others, rec)
		}
	}

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
		if trackedByParent(rec, track) {
			it.Outcome = OutcomeTrackedByParent
			it.Detail = fmt.Sprintf("criterion unmet on #%d; the PR says Refs", rec.IssueNumber)
			res.Items = append(res.Items, it)
			continue
		}
		if id := duplicateOf(rec, others); id != "" {
			it.Outcome, it.Detail = OutcomeDuplicate, "duplicates "+id
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
