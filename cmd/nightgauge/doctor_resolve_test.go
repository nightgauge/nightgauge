package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// TestDoctorResolveMachineID (#2308): the command needs an explicit --keep,
// passes the choice through, and reports where the other id went.
func TestDoctorResolveMachineID(t *testing.T) {
	var got []string
	resolve := func(side string) (doctor.MachineIDResolution, error) {
		got = append(got, side)
		return doctor.MachineIDResolution{Kept: side, Path: "/s/machine-id", ID: "new-id",
			Backup: "/s/machine-id.replaced-20260930T120000Z", BackupID: "old-id", Legacy: "/h/.nightgauge/machine-id"}, nil
	}
	run := func(args ...string) (string, error) {
		cmd := doctorResolveMachineIDCmd(resolve)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}

	if _, err := run(); err == nil || !strings.Contains(err.Error(), "keep") {
		t.Errorf("no --keep: err %v, want the required flag named", err)
	}
	if len(got) != 0 {
		t.Fatalf("resolved without a choice: %v", got)
	}

	out, err := run("--keep", "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "legacy" {
		t.Errorf("resolver got %v, want [legacy]", got)
	}
	for _, want := range []string{"new-id", "/s/machine-id", "old-id", "/s/machine-id.replaced-20260930T120000Z",
		"/h/.nightgauge/machine-id", "nightgauge doctor --fix"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}

	out, err = run("--keep", "state", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res doctor.MachineIDResolution
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Backup == "" || res.Kept != "state" {
		t.Errorf("--json output %q (%v)", out, err)
	}
}

// TestDoctorResolveIsWired: `nightgauge doctor resolve machine-id` is
// reachable from the doctor command.
func TestDoctorResolveIsWired(t *testing.T) {
	cmd, _, err := doctorCmd().Find([]string{"resolve", "machine-id"})
	if err != nil || cmd.Name() != "machine-id" {
		t.Fatalf("find machine-id: %v, %v", cmd, err)
	}
}
