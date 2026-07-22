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
	// StateResolved — terminal: an option was applied.
	StateResolved State = "resolved"
	// StateExpired — terminal: the sweep applied the default_action past
	// expires_at.
	StateExpired State = "expired"
)

// IsTerminal reports whether the state is a terminal (resolved | expired) state.
func (s State) IsTerminal() bool {
	return s == StateResolved || s == StateExpired
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
	// Resolved and Expired are mutually exclusive terminal records.
	Resolved *ResolvedRecord `json:"resolved,omitempty"`
	Expired  *ExpiredRecord  `json:"expired,omitempty"`
}

// AckRecord records a non-blocking acknowledgement.
type AckRecord struct {
	Actor string `json:"actor"`
	At    string `json:"at"`
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
