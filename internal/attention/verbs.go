package attention

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// The verb registry is the security boundary (ADR 015 §B/§J): every
// `option.verb` MUST resolve to an entry here — a closed allowlist of
// deterministic operations the fleet already trusts. A resolution can trigger
// ONLY a registered verb, with args bounded by the request. A resolve naming an
// unknown option, or an option whose verb is not registered, is rejected — a
// surface can never conjure a verb or arg the producer did not declare.

// Verb is a registered deterministic operation an option may bind to.
type Verb = string

const (
	// --- Verbs that exist today (wired to existing trusted primitives) ---

	// VerbQueueAdd enqueues an issue for dispatch (Scheduler.QueueAddItem).
	VerbQueueAdd Verb = "queue.add"
	// VerbIssueRemoveBlockedBy removes a stale blockedBy edge
	// (IssueService.RemoveBlockedByNumber).
	VerbIssueRemoveBlockedBy Verb = "issue.removeBlockedBy"
	// VerbAutonomousResume resumes a paused fleet (AutonomousScheduler.Resume).
	VerbAutonomousResume Verb = "autonomous.resume"
	// VerbAutonomousRescan wakes the scheduler loop immediately.
	VerbAutonomousRescan Verb = "autonomous.rescan"
	// VerbAutonomousComplete marks a human-only task done and requeues
	// dependents (NotifyComplete → promoteUnblockedToReady).
	VerbAutonomousComplete Verb = "autonomous.complete"
	// VerbAutonomousClearIssueFailures clears an issue's failure cooldown so it
	// can be retried after manual triage.
	VerbAutonomousClearIssueFailures Verb = "autonomous.clearIssueFailures"
	// VerbProjectSyncStatus sets a board Status field (e.g. promote to Ready).
	VerbProjectSyncStatus Verb = "project.syncStatus"
	// VerbIssueClose closes a GitHub issue.
	VerbIssueClose Verb = "issue.close"

	// --- Verbs E1 adds, each fronting an existing enforcement path (ADR §B) ---

	// VerbBudgetRaiseCeiling applies a runtime budget-ceiling override honored
	// by safety_rails before the budget_ceiling_hit terminal.
	VerbBudgetRaiseCeiling Verb = "budget.raiseCeiling"
	// VerbRunRetryWithEscalation clears the failure cooldown AND forces the next
	// model tier for the retry.
	VerbRunRetryWithEscalation Verb = "run.retryWithEscalation"

	// VerbIssueApproveArchitecture grants the architecture-approval gate
	// (#4098/#4222) for one issue: it applies the configured approval label
	// (default `approved:architecture`, idempotent — an existing label is
	// reused, never duplicated), clears the issue's failure cooldown, and
	// requeues it so the next run passes the gate instead of re-halting.
	//
	// This is the first verb that mutates labels. That is deliberate and
	// bounded: the label name is not caller-supplied — it is resolved from
	// pipeline.architecture_approval.approval_label on the daemon side — so a
	// surface can request "approve THIS issue's architecture" and nothing else.
	// It cannot name an arbitrary label, and the args carry only owner/repo/
	// issue, which the producer already declared.
	VerbIssueApproveArchitecture Verb = "issue.approveArchitecture"

	// VerbDependabotEnableAlerts turns the forge's dependency-alert scanning ON
	// for one repository that is ALREADY in the configured repo list (#344).
	//
	// It exists because the condition it repairs is, unusually for a security
	// card, repairable by a bounded deterministic operation. #343 deliberately
	// ships no repair verb — nothing in a closed allowlist can patch a
	// vulnerability, and a button implying otherwise is worse than none. "This
	// repository is not being scanned" is the opposite case: one setting, one
	// flip, no judgement, and the card is a dead end without it.
	//
	// The argument surface is EMPTY, and that is the security property rather
	// than a convenience. The target is read from the persisted request's
	// Context.Repo — what the producer declared — and is then required to be in
	// the configured repo list, so neither half of the target can come from the
	// resolving surface. See ExecuteEnableAlerts, which is the only place the
	// verb is allowed to resolve a target.
	VerbDependabotEnableAlerts Verb = "dependabot.enableAlerts"

	// VerbWorkspaceAddRepo brings ONE repository that is present in the
	// workspace but absent from the manifest under management (#706).
	//
	// coverage-gap ships without a repair verb by design: its comment recorded
	// that no registered verb could edit the workspace manifest, and a button
	// that silently does nothing is worse than no button (Invariant 3). #703
	// inverted that reasoning by giving the manifest a deterministic writer, so
	// the condition is now exactly the shape the registry is for — one bounded
	// operation, no judgement, and a card that is otherwise a dead end. Same
	// argument as VerbDependabotEnableAlerts above.
	//
	// The argument surface is EMPTY, which is the security property and not a
	// convenience. The target is read from the persisted request's Context.Repo
	// — what the producer declared — so the resolving surface cannot name a
	// repository. See ExecuteAddRepo, the only place the verb resolves a target.
	//
	// Its configured-set check is the INVERSE of every other verb's: this verb
	// exists to act on a repository that is NOT yet configured, so a target
	// already in the list is refused as nothing-to-do rather than required to be
	// present.
	VerbWorkspaceAddRepo Verb = "workspace.addRepo"

	// VerbNoop is the explicit "do nothing but resolve" choice — the registry
	// binding for the ADR's leave / keep-paused / wait / halt options, where the
	// operator deliberately declines to mutate the fleet. Registry-gated like any
	// other verb so a surface cannot smuggle an unregistered no-action.
	VerbNoop Verb = "noop"
)

// registry is the closed allowlist. A verb absent from this map cannot be
// executed by any resolution.
var registry = map[Verb]struct{}{
	VerbQueueAdd:                     {},
	VerbIssueRemoveBlockedBy:         {},
	VerbAutonomousResume:             {},
	VerbAutonomousRescan:             {},
	VerbAutonomousComplete:           {},
	VerbAutonomousClearIssueFailures: {},
	VerbProjectSyncStatus:            {},
	VerbIssueClose:                   {},
	VerbBudgetRaiseCeiling:           {},
	VerbRunRetryWithEscalation:       {},
	VerbIssueApproveArchitecture:     {},
	VerbDependabotEnableAlerts:       {},
	VerbWorkspaceAddRepo:             {},
	VerbNoop:                         {},
}

// IsRegisteredVerb reports whether v is in the closed allowlist.
func IsRegisteredVerb(v string) bool {
	_, ok := registry[v]
	return ok
}

// cliExecutableVerbs is the subset of registered verbs a standalone CLI
// process can execute without a daemon connection — the deterministic,
// file-based verbs cliVerbExecutor implements directly
// (cmd/nightgauge/attention.go). Every other registered verb needs the live
// scheduler/GitHub clients only the daemon (or the VSCode extension's private
// IPC connection) holds.
//
// workspace.addRepo is deliberately NOT here despite writing a file. Resolving
// the repository's project board goes through the authoritative repo→project
// resolver, which reads config and can reach the forge; a standalone CLI
// process would either duplicate that resolution or write an entry with no
// project number — the `project_number: 0` misroute the writer exists to
// prevent. It is daemon-executed for the resolver, not for the write.
var cliExecutableVerbs = map[Verb]struct{}{
	VerbNoop:                   {},
	VerbBudgetRaiseCeiling:     {},
	VerbRunRetryWithEscalation: {},
}

// IsCLIExecutableVerb reports whether v can be executed by a standalone CLI
// process with no daemon connection (attention show's executability
// annotation, #263).
func IsCLIExecutableVerb(v string) bool {
	_, ok := cliExecutableVerbs[v]
	return ok
}

// RegisteredVerbs returns the sorted allowlist, for diagnostics and tests.
func RegisteredVerbs() []string {
	out := make([]string, 0, len(registry))
	for v := range registry {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// ValidateOption re-validates a resolve against the persisted request and the
// registry (ADR 015 §J defense-in-depth). It returns the resolved option on
// success, or an error when the option id is unknown or its verb is not
// registered. Callers surface a generic client error (§J error hygiene) and log
// the detail.
func ValidateOption(req *DecisionRequest, optionID string) (Option, error) {
	opt := req.FindOption(optionID)
	if opt == nil {
		return Option{}, fmt.Errorf("attention: option %q is not declared on request %s", optionID, req.ID)
	}
	if !IsRegisteredVerb(opt.Verb) {
		return Option{}, fmt.Errorf("attention: option %q binds unregistered verb %q", optionID, opt.Verb)
	}
	return *opt, nil
}

// VerbExecutor executes a registered verb with the option's bounded args
// against the request's context. It is implemented by surfaces that hold the
// capabilities a verb needs: the IPC server (scheduler + GitHub clients) is the
// full executor; the CLI provides a subset. An executor MUST reject any verb it
// cannot perform rather than silently succeed.
//
// The store calls ExecuteVerb BEFORE it applies and persists the resolution
// (CAS): a verb failure short-circuits Resolve's error return and the request
// stays untouched — open, unpersisted, unjournaled — instead of being
// silently consumed.
type VerbExecutor interface {
	ExecuteVerb(ctx context.Context, req *DecisionRequest, opt Option) error
}

// VerbExecutionError reports a verb that failed to apply. Retryable
// distinguishes a transient condition the SAME surface can clear (e.g. the
// daemon starts) from one it fundamentally cannot (e.g. this CLI process has
// no code path for this verb at all).
type VerbExecutionError struct {
	Verb      string
	Retryable bool
	Err       error
}

// Error implements error.
func (e *VerbExecutionError) Error() string {
	how := "not retryable from this surface"
	if e.Retryable {
		how = "retryable — retry once the condition clears"
	}
	return fmt.Sprintf("verb %q failed (%s): %v", e.Verb, how, e.Err)
}

// Unwrap supports errors.Is/errors.As against the wrapped cause.
func (e *VerbExecutionError) Unwrap() error { return e.Err }

// NoopExecutor records resolutions without side effects. Used by read-only
// surfaces and tests; every verb is a no-op (still registry-validated upstream).
type NoopExecutor struct{}

// ExecuteVerb implements VerbExecutor.
func (NoopExecutor) ExecuteVerb(_ context.Context, _ *DecisionRequest, _ Option) error {
	return nil
}

// --- dependabot.enableAlerts (#344) -----------------------------------------

// ErrVerbTargetNotConfigured reports a resolution whose target repository is
// not in the configured repo list.
//
// It is a SECURITY refusal, not a lookup miss. The verb performs a real
// mutation on a real repository with the fleet's own credential, so the set of
// repositories it may touch has to come from configuration — the thing an
// operator edits deliberately — and never from the request being resolved.
var ErrVerbTargetNotConfigured = errors.New("attention: verb target repository is not in the configured repo list")

// ErrVerbArgsNotAccepted reports arguments supplied to a verb whose contract is
// that it takes none.
//
// Rejecting rather than ignoring is the point: an ignored argument is a silent
// invitation to add policy later at the surface, which is exactly how a bounded
// verb becomes a general one. If this verb ever needs a parameter, it has to be
// declared here and defended here.
var ErrVerbArgsNotAccepted = errors.New("attention: this verb accepts no arguments")

// DependabotAlertEnabler is the single forge capability
// dependabot.enableAlerts needs.
//
// Declared as a one-method interface rather than taken as a whole forge client
// so the verb layer depends on the one operation it performs: an executor that
// can enable scanning cannot, through this seam, also close an issue or push a
// branch. The daemon passes its forge adapter; tests pass a recorder.
type DependabotAlertEnabler interface {
	EnableSecurityAlerts(ctx context.Context, owner, repo string) error
}

// RepoInConfiguredSet reports whether repo is in the configured repo list,
// matching on the canonical "owner/name" spec or on the bare name so a manifest
// entry written either way counts.
//
// It is the verb-side twin of sweep.WorkspaceInput.Covers and MUST agree with
// it: the sweep decides which repos may be carded, and this decides which
// carded repos may be acted on. If the two matchers disagree, the producer
// emits a card whose repair button the executor then refuses — a dead
// affordance on the one card that exists to have a working one. They are
// separate functions only because the sweep package imports this one and not
// the reverse; sweep's dependabotcoverage_test.go pins them to the same answers.
func RepoInConfiguredSet(configured []string, repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	for _, c := range configured {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.EqualFold(c, repo) {
			return true
		}
		if i := strings.Index(c, "/"); i >= 0 && strings.EqualFold(c[i+1:], repo) {
			return true
		}
		if i := strings.Index(repo, "/"); i >= 0 && strings.EqualFold(c, repo[i+1:]) {
			return true
		}
	}
	return false
}

// ExecuteEnableAlerts is the ONLY implementation of the
// dependabot.enableAlerts verb, so no surface can re-derive its target
// differently.
//
// Four refusals guard one mutation, in this order:
//
//  1. the option must actually bind this verb (a mis-dispatched arm must not
//     silently perform a repository mutation);
//  2. the option must carry NO arguments — the verb takes no caller-supplied
//     policy at all, so anything present means the caller believes it can steer
//     something it cannot;
//  3. the target is req.Context.Repo, the repository the PRODUCER declared when
//     it raised the card, never anything read from the resolve payload;
//  4. that target must be in the configured repo list.
//
// (3) is what actually closes the hole and (4) is defense in depth: even a
// store write that somehow carried a foreign repo cannot reach the forge unless
// configuration already covers it.
func ExecuteEnableAlerts(ctx context.Context, enabler DependabotAlertEnabler, req *DecisionRequest, opt Option, configuredRepos []string) error {
	if req == nil {
		return fmt.Errorf("attention: %s requires the persisted request", VerbDependabotEnableAlerts)
	}
	if opt.Verb != VerbDependabotEnableAlerts {
		return fmt.Errorf("attention: %s executor invoked for verb %q", VerbDependabotEnableAlerts, opt.Verb)
	}
	if len(opt.Args) > 0 {
		return fmt.Errorf("%w: %s was given %d", ErrVerbArgsNotAccepted, VerbDependabotEnableAlerts, len(opt.Args))
	}

	repo := strings.TrimSpace(req.Context.Repo)
	if repo == "" {
		return fmt.Errorf("attention: %s: request %s names no repository", VerbDependabotEnableAlerts, req.ID)
	}
	if !RepoInConfiguredSet(configuredRepos, repo) {
		return fmt.Errorf("%w: %q", ErrVerbTargetNotConfigured, repo)
	}

	owner, name, found := strings.Cut(repo, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("attention: %s target %q is not owner/name", VerbDependabotEnableAlerts, repo)
	}
	if enabler == nil {
		// A surface that cannot perform the verb must say so rather than
		// succeed: the store CAS-resolves only after the verb returns nil, so a
		// silent success here would consume the card and leave scanning off.
		return fmt.Errorf("attention: %s is not available on this surface", VerbDependabotEnableAlerts)
	}
	return enabler.EnableSecurityAlerts(ctx, owner, name)
}

// --- workspace.addRepo (#706) -----------------------------------------------

// ErrVerbTargetAlreadyConfigured reports a workspace.addRepo resolution whose
// target repository is already in the configured repo list.
//
// It is the inverse of ErrVerbTargetNotConfigured and, unlike it, is a
// nothing-to-do refusal rather than a security one: the card exists only while
// the repository is uncovered, so a target that is already configured means the
// condition was repaired by something else between the sweep and the click.
// Refusing keeps the store from CAS-resolving a card whose repair never ran.
var ErrVerbTargetAlreadyConfigured = errors.New("attention: verb target repository is already in the configured repo list")

// WorkspaceRepoAdder is the single capability workspace.addRepo needs.
//
// One method rather than a whole manifest client, for the same reason
// DependabotAlertEnabler is: an executor that can bring a repository under
// management cannot, through this seam, also remove one or rewrite routing.
// The daemon passes an implementation backed by the deterministic manifest
// writer; tests pass a recorder.
type WorkspaceRepoAdder interface {
	AddWorkspaceRepo(ctx context.Context, repo string) error
}

// ExecuteAddRepo performs workspace.addRepo against the request's declared
// target, enforcing the four properties the verb promises:
//
//  1. the option carries no arguments,
//  2. the target comes from the persisted request's Context.Repo and nowhere
//     else,
//  3. that target must NOT already be in the configured repo list, and
//  4. a surface without the capability fails loudly rather than succeeding.
//
// (4) matters more here than it reads: the store CAS-resolves only after the
// verb returns nil, so a silent success would consume the card and leave the
// repository exactly as unwatched as before — with the one affordance that
// could have fixed it now gone.
func ExecuteAddRepo(ctx context.Context, adder WorkspaceRepoAdder, req *DecisionRequest, opt Option, configuredRepos []string) error {
	if req == nil {
		return fmt.Errorf("attention: %s requires the persisted request", VerbWorkspaceAddRepo)
	}
	if opt.Verb != VerbWorkspaceAddRepo {
		return fmt.Errorf("attention: %s executor invoked for verb %q", VerbWorkspaceAddRepo, opt.Verb)
	}
	if len(opt.Args) > 0 {
		return fmt.Errorf("%w: %s was given %d", ErrVerbArgsNotAccepted, VerbWorkspaceAddRepo, len(opt.Args))
	}

	repo := strings.TrimSpace(req.Context.Repo)
	if repo == "" {
		return fmt.Errorf("attention: %s: request %s names no repository", VerbWorkspaceAddRepo, req.ID)
	}
	if RepoInConfiguredSet(configuredRepos, repo) {
		return fmt.Errorf("%w: %q", ErrVerbTargetAlreadyConfigured, repo)
	}
	if adder == nil {
		return fmt.Errorf("attention: %s is not available on this surface", VerbWorkspaceAddRepo)
	}
	return adder.AddWorkspaceRepo(ctx, repo)
}
