package execution

import (
	"regexp"
	"strconv"
)

// PhaseMarker holds parsed data from a phase:start HTML comment in skill output.
type PhaseMarker struct {
	Name  string
	Index int
	Total int
	Stage string
}

var phaseStartRe = regexp.MustCompile(
	`<!--\s*phase:start\s+name="([^"]+)"\s+index=(\d+)\s+total=(\d+)\s+stage="([^"]+)"\s*-->`,
)

// ParsePhaseMarker attempts to detect a phase:start HTML comment in a line.
// Returns the parsed marker and true if a marker was found.
func ParsePhaseMarker(line string) (*PhaseMarker, bool) {
	m := phaseStartRe.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	index, _ := strconv.Atoi(m[2])
	total, _ := strconv.Atoi(m[3])
	return &PhaseMarker{
		Name:  m[1],
		Index: index,
		Total: total,
		Stage: m[4],
	}, true
}
