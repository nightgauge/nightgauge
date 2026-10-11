package credshape

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Egress redaction for conversation turns (#1569, ADR-017). A turn's reply
// leaves the workspace for a hosted event log that is append-only and replays
// to every teammate, so the daemon replaces anything shaped like a credential
// before a frame is sent. The hosted service refuses a frame that still
// matches a credential shape and ends the turn; these shapes are a superset of
// the ones it checks, so a reply the daemon redacted is never refused for one.
//
// The egress set is wider than Patterns. Patterns is what the config loader
// and doctor's tracked-file scan look for: the credentials Nightgauge itself
// stores. A model's reply can quote any credential it was shown, so egress
// adds provider API keys, cloud and package-registry tokens, signed JWTs and
// PEM private-key blocks.

// Marker replaces every credential-shaped string that leaves the machine in a
// conversation frame.
const Marker = "[REDACTED]"

// egressExtra are the shapes beyond Patterns. Like Patterns, none carries a
// leading word boundary, and each one's minimum length keeps prose from
// matching.
var egressExtra = []Pattern{
	{Name: "anthropic-api-key", Prefix: "sk-ant-", re: regexp.MustCompile(`sk-ant-[A-Za-z0-9]{2,10}-[A-Za-z0-9_-]{20,}`)},
	{Name: "openai-api-key", Prefix: "sk-", re: regexp.MustCompile(`sk-(?:proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}T3BlbkFJ[A-Za-z0-9_-]{20,}`)},
	{Name: "openai-project-key", Prefix: "sk-proj-", re: regexp.MustCompile(`sk-proj-[A-Za-z0-9_-]{40,}`)},
	{Name: "xai-api-key", Prefix: "xai-", re: regexp.MustCompile(`xai-[A-Za-z0-9]{40,}`)},
	{Name: "google-api-key", Prefix: "AIza", re: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35,}`)},
	{Name: "aws-access-key-id", Prefix: "AKIA", re: regexp.MustCompile(`(?:AKIA|ASIA)[0-9A-Z]{16}`)},
	{Name: "slack-token", Prefix: "xox", re: regexp.MustCompile(`xox[abposr]-[0-9A-Za-z]+-[0-9A-Za-z-]{10,}`)},
	{Name: "stripe-secret-key", Prefix: "sk_", re: regexp.MustCompile(`(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,}`)},
	{Name: "npm-token", Prefix: "npm_", re: regexp.MustCompile(`npm_[A-Za-z0-9]{36,}`)},
	{Name: "jwt", Prefix: "eyJ", re: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}`)},
}

// pemLabel matches the label of every PEM private key: RSA, EC, DSA, OpenSSH,
// PKCS#8 (encrypted or not) and PGP's "PRIVATE KEY BLOCK".
const pemLabel = `(?:[A-Z0-9]+ )?(?:ENCRYPTED )?PRIVATE KEY(?: BLOCK)?`

var (
	pemBegin = regexp.MustCompile(`-----BEGIN ` + pemLabel + `-----`)
	pemEnd   = regexp.MustCompile(`-----END ` + pemLabel + `-----`)
	// pemBlock is a whole block, or a header with no footer through the end
	// of the text: the key material after a header is the secret, not the
	// header.
	pemBlock = regexp.MustCompile(`(?s)-----BEGIN ` + pemLabel + `-----.*?(?:-----END ` + pemLabel + `-----|\z)`)
)

// egressPatterns is Patterns followed by egressExtra.
var egressPatterns = append(append([]Pattern(nil), patterns...), egressExtra...)

// EgressPatterns returns every shape egress redaction replaces, PEM blocks
// aside, in a stable order.
func EgressPatterns() []Pattern {
	return append([]Pattern(nil), egressPatterns...)
}

// RedactEgress returns s with every PEM private-key block and every
// credential-shaped string replaced by Marker.
func RedactEgress(s string) string {
	s = pemBlock.ReplaceAllLiteralString(s, Marker)
	for _, p := range egressPatterns {
		s = p.re.ReplaceAllLiteralString(s, Marker)
	}
	return s
}

// ContainsEgress reports whether s holds anything RedactEgress would replace.
func ContainsEgress(s string) bool {
	if pemBegin.MatchString(s) {
		return true
	}
	for _, p := range egressPatterns {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

// Stream redacts text that arrives in pieces, such as a streamed reply, so
// that no credential leaves whole or in parts. A credential can be split
// across two pieces, and the hosted service scans each frame joined to the
// reply before it, so redacting each piece alone is not enough: a piece is
// released only up to a point no credential can straddle.
//
// Every shape is a run of token characters (letters, digits, '_', '-', '.'),
// so a piece is released through its last non-token character and the
// trailing run is held until a delimiter arrives. A PEM header contains
// spaces, so a line that has begun one is held whole; once a header is
// complete, everything up to the matching footer is dropped and one Marker is
// released in its place. A run longer than streamHoldMax with no delimiter is
// released in part, keeping streamHoldKeep bytes back.
type Stream struct {
	pending string
	inKey   bool
}

const (
	streamHoldMax  = 8 << 10
	streamHoldKeep = 1 << 10
	// pemFooterHold is what is kept of a key block's body while its footer
	// has not arrived: enough to hold a footer split across pieces.
	pemFooterHold = 128
)

// NewStream returns an empty redacting stream.
func NewStream() *Stream { return &Stream{} }

// Write takes the next piece and returns the text now safe to send, already
// redacted. It may return "" while text is held.
func (s *Stream) Write(piece string) string {
	s.pending += piece
	var out strings.Builder
	for {
		if s.inKey {
			loc := pemEnd.FindStringIndex(s.pending)
			if loc == nil {
				if len(s.pending) > pemFooterHold {
					s.pending = s.pending[len(s.pending)-pemFooterHold:]
				}
				return out.String()
			}
			s.pending = s.pending[loc[1]:]
			s.inKey = false
			continue
		}
		if loc := pemBegin.FindStringIndex(s.pending); loc != nil {
			out.WriteString(RedactEgress(s.pending[:loc[0]]))
			out.WriteString(Marker)
			s.pending = s.pending[loc[1]:]
			s.inKey = true
			continue
		}
		cut := releasable(s.pending)
		out.WriteString(RedactEgress(s.pending[:cut]))
		s.pending = s.pending[cut:]
		return out.String()
	}
}

// Flush returns everything still held, redacted. The rest of a key block
// whose footer never arrived is dropped: its Marker was already released.
func (s *Stream) Flush() string {
	held := s.pending
	s.pending = ""
	if s.inKey {
		s.inKey = false
		return ""
	}
	return RedactEgress(held)
}

// releasable is how much of pending can be released: through its last
// non-token rune, and never past the start of a line that has begun a PEM
// header. A trailing run past streamHoldMax is released but for its last
// streamHoldKeep bytes.
func releasable(pending string) int {
	cut := trailingRunStart(pending)
	if len(pending)-cut > streamHoldMax {
		cut = len(pending) - streamHoldKeep
		for cut > 0 && !utf8.RuneStart(pending[cut]) {
			cut--
		}
	}
	if i := strings.LastIndex(pending, "-----BEGIN"); i >= 0 && i < cut && !strings.Contains(pending[i:], "\n") {
		cut = i
	}
	return cut
}

// trailingRunStart is the index where pending's trailing run of token runes
// begins: len(pending) when it ends in a delimiter.
func trailingRunStart(pending string) int {
	i := len(pending)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(pending[:i])
		if !isTokenRune(r) {
			break
		}
		i -= size
	}
	return i
}

// isTokenRune reports whether r can be part of a credential shape.
func isTokenRune(r rune) bool {
	return r == '_' || r == '-' || r == '.' ||
		('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
}
