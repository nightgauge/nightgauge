package credshape

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Every example is assembled at run time, so no credential-shaped literal
// sits in the repository for a secret scanner to find.
func rep(s string, n int) string { return strings.Repeat(s, n) }

// egressExamples are one string per shape the hosted service refuses on a
// conversation frame, plus the ones Nightgauge itself stores.
func egressExamples() map[string]string {
	return map[string]string{
		"github token":       "gh" + "p_" + rep("A1b2", 9),
		"github oauth token": "gh" + "o_" + rep("Z9y8", 9),
		"github fine":        "github" + "_pat_" + rep("Ab1_", 21),
		"anthropic":          "sk-" + "ant-api03-" + rep("Ab9-", 20),
		"openai":             "sk-" + rep("Ab12", 6) + "T3Blbk" + "FJ" + rep("Cd34", 6),
		"openai project":     "sk-" + "proj-" + rep("Ab1_", 12),
		"xai":                "xa" + "i-" + rep("Ab12", 12),
		"google":             "AI" + "za" + rep("Ab1_", 9),
		"aws":                "AK" + "IA" + rep("ABC1", 4),
		"aws session":        "AS" + "IA" + rep("XYZ9", 4),
		"slack":              "xo" + "xb-" + "1234567890-" + rep("abcdef", 3),
		"stripe":             "sk" + "_live_" + rep("Ab12", 6),
		"npm":                "np" + "m_" + rep("Ab12", 9),
		"jwt":                "ey" + "J" + rep("hbGc", 4) + ".ey" + "J" + rep("zdWI", 4) + "." + rep("SflK", 6),
		"license":            "ib" + "_live_" + rep("Ab12", 6),
	}
}

func pemBlockExample(label string) string {
	return "-----BEGIN " + label + "-----\n" + rep("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n", 4) + "-----END " + label + "-----"
}

func TestRedactEgress_ReplacesEveryShape(t *testing.T) {
	for name, secret := range egressExamples() {
		in := "before " + secret + " after"
		got := RedactEgress(in)
		if got != "before "+Marker+" after" {
			t.Errorf("%s: RedactEgress = %q", name, got)
		}
		if !ContainsEgress(in) || ContainsEgress(got) {
			t.Errorf("%s: ContainsEgress before %v after %v", name, ContainsEgress(in), ContainsEgress(got))
		}
	}
	for _, label := range []string{"RSA PRIVATE KEY", "EC PRIVATE KEY", "OPENSSH PRIVATE KEY", "PRIVATE KEY",
		"ENCRYPTED PRIVATE KEY", "PGP PRIVATE KEY BLOCK"} {
		got := RedactEgress("key:\n" + pemBlockExample(label) + "\ndone")
		if got != "key:\n"+Marker+"\ndone" {
			t.Errorf("%s block: RedactEgress = %q", label, got)
		}
	}
	// A header whose footer never came: the body after it is the secret.
	if got := RedactEgress("x -----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34"); got != "x "+Marker {
		t.Errorf("unterminated block: %q", got)
	}
}

// Prose, hashes and ids are not credentials: a false positive would end
// ordinary replies.
func TestRedactEgress_LeavesProseAlone(t *testing.T) {
	for _, s := range []string{
		"The ghp_ prefix marks a classic token; sk-ant- marks an Anthropic key.",
		"commit 3f2a9c1e4b5d6a7f8e9d0c1b2a3f4e5d6c7b8a9f is on main",
		"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----",
		"an id like 00000000-0000-4000-8000-000000000000",
	} {
		if got := RedactEgress(s); got != s {
			t.Errorf("RedactEgress(%q) = %q", s, got)
		}
	}
}

// streamAll feeds pieces through a Stream and returns every released piece,
// the flush included.
func streamAll(pieces []string) []string {
	s := NewStream()
	var out []string
	for _, p := range pieces {
		out = append(out, s.Write(p))
	}
	return append(out, s.Flush())
}

// chop cuts s into pieces of n bytes, never inside a rune.
func chop(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		cut := min(n, len(s))
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	return out
}

// A credential split across pieces never leaves in parts: no released piece,
// and no released piece joined to the text before it (the hosted service's own
// check), holds any of it.
func TestStream_ACredentialSplitAcrossPiecesNeverLeaves(t *testing.T) {
	for name, secret := range egressExamples() {
		reply := "Here is the value you asked about: " + secret + ". Keep it safe."
		for _, size := range []int{1, 3, 7, 16, 64} {
			released := streamAll(chop(reply, size))
			joined := strings.Join(released, "")
			if joined != "Here is the value you asked about: "+Marker+". Keep it safe." {
				t.Errorf("%s / %d-byte pieces: released %q", name, size, joined)
			}
			sofar := ""
			for _, r := range released {
				sofar += r
				if ContainsEgress(r) || ContainsEgress(sofar) {
					t.Fatalf("%s / %d-byte pieces: a released piece carries the credential: %q", name, size, r)
				}
				if len(secret) > 12 && strings.Contains(r, secret[len(secret)-12:]) {
					t.Fatalf("%s / %d-byte pieces: the credential's tail leaked: %q", name, size, r)
				}
			}
		}
	}
}

// A private-key block streamed in pieces leaves as one Marker: neither its
// header nor any line of its body is released.
func TestStream_PrivateKeyBlockLeavesAsOneMarker(t *testing.T) {
	block := pemBlockExample("OPENSSH PRIVATE KEY")
	reply := "Your key:\n" + block + "\nUse it carefully."
	for _, size := range []int{1, 5, 13, 40, 4096} {
		joined := strings.Join(streamAll(chop(reply, size)), "")
		if joined != "Your key:\n"+Marker+"\nUse it carefully." {
			t.Errorf("%d-byte pieces: released %q", size, joined)
		}
	}
	// No footer: everything after the header is dropped.
	unterminated := "k: -----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu"
	if got := strings.Join(streamAll(chop(unterminated, 9)), ""); got != "k: "+Marker {
		t.Errorf("unterminated block released %q", got)
	}
}

// Text with nothing to redact leaves unchanged, in order, and every piece
// ends on a rune boundary.
func TestStream_PassesOrdinaryTextThrough(t *testing.T) {
	reply := "Rivers carve valleys — slowly, über Jahrtausende. 川は流れる。\n- item one\n- item two\n" +
		"See https://example.com/a_b-c.d and v1.2.3.\n"
	for _, size := range []int{1, 2, 9, 100} {
		released := streamAll(chop(reply, size))
		if got := strings.Join(released, ""); got != reply {
			t.Errorf("%d-byte pieces: %q", size, got)
		}
		for _, r := range released {
			if !utf8.ValidString(r) {
				t.Errorf("%d-byte pieces: released invalid UTF-8 %q", size, r)
			}
		}
	}
}

// A token run with no delimiter is not held forever.
func TestStream_ReleasesALongUndelimitedRun(t *testing.T) {
	s := NewStream()
	got := s.Write(rep("a", streamHoldMax+100))
	if len(got) == 0 || len(got) > streamHoldMax+100-streamHoldKeep {
		t.Errorf("released %d bytes of an undelimited run", len(got))
	}
	if rest := s.Flush(); len(got)+len(rest) != streamHoldMax+100 {
		t.Errorf("released %d + %d bytes, want %d", len(got), len(rest), streamHoldMax+100)
	}
}
