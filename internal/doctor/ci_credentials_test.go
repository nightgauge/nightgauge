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
	fs, _ := ciMachineCredentialFindings(envOf(nil))
	if len(fs) != 0 {
		t.Fatalf("off CI: findings = %s; want a pass", findingsText(fs))
	}
}

func TestCIMachineCredentialsReportedOnCI(t *testing.T) {
	stubMachineCredentials(t, []string{"github_auth.token", "platform.license_key"}, nil)
	fs, _ := ciMachineCredentialFindings(envOf(map[string]string{"CI": "true"}))
	if len(fs) == 0 {
		t.Fatal("CI with a machine-file credential: no finding; want a warning")
	}
	text := findingsText(fs)
	for _, want := range []string{"platform.license_key", "github_auth.token", "config.yaml"} {
		if !strings.Contains(text, want) {
			t.Errorf("findings %q do not name %q", text, want)
		}
	}
}

func TestCIMachineCredentialsCleanOnCI(t *testing.T) {
	stubMachineCredentials(t, nil, nil)
	fs, _ := ciMachineCredentialFindings(envOf(map[string]string{"CI": "true"}))
	if len(fs) != 0 {
		t.Fatalf("CI with no machine-file credential: findings = %s", findingsText(fs))
	}
}

func TestCIMachineCredentialsReadErrorWarns(t *testing.T) {
	stubMachineCredentials(t, nil, errors.New("permission denied"))
	fs, _ := ciMachineCredentialFindings(envOf(map[string]string{"CI": "1"}))
	if len(fs) == 0 {
		t.Fatal("read error on CI: no finding; want a warning")
	}
}
