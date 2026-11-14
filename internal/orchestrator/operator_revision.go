package orchestrator

// Operator revision reaches feature-dev (#3163).
//
// An Action Center steer is written to feedback-{N}.json as an OPERATOR_STEER
// signal (operator_steer.go). feature-planning reads it and records the
// revision in `revision_reasons` with the `operator-origin:` marker, but until
// #3163 nothing carried the steer itself, or the rule that the revised plan
// binds, to feature-dev. In one downstream re-run (#1051) feature-dev re-judged
// the partial-delivery remainder on its own, found it met, and wrote nothing.
//
// Two pieces close that gap, shared by both dispatch paths:
//   - spliceOperatorSteer copies every steer into planning-{N}.json as
//     `operator_steer`, the twin of ContextAssembler.spliceOperatorSteer;
//   - OperatorRevisionPromptBlock renders the feature-dev prompt block from
//     planning-{N}.json. The scheduler appends it, and the extension appends
//     the same text through `nightgauge issue operator-revision`.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	stagecontext "github.com/nightgauge/nightgauge/internal/execution/context"
)

// operatorOriginPrefix marks a planning `revision_reasons` entry that an
// operator's steer caused (operatorSteerOriginMarker begins with it).
const operatorOriginPrefix = "operator-origin:"

// operatorRevisionPromptHeading heads the feature-dev operator block.
const operatorRevisionPromptHeading = "## Operator-Revised Plan"

// OperatorSteerEntry is one element of planning-{N}.json's `operator_steer`.
type OperatorSteerEntry struct {
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

// operatorSteers returns the OPERATOR_STEER signals in ws's feedback-{N}.json,
// in the order they were written. Absent or unreadable feedback has none.
func operatorSteers(ws string, issue int) []OperatorSteerEntry {
	path, err := stagecontext.ContextPath(ws, issue, "feedback")
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var fb FeedbackContext
	if err := json.Unmarshal(raw, &fb); err != nil {
		log.Printf("#%d: feedback context is not valid JSON, no operator steer is spliced: %v", issue, err)
		return nil
	}
	var out []OperatorSteerEntry
	for _, sig := range fb.Signals {
		if sig.SignalType != OperatorSteerSignalType || strings.TrimSpace(sig.Rationale) == "" {
			continue
		}
		ev := sig.Evidence
		if ev == nil {
			ev = []string{}
		}
		out = append(out, OperatorSteerEntry{Text: sig.Rationale, Evidence: ev})
	}
	return out
}

// spliceOperatorSteer writes ws's operator steers into planning-{N}.json as
// `operator_steer`. No steer, no-op: the planning context stays untouched.
// Best-effort, like splicePartialDelivery.
func spliceOperatorSteer(ws string, issue int, planningFile string) {
	steers := operatorSteers(ws, issue)
	if len(steers) == 0 {
		return
	}
	raw, err := os.ReadFile(planningFile)
	if err != nil {
		return
	}
	var planning map[string]any
	if err := json.Unmarshal(raw, &planning); err != nil {
		log.Printf("#%d: planning context is not valid JSON, cannot splice operator_steer: %v", issue, err)
		return
	}
	planning["operator_steer"] = steers
	merged, err := json.MarshalIndent(planning, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(planningFile, merged, 0o644); err != nil {
		log.Printf("#%d: could not persist the spliced planning context: %v", issue, err)
	}
}

// operatorRevision is what feature-dev must honour from planning-{N}.json.
type operatorRevision struct {
	// Revised: a `revision_reasons` entry carries the operator-origin marker.
	Revised       bool
	FilesToModify []string
	FilesToCreate []string
	Steers        []string
}

// binding reports whether the plan is an operator revision that names files:
// the case where a zero-change feature-dev ignored the operator (#1051).
func (r operatorRevision) binding() bool {
	return r.Revised && len(r.FilesToModify)+len(r.FilesToCreate) > 0
}

// readOperatorRevision reads planningFile. Absent or invalid, it is empty.
func readOperatorRevision(planningFile string) operatorRevision {
	raw, err := os.ReadFile(planningFile)
	if err != nil {
		return operatorRevision{}
	}
	var p struct {
		RevisionReasons []string             `json:"revision_reasons"`
		FilesToModify   []string             `json:"files_to_modify"`
		FilesToCreate   []string             `json:"files_to_create"`
		OperatorSteer   []OperatorSteerEntry `json:"operator_steer"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return operatorRevision{}
	}
	var r operatorRevision
	for _, reason := range p.RevisionReasons {
		if strings.Contains(reason, operatorOriginPrefix) {
			r.Revised = true
			break
		}
	}
	r.FilesToModify = p.FilesToModify
	r.FilesToCreate = p.FilesToCreate
	for _, s := range p.OperatorSteer {
		if t := strings.TrimSpace(s.Text); t != "" {
			r.Steers = append(r.Steers, t)
		}
	}
	return r
}

// OperatorRevisionPromptBlock is the feature-dev prompt trailer for ws's
// planning-{N}.json: "" when the plan carries neither an operator-origin
// revision nor a steer, so every other prompt stays byte-identical.
func OperatorRevisionPromptBlock(ws string, issue int) string {
	path, err := stagecontext.ContextPath(ws, issue, "planning")
	if err != nil {
		return ""
	}
	return renderOperatorRevisionBlock(readOperatorRevision(path))
}

func renderOperatorRevisionBlock(r operatorRevision) string {
	if !r.Revised && len(r.Steers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + operatorRevisionPromptHeading + "\n\n")
	if len(r.Steers) > 0 {
		b.WriteString("An operator steered this run from the Action Center. They wrote:\n\n")
		for _, s := range r.Steers {
			for _, line := range strings.Split(s, "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")
		}
		b.WriteString("The planning context records this under `operator_steer`.\n\n")
	}
	if !r.Revised {
		b.WriteString("Honour it as context for this stage.\n")
		return b.String()
	}
	b.WriteString("feature-planning revised the plan for the operator (`revision_reasons` carries `" +
		operatorOriginPrefix + "`). This plan is binding on this stage.")
	files := append(append([]string{}, r.FilesToModify...), r.FilesToCreate...)
	if len(files) > 0 {
		b.WriteString(" Implement its files:\n\n")
		for _, f := range files {
			b.WriteString(fmt.Sprintf("- `%s`\n", f))
		}
		b.WriteString("\n")
	} else {
		b.WriteString("\n\n")
	}
	b.WriteString("If you cannot, record a `PLAN_REVISION_NEEDED` signal in the dev context whose evidence " +
		"names the plan step you dispute. Never end with zero changes on your own reading of the remaining " +
		"criteria: the operator revision outranks the partial-delivery list.\n")
	return b.String()
}

// planRevisionDispute returns the evidence of the first PLAN_REVISION_NEEDED
// signal in ws's dev-{N}.json that names something, or "" when there is none:
// a dispute with no evidence names no plan step.
func planRevisionDispute(ws string, issue int) string {
	path, err := stagecontext.ContextPath(ws, issue, "dev")
	if err != nil {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var dev struct {
		Feedback []FeedbackSignal `json:"feedback"`
	}
	if json.Unmarshal(raw, &dev) != nil {
		return ""
	}
	for _, sig := range dev.Feedback {
		if sig.SignalType != "PLAN_REVISION_NEEDED" {
			continue
		}
		var named []string
		for _, e := range sig.Evidence {
			if t := strings.TrimSpace(e); t != "" {
				named = append(named, t)
			}
		}
		if len(named) > 0 {
			return strings.Join(named, "; ")
		}
	}
	return ""
}
