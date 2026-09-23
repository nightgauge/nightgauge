package doctor

import (
	"fmt"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
)

// A credential in the machine-tier file on a CI host (ADR-024 § 5).
//
// In CI Nightgauge never writes a credential to disk and resolves from the
// environment first, but a file written before that rule, or by hand, is still
// read as a fallback, and on a self-hosted runner shared between jobs it hands
// one job's key to the next. This check names the file and the keys, never a
// value. It is a warning: the remedy (remove the key, rotate it if the runner
// is shared) is the operator's.

// ciMachineCredentialsCheck is the check's key in DoctorResult.Checks.
const ciMachineCredentialsCheck = "ci_machine_credentials"

// machineFileCredentials lists the plaintext credentials in the machine-tier
// file. A variable so a test can supply the file's contents.
var machineFileCredentials = config.MachineFileCredentials

func checkCIMachineCredentials(getenv func(string) string) (CheckItem, string) {
	if !config.CIHost(getenv) {
		return CheckItem{OK: true, Detail: "skipped: not a CI host"}, ""
	}
	keys, path, err := machineFileCredentials()
	if err != nil {
		return CheckItem{OK: false, Error: "could not read the machine-tier config file: " + err.Error()},
			"ci_machine_credentials: could not read the machine-tier config file on a CI host"
	}
	if len(keys) == 0 {
		return CheckItem{OK: true, Detail: "CI host: no credential in the machine-tier config file"}, ""
	}
	msg := fmt.Sprintf("CI host: %s holds %d credential(s) in plaintext (%s; values redacted). "+
		"On a runner shared between jobs every later job can read them. Remove them from the file, "+
		"supply credentials through the job's environment (NIGHTGAUGE_LICENSE_KEY, GITHUB_TOKEN), "+
		"and rotate any key a shared runner may have exposed",
		path, len(keys), strings.Join(keys, ", "))
	return CheckItem{OK: false, Error: msg},
		fmt.Sprintf("ci_machine_credentials: %d credential(s) in %s on a CI host", len(keys), path)
}

// ciGetenv is the environment the check reads. A variable for tests.
var ciGetenv = os.Getenv
