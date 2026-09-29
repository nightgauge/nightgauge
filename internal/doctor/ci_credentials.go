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
// value. It is a blocker (#2091): the remedy (remove the key, rotate it if the runner
// is shared) is the operator's.

// ciMachineCredentialsCheck is the check's key in DoctorResult.Checks.
const ciMachineCredentialsCheck = "ci_machine_credentials"

// machineFileCredentials lists the plaintext credentials in the machine-tier
// file. A variable so a test can supply the file's contents.
var machineFileCredentials = config.MachineFileCredentials

func ciMachineCredentialFindings(getenv func(string) string) ([]Finding, string) {
	const check, code = ciMachineCredentialsCheck, "NGD025"
	if !config.CIHost(getenv) {
		return nil, "skipped: not a CI host"
	}
	keys, path, err := machineFileCredentials()
	if err != nil {
		return []Finding{unverifiableFinding(check, code, SeverityWarning, "ci_machine_credentials",
				"could not read the machine-tier config file on a CI host: "+err.Error())},
			"could not read the machine-tier config file"
	}
	if len(keys) == 0 {
		return nil, "CI host: no credential in the machine-tier config file"
	}
	// Evidence names the file and the keys, never a value.
	return []Finding{newFinding(check, code, SeverityBlocker,
			fmt.Sprintf("ci_machine_credentials: %d credential(s) in %s on a CI host", len(keys), path),
			fmt.Sprintf("CI host: %s holds %d credential(s) in plaintext (%s; values redacted). "+
				"On a runner shared between jobs every later job can read them",
				path, len(keys), strings.Join(keys, ", ")),
			map[string]string{"path": path, "keys": strings.Join(keys, ", ")},
			[]string{path},
			manualRemedy("rotate", "Remove the credentials from the machine file and rotate them", check,
				"Remove "+strings.Join(keys, ", ")+" from "+path,
				"Supply credentials through the job's environment (NIGHTGAUGE_LICENSE_KEY, GITHUB_TOKEN)",
				"Rotate any key a shared runner may have exposed, at its issuer"))},
		fmt.Sprintf("CI host: %d credential(s) in %s", len(keys), path)
}

// ciGetenv is the environment the check reads. A variable for tests.
var ciGetenv = os.Getenv
