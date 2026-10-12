package tools

import (
	"regexp"
	"strings"
	"testing"
)

// firstGate is the request gate as #1570 first shipped it (c09f471b), frozen
// here so the property below holds against it whatever later edits do to
// the production copy.
var (
	firstGateRequest = regexp.MustCompile(`(?i)\b(?:file|create|open|raise|log|submit|make|write|add)\s+` +
		`(?:(?:[\w'-]+\s+){0,4}(?:issues?|tickets?|bug\s+reports?)\b(?:\s*#?\d)?|(?:it|that|this|them|those|one)\b)`)
	firstGateNumber       = regexp.MustCompile(`\s*#?\d$`)
	firstGateConfirmation = regexp.MustCompile(`(?i)^\W*(?:yes|yeah|yep|yup|ok|okay|sure|confirm(?:ed)?|approved?|go ahead|do it|please do|ship it|lgtm|sounds good)\b`)
)

func firstGate(message, quote string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	m, q := norm(message), norm(quote)
	if q == "" || !strings.Contains(strings.ToLower(m), strings.ToLower(q)) {
		return false
	}
	for _, loc := range firstGateRequest.FindAllStringIndex(q, -1) {
		if !firstGateNumber.MatchString(q[loc[0]:loc[1]]) {
			return true
		}
	}
	return firstGateConfirmation.MatchString(q) && strings.HasPrefix(strings.ToLower(m), strings.ToLower(q))
}

// gateCorpus is every message the gate tests use, and variations of each.
func gateCorpus() []string {
	base := []string{
		"thanks", "Thanks!", "thank you", "please", "Please.", "go for it", "approve", "confirm", "create it",
		"open it", "file that", "create that", "yes, file it", "Yes please", "ok thanks", "please file an​ issue",
		"Please don’t file it", "file an issue", "FILE AN ISSUE", "yes", "Yes, file it.", "sure, go ahead",
		"yes​", "​yes", "file an issue", "lgtm, ship it", "Do not file an issue for this.",
		"No need to file it", "File an issue? No, wait.", "Let me profile an issue in the parser",
		"Yes, but don't file it.", "Yes. What's the status?", "What's the status?", "Summarize issue 12",
		"Open issue #12 and tell me what it says", "Please file an issue for the flaky login test.",
		"can you   create a ticket for this", "Write up a bug report for the crash", "OK go ahead",
		"Sounds good, thanks", "Tell me about it. Yes it is urgent.", "do n​ot file an issue",
		"The tests pass now. Can you open a ticket to track the flaky one?", "file it", "make one", "add them",
	}
	var out []string
	for _, m := range base {
		out = append(out, m, strings.ToUpper(m), "please "+m, m+" thanks", "yes, "+m, m+". no.")
	}
	return out
}

// gateQuotes is every quote a model might pass for m: the whole message, each
// run of its words, and stock phrases.
func gateQuotes(m string) []string {
	quotes := []string{m, strings.ToLower(m), strings.ToUpper(m), "file an issue", "yes", "file it", "thanks",
		"please", "go ahead", "it", ""}
	words := strings.Fields(m)
	for i := range words {
		for j := i + 1; j <= len(words); j++ {
			quotes = append(quotes, strings.Join(words[i:j], " "))
		}
	}
	return quotes
}

// The gate never accepts what the first gate refused (security review of
// 8ff73794): every message and quote of the corpus that the first gate
// refuses, the gate refuses too, even with an issue proposed.
func TestRequested_NeverAcceptsWhatTheFirstGateRefused(t *testing.T) {
	checked := 0
	for _, m := range gateCorpus() {
		for _, q := range gateQuotes(m) {
			checked++
			if !firstGate(m, q) && Requested(m, q, true) {
				t.Errorf("Requested(%q, %q) = true; the first gate refused it", m, q)
			}
		}
	}
	if checked < 2000 {
		t.Fatalf("checked only %d pairs", checked)
	}
}

// The paths 8ff73794 opened, each closed: a courtesy alone is no consent, a
// fragment of a confirmation is no quote, a quote that differs from the
// message by an invisible character does not match, and a confirmation or a
// request that names no issue counts only after a reply that proposed one.
func TestRequested_ClosesTheAcceptingPaths(t *testing.T) {
	for _, c := range []struct {
		message, quote string
		proposed       bool
	}{
		{"thanks", "thanks", true},
		{"Thanks!", "Thanks!", true},
		{"thank you", "thank you", true},
		{"please", "please", true},
		{"Please.", "please", true},
		{"go for it", "go for it", true},
		{"go for it", "for it", true},
		{"thank you", "you", true},
		{"create it", "it", true},
		{"Yes, file it.", "it.", true},
		{"yes, file it", "file", true},
		{"sure, go ahead", "go ahead", true},
		{"Yes please", "please", true},
		{"please file an​ issue", "file an issue", true},
		{"yes", "yes", false},
		{"OK go ahead", "OK", false},
		{"file it", "file it", false},
		{"Yes, create that.", "Yes, create that.", false},
	} {
		if Requested(c.message, c.quote, c.proposed) {
			t.Errorf("Requested(%q, %q, proposed %v) = true, want false", c.message, c.quote, c.proposed)
		}
	}
}
