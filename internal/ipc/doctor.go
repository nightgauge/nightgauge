package ipc

// The doctor.* IPC methods (ADR-025): the VS Code Doctor panel and the
// Action Center drive the same Go remedy engine as `nightgauge doctor --fix`
// (internal/doctor/remedy.go) instead of parsing the CLI's stdout.
//
//   - doctor.run scans every check, streams `doctor.progress` events in check
//     order and returns JSON v2. The scan becomes the daemon's current scan.
//   - doctor.applyRemedy acts on one (fingerprint, remedyId) pair from the
//     current scan. The engine re-runs the owning check first and answers
//     `stale`, applying nothing, when the fingerprint is gone; a confirm
//     remedy needs confirm: true.
//   - doctor.recheck re-runs one check, named by check ID or finding code.
//   - doctor.history reads the local fix log.
//
// No parameter carries a verb name, a command or a path: the verb comes from
// the re-scanned finding's own remedy, and verbs are a closed Go registry.
// Every doctor.* call is serialized on doctorMu because the Fixer holding the
// current scan is not safe for concurrent use.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/doctor"
)

// doctorProgressEvent is the notification doctor.run streams.
const doctorProgressEvent = "doctor.progress"

var (
	doctorFingerprintRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
	doctorRemedyIDRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// doctorSelectorRe matches a finding code (NGD017) or a check ID.
	doctorSelectorRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	doctorAdapterRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// buildDoctorFixer returns a Fixer for the workspace root. A fix log that
// cannot be resolved is logged, not fatal: the Fixer then records nothing.
func (s *Server) buildDoctorFixer(root string, adapters []string) (*doctor.Fixer, error) {
	if s.newDoctorFixer != nil {
		return s.newDoctorFixer(root, adapters)
	}
	cfg, cfgErr := config.Load(root)
	fx, logErr := doctor.NewFixer(cfg, cfgErr, s.client, adapters)
	if fx == nil {
		return nil, fmt.Errorf("doctor: %w", logErr)
	}
	if logErr != nil {
		log.Printf("WARNING: doctor fix log unavailable, applied remedies will not be recorded: %v", logErr)
	}
	// The daemon's working directory is not the workspace; every check and
	// verb reads Env.Cwd.
	fx.Env.Cwd = root
	return fx, nil
}

// scanDoctor runs a full scan with progress streamed and makes it the
// current scan. The caller holds doctorMu.
func (s *Server) scanDoctor(ctx context.Context, adapters []string) (*doctor.Fixer, error) {
	root := s.workspaceRootPath()
	if root == "" {
		return nil, fmt.Errorf("no workspace root configured")
	}
	fx, err := s.buildDoctorFixer(root, adapters)
	if err != nil {
		return nil, err
	}
	fx.Runner.Progress = func(ev doctor.CheckProgress) { s.Emit(doctorProgressEvent, ev) }
	fx.Scan(ctx)
	fx.Runner.Progress = nil
	s.doctorFixer = fx
	return fx, nil
}

// currentDoctorFixer returns the Fixer holding the current scan, scanning
// first when the daemon has none (fresh start). fresh reports that scan.
// The caller holds doctorMu.
func (s *Server) currentDoctorFixer(ctx context.Context) (fx *doctor.Fixer, fresh bool, err error) {
	if s.doctorFixer != nil {
		return s.doctorFixer, false, nil
	}
	fx, err = s.scanDoctor(ctx, nil)
	return fx, err == nil, err
}

func (s *Server) handleDoctorRun(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p DoctorRunParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
	}
	var opts doctor.FixOptions
	for _, o := range p.Only {
		if !doctorSelectorRe.MatchString(o) {
			return nil, fmt.Errorf("only: %q is not a finding code or check ID", o)
		}
		opts.Only = append(opts.Only, o)
	}
	for _, sv := range p.Severity {
		sev, err := doctor.ParseSeverity(sv)
		if err != nil {
			return nil, fmt.Errorf("severity: %w", err)
		}
		opts.Severities = append(opts.Severities, sev)
	}
	adapters, err := doctorAdapters(p.Adapters)
	if err != nil {
		return nil, err
	}

	s.doctorMu.Lock()
	defer s.doctorMu.Unlock()
	fx, err := s.scanDoctor(ctx, adapters)
	if err != nil {
		return nil, err
	}
	return fx.State(opts), nil
}

// doctorAdapters validates adapter names and expands "all".
func doctorAdapters(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, a := range in {
		a = strings.TrimSpace(a)
		if strings.EqualFold(a, "all") {
			return doctor.AllAdapterNames(), nil
		}
		if !doctorAdapterRe.MatchString(a) {
			return nil, fmt.Errorf("adapters: %q is not an adapter name", a)
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *Server) handleDoctorApplyRemedy(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p DoctorApplyRemedyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if !doctorFingerprintRe.MatchString(p.Fingerprint) {
		return nil, fmt.Errorf("fingerprint must be 16 lowercase hex characters")
	}
	if !doctorRemedyIDRe.MatchString(p.RemedyID) {
		return nil, fmt.Errorf("remedyId is not a remedy ID")
	}

	s.doctorMu.Lock()
	defer s.doctorMu.Unlock()
	fx, _, err := s.currentDoctorFixer(ctx)
	if err != nil {
		return nil, err
	}
	action := doctor.ActionApplied
	if p.DryRun {
		action = doctor.ActionPreviewed
	}
	check, ok := fx.CheckFor(p.Fingerprint)
	if !ok {
		return DoctorApplyRemedyResult{
			Outcome: doctor.OutcomeStale, Action: action,
			Detail: fmt.Sprintf("fingerprint %s is not in the current scan", p.Fingerprint),
		}, nil
	}
	var res doctor.FixResult
	if p.DryRun {
		res = fx.PreviewFingerprint(ctx, check, p.Fingerprint, p.RemedyID)
	} else {
		res = fx.ApplyFingerprint(ctx, check, p.Fingerprint, p.RemedyID, p.Confirm)
	}
	out := DoctorApplyRemedyResult{
		Outcome: res.Outcome, Action: res.Action, Preview: res.Preview,
		Detail: res.Detail, Remedy: res.Remedy, Evidence: res.Evidence,
	}
	if res.Outcome != doctor.OutcomeStale {
		f := res.Finding
		out.Finding = &f
	}
	return out, nil
}

func (s *Server) handleDoctorRecheck(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	var p DoctorRecheckParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("parse params: %w", err)
	}
	if (p.Code == "") == (p.Check == "") {
		return nil, fmt.Errorf("give exactly one of code or check")
	}
	sel := p.Code + p.Check
	if !doctorSelectorRe.MatchString(sel) {
		return nil, fmt.Errorf("%q is not a finding code or check ID", sel)
	}

	s.doctorMu.Lock()
	defer s.doctorMu.Unlock()
	fx, fresh, err := s.currentDoctorFixer(ctx)
	if err != nil {
		return nil, err
	}
	checkID := p.Check
	if p.Code != "" {
		id, ok := fx.CheckForCode(p.Code)
		if !ok {
			return nil, fmt.Errorf("unknown finding code %q", p.Code)
		}
		checkID = id
	}
	var r doctor.CheckResult
	var ok bool
	if fresh {
		// The scan that just established the current state already ran it.
		for _, cr := range fx.State(doctor.FixOptions{}).Results {
			if cr.ID == checkID {
				r, ok = cr, true
			}
		}
	} else {
		r, ok = fx.Recheck(ctx, checkID)
	}
	if !ok {
		return nil, fmt.Errorf("unknown check %q", checkID)
	}
	findings := make([]doctor.Finding, len(r.Findings))
	for i, f := range r.Findings {
		findings[i] = doctor.RedactFinding(f)
	}
	return DoctorRecheckResult{
		Check: r.ID, Title: r.Title, Status: r.Status,
		Detail: config.RedactSecretString(r.Detail), Findings: findings,
	}, nil
}

func (s *Server) handleDoctorHistory(_ context.Context, raw json.RawMessage) (interface{}, error) {
	var p DoctorHistoryParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
	}
	if p.Limit < 0 {
		return nil, fmt.Errorf("limit must not be negative")
	}
	pathFn := s.doctorFixLogPath
	if pathFn == nil {
		pathFn = doctor.DefaultFixLogPath
	}
	path, err := pathFn()
	if err != nil {
		return nil, err
	}
	entries, malformed, err := doctor.ReadFixLog(path)
	if err != nil {
		return nil, err
	}
	if p.Limit > 0 && len(entries) > p.Limit {
		entries = entries[len(entries)-p.Limit:]
	}
	if entries == nil {
		entries = []doctor.FixLogEntry{}
	}
	return DoctorHistoryResult{Entries: entries, Malformed: malformed}, nil
}
