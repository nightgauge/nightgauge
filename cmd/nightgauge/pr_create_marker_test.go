package main

import (
	"strings"
	"testing"
)

func TestPrCreateBody(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		issue int
		run   string
		want  string
	}{
		{"no issue leaves body unchanged", "Closes #7", 0, "r1", "Closes #7"},
		{"stamps with run id", "Closes #7\n", 7, "r1",
			"Closes #7\n\n<!-- nightgauge:pipeline stage=pr-create issue=7 run=r1 -->"},
		{"unknown run renders dash", "Closes #7", 7, "",
			"Closes #7\n\n<!-- nightgauge:pipeline stage=pr-create issue=7 run=- -->"},
		{"empty body is just the marker", "", 7, "r1",
			"<!-- nightgauge:pipeline stage=pr-create issue=7 run=r1 -->"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prCreateBody(tc.body, tc.issue, tc.run); got != tc.want {
				t.Fatalf("prCreateBody() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrCreateBodyDoesNotDoubleStamp(t *testing.T) {
	once := prCreateBody("x", 7, "r1")
	if got := prCreateBody(once, 7, "r1"); strings.Count(got, "nightgauge:pipeline") != 1 {
		t.Fatalf("double stamp: %q", got)
	}
}

func TestPrCreateCmdHasMarkerFlags(t *testing.T) {
	cmd := prCreateCmd()
	for _, f := range []string{"issue", "run-id"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Fatalf("pr create lacks --%s", f)
		}
	}
}
