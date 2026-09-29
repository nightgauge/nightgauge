package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// The remedy engine (ADR-025 § 3, § 4, § 8). It resolves each finding's
// preferred remedy to a verb in a closed Go registry, previews it, obtains
// consent where the remedy's kind requires it, re-checks the verb's
// precondition immediately before applying it, applies it, and then re-runs
// the owning check. The outcome is `fixed` only when that re-run no longer
// reports the finding's fingerprint; Apply's own return value is never taken
// as success.
//
// It is the single `--fix` implementation. The interactive CLI, the IPC
// methods and Action Center cards drive the same Fixer: Run for a whole
// pass, ApplyFingerprint for one (fingerprint, remedy) pair, Recheck for one
// check.

// RemedyVerb is one registered remedy action. Implementations are Go code in
// this package; a verb never runs a shell string or command text taken from
// config, a finding, an issue body or an IPC parameter.
type RemedyVerb interface {
	// Preview describes exactly what Apply would do, without changing anything.
	Preview(ctx context.Context, f Finding) (string, error)
	// Precondition re-derives, immediately before Apply, the claim the finding
	// made at scan time. A non-nil error means the world changed: the remedy
	// is reported `blocked` (or `conflict` for ErrRemedyConflict) and Apply
	// is not called.
	Precondition(ctx context.Context, f Finding) error
	// Apply performs the action. Its return value is advisory: the engine
	// decides the outcome by re-running the owning check.
	Apply(ctx context.Context, f Finding) error
}

// VerbFuncs adapts three functions to RemedyVerb.
type VerbFuncs struct {
	PreviewFunc      func(ctx context.Context, f Finding) (string, error)
	PreconditionFunc func(ctx context.Context, f Finding) error
	ApplyFunc        func(ctx context.Context, f Finding) error
}

// Preview implements RemedyVerb.
func (v VerbFuncs) Preview(ctx context.Context, f Finding) (string, error) {
	if v.PreviewFunc == nil {
		return "", errors.New("verb has no preview")
	}
	return v.PreviewFunc(ctx, f)
}

// Precondition implements RemedyVerb. A verb without one fails closed.
func (v VerbFuncs) Precondition(ctx context.Context, f Finding) error {
	if v.PreconditionFunc == nil {
		return errors.New("verb declares no precondition; refusing to apply blind")
	}
	return v.PreconditionFunc(ctx, f)
}

// Apply implements RemedyVerb.
func (v VerbFuncs) Apply(ctx context.Context, f Finding) error {
	if v.ApplyFunc == nil {
		return errors.New("verb has no apply step")
	}
	return v.ApplyFunc(ctx, f)
}

// ErrRemedyConflict marks a precondition or apply refusal because both the old
// and the new state exist and nothing may be overwritten (ADR-024's reserved
// exit 3). ErrRemedyBlocked marks a refusal at apply time (exit 4).
var (
	ErrRemedyConflict = errors.New("remedy conflict: nothing was overwritten")
	ErrRemedyBlocked  = errors.New("remedy blocked at apply time")
)

// VerbRegistry is the closed set of remedy verbs. Only Go code registers a
// verb; nothing read at run time can add one.
type VerbRegistry struct {
	verbs map[string]RemedyVerb
}

// NewVerbRegistry returns an empty verb registry.
func NewVerbRegistry() *VerbRegistry {
	return &VerbRegistry{verbs: map[string]RemedyVerb{}}
}

// Register adds a verb under name. A name is registered once.
func (r *VerbRegistry) Register(name string, v RemedyVerb) error {
	if name == "" || v == nil {
		return errors.New("doctor: a verb needs a name and an implementation")
	}
	if _, dup := r.verbs[name]; dup {
		return fmt.Errorf("doctor: verb %q registered twice", name)
	}
	r.verbs[name] = v
	return nil
}

// Lookup returns the verb registered under name.
func (r *VerbRegistry) Lookup(name string) (RemedyVerb, bool) {
	if r == nil {
		return nil, false
	}
	v, ok := r.verbs[name]
	return v, ok
}

// Names lists the registered verbs, sorted.
func (r *VerbRegistry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.verbs))
	for n := range r.verbs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Outcome is the verified result of one remedy (ADR-025 § 3).
type Outcome string

const (
	OutcomeFixed        Outcome = "fixed"         // the owning check no longer reports the fingerprint
	OutcomeStillPresent Outcome = "still-present" // applied, but the check still reports it
	OutcomeSkipped      Outcome = "skipped"       // not applied: no consent, or manual
	OutcomeBlocked      Outcome = "blocked"       // refused at apply time; nothing done (exit 4)
	OutcomeConflict     Outcome = "conflict"      // refused: nothing overwritten (exit 3)
	OutcomeStale        Outcome = "stale"         // the fingerprint is not in the current scan
)

// FixAction says what the engine did with a finding.
type FixAction string

const (
	ActionApplied         FixAction = "applied"          // precondition and apply ran (or were refused)
	ActionPreviewed       FixAction = "previewed"        // --dry-run: preview only
	ActionAwaitingConsent FixAction = "awaiting-consent" // confirm remedy without consent
	ActionManual          FixAction = "manual"           // only the operator can act
	ActionNoRemedy        FixAction = "no-remedy"        // the finding declares no remedy
)

// FixResult is one finding's line in a fix pass. Finding is redacted; Preview
// and Detail pass through the same redactor.
type FixResult struct {
	Finding Finding   `json:"finding"`
	Remedy  *Remedy   `json:"remedy,omitempty"`
	Action  FixAction `json:"action"`
	Outcome Outcome   `json:"outcome,omitempty"`
	Preview string    `json:"preview,omitempty"`
	// Detail explains a refusal, an apply error, or how the outcome was
	// verified.
	Detail string `json:"detail,omitempty"`
	// Evidence is the owning check's new evidence when the finding is still
	// present after apply.
	Evidence map[string]string `json:"evidence,omitempty"`
}

// FixCounts tallies a pass.
type FixCounts struct {
	Fixed           int `json:"fixed"`
	StillPresent    int `json:"still_present"`
	Blocked         int `json:"blocked"`
	Conflict        int `json:"conflict"`
	Stale           int `json:"stale"`
	Previewed       int `json:"previewed"`
	AwaitingConsent int `json:"awaiting_consent"`
	Manual          int `json:"manual"`
	NoRemedy        int `json:"no_remedy"`
}

// FixReport is the result of one `doctor --fix` pass.
type FixReport struct {
	V       int         `json:"v"`
	DryRun  bool        `json:"dry_run"`
	Results []FixResult `json:"results"`
	Counts  FixCounts   `json:"counts"`
	// Doctor is the state after verification: every check a remedy re-ran
	// carries its re-run result; the rest carry the scan's.
	Doctor DoctorResult `json:"doctor"`
	// ExitCode follows ADR-025 § 4: 3 on any conflict, else 4 on any blocked
	// remedy, else the post-fix doctor exit code.
	ExitCode int `json:"exit_code"`
}

// FixOptions selects and authorizes a pass.
type FixOptions struct {
	// DryRun prints every preview and changes nothing.
	DryRun bool
	// Yes consents to every confirm remedy.
	Yes bool
	// Consent, when set, is asked about each confirm remedy Yes does not
	// already cover (interactive consent). It receives redacted copies, and
	// the remedy's Preview is the verb's live preview.
	Consent func(Finding, Remedy) bool
	// ConsentAuto extends Consent to auto remedies, so the interactive CLI
	// can let the operator skip a safe remedy too. It has no effect without
	// Consent.
	ConsentAuto bool
	// Only restricts the pass to findings whose code or check ID is listed.
	Only []string
	// Severities restricts the pass to findings of these severities.
	Severities []Severity
	// Progress, when set, receives each result as it is decided.
	Progress func(FixResult)
}

// ParseSeverity validates a severity name.
func ParseSeverity(s string) (Severity, error) {
	for _, v := range Severities {
		if strings.EqualFold(s, string(v)) {
			return v, nil
		}
	}
	return "", fmt.Errorf("unknown severity %q (want one of blocker, warning, housekeeping, info)", s)
}

func (o FixOptions) selects(f Finding) bool {
	if len(o.Only) > 0 {
		hit := false
		for _, want := range o.Only {
			if strings.EqualFold(want, f.Code) || want == f.Check {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(o.Severities) > 0 {
		hit := false
		for _, s := range o.Severities {
			if s == f.Severity {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// Fixer runs the remedy engine over a check registry.
type Fixer struct {
	Registry *Registry
	Verbs    *VerbRegistry
	// Env is the scan's environment. Verification re-runs a check in a fresh
	// Env with the same inputs and a clock advanced by the elapsed time.
	Env    *Env
	Runner Runner
	// Log, when set, receives one entry per remedy the engine attempted.
	Log *FixLog

	scanStart time.Time
	results   []CheckResult // raw (unredacted) scan results, in registry order
	latest    map[string]CheckResult
}

// NewFixer builds a Fixer over the default registry and the built-in verbs
// for the current directory, with the fix log in the machine-state directory.
// A fix log that cannot be resolved is returned as an error alongside a
// usable Fixer that keeps no log.
func NewFixer(cfg *config.Config, cfgErr error, client *gh.Client, adapters []string) (*Fixer, error) {
	cwd, _ := os.Getwd()
	env := &Env{Cfg: cfg, CfgErr: cfgErr, Client: client, Cwd: cwd, Now: time.Now(), Adapters: adapters}
	fx := &Fixer{Registry: DefaultRegistry(), Verbs: BuiltinVerbs(env), Env: env}
	path, err := DefaultFixLogPath()
	if err != nil {
		return fx, err
	}
	fx.Log = &FixLog{Path: path}
	return fx, nil
}

// Scan runs every registered check and remembers the raw results. Run calls
// it; IPC callers call it once and then ApplyFingerprint per request.
func (fx *Fixer) Scan(ctx context.Context) []CheckResult {
	fx.scanStart = time.Now()
	fx.results = fx.Runner.Run(ctx, fx.Registry, fx.Env)
	fx.latest = map[string]CheckResult{}
	return cloneResults(fx.results)
}

// Run is one `doctor --fix` pass: scan, then decide every selected finding.
func (fx *Fixer) Run(ctx context.Context, opts FixOptions) FixReport {
	fx.Scan(ctx)
	var pending []Finding
	for _, r := range fx.results {
		for _, f := range r.Findings {
			if opts.selects(f) {
				pending = append(pending, f)
			}
		}
	}
	sort.SliceStable(pending, func(a, b int) bool {
		return pending[a].Severity.rank() < pending[b].Severity.rank()
	})

	rep := FixReport{V: SchemaVersion, DryRun: opts.DryRun, Results: []FixResult{}}
	for _, f := range pending {
		res := fx.decide(ctx, f, opts)
		rep.Results = append(rep.Results, res)
		if opts.Progress != nil {
			opts.Progress(res)
		}
	}
	return fx.finish(rep)
}

// preferredRemedy is the first remedy the engine can act on; manual remedies
// are returned only when there is nothing else.
func preferredRemedy(f Finding) (Remedy, bool) {
	for _, r := range f.Remedies {
		if r.Kind == RemedyAuto || r.Kind == RemedyConfirm {
			return r, true
		}
	}
	for _, r := range f.Remedies {
		if r.Kind == RemedyManual {
			return r, true
		}
	}
	return Remedy{}, false
}

func (fx *Fixer) decide(ctx context.Context, f Finding, opts FixOptions) FixResult {
	rem, ok := preferredRemedy(f)
	if !ok {
		return newFixResult(f, nil, ActionNoRemedy, "", "", "")
	}
	if rem.Kind == RemedyManual {
		return newFixResult(f, &rem, ActionManual, OutcomeSkipped, "", "only the operator can act on this finding")
	}
	verb, registered := fx.Verbs.Lookup(rem.Verb)
	preview := rem.Preview
	if registered {
		if p, err := verb.Preview(ctx, f); err == nil && p != "" {
			preview = p
		}
	}
	if opts.DryRun {
		detail := ""
		switch {
		case !registered:
			detail = fmt.Sprintf("verb %q is not registered: --fix would refuse this remedy", rem.Verb)
		case rem.Kind == RemedyConfirm:
			detail = "a confirm remedy: --fix applies it only with --yes or interactive consent"
		}
		return newFixResult(f, &rem, ActionPreviewed, "", preview, detail)
	}
	askConfirm := rem.Kind == RemedyConfirm && !opts.Yes
	askAuto := rem.Kind == RemedyAuto && opts.ConsentAuto && opts.Consent != nil
	if askConfirm || askAuto {
		shown := rem
		shown.Preview = preview
		if opts.Consent == nil || !opts.Consent(RedactFinding(f), redactRemedy(shown)) {
			detail := "a confirm remedy: not applied without --yes or interactive consent"
			if askAuto {
				detail = "declined interactively"
			}
			return newFixResult(f, &rem, ActionAwaitingConsent, OutcomeSkipped, preview, detail)
		}
	}
	return fx.apply(ctx, f, rem, verb, registered, preview)
}

// apply runs precondition, apply and verification for one consented remedy.
func (fx *Fixer) apply(ctx context.Context, f Finding, rem Remedy, verb RemedyVerb, registered bool, preview string) FixResult {
	res := newFixResult(f, &rem, ActionApplied, "", preview, "")
	verify := rem.Verify
	if verify == "" {
		verify = f.Check
	}
	// A remedy applied earlier in this pass may already have resolved this
	// finding; the latest re-run of its check is the authority.
	if latest, ok := fx.latest[verify]; ok && latest.Status != StatusTimeout && latest.Status != StatusSkipped &&
		!hasFingerprint(latest, f.Fingerprint) {
		res.Outcome = OutcomeFixed
		res.Detail = redact(fmt.Sprintf("already resolved: %s no longer reports it after an earlier remedy in this pass", verify))
		return res
	}
	if !registered {
		res.Outcome = OutcomeBlocked
		res.Detail = redact(fmt.Sprintf("verb %q is not registered, so the remedy was not applied (fails closed); act by hand: %s", rem.Verb, rem.Summary))
		fx.record(f, rem, res)
		return res
	}
	if err := verb.Precondition(ctx, f); err != nil {
		res.Outcome = refusal(err)
		res.Detail = redact("precondition failed at apply time, nothing was done: " + err.Error())
		fx.record(f, rem, res)
		return res
	}
	applyErr := verb.Apply(ctx, f)
	if applyErr != nil && (errors.Is(applyErr, ErrRemedyConflict) || errors.Is(applyErr, ErrRemedyBlocked)) {
		res.Outcome = refusal(applyErr)
		res.Detail = redact("refused at apply time: " + applyErr.Error())
		fx.record(f, rem, res)
		return res
	}

	after, ok := fx.Recheck(ctx, verify)
	switch {
	case !ok:
		res.Outcome = OutcomeStillPresent
		res.Detail = fmt.Sprintf("cannot verify: check %q is not registered", verify)
	case after.Status == StatusTimeout || after.Status == StatusSkipped:
		res.Outcome = OutcomeStillPresent
		res.Detail = fmt.Sprintf("cannot verify: %s did not complete (%s)", verify, after.Status)
	case hasFingerprint(after, f.Fingerprint):
		res.Outcome = OutcomeStillPresent
		res.Detail = fmt.Sprintf("%s still reports it after apply", verify)
		for _, nf := range after.Findings {
			if nf.Fingerprint == f.Fingerprint {
				res.Evidence = config.RedactEvidence(nf.Evidence)
			}
		}
	default:
		res.Outcome = OutcomeFixed
		res.Detail = fmt.Sprintf("verified: %s no longer reports it", verify)
	}
	if applyErr != nil {
		res.Detail += "; apply reported: " + applyErr.Error()
	}
	res.Detail = redact(res.Detail)
	fx.record(f, rem, res)
	return res
}

// ApplyFingerprint applies one remedy of one finding, identified the way IPC
// and Action Center cards identify it: the owning check, the fingerprint and
// the remedy ID. The check is re-run first; a fingerprint it no longer
// reports is `stale`. confirmed is the caller's consent for a confirm remedy.
func (fx *Fixer) ApplyFingerprint(ctx context.Context, checkID, fingerprint, remedyID string, confirmed bool) FixResult {
	if fx.scanStart.IsZero() {
		fx.scanStart = time.Now()
		fx.latest = map[string]CheckResult{}
	}
	cur, ok := fx.Recheck(ctx, checkID)
	var f *Finding
	if ok {
		for i := range cur.Findings {
			if cur.Findings[i].Fingerprint == fingerprint {
				f = &cur.Findings[i]
			}
		}
	}
	if f == nil {
		return FixResult{
			Finding: Finding{Check: checkID, Fingerprint: fingerprint, Evidence: map[string]string{}, Remedies: []Remedy{}},
			Action:  ActionApplied, Outcome: OutcomeStale,
			Detail: fmt.Sprintf("%s no longer reports fingerprint %s", checkID, fingerprint),
		}
	}
	var rem *Remedy
	for i := range f.Remedies {
		if f.Remedies[i].ID == remedyID {
			rem = &f.Remedies[i]
		}
	}
	if rem == nil {
		return newFixResult(*f, nil, ActionApplied, OutcomeBlocked, "", fmt.Sprintf("the finding declares no remedy %q", remedyID))
	}
	if rem.Kind == RemedyManual {
		return newFixResult(*f, rem, ActionManual, OutcomeSkipped, "", "only the operator can act on this finding")
	}
	verb, registered := fx.Verbs.Lookup(rem.Verb)
	preview := rem.Preview
	if rem.Kind == RemedyConfirm && !confirmed {
		return newFixResult(*f, rem, ActionAwaitingConsent, OutcomeSkipped, preview, "a confirm remedy needs consent")
	}
	return fx.apply(ctx, *f, *rem, verb, registered, preview)
}

// Recheck re-runs one registered check in a fresh environment and records
// the result as the latest for that check. ok is false for an unregistered
// check. Dependencies are not re-run: they passed for the finding to exist.
func (fx *Fixer) Recheck(ctx context.Context, checkID string) (CheckResult, bool) {
	idx, ok := fx.Registry.index[checkID]
	if !ok {
		return CheckResult{}, false
	}
	res := fx.Runner.runOne(ctx, fx.Registry.checks[idx], fx.verifyEnv())
	if fx.latest == nil {
		fx.latest = map[string]CheckResult{}
	}
	fx.latest[checkID] = res
	return cloneResult(res), true
}

// verifyEnv is a fresh Env with the scan's inputs and a clock advanced by the
// time elapsed since the scan, so age thresholds read the same way they did.
func (fx *Fixer) verifyEnv() *Env {
	e := fx.Env
	now := e.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !fx.scanStart.IsZero() {
		now = now.Add(time.Since(fx.scanStart))
	}
	return &Env{Cfg: e.Cfg, CfgErr: e.CfgErr, Client: e.Client, Cwd: e.Cwd, Now: now, Adapters: e.Adapters}
}

// finish computes the post-fix doctor state and the exit code.
func (fx *Fixer) finish(rep FixReport) FixReport {
	merged := make([]CheckResult, len(fx.results))
	for i, r := range fx.results {
		if l, ok := fx.latest[r.ID]; ok {
			r = l
		}
		merged[i] = r
	}
	rep.Doctor = BuildResult(cloneResults(merged))
	fx.Env.mu.Lock()
	rep.Doctor.InstallInstructions = fx.Env.install
	rep.Doctor.Adapters = fx.Env.adapter
	fx.Env.mu.Unlock()
	for _, r := range rep.Results {
		switch r.Outcome {
		case OutcomeFixed:
			rep.Counts.Fixed++
		case OutcomeStillPresent:
			rep.Counts.StillPresent++
		case OutcomeBlocked:
			rep.Counts.Blocked++
		case OutcomeConflict:
			rep.Counts.Conflict++
		case OutcomeStale:
			rep.Counts.Stale++
		}
		switch r.Action {
		case ActionPreviewed:
			rep.Counts.Previewed++
		case ActionAwaitingConsent:
			rep.Counts.AwaitingConsent++
		case ActionManual:
			rep.Counts.Manual++
		case ActionNoRemedy:
			rep.Counts.NoRemedy++
		}
	}
	rep.ExitCode = FixExitCode(rep.Results, rep.Doctor.ExitCode)
	return rep
}

// FixExitCode applies ADR-025 § 4 under --fix: 3 (conflict) and 4 (blocked)
// take precedence over the post-verification doctor code.
func FixExitCode(results []FixResult, doctorExit int) int {
	blocked := false
	for _, r := range results {
		if r.Outcome == OutcomeConflict {
			return 3
		}
		if r.Outcome == OutcomeBlocked {
			blocked = true
		}
	}
	if blocked {
		return 4
	}
	return doctorExit
}

func (fx *Fixer) record(f Finding, rem Remedy, res FixResult) {
	if fx.Log == nil {
		return
	}
	// A log that cannot be written must not undo a verified fix; the result
	// says so instead.
	_ = fx.Log.Append(FixLogEntry{
		Time: time.Now().UTC(), Code: f.Code, Check: f.Check, Fingerprint: f.Fingerprint,
		Remedy: rem.ID, Verb: rem.Verb, Outcome: res.Outcome, Detail: res.Detail,
	})
}

func refusal(err error) Outcome {
	if errors.Is(err, ErrRemedyConflict) {
		return OutcomeConflict
	}
	return OutcomeBlocked
}

func hasFingerprint(r CheckResult, fp string) bool {
	for _, f := range r.Findings {
		if f.Fingerprint == fp {
			return true
		}
	}
	return false
}

func newFixResult(f Finding, rem *Remedy, action FixAction, outcome Outcome, preview, detail string) FixResult {
	res := FixResult{Finding: RedactFinding(f), Action: action, Outcome: outcome, Preview: redact(preview), Detail: redact(detail)}
	if rem != nil {
		r := redactRemedy(*rem)
		res.Remedy = &r
	}
	return res
}

func redact(s string) string { return config.RedactSecretString(s) }

// redactRemedy passes a remedy's operator-facing text through the redactor: a
// preview can quote a process command line, which can carry a credential.
func redactRemedy(r Remedy) Remedy {
	r.Summary = redact(r.Summary)
	r.Preview = redact(r.Preview)
	if len(r.Steps) > 0 {
		steps := make([]string, len(r.Steps))
		for i, s := range r.Steps {
			steps[i] = redact(s)
		}
		r.Steps = steps
	}
	return r
}

// cloneResults copies results deeply enough that redacting the copy never
// touches the raw findings the engine hands to verbs.
func cloneResults(in []CheckResult) []CheckResult {
	out := make([]CheckResult, len(in))
	for i, r := range in {
		out[i] = cloneResult(r)
	}
	return out
}

func cloneResult(r CheckResult) CheckResult {
	r.Findings = append([]Finding(nil), r.Findings...)
	return r
}
