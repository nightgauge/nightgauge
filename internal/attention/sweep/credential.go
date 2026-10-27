package sweep

// Forge-credential card: one card per owner when the forge rejects the sweep's
// credential (#2578).
//
// A sweep skipped on a 401 reconciles nothing, which is correct: a partial
// view must never retract a live card. It also raises nothing, so the Action
// Center keeps showing yesterday's cards while no producer has observed a
// repository for hours. The skip was a log line per repo per sweep, which no
// operator reads. This card is the signal.
//
// It is per owner, not per repo: one credential serves every repository of an
// owner, so N repos failing together is one fact. It retracts on the first
// pass where every swept repository of that owner was evaluated.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
)

// ProducerForgeCredential is the stable producer id. It is half of the sticky
// (producer, idempotency_key) identity, so it must never change.
const ProducerForgeCredential = "forge-credential"

// CredentialCardKey is the card's idempotency key for owner.
func CredentialCardKey(owner string) string { return "forge-credential:" + owner }

// CredentialObservation is one swept repository's credential outcome.
type CredentialObservation struct {
	// Repo is "owner/name".
	Repo string
	// Unauthorized is true when the forge rejected the credential for Repo.
	Unauthorized bool
	// Evaluated is true when the sweep reached and reconciled Repo. A repo
	// that was skipped for any reason, or never reached, is not evidence that
	// the credential works.
	Evaluated bool
	// Reason is the skip reason, quoted on the card.
	Reason string
}

// CredentialCard is the card for owner's rejected credential. repos are the
// affected repositories; reason is one of their skip reasons.
//
// The fingerprint is the owner alone, never the time or the error text: the
// same dead credential re-observed every sweep refreshes the card silently
// instead of re-alerting.
func CredentialCard(owner string, repos []string, reason string) attention.DecisionRequest {
	sorted := append([]string(nil), repos...)
	sort.Strings(sorted)

	var b strings.Builder
	fmt.Fprintf(&b, "GitHub rejected Nightgauge's credential for %s (401 Bad credentials). ", owner)
	b.WriteString("The attention sweep skipped these repositories and left their cards as they were, ")
	b.WriteString("so the Action Center is not observing them:\n\n")
	for _, r := range sorted {
		fmt.Fprintf(&b, "- %s\n", r)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		fmt.Fprintf(&b, "\nFirst failure: %s\n", truncateReason(reason))
	}
	b.WriteString("\nRun `nightgauge doctor` to see which credential resolves for this owner. ")
	b.WriteString("If the GitHub App was uninstalled or its key rotated, or the personal token was revoked, ")
	b.WriteString("re-authenticate. The sweep retries on every pass, and this card retracts on its own ")
	b.WriteString("once every repository of the owner sweeps cleanly.")

	return attention.DecisionRequest{
		IdempotencyKey: CredentialCardKey(owner),
		Producer:       ProducerForgeCredential,
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingFleet,
		Title:          fmt.Sprintf("GitHub rejects the credential for %s", owner),
		Body:           b.String(),
		Standing:       true,
		Fingerprint:    "owner:" + owner,
		ExpiresAt:      time.Now().UTC().Add(attention.StandingExpiry).Format(time.RFC3339Nano),
		Context: attention.Context{
			Repo:    sorted[0],
			Blocker: fmt.Sprintf("forge credential rejected for %d repositor%s of %s", len(sorted), plural(len(sorted), "y", "ies"), owner),
		},
		Options: []attention.Option{
			{ID: "dismiss", Label: "Dismiss — I've seen it", Verb: attention.VerbNoop, Style: attention.StyleDefault},
		},
		DefaultAction: attention.ExpireNoop,
	}
}

// CredentialResult counts what ReconcileCredentialCards changed.
type CredentialResult struct {
	Created      int
	Updated      int
	AutoResolved int
}

// ReconcileCredentialCards raises one card per owner with any unauthorized
// repository, and retracts an owner's card when every one of its swept
// repositories was evaluated. An owner with a repository that was neither is
// left alone: that pass observed nothing about the credential.
func ReconcileCredentialCards(store *attention.Store, obs []CredentialObservation) (CredentialResult, error) {
	var res CredentialResult
	type ownerState struct {
		unauthorized []string
		reason       string
		allEvaluated bool
	}
	owners := map[string]*ownerState{}
	var order []string
	for _, o := range obs {
		owner, _, ok := strings.Cut(o.Repo, "/")
		if !ok || owner == "" {
			continue
		}
		st := owners[owner]
		if st == nil {
			st = &ownerState{allEvaluated: true}
			owners[owner] = st
			order = append(order, owner)
		}
		if o.Unauthorized {
			st.unauthorized = append(st.unauthorized, o.Repo)
			if st.reason == "" {
				st.reason = o.Reason
			}
		}
		if !o.Evaluated {
			st.allEvaluated = false
		}
	}

	var errs []string
	for _, owner := range order {
		st := owners[owner]
		switch {
		case len(st.unauthorized) > 0:
			req := CredentialCard(owner, st.unauthorized, st.reason)
			id, idErr := attention.NewID()
			if idErr != nil {
				errs = append(errs, fmt.Sprintf("%s: mint id: %v", owner, idErr))
				continue
			}
			req.ID = id
			outcome, _, rErr := store.Raise(req)
			if rErr != nil {
				errs = append(errs, fmt.Sprintf("%s: raise: %v", owner, rErr))
				continue
			}
			switch outcome {
			case attention.OutcomeCreated:
				res.Created++
			case attention.OutcomeUpdated:
				res.Updated++
			}
		case st.allEvaluated:
			ok, rErr := store.AutoResolveKey(ProducerForgeCredential, CredentialCardKey(owner))
			if rErr != nil {
				errs = append(errs, fmt.Sprintf("%s: retract: %v", owner, rErr))
				continue
			}
			if ok {
				res.AutoResolved++
			}
		}
	}
	if len(errs) > 0 {
		return res, fmt.Errorf("forge-credential cards: %s", strings.Join(errs, "; "))
	}
	return res, nil
}

func truncateReason(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
