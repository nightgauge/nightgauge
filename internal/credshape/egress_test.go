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
		"license":            "ng" + "_live_" + rep("Ab12", 6),
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

// A credential padded past the hold window is never released in parts: an
// over-long run of token characters leaves as one Marker and nothing of it,
// before or after the credential, is released. Before the fix the run was
// cut 1 KiB from its end, and `…ghp_ZZZ` and `ZZZ…` each matched nothing.
func TestStream_ACredentialPaddedPastTheHoldIsNeverSplit(t *testing.T) {
	secret := "gh" + "p_" + rep("Z", 1030)
	reply := rep("a", 8000) + secret + " — done."
	for _, size := range []int{1, 7, 512, 4096, len(reply)} {
		released := streamAll(chop(reply, size))
		// A run that arrives whole with its delimiter is released whole and
		// redacted; one held past the window leaves as one Marker. Either
		// way the credential leaves as the Marker and in no part.
		joined := strings.Join(released, "")
		if !strings.HasSuffix(joined, Marker+" — done.") || ContainsEgress(joined) {
			t.Errorf("%d-byte pieces: released %d bytes ending %q", size, len(joined), joined[max(0, len(joined)-40):])
		}
		for _, r := range released {
			if strings.Contains(r, "ZZZZ") || strings.Contains(r, "gh"+"p_") {
				t.Fatalf("%d-byte pieces: a part of the credential left: %q", size, r[:min(len(r), 60)])
			}
		}
	}
	// A run that is merely long, with no credential, is dropped the same way;
	// the text around it is untouched.
	if got := strings.Join(streamAll(chop("before "+rep("f", streamHoldMax+10)+" after", 100)), ""); got != "before "+Marker+" after" {
		t.Errorf("long run: %q", got)
	}
}

// A private-key block right after an over-long run is still found: the run's
// end may be a PEM header's opening, which survives the drop.
func TestStream_AKeyBlockAfterAnOverLongRunIsStillDropped(t *testing.T) {
	reply := rep("a", streamHoldMax+100) + pemBlockExample("RSA PRIVATE KEY") + "\nok"
	for _, size := range []int{1, 3, 64, 4096} {
		joined := strings.Join(streamAll(chop(reply, size)), "")
		if strings.Contains(joined, "MIIE") || strings.Contains(joined, "PRIVATE KEY") {
			t.Errorf("%d-byte pieces: the key block leaked: %q", size, joined)
		}
		if !strings.HasSuffix(joined, "\nok") {
			t.Errorf("%d-byte pieces: released %q", size, joined)
		}
	}
}

// An invisible character inside a credential breaks its shape while it still
// displays whole. Every one inside ASCII text is removed before matching, so
// the credential leaves as the Marker; one inside an emoji sequence stays.
func TestStream_InvisibleCharactersCannotSplitACredential(t *testing.T) {
	token := "gh" + "p_" + rep("Q7w8", 9)
	for _, cp := range []rune{0x200b, 0x200c, 0x200d, 0x2060, 0xfeff, 0x00ad, 0x202e, 0xfe0f, 0x034f} {
		zw := string(cp)
		split := token[:2] + zw + token[2:10] + zw + zw + token[10:]
		reply := "token: " + split + " end"
		for _, size := range []int{1, 5, 4096} {
			joined := strings.Join(streamAll(chop(reply, size)), "")
			if joined != "token: "+Marker+" end" {
				t.Errorf("%U, %d-byte pieces: released %q", []rune(zw)[0], size, joined)
			}
		}
		if got := RedactEgress(reply); got != "token: "+Marker+" end" {
			t.Errorf("%U: RedactEgress = %q", []rune(zw)[0], got)
		}
		if !ContainsEgress(reply) {
			t.Errorf("%U: ContainsEgress missed it", []rune(zw)[0])
		}
	}
	// A PEM header with an invisible character in it is still a header.
	header := "-----BEGIN RSA" + string(rune(0x200b)) + " PRIVATE KEY-----"
	block := "k: " + header + "\nMIIBOgIBAAJBAKj34\n-----END RSA PRIVATE KEY-----\nx"
	if got := strings.Join(streamAll(chop(block, 4)), ""); got != "k: "+Marker+"\nx" {
		t.Errorf("header with an invisible character: %q", got)
	}
	// Emoji joiner sequences are not ASCII text and are left alone.
	emoji := "Pair: " + string([]rune{0x1F469, 0x200d, 0x1F4BB}) + " and " + string([]rune{0x2764, 0xfe0f}) + " done"
	if got := strings.Join(streamAll(chop(emoji, 3)), ""); got != emoji {
		t.Errorf("emoji sequence changed: %q", got)
	}
}
