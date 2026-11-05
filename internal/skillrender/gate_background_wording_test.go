package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gateRun matches a line that starts the full gate in command position. An
// assignment such as ("nightgauge ci gate-run") is not a start.
var gateRun = regexp.MustCompile(`(^|[\s;&|])("\$BINARY"|\$BINARY|nightgauge|bash scripts/ci-local\.sh|\./scripts/ci-local\.sh)(\s+(ci gate-run|pre-push validate)\b|\s*$)`)

// gateWording is what every step that runs the gate must say (#2953): a
// foreground call is killed at twice its timeout.
var gateWording = []string{"run_in_background: true", "`timeout`", "twice its timeout", "`gate_command` is never run directly"}

// gateSteps are the steps that start the gate even where the command is
// held in a variable the regex cannot see.
var gateSteps = map[string][]string{
	"nightgauge-feature-validate/_includes/ci-and-knowledge.md": {
		"### Step 2.5.2: Execute CI Commands and Collect Failures",
		"### Step 2.7.1: Resolve Binary and Run Gate",
	},
}

// stepProse returns the prose of the ### step starting at heading, before
// its first fenced block.
func stepProse(content, heading string) (string, bool) {
	h := strings.Index(content, heading)
	if h < 0 {
		return "", false
	}
	rest := content[h:]
	if end := strings.Index(rest, "```"); end >= 0 {
		rest = rest[:end]
	}
	return rest, true
}

func assertGateWording(t *testing.T, rel, heading, prose string) {
	t.Helper()
	for _, w := range gateWording {
		if !strings.Contains(prose, w) {
			t.Errorf("%s %q runs the full gate but no longer says %q", rel, heading, w)
		}
	}
}

func TestGateSteps_NameBackgroundForm(t *testing.T) {
	root := filepath.Join("..", "..", "skills")
	for rel, headings := range gateSteps {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, heading := range headings {
			prose, ok := stepProse(string(data), heading)
			if !ok {
				t.Fatalf("%s no longer contains %q", rel, heading)
			}
			assertGateWording(t, rel, heading, prose)
		}
	}

	// Every other step whose code starts the gate must say the same.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		heading, prose, inCode := "", "", false
		for _, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "```"):
				inCode = !inCode
			case inCode:
				code := strings.TrimSpace(line)
				if heading != "" && !strings.HasPrefix(code, "#") && gateRun.MatchString(code) {
					assertGateWording(t, rel, heading, prose)
					heading = "" // report a step once
				}
			case strings.HasPrefix(line, "### "):
				heading, prose = line, line+"\n"
			default:
				prose += line + "\n"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLongRunningProcesses_StatesBackgroundRule(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "skills", "_shared", "LONG_RUNNING_PROCESSES.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"run_in_background: true", "twice its timeout", "`&`"} {
		if !strings.Contains(string(data), w) {
			t.Errorf("LONG_RUNNING_PROCESSES.md no longer states %q", w)
		}
	}
}
