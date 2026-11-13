package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// cleanInstallScript is gate G1, which packages a VSIX from the tree. It must
// build the bundled binary the way the release does (#3114).
const cleanInstallScript = "scripts/clean-install-e2e.sh"

// logicalLines joins backslash-continued shell lines into one line each.
func logicalLines(src string) []string {
	var out []string
	var cur strings.Builder
	for _, l := range strings.Split(src, "\n") {
		trimmed := strings.TrimRight(l, " \t")
		if strings.HasSuffix(trimmed, "\\") {
			cur.WriteString(strings.TrimSuffix(trimmed, "\\"))
			cur.WriteString(" ")
			continue
		}
		cur.WriteString(l)
		out = append(out, cur.String())
		cur.Reset()
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

var bundledOutput = regexp.MustCompile(`-o\s+"?[^\s"]*dist/bin/nightgauge"?(\s|$)`)

// bundledBuildLines returns the logical `go build` lines whose -o target is
// the VSIX's bundled binary, dist/bin/nightgauge.
func bundledBuildLines(src string) []string {
	var hits []string
	for _, l := range logicalLines(src) {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		if strings.Contains(l, "go build") && bundledOutput.MatchString(l) {
			hits = append(hits, l)
		}
	}
	return hits
}

// releaseFlagProblems lists how a build line departs from the release flags.
func releaseFlagProblems(line, vendor string) []string {
	var p []string
	if f := stripFlagIn(line); f != "" {
		p = append(p, "strip flag "+f)
	}
	for _, want := range []string{"-trimpath", "-compressdwarf=false", "main.vendor=" + vendor} {
		if !strings.Contains(line, want) {
			p = append(p, "missing "+want)
		}
	}
	return p
}

func makefileVendor(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("HARNESS ERROR: read Makefile: %v", err)
	}
	m := regexp.MustCompile(`(?m)^VENDOR \?= (.+)$`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("HARNESS ERROR: no `VENDOR ?=` line in the Makefile")
	}
	return strings.TrimSpace(m[1])
}

func readCleanInstall(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, cleanInstallScript))
	if err != nil {
		t.Fatalf("HARNESS ERROR: read %s: %v", cleanInstallScript, err)
	}
	return string(b)
}

func TestCleanInstallBuildsLikeRelease(t *testing.T) {
	vendor := makefileVendor(t)
	lines := bundledBuildLines(readCleanInstall(t))
	if len(lines) == 0 {
		t.Fatalf("HARNESS ERROR: no go build line targeting dist/bin/nightgauge in %s", cleanInstallScript)
	}
	for _, l := range lines {
		if p := releaseFlagProblems(l, vendor); len(p) > 0 {
			t.Errorf("%s builds the bundled binary unlike the release (%s): %s",
				cleanInstallScript, strings.Join(p, ", "), strings.TrimSpace(l))
		}
	}
}

func TestCleanInstallInstallsDepsBeforePackaging(t *testing.T) {
	src := readCleanInstall(t)
	sdk := strings.Index(src, "npm run -w @nightgauge/sdk build")
	if sdk < 0 {
		t.Fatalf("HARNESS ERROR: no SDK build line in %s", cleanInstallScript)
	}
	guard := strings.Index(src, `[[ ! -d "$ROOT/node_modules" ]]`)
	ci := strings.Index(src, "npm ci)")
	dieCI := strings.Index(src, `die "npm ci failed`)
	switch {
	case guard < 0 || guard > sdk:
		t.Errorf("no node_modules guard before the SDK build in %s", cleanInstallScript)
	case ci < guard || ci > sdk:
		t.Errorf("the node_modules guard does not run npm ci before the SDK build")
	case dieCI < ci || dieCI > sdk:
		t.Errorf("npm ci failure is not reported with die before the SDK build")
	}
}

func TestCleanInstallBuildLineDetector(t *testing.T) {
	const vendor = "Edibu, LLC"
	cases := []struct {
		name     string
		src      string
		selected bool
		ok       bool
	}{
		{"stripped continued line", "go build -ldflags \"-s -w -X main.version=$V\" \\\n  -o \"$PKG/dist/bin/nightgauge\" ./cmd/nightgauge", true, false},
		{"release flags", "go build -trimpath \\\n  -ldflags \"-compressdwarf=false -X main.version=$V -X 'main.vendor=Edibu, LLC'\" \\\n  -o \"$PKG/dist/bin/nightgauge\" ./cmd/nightgauge", true, true},
		{"missing trimpath", "go build -ldflags \"-compressdwarf=false -X 'main.vendor=Edibu, LLC'\" -o \"$PKG/dist/bin/nightgauge\" ./cmd/nightgauge", true, false},
		{"missing vendor", "go build -trimpath -ldflags \"-compressdwarf=false\" -o \"$PKG/dist/bin/nightgauge\" ./cmd/nightgauge", true, false},
		{"host binary", "go build -o \"$RUN_DIR/nightgauge-host\" ./cmd/nightgauge", false, false},
		{"commented", "# go build -s -o \"$PKG/dist/bin/nightgauge\" ./cmd/nightgauge", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := bundledBuildLines(tc.src)
			if (len(lines) > 0) != tc.selected {
				t.Fatalf("selected=%v, want %v", len(lines) > 0, tc.selected)
			}
			if !tc.selected {
				return
			}
			if got := len(releaseFlagProblems(lines[0], vendor)) == 0; got != tc.ok {
				t.Errorf("ok=%v, want %v (%v)", got, tc.ok, releaseFlagProblems(lines[0], vendor))
			}
		})
	}
}
