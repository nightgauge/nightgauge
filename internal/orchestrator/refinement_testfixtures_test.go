package orchestrator

import (
	"net/http"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// requiredLabelsRESTBody is the REST payload GetRepoLabels expects, populated
// from the required-label registry itself rather than a hand-written list.
//
// Deriving it from gh.RequiredLabels is deliberate: a fixture that hardcodes
// the label names would keep passing after someone adds a fourth required
// label, and the refinement preflight would then be skipping every repo in
// production while the suite stayed green (#993).
func requiredLabelsRESTBody() string {
	var b strings.Builder
	b.WriteString("[")
	for i, l := range gh.RequiredLabels {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"node_id":"LABEL_`)
		b.WriteString(l.Name)
		b.WriteString(`","name":"`)
		b.WriteString(l.Name)
		b.WriteString(`"}`)
	}
	b.WriteString("]")
	return b.String()
}

// serveRepoLabels answers the REST labels endpoint that the refinement
// preflight calls before listing candidates, and reports whether it handled the
// request. Refinement-cycle fixtures that only speak GraphQL must call this
// first, or the preflight cannot verify the repo and skips it — which looks
// exactly like "the scheduler dispatched nothing".
func serveRepoLabels(w http.ResponseWriter, r *http.Request) bool {
	if r == nil || !strings.Contains(r.URL.Path, "/labels") {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(requiredLabelsRESTBody()))
	return true
}
