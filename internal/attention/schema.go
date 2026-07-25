// Package attention implements the local-first DecisionRequest store — the
// single authoritative writer for `.nightgauge/attention/` (ADR 015).
//
// A DecisionRequest is a durable, mutable record any pipeline component raises
// when it needs a human decision at a dead-end that is otherwise silent or
// one-way (work exhaustion, cascade pause, budget ceiling, branch-protection
// block, ...). Surfaces (VSCode extension, dashboard, future Discord bot) list,
// subscribe to, and resolve requests; a resolution routes ONLY to a
// deterministic verb in the closed registry (verbs.go).
//
// The schema mirrors the ADR-013 trace conventions: a `schema_version`
// envelope, additive payloads, and a `(producer, idempotency_key)` identity so
// readers tolerate unknown fields and new kinds without a version bump.
//
// See docs/decisions/015-decision-requests.md for the authoritative contract.
package attention

// SchemaVersion is the DecisionRequest envelope version. Bumped only on a
// breaking envelope change; payloads (kinds, options, context) evolve
// additively under the same version.
const SchemaVersion = 1

// Kind is the closed set of decision shapes (ADR 015 §A). Every kind maps to a
// card affordance a surface renders identically.
type Kind string

const (
	// KindUnblock — a run is blocked by an external condition a human must
	// clear (e.g. branch protection).
	KindUnblock Kind = "unblock"
	// KindApprove — a gate needs a yes/no with consequences (e.g. raise the
	// budget ceiling or halt).
	KindApprove Kind = "approve"
	// KindChoose — several viable paths; the operator picks one.
	KindChoose Kind = "choose"
	// KindProvideInput — the pipeline needs information only a human has
	// (e.g. re-authenticate, then retry).
	KindProvideInput Kind = "provide_input"
	// KindHandoff — a human-only task (e.g. an owner-action checklist) the
	// fleet cannot perform.
	KindHandoff Kind = "handoff"
	// KindResume — the fleet paused itself and asks whether to resume.
	KindResume Kind = "resume"
)

var allKinds = []Kind{KindUnblock, KindApprove, KindChoose, KindProvideInput, KindHandoff, KindResume}

// IsValidKind reports whether k is one of the declared kinds.
func IsValidKind(k Kind) bool {
	for _, c := range allKinds {
		if c == k {
			return true
		}
	}
	return false
}

// Severity drives alerting and SLA (ADR 015 §I).
type Severity string

const (
	// SeverityFYI — informational; badge only, no interruption.
	SeverityFYI Severity = "fyi"
	// SeverityBlockingRun — one run waits; badge + subtle toast.
	SeverityBlockingRun Severity = "blocking_run"
	// SeverityBlockingFleet — the fleet is stopped; interrupt-worthy.
	SeverityBlockingFleet Severity = "blocking_fleet"
)

var allSeverities = []Severity{SeverityFYI, SeverityBlockingRun, SeverityBlockingFleet}

// IsValidSeverity reports whether s is one of the declared severities.
func IsValidSeverity(s Severity) bool {
	for _, c := range allSeverities {
		if c == s {
			return true
		}
	}
	return false
}

// State is the lifecycle state machine: open → acknowledged → resolved | expired.
type State string

const (
	// StateOpen — raised, awaiting a human.
	StateOpen State = "open"
	// StateAcknowledged — a surface marked it seen (clears the badge) without
	// resolving. Non-blocking; a resolve can still follow.
	StateAcknowledged State = "acknowledged"
	// StateResolved — terminal: an option was applied. A HUMAN decided.
	StateResolved State = "resolved"
	// StateExpired — terminal: the sweep applied the default_action past
	// expires_at.
	StateExpired State = "expired"
	// StateAutoResolved — terminal: a repo sweep no longer observed the
	// standing condition and retracted the card (issue #92). Deliberately
	// distinct from StateResolved: a card the system withdrew because the
	// problem fixed itself is a different fact from a card someone acted on,
	// and the audit trail has to be able to tell them apart.
	StateAutoResolved State = "auto_resolved"
)

// IsTerminal reports whether the state is terminal (resolved | expired |
// auto_resolved).
func (s State) IsTerminal() bool {
	return s == StateResolved || s == StateExpired || s == StateAutoResolved
}

// ExpireNoop is the sentinel default_action meaning "on expiry, do nothing but
// mark the request expired" — a declared, safe default (ADR 015 §C).
const ExpireNoop = "expire_noop"

// OptionStyle is a purely visual weight hint for the card button.
type OptionStyle string

const (
	StylePrimary OptionStyle = "primary"
	StyleDefault OptionStyle = "default"
	StyleDanger  OptionStyle = "danger"
)

// DecisionRequest is one JSON object per request — the materialized read model
// persisted as `<id>.json` (ADR 015 §A).
type DecisionRequest struct {
	SchemaVersion int `json:"schema_version"`
	// ID is `dr_<uuidv7>` — the stable identity and resolution idempotency key.
	ID string `json:"id"`
	// IdempotencyKey is `<producer>:<scope>` — at most ONE open request per key.
	IdempotencyKey string   `json:"idempotency_key"`
	Kind           Kind     `json:"kind"`
	Severity       Severity `json:"severity"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Context        Context  `json:"context"`
	Producer       string   `json:"producer"`
	Options        []Option `json:"options"`
	// Steer is the optional free-text steer box (ADR 015 §G). Absent ⇒ no box.
	Steer *Steer `json:"steer,omitempty"`
	// CreatedAt / ExpiresAt are RFC3339Nano UTC.
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	// DefaultAction is an option id applied on expiry, or ExpireNoop.
	DefaultAction string    `json:"default_action"`
	Lifecycle     Lifecycle `json:"lifecycle"`

	// Standing marks a request raised from a STANDING condition — one that
	// persists across observations and is reconciled by `attention sweep`,
	// rather than an event a run loop observed once and moved past (issue
	// #92). Only standing requests auto-resolve when their condition clears.
	Standing bool `json:"standing,omitempty"`

	// Fingerprint is the MATERIAL state of a standing condition: which
	// required checks are failing, which merge blocker applies — never a
	// timestamp, elapsed duration, or counter that moves on its own.
	//
	// Two observations with equal fingerprints are the same condition, so
	// re-observation refreshes the card's content WITHOUT re-alerting; a
	// changed fingerprint is a genuine state transition and does alert. It is
	// also what "mute until the condition changes" is measured against.
	// Required on every standing request; ignored on event-scoped ones.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Context carries everything a card needs without a join, plus the ADR-013
// trace back-reference (ADR 015 §A).
type Context struct {
	Repo  string `json:"repo"`
	Issue int    `json:"issue,omitempty"`
	// RunID is absent for fleet-scoped requests (e.g. work exhaustion).
	RunID string `json:"run_id,omitempty"`
	// Stage is absent for run-scoped/fleet-scoped requests.
	Stage string `json:"stage,omitempty"`
	// CostSoFarUSD is the operator's own run spend, for context only.
	CostSoFarUSD float64 `json:"cost_so_far_usd,omitempty"`
	Blocker      string  `json:"blocker,omitempty"`
	// TraceRef points at the exact ADR-013 trace node that raised the request,
	// so the card deep-links into the Lifecycle Explorer and the audit is
	// bidirectional. Absent for fleet-scoped requests with no run trace.
	TraceRef *TraceRef `json:"trace_ref,omitempty"`
}

// TraceRef is the (run_id, producer, seq) key of the trace node that raised the
// request (ADR-013 identity).
type TraceRef struct {
	RunID    string `json:"run_id"`
	Producer string `json:"producer"`
	Seq      int64  `json:"seq"`
}

// Option is a machine-actionable choice — a button, never prose. Its verb MUST
// resolve to an entry in the closed verb registry (verbs.go / ADR 015 §B).
type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Verb  string `json:"verb"`
	// Args are bounded by the request; the writer never accepts args a surface
	// adds at resolve time.
	Args  map[string]any `json:"args,omitempty"`
	Style OptionStyle    `json:"style,omitempty"`
}

// Steer describes the optional free-text steer box (ADR 015 §G).
type Steer struct {
	Enabled bool   `json:"enabled"`
	Hint    string `json:"hint,omitempty"`
}

// Lifecycle is the state machine and its audit fields (ADR 015 §A).
type Lifecycle struct {
	State State `json:"state"`
	// Acknowledged is optional and non-blocking.
	Acknowledged *AckRecord `json:"acknowledged,omitempty"`
	// Muted suppresses alerting until the CONDITION changes, not until a
	// timer expires (issue #92). Non-terminal: the card stays in the inbox.
	Muted *MuteRecord `json:"muted,omitempty"`
	// Resolved, Expired and AutoResolved are mutually exclusive terminal
	// records.
	Resolved     *ResolvedRecord     `json:"resolved,omitempty"`
	Expired      *ExpiredRecord      `json:"expired,omitempty"`
	AutoResolved *AutoResolvedRecord `json:"auto_resolved,omitempty"`
}

// AckRecord records a non-blocking acknowledgement.
type AckRecord struct {
	Actor string `json:"actor"`
	At    string `json:"at"`
}

// MuteRecord audits a mute. Fingerprint pins the condition the operator chose
// to silence: the mute survives every re-observation of that same condition and
// is dropped the moment the fingerprint moves, so an operator who knows `main`
// is red because they are fixing it is not told again — but IS told when a
// second check starts failing (issue #92).
type MuteRecord struct {
	Actor string `json:"actor"`
	At    string `json:"at"`
	// Fingerprint is the request's fingerprint at mute time.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// AutoResolvedRecord audits a system retraction: a sweep evaluated the
// producer successfully and no longer observed the condition. Separate from
// ResolvedRecord (which always names a human actor and an option) precisely so
// the scorecard can count "problems that fixed themselves" apart from
// "decisions an operator made".
type AutoResolvedRecord struct {
	At string `json:"at"`
	// Producer is the producer whose sweep observed the condition had cleared.
	Producer string `json:"producer,omitempty"`
	// Reason is a short machine-stable explanation, e.g. ReasonConditionCleared.
	Reason string `json:"reason"`
}

// ResolvedRecord audits a resolution.
type ResolvedRecord struct {
	Actor    string `json:"actor"`
	At       string `json:"at"`
	OptionID string `json:"option_id"`
	// SteerText is present only when the operator typed steering.
	SteerText string `json:"steer_text,omitempty"`
	Note      string `json:"note,omitempty"`
}

// ExpiredRecord audits an expiry. Applied is the option id executed as the
// default_action, or ExpireNoop.
type ExpiredRecord struct {
	At      string `json:"at"`
	Applied string `json:"applied"`
}

// FindOption returns the option with the given id, or nil when absent.
func (r *DecisionRequest) FindOption(id string) *Option {
	for i := range r.Options {
		if r.Options[i].ID == id {
			return &r.Options[i]
		}
	}
	return nil
}

// IsOpenish reports whether the request is still actionable (open or
// acknowledged — i.e. not terminal).
func (r *DecisionRequest) IsOpenish() bool {
	return !r.Lifecycle.State.IsTerminal()
}

// IsMuted reports whether alerting on this request is currently suppressed.
func (r *DecisionRequest) IsMuted() bool {
	return r != nil && r.Lifecycle.Muted != nil
}
