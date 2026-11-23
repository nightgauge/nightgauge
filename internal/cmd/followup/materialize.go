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
	// FindByMarker returns an existing issue, open or closed, whose body
	// carries marker, or a zero Found.
	FindByMarker(ctx context.Context, marker string) (Found, error)
	// ParentBody returns issue number's current body. Materialize reads the
	// record's marker and the unchecked "- [ ]" criteria from it, and treats an
	// error as an empty body, so the record is filed.
	ParentBody(ctx context.Context, number int) (string, error)
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
	// FindOpenInSubsystem returns an open issue in the same subsystem as a
	// follow-up of parent touching files, chosen by PickSubsystemIssue, or 0
	// (#3278). A target whose board Priority is lower than priority is not
	// returned: the follow-up is filed on its own instead (#2872).
	FindOpenInSubsystem(ctx context.Context, parent int, files []string, priority string) (int, string, error)
	// AppendPart appends rec to issue number as a new part (AppendPartToBody)
	// and returns the part's label.
	AppendPart(ctx context.Context, number int, rec discoveredwork.Record) (string, error)
}

// Found is an issue FindByMarker found; the zero value is none.
type Found struct {
	Number int
	URL    string
	// Closed is set for any state but open; the item's detail then reads
	// "marker on closed #N".
	Closed bool
}

// Outcome values for one record.
const (
	OutcomeFiled = "filed"
	// OutcomeFolded is a follow-up appended as a new part to an open issue
	// in the same subsystem instead of filed as its own issue (#3278).
	OutcomeFolded        = "folded"
	OutcomeAlreadyExists = "already-exists"
	OutcomeDeduped       = "deduped"
	OutcomeWouldFile     = "would-file"
	OutcomeRefused       = "refused"
	OutcomeInvalid       = "invalid"
	// OutcomeTrackedByParent is a record, from any producer, whose marker is
	// in the parent issue's current body, or which restates one of the issue's
	// own acceptance criteria that this run's PR holds unmet or the issue's
	// body leaves unchecked: the parent already tracks it, so no issue is
	// filed (#3186, #2872).
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

// trackedByParent reports why rec, from any producer, is already tracked by
// its parent, or "". Its summary or criterion restates (see restates) a
// criterion this run's PR holds unmet (absent run ids match each other), or
// restates (see restatesBodyCriterion) an unchecked criterion in the parent's
// current body.
func trackedByParent(rec discoveredwork.Record, t Tracking, parentCriteria []string) string {
	texts := []string{rec.Summary, rec.Criterion}
	if runIDOf(rec) == t.RunID {
		for c := range t.Unmet {
			if restatesAny(texts, c, restates) {
				return fmt.Sprintf("criterion unmet on #%d; the PR says Refs", rec.IssueNumber)
			}
		}
	}
	for _, c := range parentCriteria {
		if restatesAny(texts, c, restatesBodyCriterion) {
			return fmt.Sprintf("restates an unchecked criterion of #%d", rec.IssueNumber)
		}
	}
	return ""
}

// minRestateLen is the shortest normalized text a containment match accepts;
// shorter texts ("fix it") would match almost anything.
const minRestateLen = 24

// restates reports whether a and b say the same thing: equal once normalized,
// or one containing the other when the shorter is at least minRestateLen
// characters.
func restates(a, b string) bool {
	a, b = NormalizeTitle(a), NormalizeTitle(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= minRestateLen && strings.Contains(b, a)
}

// bodyRestateCoverage is the share of the longer text the shorter must cover
// for restatesBodyCriterion to accept containment.
const bodyRestateCoverage = 2.0 / 3.0

// resolvedPrefix opens the criterion AppendPartToBody writes for a folded part.
const resolvedPrefix = "Resolved:"

// backtickLookalike is what criterionLine writes in place of a backtick, so
// record text cannot close the code span around it.
const backtickLookalike = "ˋ"

// criterionTrailing is the punctuation, and the truncation ellipsis
// criterionLine adds, that bodyCriterionText trims from a criterion's end.
const criterionTrailing = ".,;:!?… "

// stripBackticks drops backticks and their look-alike from criterion text.
var stripBackticks = strings.NewReplacer("`", "", backtickLookalike, "")

// restatesBodyCriterion reports whether text restates criterion, an unchecked
// line of the parent's live body (#2872 Part D). That body is editable and
// accumulates folded parts, so bare containment would let a generic line
// ("Unit tests cover the new code paths") suppress a longer, specific finding.
// Both sides drop the "Resolved:" prefix AppendPartToBody writes, backticks
// (and the backtickLookalike criterionLine writes) and trailing punctuation;
// then they must be equal, or the shorter must be at least minRestateLen and
// cover at least bodyRestateCoverage of the longer. An uncertain match files
// the record, the cheap direction. The PR's unmet list, the pipeline's own
// verdict, keeps plain restates.
func restatesBodyCriterion(text, criterion string) bool {
	a, b := bodyCriterionText(text), bodyCriterionText(criterion)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= minRestateLen && float64(len(a)) >= bodyRestateCoverage*float64(len(b)) &&
		strings.Contains(b, a)
}

// bodyCriterionText normalizes one side of restatesBodyCriterion.
func bodyCriterionText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= len(resolvedPrefix) && strings.EqualFold(s[:len(resolvedPrefix)], resolvedPrefix) {
		s = s[len(resolvedPrefix):]
	}
	return strings.TrimRight(NormalizeTitle(stripBackticks.Replace(s)), criterionTrailing)
}

// restatesAny reports whether any of texts restates criterion under match.
func restatesAny(texts []string, criterion string, match func(a, b string) bool) bool {
	for _, t := range texts {
		if match(t, criterion) {
			return true
		}
	}
	return false
}

var uncheckedRe = regexp.MustCompile(`(?m)^\s*[-*]\s+\[ \]\s+(.+?)\s*$`)

// UncheckedCriteria returns the text of every unchecked "- [ ]" item in body.
func UncheckedCriteria(body string) []string {
	var out []string
	for _, m := range uncheckedRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
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
			case OutcomeFiled, OutcomeFolded, OutcomeAlreadyExists, OutcomeDeduped, OutcomeWouldFile,
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
// without allow_untrusted_authors, every valid record that is not tracked or
// a duplicate is would-file, and the Filer is only read for the parent's body
// (ParentBody), so tracked-by-parent holds there too; a nil Filer skips that
// check. In file mode records already filed (marker) are skipped,
// records matching an open issue's title become a provenance
// comment, records with an open issue in the same subsystem are appended to
// it as a new part (folded), and at most gate.Cap issues are created: the rest
// are refused and a *filing.CapError is returned with the result.
//
// Before any of that, in either mode, a record from any producer is not filed
// when its marker is in the parent's current body, whatever the criterion
// under it says, or when it restates a criterion this run's PR holds unmet or
// the parent's body leaves unchecked (tracked-by-parent), and a review record
// is not filed when it restates a valid record another producer wrote for the
// same issue and run (duplicate). Both are reported and consumed, never
// dropped silently (#3186, #2872). A marker found only on another issue, open
// or closed, is already-exists; a record more urgent than the subsystem pick's
// board Priority is filed instead of folded.
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

	// The parent's body, read once per parent before this run writes anything.
	// An unreadable body tracks nothing, so the record is filed: the safe
	// direction.
	type parentRead struct {
		body     string
		criteria []string
	}
	parents := map[int]parentRead{}
	parentOf := func(n int) parentRead {
		if f == nil || n <= 0 {
			return parentRead{}
		}
		if p, ok := parents[n]; ok {
			return p
		}
		var p parentRead
		if b, err := f.ParentBody(ctx, n); err == nil {
			p = parentRead{body: b, criteria: UncheckedCriteria(b)}
		}
		parents[n] = p
		return p
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
		parent := parentOf(rec.IssueNumber)
		if strings.Contains(parent.body, Marker(rec)) {
			it.Outcome = OutcomeTrackedByParent
			it.Detail = fmt.Sprintf("marker in #%d's body", rec.IssueNumber)
			res.Items = append(res.Items, it)
			continue
		}
		if why := trackedByParent(rec, track, parent.criteria); why != "" {
			it.Outcome = OutcomeTrackedByParent
			it.Detail = why
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
		found, err := f.FindByMarker(ctx, marker)
		if err != nil {
			return res, fmt.Errorf("idempotency lookup for %s: %w", rec.ID, err)
		}
		if found.Number != 0 {
			it.Outcome, it.IssueNumber, it.URL = OutcomeAlreadyExists, found.Number, found.URL
			if found.Closed {
				it.Detail = fmt.Sprintf("marker on closed #%d", found.Number)
			}
			res.Items = append(res.Items, it)
			continue
		}
		num, url, err := f.FindOpenByTitle(ctx, rec.Summary)
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
		// Fold into an open issue in the same subsystem before filing a new
		// one (#3278). Folding creates no issue, so the cap does not count it.
		num, url, err = f.FindOpenInSubsystem(ctx, rec.IssueNumber, rec.CandidateTargetFiles, rec.SuggestedPriority)
		if err != nil {
			return res, fmt.Errorf("subsystem lookup for %s: %w", rec.ID, err)
		}
		if num != 0 {
			part, err := f.AppendPart(ctx, num, rec)
			if err != nil {
				return res, fmt.Errorf("append %s to #%d: %w", rec.ID, num, err)
			}
			it.Outcome, it.IssueNumber, it.URL = OutcomeFolded, num, url
			it.Detail = fmt.Sprintf("appended as Part %s of #%d", part, num)
			if part == "" {
				it.Detail = fmt.Sprintf("already a part of #%d", num)
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

// Subsystem folding (#3278). A small follow-up joins an open issue in the same
// subsystem as a new part, and becomes an issue of its own only when nothing
// matches.

// SubsystemCandidate is one open issue PickSubsystemIssue weighs.
type SubsystemCandidate struct {
	Number      int
	URL         string
	Body        string
	Labels      []string
	BoardStatus string
	// Parent is the candidate's parent issue (epic), 0 for none or unknown.
	Parent int
	// Priority is the candidate's board Priority (P0..P3), "" when unknown.
	Priority string
}

// Subsystem identifies a follow-up's subsystem: the issue it was discovered
// in, the files it names, the parent's component:* and program:* labels, the
// parent's own epic, and which component labels are too broad to say anything.
type Subsystem struct {
	Parent     int
	Files      []string
	Components []string
	// Epic is the parent issue's parent, 0 for none.
	Epic int
	// Programs are the parent's program:* labels.
	Programs []string
	// Broad holds lower-cased component labels carried by so many open issues
	// that sharing one says nothing about the subsystem.
	Broad map[string]bool
}

// foldExcludedLabels mark an issue no follow-up may join: an epic is a
// container, and a human-only issue is not pipeline work.
var foldExcludedLabels = []string{"type:epic", "owner-action", "blocked"}

// ubiquitousFiles are basenames so many issues mention that naming one says
// nothing about the subsystem: they never qualify a fold on their own.
var ubiquitousFiles = map[string]bool{
	"package-lock.json": true, "package.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"go.sum": true, "go.mod": true, "cargo.lock": true, "poetry.lock": true,
	"changelog.md": true, "readme.md": true,
}

// Ubiquitous reports whether f is one of ubiquitousFiles.
func Ubiquitous(f string) bool {
	return ubiquitousFiles[strings.ToLower(path.Base(strings.TrimSpace(f)))]
}

// namesPath reports whether body names f as a path token: bounded on the left
// by the line start, whitespace, a backtick, "(" or "[", and on the right by
// the line end, whitespace, a backtick, ":", ")", "]", "," or a sentence-final
// ".". "b/a/x.go" does not name "a/x.go", and "x.go.bak" does not name "x.go".
func namesPath(body, f string) bool {
	if f == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(body[i:], f)
		if j < 0 {
			return false
		}
		at := i + j
		end := at + len(f)
		if leftBound(body, at) && rightBound(body, end) {
			return true
		}
		i = at + 1
	}
}

func leftBound(body string, at int) bool {
	if at == 0 {
		return true
	}
	switch body[at-1] {
	case ' ', '\t', '\n', '\r', '`', '(', '[':
		return true
	}
	return false
}

func rightBound(body string, end int) bool {
	if end == len(body) {
		return true
	}
	switch body[end] {
	case ' ', '\t', '\n', '\r', '`', ':', ')', ']', ',':
		return true
	case '.':
		// Sentence-final only: the next byte ends the text or the word.
		return end+1 == len(body) || strings.ContainsRune(" \t\n\r`)]", rune(body[end+1]))
	}
	return false
}

// minSharedComponents is how many non-broad component:* labels a candidate
// must share with the parent to qualify on labels alone. One shared label,
// or any number of broad ones, never does (#2872).
const minSharedComponents = 2

// Qualification signals, strongest last.
const (
	noMatch = iota
	labelMatch
	programMatch
	fileMatch
)

// statusRank orders board Status: Ready over Backlog over anything else.
func statusRank(status string) int {
	switch strings.ToLower(strings.Join(strings.Fields(status), " ")) {
	case "ready":
		return 2
	case "backlog":
		return 1
	}
	return 0
}

// qualify is the strongest signal tying c to s, and whether c is an open
// sibling under the parent's epic.
func qualify(s Subsystem, c SubsystemCandidate) (int, bool) {
	sibling := s.Epic > 0 && c.Parent == s.Epic
	rank := noMatch
	for _, f := range s.Files {
		if f = strings.TrimSpace(f); f != "" && !Ubiquitous(f) && namesPath(c.Body, f) {
			rank = fileMatch
			break
		}
	}
	if rank == noMatch {
		programs := lowerSet(s.Programs)
		for _, l := range c.Labels {
			if programs[strings.ToLower(l)] {
				rank = programMatch
				break
			}
		}
	}
	if rank == noMatch {
		comps := lowerSet(s.Components)
		shared := 0
		for _, l := range c.Labels {
			l = strings.ToLower(l)
			if comps[l] && !s.Broad[l] {
				shared++
			}
		}
		if shared >= minSharedComponents {
			rank = labelMatch
		}
	}
	return rank, sibling
}

func lowerSet(xs []string) map[string]bool {
	out := map[string]bool{}
	for _, x := range xs {
		out[strings.ToLower(x)] = true
	}
	return out
}

// PickSubsystemIssue chooses the open issue a follow-up folds into, or 0.
//
// A candidate qualifies when its body names one of the follow-up's files as a
// path token (a ubiquitous file such as package-lock.json never counts), it is
// an open sibling under the parent's epic, it shares a program:* label with
// the parent, or it shares at least minSharedComponents non-broad component:*
// labels with it. It is excluded when it is an epic or human-only, or its
// board Status is In progress, In review or Done: rewriting a body a running
// stage or an open PR was written against changes work under its feet.
//
// Among qualifying candidates the parent is chosen only when nothing else
// qualifies; then an open sibling under the epic wins, then board Status Ready
// over Backlog, then the stronger signal (file, program, labels), and the
// lowest number decides only among equals, so the choice is deterministic.
func PickSubsystemIssue(s Subsystem, cands []SubsystemCandidate) (int, string) {
	type key struct {
		notParent, sibling bool
		status, signal     int
	}
	less := func(a, b key) bool { // a ranks below b
		switch {
		case a.notParent != b.notParent:
			return b.notParent
		case a.sibling != b.sibling:
			return b.sibling
		case a.status != b.status:
			return a.status < b.status
		default:
			return a.signal < b.signal
		}
	}
	best, bestURL := 0, ""
	var bestKey key
	for _, c := range cands {
		if c.Number <= 0 || foldExcluded(c) {
			continue
		}
		signal, sibling := qualify(s, c)
		if signal == noMatch && !sibling {
			continue
		}
		k := key{notParent: c.Number != s.Parent, sibling: sibling, status: statusRank(c.BoardStatus), signal: signal}
		if best == 0 || less(bestKey, k) || (!less(k, bestKey) && c.Number < best) {
			best, bestURL, bestKey = c.Number, c.URL, k
		}
	}
	return best, bestURL
}

// priorityOrder ranks board Priority options, most urgent first.
var priorityOrder = map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3}

// Outranks reports whether a record of priority (critical..low) is more urgent
// than a target whose board Priority is board. An unknown board Priority is
// never outranked; an unknown record priority counts as medium (P2), as
// filing.PriorityToBoardOption maps it.
func Outranks(priority, board string) bool {
	b, ok := priorityOrder[strings.ToUpper(strings.TrimSpace(board))]
	if !ok {
		return false
	}
	return priorityOrder[filing.PriorityToBoardOption(priority)] < b
}

func foldExcluded(c SubsystemCandidate) bool {
	for _, l := range c.Labels {
		for _, x := range foldExcludedLabels {
			if strings.EqualFold(l, x) {
				return true
			}
		}
	}
	switch strings.ToLower(strings.Join(strings.Fields(c.BoardStatus), " ")) {
	case "in progress", "in review", "done":
		return true
	}
	return false
}

// ComponentLabels returns the component:* labels in labels.
func ComponentLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if strings.HasPrefix(strings.ToLower(l), "component:") {
			out = append(out, l)
		}
	}
	return out
}

// ProgramLabels returns the program:* labels in labels.
func ProgramLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if strings.HasPrefix(strings.ToLower(l), "program:") {
			out = append(out, l)
		}
	}
	return out
}

var partHeadingRe = regexp.MustCompile(`(?m)^##\s+Part\s+([A-Z])\b`)

// nextPartLabel is the letter after the highest "## Part X" heading in body.
// A body with no parts is part A, so the first appended part is B; past Z the
// label is numeric.
func nextPartLabel(body string) string {
	max := 'A'
	for _, m := range partHeadingRe.FindAllStringSubmatch(body, -1) {
		if r := rune(m[1][0]); r > max {
			max = r
		}
	}
	if max >= 'Z' {
		return strconv.Itoa(int(max-'A') + 2)
	}
	return string(max + 1)
}

// AppendPartToBody returns body with rec appended as a new part, the part's
// label, and whether anything changed. A body already carrying rec's marker is
// returned unchanged, so folding is idempotent.
func AppendPartToBody(body string, rec discoveredwork.Record) (string, string, bool) {
	marker := Marker(rec)
	if strings.Contains(body, marker) {
		return body, "", false
	}
	runID := ""
	if rec.RunID != nil {
		runID = *rec.RunID
	}
	label := nextPartLabel(body)
	var b strings.Builder
	b.WriteString(strings.TrimRight(body, "\n") + "\n\n")
	fmt.Fprintf(&b, "## Part %s: follow-up discovered in #%d\n\n", label, rec.IssueNumber)
	b.WriteString(marker + "\n\n")
	b.WriteString(filing.ProvenanceLine(rec.IssueNumber, runID, rec.SourceStage) + "\n\n")
	b.WriteString(filing.RenderUntrusted(rec.Summary, 0) + "\n\n")
	b.WriteString("### Evidence\n\n" + filing.RenderUntrusted(strings.Join(rec.Evidence, "\n\n"), maxEvidenceRender) + "\n\n")
	if len(rec.CandidateTargetFiles) > 0 {
		b.WriteString("### Candidate files\n\n" +
			filing.RenderUntrusted(strings.Join(rec.CandidateTargetFiles, "\n"), maxFilesRender) + "\n\n")
	}
	// The part's criteria are the record's own claims, each falsifiable, never
	// "resolved, or recorded as not needed" (#2872).
	fmt.Fprintf(&b, "### Acceptance criteria (Part %s)\n\n", label)
	b.WriteString("- [ ] " + resolvedPrefix + " " + criterionLine(rec.Summary) + "\n")
	for _, e := range rec.Evidence {
		if strings.TrimSpace(e) != "" {
			b.WriteString("- [ ] No longer true: " + criterionLine(e) + "\n")
		}
	}
	return b.String(), label, true
}

// maxCriterionRender bounds one rendered criterion line.
const maxCriterionRender = 300

// criterionLine renders record text for a checklist item: one line, bounded,
// inside a code span (so mentions and HTML stay inert), with backticks swapped
// for the look-alike RenderUntrusted uses so the text cannot close the span.
func criterionLine(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > maxCriterionRender {
		text = string(r[:maxCriterionRender]) + "…"
	}
	return "`" + strings.ReplaceAll(text, "`", backtickLookalike) + "`"
}
