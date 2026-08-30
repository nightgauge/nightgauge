package gates

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nightgauge/nightgauge/internal/deliverable"
)

// #1076. `feature-dev` was the last stage-boundary artifact still trusted to a
// model's turn discipline: `dev-{N}.json` existed only because the skill
// remembered to write it, at phase 14 of 18, with four phases of bookkeeping
// standing between finishing the implementation and recording that it
// happened.
//
// #223 built the detector for what that costs and named it precisely. Nobody
// ever fixed the producer, so the detector has been faithfully reporting the
// same defect ever since — four times in three days across this workspace, each
// one a full stage's spend converted into a terminal failure sitting on top of
// a complete, correct implementation.
//
// The failures were never a context-length or attention problem, which is why
// more prompt emphasis kept not working. The clearest specimen recorded so far:
// `implementation` COMPLETED, and every phase after it —
// `write-dev-context` included — is recorded `skipped`. The stage backgrounded
// its test run and spun on `sleep 1; echo waiting` / `true` / `wait 2>/dev/null`
// until it idled out. A handoff that only exists as a prompt instruction is
// unreachable the moment a stage stops advancing phases, for any reason at all.
//
// So the handoff stops being an instruction. `pr-create` and `pr-merge` both
// made this move already (docs/PR_CREATE_STAGE.md, docs/PR_MERGE_STAGE.md):
// deterministic path first, model as enrichment. Here the deterministic path is
// git — the same evidence `inspectDevWork` already gathers to convict the
// missing handoff is sufficient to write one.
//
// What this does NOT do is manufacture consent for unverified work. The derived
// document records only what git can prove; `build_verification` and
// `tests_status` are stamped as unverified rather than passed, and
// `handoff_source` marks the provenance so a lost narrative is visible instead
// of silent. feature-validate re-runs the suite for real regardless — it always
// did — so the honest degraded record is strictly better than a terminal
// failure over work that is sitting on disk.

// Handoff provenance values recorded in `handoff_source`.
const (
	// HandoffSourceAuthored means the stage wrote its own handoff and it
	// stood on its own — nothing here touched the file list.
	HandoffSourceAuthored = "authored"
	// HandoffSourceDerived means `files_changed` came from git because the
	// stage's handoff was missing or recorded nothing. Narrative fields from
	// a partial authored document, when one existed, are preserved.
	HandoffSourceDerived = "derived"
)

// deriveDevHandoffEnabled is the mutation switch for #1076's derivation.
//
// It is a var so a test can turn the fix off and assert the gate falls back to
// the exact `[dev-handoff-missing]` terminal verdict it produced before —
// AC6, and the difference between a regression test and decoration. A test
// that cannot make the new behaviour disappear cannot prove the new behaviour
// is what is passing it.
//
// It doubles as an operator escape hatch: if derivation ever misfires, the
// detector underneath is untouched and still correct.
var deriveDevHandoffEnabled = true

// devHandoffOutcome is what ensureDevHandoff resolved the handoff question to,
// before the gate judges the document's contents.
type devHandoffOutcome struct {
	// Source is HandoffSourceAuthored or HandoffSourceDerived. Empty when
	// no derivation was attempted or one was attempted and declined.
	Source string
	// Verdict is set only when derivation was declined or failed: the gate
	// must return this unchanged instead of proceeding. A declined
	// derivation is the healthy path for a genuinely empty tree — #202's
	// `dev_produced_no_changes` and the missing-context no-op both still
	// have to reach the operator exactly as before.
	Verdict *devHandoffVerdict
	// Notes ride into the gate's evidence so a derived pass is legible in
	// the run record without opening the workspace.
	Notes []string
	// Policy is the deliverable-schema outcome for whichever document the
	// gate is about to read — the stage's own, or the one just derived.
	//
	// It is carried rather than recomputed. The policy has to run BEFORE the
	// zero-changes count (a repairable `files_changed` shape holds a real
	// file list this function would otherwise read as empty), and it is
	// idempotent, so a second application in the gate would find nothing to
	// repair and report Changed=false — silently dropping #1176's repair
	// notes from the run record. A repair that passes silently is a repair
	// nobody fixes.
	Policy deliverable.PolicyOutcome
	// PolicyErr is non-nil when the policy could not be applied at all
	// (unreadable file). The gate treats it exactly as it treated its own
	// ApplyPolicyToFile error: skip the schema verdict, keep going.
	PolicyErr error
	// Files and FileCount carry the deliverable paths a derived pass
	// recovered, so `gate verify --json` reports them on the pass path too.
	//
	// #134 put this list on the JSON payload for feature-validate's Phase 0,
	// but only ever populated it on the dev_handoff_missing FAILURE. #1076
	// turns that failure into a pass, which would have silently emptied the
	// field for its only consumer — the field surviving only on the path that
	// no longer happens is how a contract quietly stops being honoured.
	Files     []string
	FileCount int
}

// ensureDevHandoff guarantees that, whenever git can prove the stage produced
// work, a valid `dev-{N}.json` exists on disk before the gate reads it.
//
// It runs BEFORE the gate's own checks, in the same position and for the same
// reason as deliverable.ApplyPolicyToFile: repair what is deterministically
// repairable while the run is still alive, and let the gate judge the repaired
// document. The two repairs compose — the policy fixes a malformed receipt,
// this one supplies an absent one.
//
// The three outcomes:
//
//   - The handoff is present and records file changes → nothing to do,
//     Source=authored.
//   - The handoff is missing or records nothing, and git finds work → write a
//     derived document, Source=derived.
//   - The handoff is missing or records nothing, and git finds nothing (or
//     cannot answer) → Verdict is nil and Source is empty; the caller
//     proceeds into its unchanged no-op paths. This is the AC5 boundary: an
//     empty tree must still fail, or #1076 has papered over #202.
func ensureDevHandoff(workspace string, issueNumber int, ctxPath string, now time.Time) devHandoffOutcome {
	raw, err := os.ReadFile(ctxPath)
	switch {
	case err == nil:
		// Normalise BEFORE counting. `files_changed` has repairable shapes
		// that carry a real file list in a form this function would read as
		// empty — #1176's flat array, and the sibling-manifest form the
		// policy folds in. Counting first would call a complete deliverable
		// empty and overwrite it, discarding exactly what #1176 was fixed to
		// preserve. The policy is idempotent, so the gate re-applying it a
		// few lines later is a no-op rather than a second opinion.
		policy, perr := deliverable.ApplyPolicyToFile("dev", ctxPath, now)
		authoredOutcome := devHandoffOutcome{
			Source:    HandoffSourceAuthored,
			Policy:    policy,
			PolicyErr: perr,
		}
		if perr == nil {
			if !policy.OK() {
				// Irreparable. The gate reports the schema failure with the
				// policy's own summary; do not pre-empt it.
				return authoredOutcome
			}
			if policy.Changed {
				if repaired, rerr := os.ReadFile(ctxPath); rerr == nil {
					raw = repaired
				}
			}
		}
		// A readable file still needs the zero-changes question asked.
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			// Malformed. The gate's decode path reports this precisely;
			// overwriting it here would destroy the evidence.
			return authoredOutcome
		}
		if declaredFileCount(doc) > 0 {
			return authoredOutcome
		}
		return deriveHandoff(workspace, issueNumber, ctxPath,
			"dev context records zero file changes", doc, now, authoredOutcome)
	case os.IsNotExist(err):
		return deriveHandoff(workspace, issueNumber, ctxPath,
			"dev context file missing", nil, now, devHandoffOutcome{})
	default:
		// Unreadable for some other reason (permissions). The gate reports
		// that on its own.
		return devHandoffOutcome{Source: HandoffSourceAuthored}
	}
}

// deriveHandoff asks git the same question devHandoffMissing asks, and writes
// the answer down instead of only complaining about it.
func deriveHandoff(workspace string, issueNumber int, ctxPath, condition string, authored map[string]any, now time.Time, declined devHandoffOutcome) devHandoffOutcome {
	v := devHandoffMissing(workspace, condition, ctxPath)
	if !v.OK {
		// git found nothing, or could not answer. Leave every existing
		// verdict path exactly as it was — including the policy outcome for
		// the document the gate is still about to read.
		return declined
	}
	if !deriveDevHandoffEnabled {
		return devHandoffOutcome{Verdict: &v}
	}

	work := inspectDevWork(workspace, nil)
	if work.AllFiles.Total() == 0 {
		// devHandoffMissing said there is work but the classified set is
		// empty — the two disagree, so write nothing and report the original
		// verdict. Deriving an empty file list here would produce a document
		// claiming zero changes, which is the very state being repaired.
		return devHandoffOutcome{Verdict: &v}
	}

	doc := derivedDevContext(issueNumber, work, authored, condition, now)
	if err := writeDevContext(ctxPath, doc); err != nil {
		// The work is real and we could not record it. Fail with the
		// original verdict plus why the repair did not happen — silently
		// passing a stage whose handoff does not exist is how this defect
		// reaches the NEXT stage instead of stopping here.
		w := v
		w.Evidence = append(w.Evidence,
			fmt.Sprintf("handoff derivation failed: %v", err))
		return devHandoffOutcome{Verdict: &w}
	}

	notes := []string{
		fmt.Sprintf("derived from git: %s; %d file(s) recovered", condition, work.AllFiles.Total()),
		"build_verification and tests_status are unverified — git proves the files, not the suite; feature-validate runs it for real",
	}
	if len(authored) > 0 {
		notes = append(notes, "narrative fields preserved from the stage's own partial handoff")
	} else {
		notes = append(notes, "no stage-authored handoff existed — approach, decisions and known gaps are lost for this run")
	}
	// Re-apply the policy to the document just written, so the gate judges
	// the derived deliverable by the same rule table as an authored one.
	// Deriving a document that the schema would reject is a defect this
	// should surface, not hide.
	policy, perr := deliverable.ApplyPolicyToFile("dev", ctxPath, now)
	return devHandoffOutcome{
		Source:    HandoffSourceDerived,
		Notes:     notes,
		Policy:    policy,
		PolicyErr: perr,
		Files:     v.Files,
		FileCount: v.FileCount,
	}
}

// derivedDevContext builds the deliverable from git ground truth, letting a
// partial authored document supply everything git cannot know.
//
// The split is the point. git is authoritative for WHAT changed and nothing
// else; the model is authoritative for WHY, and worthless as a witness to its
// own file list — that asymmetry is the whole of #202, #223 and this issue.
func derivedDevContext(issueNumber int, work devWorkState, authored map[string]any, condition string, now time.Time) map[string]any {
	doc := map[string]any{}
	// Narrative first, so the mechanical fields below always win.
	for _, field := range narrativeFields {
		if v, ok := authored[field]; ok {
			doc[field] = v
		}
	}

	schemaVersion, _ := deliverable.CanonicalSchemaVersion("dev")
	doc["schema_version"] = schemaVersion
	doc["issue_number"] = issueNumber
	doc["files_changed"] = map[string]any{
		"created":  nonNil(work.AllFiles.Created),
		"modified": nonNil(work.AllFiles.Modified),
		"deleted":  nonNil(work.AllFiles.Deleted),
	}
	// Unverified, not passed. The gate fails only on status=="failed", so
	// this lets the run continue while stating plainly that nothing here
	// witnessed a build. Claiming "passed" would be the self-granted
	// exemption docs/FAILURE_TAXONOMY.md names, and would make a derived
	// handoff indistinguishable from a healthy one downstream.
	doc["build_verification"] = map[string]any{
		"ran":          false,
		"status":       "unverified",
		"commands_run": []string{},
	}
	doc["tests_status"] = map[string]any{
		"passed":       0,
		"failed":       0,
		"coverage":     nil,
		"test_command": "",
	}
	doc["quality_checks"] = map[string]any{
		"code_standards":  "not_run",
		"security_review": "not_run",
		"type_check":      "not_run",
		"dead_code_scan":  "not_run",
	}
	doc["handoff_source"] = HandoffSourceDerived
	doc["handoff_derivation"] = map[string]any{
		"derived_at":          now.UTC().Format(time.RFC3339),
		"reason":              condition,
		"probe_mode":          probeMode(work),
		"narrative_preserved": len(authored) > 0,
	}
	if _, ok := doc["created_at"]; !ok {
		doc["created_at"] = now.UTC().Format(time.RFC3339)
	}
	if _, ok := doc["retry_count"]; !ok {
		doc["retry_count"] = 0
	}
	if _, ok := doc["feedback"]; !ok {
		doc["feedback"] = []any{}
	}
	return doc
}

// narrativeFields are the parts of the deliverable git cannot reconstruct.
// A stage that wrote a partial handoff — recording its approach and then
// dying before it had a file list — keeps every one of them.
var narrativeFields = []string{
	"feedback",
	"retry_count",
	"retry_reasons",
	"knowledge_path",
	"architectural_constraints",
	"commit_sha",
	"created_at",
}

// probeMode reports which ground-truth probe produced the file list, so a
// bookkeeping-only deliverable (#237) stays distinguishable in the record.
func probeMode(work devWorkState) string {
	if work.Mode != "" {
		return work.Mode
	}
	return "standard"
}

// nonNil guarantees a JSON array rather than null. A null `created` would be
// decoded as an empty slice by the gate but reads as "unknown" to a human and
// to the TypeScript consumers, and this document's entire purpose is to be
// unambiguous about what changed.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// writeDevContext writes the deliverable atomically.
//
// Atomic because the gate is not the only reader: a concurrent stage or an
// operator tailing the file must never observe a half-written handoff, which
// would present as exactly the malformed-JSON failure this code exists to
// remove.
func writeDevContext(path string, doc map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dev-handoff-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// declaredFileCount counts the paths a decoded dev context claims, across the
// three `files_changed` buckets.
//
// It reads the same document shape the gate decodes into a struct, but from
// the generic map ensureDevHandoff already has — decoding twice to ask one
// question would be the second parser this package spent #330 removing.
func declaredFileCount(doc map[string]any) int {
	fc, ok := doc["files_changed"].(map[string]any)
	if !ok {
		return 0
	}
	n := 0
	for _, bucket := range []string{"created", "modified", "deleted"} {
		if entries, ok := fc[bucket].([]any); ok {
			n += len(entries)
		}
	}
	return n
}
