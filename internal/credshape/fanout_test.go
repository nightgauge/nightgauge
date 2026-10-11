package credshape_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/credshape"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// everyLicenseKeyPrefix is every prefix a Nightgauge license key has been
// issued under. The registry may only widen: a key issued under a prefix can
// sit in a config file or a log long after the prefix stops being issued.
// ib_test_ was issued before the rebrand too and was missing until #2451.
var everyLicenseKeyPrefix = []string{"ng_live_", "ng_ci_", "ng_test_", "ib_live_", "ib_test_", "ib_ci_"}

// TestLicenseKeyShapesOnlyWiden pins the registry: no prefix is ever dropped
// from it. #2444 first replaced the ib_ prefixes with the ng_ ones, and every
// consumer below silently redacted less.
func TestLicenseKeyShapesOnlyWiden(t *testing.T) {
	got := platform.LicenseKeyPrefixes()
	for _, p := range everyLicenseKeyPrefix {
		if !slices.Contains(got, p) {
			t.Errorf("license-key prefix %q dropped from the registry (%v)", p, got)
		}
	}
}

// Every consumer of the shared registry recognises and redacts a key of every
// prefix: the config check, the doctor's redaction of findings and evidence,
// and conversation egress, whole and streamed.
func TestEveryRegistryConsumerRedactsEveryLicenseKeyShape(t *testing.T) {
	for _, prefix := range everyLicenseKeyPrefix {
		key := prefix + strings.Repeat("Ab1-", 6)
		in := "key=" + key + " end"
		if !credshape.Contains(in) {
			t.Errorf("%s: credshape.Contains missed it", prefix)
		}
		if got := credshape.ReplaceAll(in, "X"); got != "key=X end" {
			t.Errorf("%s: credshape.ReplaceAll = %q", prefix, got)
		}
		if got := config.RedactSecretString(in); got != "key="+config.RedactedValue+" end" {
			t.Errorf("%s: config.RedactSecretString = %q", prefix, got)
		}
		if got := config.RedactEvidence(map[string]string{"line": in})["line"]; strings.Contains(got, key) {
			t.Errorf("%s: config.RedactEvidence = %q", prefix, got)
		}
		if !credshape.ContainsEgress(in) || credshape.RedactEgress(in) != "key="+credshape.Marker+" end" {
			t.Errorf("%s: egress redaction = %q", prefix, credshape.RedactEgress(in))
		}
		s := credshape.NewStream()
		var out strings.Builder
		for _, r := range in {
			out.WriteString(s.Write(string(r)))
		}
		out.WriteString(s.Flush())
		if out.String() != "key="+credshape.Marker+" end" {
			t.Errorf("%s: streamed egress = %q", prefix, out.String())
		}
		found := false
		for _, p := range credshape.Patterns() {
			if p.Prefix == prefix && p.Count(in) == 1 {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no pattern the doctor's tracked-file scan reads matches it", prefix)
		}
	}
}
