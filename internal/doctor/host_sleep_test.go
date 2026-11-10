package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// pmsetCustom is `pmset -g custom` as a macOS laptop prints it, with the AC
// section's sleep value substituted.
func pmsetCustom(acSleep string) string {
	return "Battery Power:\n Sleep On Power Button 1\n sleep                1\n displaysleep         2\n" +
		"AC Power:\n Sleep On Power Button 1\n standby              0\n displaysleep         60\n" +
		" sleep                " + acSleep + "\n disksleep            0\n"
}

func withHostSleep(t *testing.T, goos string, out string, err error) {
	t.Helper()
	prevGOOS, prevPmset := hostSleepGOOS, hostSleepPmset
	t.Cleanup(func() { hostSleepGOOS, hostSleepPmset = prevGOOS, prevPmset })
	hostSleepGOOS = goos
	hostSleepPmset = func(context.Context) ([]byte, error) { return []byte(out), err }
}

func TestHostSleepWarnsWhenACSleepIsEnabled(t *testing.T) {
	withHostSleep(t, "darwin", pmsetCustom("1"), nil)
	fs, _ := hostSleepFindings(context.Background())
	if len(fs) != 1 {
		t.Fatalf("findings = %v, want one", fs)
	}
	f := fs[0]
	if f.Severity != SeverityWarning || f.Code != hostSleepCode {
		t.Errorf("severity %s code %s, want warning %s", f.Severity, f.Code, hostSleepCode)
	}
	if !strings.Contains(f.Title, "sleep 1") || !strings.Contains(f.Cause, "AC Power") {
		t.Errorf("finding does not name the setting: %q / %q", f.Title, f.Cause)
	}
	if len(f.Remedies) == 0 || !strings.Contains(strings.Join(f.Remedies[0].Steps, " "), "sudo pmset -c sleep 0") {
		t.Errorf("remedy does not name the fix: %+v", f.Remedies)
	}
}

func TestHostSleepPasses(t *testing.T) {
	for name, tc := range map[string]struct {
		goos, out string
		err       error
	}{
		"AC sleep disabled":  {"darwin", pmsetCustom("0"), nil},
		"battery only":       {"darwin", "Battery Power:\n sleep                1\n", nil},
		"pmset did not run":  {"darwin", "", errors.New("exec: \"pmset\": not found")},
		"not macOS":          {"linux", pmsetCustom("1"), nil},
		"battery sleep only": {"darwin", "Battery Power:\n sleep 5\nAC Power:\n displaysleep 10\n", nil},
	} {
		t.Run(name, func(t *testing.T) {
			withHostSleep(t, tc.goos, tc.out, tc.err)
			if fs, detail := hostSleepFindings(context.Background()); len(fs) != 0 {
				t.Errorf("findings = %v (detail %q), want none", fs, detail)
			}
		})
	}
}
