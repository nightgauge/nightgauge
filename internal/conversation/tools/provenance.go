package tools

import (
	"encoding/json"
	"regexp"
	"strings"
)

// ProvenanceOrigin marks an issue a conversation filed.
const ProvenanceOrigin = "conversation"

// Provenance is what an issue filed from a conversation records about where
// it came from and who asked for it. GitHub shows the App (or the operator)
// as the author; the actor is the posting account.
type Provenance struct {
	Origin           string `json:"origin"`
	ConversationID   string `json:"conversation_id"`
	TurnID           string `json:"turn_id"`
	ActorAccountID   string `json:"actor_account_id"`
	ActorGitHubLogin string `json:"actor_github_login,omitempty"`
}

// provenanceMarker opens the machine-readable record in an issue body.
const provenanceMarker = "<!-- nightgauge:provenance "

// RenderProvenance is the footer appended to a filed issue's body: a line a
// reader sees, then the record as an HTML comment. Every value is an id or a
// GitHub login, which can neither close the comment nor mention anyone.
func RenderProvenance(p Provenance) string {
	b, _ := json.Marshal(p)
	who := "Nightgauge account `" + p.ActorAccountID + "`"
	if p.ActorGitHubLogin != "" {
		who = "GitHub user `" + p.ActorGitHubLogin + "` (Nightgauge account `" + p.ActorAccountID + "`)"
	}
	return "\n\n---\n\n_Filed from a Nightgauge conversation at the request of " + who + "._\n\n" +
		provenanceMarker + string(b) + " -->\n"
}

// ParseProvenance reads the provenance record of an issue body: the last
// marker, which is the one RenderProvenance appended.
func ParseProvenance(body string) (Provenance, bool) {
	i := strings.LastIndex(body, provenanceMarker)
	if i < 0 {
		return Provenance{}, false
	}
	rest := body[i+len(provenanceMarker):]
	end := strings.Index(rest, " -->")
	if end < 0 {
		return Provenance{}, false
	}
	var p Provenance
	if err := json.Unmarshal([]byte(rest[:end]), &p); err != nil || p.Origin != ProvenanceOrigin || p.ActorAccountID == "" {
		return Provenance{}, false
	}
	return p, true
}

// forgedMarker is a provenance marker in model-authored text.
var forgedMarker = regexp.MustCompile(`(?i)<!--\s*nightgauge:provenance`)

// neutralizeProvenance defuses a provenance marker the model wrote into a
// body, so the only record an issue carries is the one appended for it.
func neutralizeProvenance(body string) string {
	return forgedMarker.ReplaceAllString(body, "<!-- (removed)")
}
