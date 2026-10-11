package credshape

import (
	"regexp"
	"strings"
	"unicode"
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
// credential-shaped string replaced by Marker. Invisible characters inside
// ASCII text are removed first (see stripInvisible), so none can split a
// credential out of its shape.
func RedactEgress(s string) string {
	s, _ = stripInvisible(s, 0)
	return redactNormalized(s)
}

// redactNormalized is RedactEgress for text stripInvisible already ran over.
func redactNormalized(s string) string {
	s = pemBlock.ReplaceAllLiteralString(s, Marker)
	for _, p := range egressPatterns {
		s = p.re.ReplaceAllLiteralString(s, Marker)
	}
	return s
}

// ContainsEgress reports whether s holds anything RedactEgress would replace.
func ContainsEgress(s string) bool {
	s, _ = stripInvisible(s, 0)
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
// released in its place.
//
// Two ways around that are closed. An invisible character (a zero-width
// space, a joiner, a bidirectional control, a variation selector) inside a
// credential would break its shape while the credential still displays
// whole, so invisible characters inside ASCII text are removed on arrival. A
// run of token characters is never released in part, because a credential
// padded past any hold window would then leave in two halves that each match
// nothing: a run that grows past streamHoldMax with no delimiter is replaced
// by one Marker, and the rest of it is dropped as it arrives.
type Stream struct {
	pending string
	inKey   bool
	// dropRun is set while the rest of an over-long run is being dropped.
	dropRun bool
	// last is the last visible rune received, for stripInvisible.
	last rune
}

const (
	// streamHoldMax bounds a held run of token characters. No credential
	// shape is near it; a run past it is not prose.
	streamHoldMax = 8 << 10
	// pemFooterHold is what is kept of a key block's body while its footer
	// has not arrived: enough to hold a footer split across pieces.
	pemFooterHold = 128
)

// NewStream returns an empty redacting stream.
func NewStream() *Stream { return &Stream{} }

// Write takes the next piece and returns the text now safe to send, already
// redacted. It may return "" while text is held.
func (s *Stream) Write(piece string) string {
	piece, s.last = stripInvisible(piece, s.last)
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
		if s.dropRun {
			// Drop the rest of the over-long run. Only a PEM header's
			// opening can survive it, so a key block after the run is
			// still found.
			end := leadingRunEnd(s.pending)
			done := end < len(s.pending)
			keep := headerOpening(s.pending[:end], done)
			s.pending = s.pending[end-keep:]
			if !done {
				return out.String() // the run goes on
			}
			s.dropRun = false
			continue
		}
		if loc := pemBegin.FindStringIndex(s.pending); loc != nil {
			out.WriteString(redactNormalized(s.pending[:loc[0]]))
			out.WriteString(Marker)
			s.pending = s.pending[loc[1]:]
			s.inKey = true
			continue
		}
		cut := releasable(s.pending)
		out.WriteString(redactNormalized(s.pending[:cut]))
		s.pending = s.pending[cut:]
		if len(s.pending) > streamHoldMax && trailingRunStart(s.pending) == 0 {
			out.WriteString(Marker)
			s.pending = ""
			s.dropRun = true
		}
		return out.String()
	}
}

// Flush returns everything still held, redacted. The rest of a key block
// whose footer never arrived is dropped: its Marker was already released.
func (s *Stream) Flush() string {
	held := s.pending
	s.pending = ""
	s.dropRun = false
	if s.inKey {
		s.inKey = false
		return ""
	}
	return redactNormalized(held)
}

// releasable is how much of pending can be released: through its last
// non-token rune, and never past the start of a line that has begun a PEM
// header.
func releasable(pending string) int {
	cut := trailingRunStart(pending)
	if i := strings.LastIndex(pending, pemOpening); i >= 0 && i < cut && !strings.Contains(pending[i:], "\n") {
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

// pemOpening is how every PEM header begins; all of it is token runes.
const pemOpening = "-----BEGIN"

// headerOpening is how many trailing bytes of run may begin a PEM header: all
// of pemOpening when the run ended with it (done), or, while the run goes on,
// its longest suffix that is a prefix of pemOpening.
func headerOpening(run string, done bool) int {
	if done {
		if strings.HasSuffix(run, pemOpening) {
			return len(pemOpening)
		}
		return 0
	}
	for n := min(len(run), len(pemOpening)); n > 0; n-- {
		if strings.HasPrefix(pemOpening, run[len(run)-n:]) {
			return n
		}
	}
	return 0
}

// leadingRunEnd is the index where pending's leading run of token runes ends.
func leadingRunEnd(pending string) int {
	for i, r := range pending {
		if !isTokenRune(r) {
			return i
		}
	}
	return len(pending)
}

// invisible reports whether r renders as nothing: a format character (zero
// width space and joiners, bidirectional controls, the soft hyphen, the byte
// order mark), a variation selector, or another default-ignorable code point.
func invisible(r rune) bool {
	return unicode.In(r, unicode.Cf, unicode.Variation_Selector, unicode.Other_Default_Ignorable_Code_Point)
}

// stripInvisible removes every invisible rune whose nearest visible
// predecessor is ASCII, the start of the text included (prev 0), and returns
// the last visible rune. Every credential shape and PEM header is ASCII, so
// nothing invisible is left inside one; an invisible rune after other
// script, such as the joiner inside an emoji sequence, is kept.
func stripInvisible(s string, prev rune) (string, rune) {
	var b strings.Builder
	stripped := false
	for i, r := range s {
		if invisible(r) {
			if prev < utf8.RuneSelf {
				if !stripped {
					b.Grow(len(s))
					b.WriteString(s[:i])
					stripped = true
				}
				continue
			}
		} else {
			prev = r
		}
		if stripped {
			b.WriteRune(r)
		}
	}
	if !stripped {
		return s, prev
	}
	return b.String(), prev
}

// isTokenRune reports whether r can be part of a credential shape.
func isTokenRune(r rune) bool {
	return r == '_' || r == '-' || r == '.' ||
		('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
}
