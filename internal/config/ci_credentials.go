package config

import (
	"errors"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// CIHost reports whether the process runs under CI (`CI=true`, or `CI=1`),
// read through getenv. On a CI host Nightgauge never writes a credential to
// disk, neither to the OS keychain nor to the 0600 machine-tier file, and
// resolves credentials from the environment first (ADR-024 § 5): a
// self-hosted runner shared between jobs would otherwise carry one job's key
// into the next.
func CIHost(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	v := strings.TrimSpace(getenv("CI"))
	return strings.EqualFold(v, "true") || v == "1"
}

// ErrCredentialWriteInCI refuses a credential write on a CI host. The error
// text names the environment variable to use instead where the caller wraps it.
var ErrCredentialWriteInCI = errors.New("CI=true: Nightgauge does not store credentials on disk in CI (ADR-024 § 5); " +
	"provide the credential through the job's environment instead")

// MachineFileCredentials returns the dotted paths of the credentials the
// machine-tier file holds in plaintext (an env: reference is not one), and the
// file's path. A missing file holds none. Values are never returned.
func MachineFileCredentials() (keys []string, path string, err error) {
	path, err = machineConfigPathInUse()
	if err != nil {
		return nil, "", err
	}
	data, err := readMachineConfigBytes()
	if errors.Is(err, errConfigNotFound) {
		return nil, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	var view repoTierCredentialView
	if err := yaml.Unmarshal(data, &view); err != nil {
		// A malformed credential block; yaml.v3's errors may quote the value.
		return []string{"github_auth / platform (unreadable block)"}, path, nil
	}
	for _, f := range repoTierCredentialFields(&view) {
		if plaintextCredential(&f.node) {
			keys = append(keys, f.path)
		}
	}
	sort.Strings(keys)
	return keys, path, nil
}
