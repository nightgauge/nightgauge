package doctor

import (
	"errors"
	"strings"
	"testing"
)

func stubMachineCredentials(t *testing.T, keys []string, err error) {
	t.Helper()
	orig := machineFileCredentials
	machineFileCredentials = func() ([]string, string, error) {
		return keys, "/home/runner/.config/nightgauge/config.yaml", err
	}
	t.Cleanup(func() { machineFileCredentials = orig })
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestCIMachineCredentialsSkippedOffCI(t *testing.T) {
	stubMachineCredentials(t, []string{"platform.license_key"}, nil)
	item, warn := checkCIMachineCredentials(envOf(nil))
	if !item.OK || warn != "" {
		t.Fatalf("off CI: item = %+v, warn = %q; want a pass", item, warn)
	}
}

func TestCIMachineCredentialsReportedOnCI(t *testing.T) {
	stubMachineCredentials(t, []string{"github_auth.token", "platform.license_key"}, nil)
	item, warn := checkCIMachineCredentials(envOf(map[string]string{"CI": "true"}))
	if item.OK || warn == "" {
		t.Fatalf("CI with a machine-file credential: item = %+v, warn = %q; want a warning", item, warn)
	}
	for _, want := range []string{"platform.license_key", "github_auth.token", "config.yaml"} {
		if !strings.Contains(item.Error, want) {
			t.Errorf("error %q does not name %q", item.Error, want)
		}
	}
}

func TestCIMachineCredentialsCleanOnCI(t *testing.T) {
	stubMachineCredentials(t, nil, nil)
	item, warn := checkCIMachineCredentials(envOf(map[string]string{"CI": "true"}))
	if !item.OK || warn != "" {
		t.Fatalf("CI with no machine-file credential: item = %+v, warn = %q", item, warn)
	}
}

func TestCIMachineCredentialsReadErrorWarns(t *testing.T) {
	stubMachineCredentials(t, nil, errors.New("permission denied"))
	item, warn := checkCIMachineCredentials(envOf(map[string]string{"CI": "1"}))
	if item.OK || warn == "" {
		t.Fatalf("read error on CI: item = %+v, warn = %q; want a warning", item, warn)
	}
}
