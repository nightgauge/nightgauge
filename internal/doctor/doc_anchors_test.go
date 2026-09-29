package doctor

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// registeredCodes is every finding code a registered check owns: each check's
// primary code, the extra codes it declares, and NGD000, which belongs to
// every check. TestEveryCodeResolvesToACheck keeps this complete against the
// NGD literals in the source.
func registeredCodes() map[string]bool {
	codes := map[string]bool{codeTimeout: true}
	for _, c := range DefaultRegistry().Checks() {
		if c.Code != "" {
			codes[c.Code] = true
		}
		for _, code := range extraCheckCodes[c.ID] {
			codes[code] = true
		}
	}
	return codes
}

// TestDocAnchors (#2100): docs/DOCTOR.md has exactly one anchor per registered
// code, and no anchor for a code that does not exist. An anchor is a heading
// that is the code alone ("### NGD014"), so its slug is the fragment every
// finding's `docs` link carries (DocsAnchor).
func TestDocAnchors(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "DOCTOR.md")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	anchorRe := regexp.MustCompile(`^#{2,6}\s+(NGD\d{3})\s*$`)
	looseRe := regexp.MustCompile(`^#{2,6}\s+.*\bNGD\d{3}\b`)
	anchors := map[string]int{}
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if m := anchorRe.FindStringSubmatch(text); m != nil {
			anchors[m[1]]++
			continue
		}
		if looseRe.MatchString(text) {
			t.Errorf("DOCTOR.md:%d: heading %q names a code but is not the code alone, so its anchor is not the one findings link to", line, text)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	codes := registeredCodes()
	if len(codes) < 50 {
		t.Fatalf("only %d registered codes; the registry walk is broken", len(codes))
	}
	var missing, unknown []string
	for code := range codes {
		if anchors[code] == 0 {
			missing = append(missing, code)
		}
		if want := "docs/DOCTOR.md#" + strings.ToLower(code); DocsAnchor(code) != want {
			t.Errorf("DocsAnchor(%s) = %s, want %s", code, DocsAnchor(code), want)
		}
	}
	for code, n := range anchors {
		if !codes[code] {
			unknown = append(unknown, code)
		}
		if n > 1 {
			t.Errorf("DOCTOR.md has %d anchors for %s, want one", n, code)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	if len(missing) > 0 {
		t.Errorf("registered codes with no anchor in DOCTOR.md: %s", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		t.Errorf("DOCTOR.md anchors codes no registered check owns: %s", strings.Join(unknown, ", "))
	}
}
