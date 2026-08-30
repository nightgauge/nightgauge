package deliverable

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// The deliverable-schema policy (#1182)
// ---------------------------------------------------------------------------
//
// A stage deliverable that does not match its schema had, until now, two
// possible fates and no way to predict which: the TypeScript context-file
// validator logged `(non-fatal, continuing)` and shipped the run, while the Go
// post-condition gate failed the stage and threw the work away. Across one
// ten-run session that produced eight mismatches in five fields — seven
// warnings and one $6.12 loss — and the difference between them was not
// severity. It was which validator happened to look at that particular field.
//
// This file makes the consequence a property of the DEFECT. One classifier,
// one closed rule table, consulted at both validation points. Every mismatch
// lands in exactly one of three dispositions:
//
//   - REPAIRED    The deliverable already carries every value the schema
//                 wants; only the shape or the vocabulary is wrong, and the
//                 mapping from what was written to what is wanted is a
//                 hand-written entry in the table below. The value is rewritten
//                 and the repair is recorded in the deliverable itself.
//
//   - QUARANTINED A value is genuinely absent (a gate metric with no gate name,
//                 a skipped phase with no reason) but the field is telemetry no
//                 consumer's control flow reads. The offending entry is REMOVED
//                 rather than passed through malformed, and the field is named
//                 in `_deliverable_policy.untrustworthy` so the gate-metrics
//                 record and the learning corpus can see that what they read is
//                 incomplete. Tolerating a mismatch and silently forwarding it
//                 are different things; only the first is acceptable.
//
//   - FATAL       A value a downstream consumer needs is genuinely absent, or
//                 present in a form no closed rule can map without guessing.
//                 Both validation points fail. This is the case the gate
//                 already handled, and it stays handled.
//
// The table is deliberately closed. Every rule is a rename or a synonym with
// all values present; none infers, partially fills, or asks a model. A rule
// that cannot prove its repair is TOTAL must return FATAL instead — see
// `applyDevFilesChangedRule`, which refuses the repair the moment the sibling
// manifest and the array disagree about even one path.
//
// The conformance corpus in schemas/deliverable-policy-corpus-v1.json is read
// by this package's tests AND by the TypeScript mirror's tests. Two
// implementations of one policy drift in silence; that file is what makes a
// drift fail a suite instead of a run.

// PolicyVersion identifies the rule table. Bump it when a rule is added,
// removed, or changes disposition.
const PolicyVersion = "1"

// PolicyMarkerField is the key under which the policy records what it did, in
// the deliverable itself. Every context schema is `.passthrough()`, so this
// survives a read-modify-write by any stage.
const PolicyMarkerField = "_deliverable_policy"

// Disposition is what the policy decided about one mismatch.
type Disposition string

const (
	// DispositionRepaired — all values present, shape or vocabulary rewritten
	// by a closed rule.
	DispositionRepaired Disposition = "repaired"
	// DispositionQuarantined — a value is genuinely absent in an advisory
	// field; the entry is dropped and the field marked untrustworthy.
	DispositionQuarantined Disposition = "quarantined"
	// DispositionFatal — a value a consumer needs is absent or unmappable.
	DispositionFatal Disposition = "fatal"
)

// Note is one policy decision about one field.
type Note struct {
	// Field is the dotted path of the offending field, e.g. "gate_metrics.0.result".
	Field string `json:"field"`
	// Disposition is the outcome the policy assigned.
	Disposition Disposition `json:"disposition"`
	// Rule is the stable id of the rule that fired. Rule ids are the contract
	// the corpus asserts against; they are not free text.
	Rule string `json:"rule"`
	// Detail is operator-facing evidence — what was found and what was done.
	Detail string `json:"detail"`
}

// PolicyOutcome is the result of applying the policy to one deliverable.
type PolicyOutcome struct {
	// Doc is the normalized document. Nil when the input was not a JSON object.
	Doc map[string]any
	// Notes are every decision the policy made, in field order.
	Notes []Note
	// Changed reports whether Doc differs from the input and should be
	// written back.
	Changed bool
}

// Fatal returns the notes that failed the deliverable.
func (o PolicyOutcome) Fatal() []Note { return o.notesWith(DispositionFatal) }

// Repairs returns the notes describing rewritten values.
func (o PolicyOutcome) Repairs() []Note { return o.notesWith(DispositionRepaired) }

// Quarantines returns the notes describing dropped entries.
func (o PolicyOutcome) Quarantines() []Note { return o.notesWith(DispositionQuarantined) }

// OK reports whether the deliverable may proceed. Repairs and quarantines
// proceed; a fatal does not.
func (o PolicyOutcome) OK() bool { return len(o.Fatal()) == 0 }

func (o PolicyOutcome) notesWith(d Disposition) []Note {
	var out []Note
	for _, n := range o.Notes {
		if n.Disposition == d {
			out = append(out, n)
		}
	}
	return out
}

// Untrustworthy lists the top-level fields whose content the policy could not
// vouch for, because an entry was dropped from them. A consumer that reads one
// of these fields is reading an incomplete record.
func (o PolicyOutcome) Untrustworthy() []string {
	seen := map[string]bool{}
	for _, n := range o.Notes {
		if n.Disposition != DispositionQuarantined {
			continue
		}
		seen[topLevelField(n.Field)] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Verdict summarises the outcome as the worst disposition present. It is the
// value both validation points branch on, so that one defect cannot be a
// warning in one stage and a run-ender in the next.
func (o PolicyOutcome) Verdict() string {
	switch {
	case len(o.Fatal()) > 0:
		return "fatal"
	case len(o.Quarantines()) > 0:
		return "quarantined"
	case len(o.Repairs()) > 0:
		return "repaired"
	default:
		return "clean"
	}
}

// Summary renders the notes as operator-facing evidence lines.
func (o PolicyOutcome) Summary() []string {
	out := make([]string, 0, len(o.Notes))
	for _, n := range o.Notes {
		out = append(out, fmt.Sprintf("%s [%s] %s: %s", n.Disposition, n.Rule, n.Field, n.Detail))
	}
	return out
}

func topLevelField(path string) string {
	if i := strings.IndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return path
}

// ---------------------------------------------------------------------------
// Canonical schema versions (#1177)
// ---------------------------------------------------------------------------

// canonicalSchemaVersion is the version of the contract this binary implements
// for each stage deliverable. It is ASSERTED onto the document rather than
// trusted from it: a skill writing its own version number is a claim, and #1177
// showed the claim can be a remembered older contract ("1.5" against an include
// that says "1.8"). Stamping turns the claim into a fact and records the
// correction, so a stage recalling a stale schema stays visible.
//
// A drift test asserts each value here equals the literal in the skill include
// that authors the deliverable, so bumping one without the other fails CI.
var canonicalSchemaVersion = map[string]string{
	"dev":      "1.9",
	"validate": "2.6",
}

// CanonicalSchemaVersion returns the contract version for a stage deliverable
// and whether the stage is known to the policy.
func CanonicalSchemaVersion(stage string) (string, bool) {
	v, ok := canonicalSchemaVersion[stage]
	return v, ok
}

// KnownStages lists the deliverable kinds the policy governs.
func KnownStages() []string {
	out := make([]string, 0, len(canonicalSchemaVersion))
	for k := range canonicalSchemaVersion {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Entry points
// ---------------------------------------------------------------------------

// ApplyPolicyBytes decodes raw deliverable JSON and applies the policy.
//
// A syntax error is returned as an error, not a note: a file that is not JSON
// at all is a different failure with a different recovery (a truncated write
// points at budget or a crash) and the gate already reports it as such.
func ApplyPolicyBytes(stage string, raw []byte) (PolicyOutcome, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return PolicyOutcome{}, err
	}
	return ApplyPolicy(stage, doc), nil
}

// ApplyPolicyToFile reads a deliverable, applies the policy, and — when the
// policy repaired or quarantined anything — stamps the marker and writes the
// normalized document back.
//
// Writing back is what makes the repair RECORDED rather than merely tolerated
// (#1176): the next stage reads the shape it expects, and a human or a corpus
// miner opening the file sees exactly which emitter got it wrong. A fatal
// outcome is never written back — there is nothing correct to write.
func ApplyPolicyToFile(stage, path string, now time.Time) (PolicyOutcome, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PolicyOutcome{}, err
	}
	out, err := ApplyPolicyBytes(stage, raw)
	if err != nil {
		return PolicyOutcome{}, err
	}
	if !out.OK() || !out.Changed {
		return out, nil
	}
	out.Stamp(now)
	encoded, err := json.MarshalIndent(out.Doc, "", "  ")
	if err != nil {
		return out, err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return out, err
	}
	return out, nil
}

// ApplyPolicy applies the closed rule table to a decoded deliverable.
//
// The input is never mutated; the returned Doc is a fresh map.
func ApplyPolicy(stage string, decoded any) PolicyOutcome {
	obj, ok := decoded.(map[string]any)
	if !ok {
		return PolicyOutcome{
			Notes: []Note{{
				Field:       "(top level)",
				Disposition: DispositionFatal,
				Rule:        "any.document.not_an_object",
				Detail:      fmt.Sprintf("expected a JSON object, got %s", jsonShape(decoded)),
			}},
		}
	}

	doc := cloneObject(obj)
	out := PolicyOutcome{Doc: doc}

	// Rules run in field order so the notes read top-to-bottom like the file.
	applySchemaVersionRule(stage, doc, &out)
	switch stage {
	case "dev":
		applyDevFilesChangedRule(doc, &out)
		applyQualityChecksRule(doc, &out)
	case "validate":
		applySkippedPhasesRule(doc, &out)
		applyGateMetricsRule(doc, &out)
	}

	out.Changed = len(out.Repairs()) > 0 || len(out.Quarantines()) > 0
	return out
}

// Stamp records what the policy did inside the deliverable, so a bad emitter
// stays visible to a human reading the file and to anything that mines it. It
// is a no-op when the policy changed nothing, so a healthy deliverable never
// grows a marker.
func (o PolicyOutcome) Stamp(now time.Time) {
	if o.Doc == nil || !o.Changed {
		return
	}
	repairs := make([]any, 0, len(o.Notes))
	quarantined := make([]any, 0, len(o.Notes))
	for _, n := range o.Notes {
		entry := map[string]any{"field": n.Field, "rule": n.Rule, "detail": n.Detail}
		switch n.Disposition {
		case DispositionRepaired:
			repairs = append(repairs, entry)
		case DispositionQuarantined:
			quarantined = append(quarantined, entry)
		}
	}
	untrustworthy := make([]any, 0)
	for _, f := range o.Untrustworthy() {
		untrustworthy = append(untrustworthy, f)
	}
	o.Doc[PolicyMarkerField] = map[string]any{
		"policy_version": PolicyVersion,
		"applied_at":     now.UTC().Format(time.RFC3339),
		"repairs":        repairs,
		"quarantined":    quarantined,
		"untrustworthy":  untrustworthy,
	}
}

// ---------------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------------

// applySchemaVersionRule stamps the contract version this binary implements
// over whatever the skill claimed (#1177).
func applySchemaVersionRule(stage string, doc map[string]any, out *PolicyOutcome) {
	canonical, known := canonicalSchemaVersion[stage]
	if !known {
		return
	}
	claimed, _ := doc["schema_version"].(string)
	if claimed == canonical {
		return
	}
	doc["schema_version"] = canonical
	detail := fmt.Sprintf("stage claimed %q; the contract this binary implements is %q", claimed, canonical)
	if claimed == "" {
		detail = fmt.Sprintf("no schema_version was written; stamped %q", canonical)
	}
	out.Notes = append(out.Notes, Note{
		Field:       "schema_version",
		Disposition: DispositionRepaired,
		Rule:        "any.schema_version.stamped",
		Detail:      detail,
	})
}

// applyDevFilesChangedRule repairs the #1176 defect: `files_changed` written as
// a flat array of paths alongside sibling `files_created` / `files_modified` /
// `files_deleted` keys carrying exactly the values the schema wants.
//
// The repair is allowed only when it is provably TOTAL. The array and the union
// of the siblings must name the same set of paths — not a subset either way. If
// the array holds a path the siblings do not classify, the created/modified
// split for that path is genuinely unknown and inventing one is exactly the
// inference this policy forbids; if the siblings claim a path the array does
// not, the deliverable contradicts itself. Both are fatal.
func applyDevFilesChangedRule(doc map[string]any, out *PolicyOutcome) {
	raw, present := doc["files_changed"]
	if !present {
		return
	}
	arr, isArray := raw.([]any)
	if !isArray {
		return // object (canonical), or null — leave it to the schema
	}

	declared, ok := stringSet(arr)
	if !ok {
		out.Notes = append(out.Notes, Note{
			Field:       "files_changed",
			Disposition: DispositionFatal,
			Rule:        "dev.files_changed.non_string_entries",
			Detail:      "files_changed is an array containing non-string entries; no rule maps it to {created, modified, deleted}",
		})
		return
	}

	created, hasCreated := stringArrayField(doc, "files_created")
	modified, hasModified := stringArrayField(doc, "files_modified")
	deleted, hasDeleted := stringArrayField(doc, "files_deleted")
	if !hasCreated && !hasModified && !hasDeleted {
		out.Notes = append(out.Notes, Note{
			Field:       "files_changed",
			Disposition: DispositionFatal,
			Rule:        "dev.files_changed.no_sibling_manifest",
			Detail:      "files_changed is a flat array and the deliverable carries no files_created/files_modified/files_deleted to classify it; the created/modified split is genuinely absent",
		})
		return
	}

	union := map[string]bool{}
	for _, group := range [][]string{created, modified, deleted} {
		for _, p := range group {
			union[p] = true
		}
	}

	var unclassified, unclaimed []string
	for p := range declared {
		if !union[p] {
			unclassified = append(unclassified, p)
		}
	}
	for p := range union {
		if !declared[p] {
			unclaimed = append(unclaimed, p)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(unclaimed)

	if len(unclassified) > 0 || len(unclaimed) > 0 {
		var parts []string
		if len(unclassified) > 0 {
			parts = append(parts, "not classified by any sibling key: "+strings.Join(unclassified, ", "))
		}
		if len(unclaimed) > 0 {
			parts = append(parts, "claimed by a sibling key but absent from files_changed: "+strings.Join(unclaimed, ", "))
		}
		out.Notes = append(out.Notes, Note{
			Field:       "files_changed",
			Disposition: DispositionFatal,
			Rule:        "dev.files_changed.sibling_manifest_incomplete",
			Detail:      "the sibling manifest does not account for files_changed exactly, so no total repair exists (" + strings.Join(parts, "; ") + ")",
		})
		return
	}

	doc["files_changed"] = map[string]any{
		"created":  toAnySlice(created),
		"modified": toAnySlice(modified),
		"deleted":  toAnySlice(deleted),
	}
	delete(doc, "files_created")
	delete(doc, "files_modified")
	delete(doc, "files_deleted")
	out.Notes = append(out.Notes, Note{
		Field:       "files_changed",
		Disposition: DispositionRepaired,
		Rule:        "dev.files_changed.from_sibling_manifest",
		Detail: fmt.Sprintf(
			"rebuilt {created, modified, deleted} from files_created/files_modified/files_deleted; every one of the %d paths in the array was accounted for exactly once",
			len(declared)),
	})
}

// qualityCheckVocabulary is the closed synonym table for the four
// `quality_checks` verdicts. Each entry is a rename of a value that means the
// same thing, never a downgrade or an interpretation of a different outcome —
// "mostly fine" has no entry and never will.
var qualityCheckVocabulary = map[string]string{
	"not run":        "not_run",
	"not-run":        "not_run",
	"notrun":         "not_run",
	"not_applicable": "not_run",
	"not applicable": "not_run",
	"n/a":            "not_run",
	"na":             "not_run",
	"none":           "not_run",
	"pass":           "passed",
	"passing":        "passed",
	"ok":             "passed",
	"fail":           "failed",
	"failing":        "failed",
	"skip":           "skipped",
}

var qualityCheckCanonical = map[string]bool{
	"passed": true, "failed": true, "skipped": true, "not_run": true,
}

var qualityCheckFields = []string{"code_standards", "security_review", "type_check", "dead_code_scan"}

// applyQualityChecksRule normalises the `quality_checks` verdict vocabulary.
//
// A value outside the closed table is QUARANTINED, not guessed at: the field is
// removed and `quality_checks` is marked untrustworthy. Passing an unrecognised
// verdict through is what let `dev-170` and `dev-340` ship with a quality signal
// nothing could read.
func applyQualityChecksRule(doc map[string]any, out *PolicyOutcome) {
	qcRaw, present := doc["quality_checks"]
	if !present {
		return
	}
	qc, ok := qcRaw.(map[string]any)
	if !ok {
		return
	}
	for _, field := range qualityCheckFields {
		v, has := qc[field]
		if !has || v == nil {
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(s))
		if qualityCheckCanonical[normalized] {
			if normalized != s {
				qc[field] = normalized
				out.Notes = append(out.Notes, Note{
					Field:       "quality_checks." + field,
					Disposition: DispositionRepaired,
					Rule:        "any.quality_checks.vocabulary",
					Detail:      fmt.Sprintf("%q normalised to %q", s, normalized),
				})
			}
			continue
		}
		if canonical, mapped := qualityCheckVocabulary[normalized]; mapped {
			qc[field] = canonical
			out.Notes = append(out.Notes, Note{
				Field:       "quality_checks." + field,
				Disposition: DispositionRepaired,
				Rule:        "any.quality_checks.vocabulary",
				Detail:      fmt.Sprintf("%q is a known synonym of %q", s, canonical),
			})
			continue
		}
		delete(qc, field)
		out.Notes = append(out.Notes, Note{
			Field:       "quality_checks." + field,
			Disposition: DispositionQuarantined,
			Rule:        "any.quality_checks.unknown_vocabulary",
			Detail:      fmt.Sprintf("%q is not a quality-check verdict and no closed rule maps it; the field was dropped rather than reported as a verdict it does not mean", s),
		})
	}
}

// applySkippedPhasesRule quarantines `skipped_phases` entries that are not
// {phase, reason} objects.
//
// A bare string names the phase and omits the reason. The reason is the entire
// point of the field — it is what a human reads to decide whether the skip was
// legitimate — and there is no total repair for a value that was never written.
// So the entry is dropped and `skipped_phases` is marked untrustworthy, rather
// than being back-filled with an invented reason or forwarded malformed.
func applySkippedPhasesRule(doc map[string]any, out *PolicyOutcome) {
	raw, present := doc["skipped_phases"]
	if !present {
		return
	}
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(arr))
	for i, entry := range arr {
		obj, isObj := entry.(map[string]any)
		if !isObj {
			out.Notes = append(out.Notes, Note{
				Field:       fmt.Sprintf("skipped_phases.%d", i),
				Disposition: DispositionQuarantined,
				Rule:        "validate.skipped_phases.entry_not_an_object",
				Detail:      fmt.Sprintf("entry is %s, not {phase, reason}; the reason was never written and cannot be invented", jsonShape(entry)),
			})
			continue
		}
		phase, _ := obj["phase"].(string)
		reason, _ := obj["reason"].(string)
		if strings.TrimSpace(phase) == "" || strings.TrimSpace(reason) == "" {
			out.Notes = append(out.Notes, Note{
				Field:       fmt.Sprintf("skipped_phases.%d", i),
				Disposition: DispositionQuarantined,
				Rule:        "validate.skipped_phases.incomplete_entry",
				Detail:      "entry lacks a non-empty phase or reason; a skip with no stated reason is not evidence of anything",
			})
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) != len(arr) {
		doc["skipped_phases"] = kept
	}
}

// gateMetricResultVocabulary is the closed synonym table for `gate_metrics[].result`.
//
// The canonical set is {pass, catch, fail} — the SAME set the gate-metrics
// record accepts. The deliverable schema used to accept only {pass, catch}
// while the record it feeds accepted {pass, catch, fail}, so a legitimate
// adversarial-judge "fail" was reported as an invalid option. That drift, not
// the emission, was the defect in `validate-340`; the two enums are now one.
var gateMetricResultVocabulary = map[string]string{
	"passed":  "pass",
	"passing": "pass",
	"ok":      "pass",
	"failed":  "fail",
	"failing": "fail",
	"caught":  "catch",
}

var gateMetricResultCanonical = map[string]bool{"pass": true, "catch": true, "fail": true}

// applyGateMetricsRule repairs the result vocabulary and quarantines entries
// with no gate name.
//
// A gate metric with no `gate_name` is unattributable: the record it becomes
// tallies hit-rates BY gate, so an entry with no name cannot be counted and
// cannot be excluded from a count either. It is dropped and `gate_metrics` is
// marked untrustworthy — which is the whole point of #1182's third acceptance
// criterion. Forwarding it, as the warn-and-continue path did, put a nameless
// row in front of the gate-metrics tally and the learning corpus with nothing
// but a log line behind it.
func applyGateMetricsRule(doc map[string]any, out *PolicyOutcome) {
	raw, present := doc["gate_metrics"]
	if !present {
		return
	}
	arr, ok := raw.([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(arr))
	for i, entry := range arr {
		obj, isObj := entry.(map[string]any)
		if !isObj {
			out.Notes = append(out.Notes, Note{
				Field:       fmt.Sprintf("gate_metrics.%d", i),
				Disposition: DispositionQuarantined,
				Rule:        "validate.gate_metrics.entry_not_an_object",
				Detail:      fmt.Sprintf("entry is %s, not a gate metric", jsonShape(entry)),
			})
			continue
		}
		name, _ := obj["gate_name"].(string)
		if strings.TrimSpace(name) == "" {
			out.Notes = append(out.Notes, Note{
				Field:       fmt.Sprintf("gate_metrics.%d", i),
				Disposition: DispositionQuarantined,
				Rule:        "validate.gate_metrics.missing_gate_name",
				Detail:      "entry has no gate_name; a gate-metrics record is tallied BY gate, so a nameless row can neither be counted nor excluded",
			})
			continue
		}

		if resRaw, has := obj["result"]; has {
			res, isStr := resRaw.(string)
			normalized := strings.ToLower(strings.TrimSpace(res))
			switch {
			case !isStr:
				out.Notes = append(out.Notes, Note{
					Field:       fmt.Sprintf("gate_metrics.%d.result", i),
					Disposition: DispositionQuarantined,
					Rule:        "validate.gate_metrics.result_not_a_string",
					Detail:      fmt.Sprintf("result is %s", jsonShape(resRaw)),
				})
				continue
			case gateMetricResultCanonical[normalized]:
				if normalized != res {
					obj["result"] = normalized
					out.Notes = append(out.Notes, Note{
						Field:       fmt.Sprintf("gate_metrics.%d.result", i),
						Disposition: DispositionRepaired,
						Rule:        "validate.gate_metrics.result_vocabulary",
						Detail:      fmt.Sprintf("%q normalised to %q", res, normalized),
					})
				}
			default:
				canonical, mapped := gateMetricResultVocabulary[normalized]
				if !mapped {
					out.Notes = append(out.Notes, Note{
						Field:       fmt.Sprintf("gate_metrics.%d.result", i),
						Disposition: DispositionQuarantined,
						Rule:        "validate.gate_metrics.unknown_result",
						Detail:      fmt.Sprintf("%q is not a gate result and no closed rule maps it", res),
					})
					continue
				}
				obj["result"] = canonical
				out.Notes = append(out.Notes, Note{
					Field:       fmt.Sprintf("gate_metrics.%d.result", i),
					Disposition: DispositionRepaired,
					Rule:        "validate.gate_metrics.result_vocabulary",
					Detail:      fmt.Sprintf("%q is a known synonym of %q", res, canonical),
				})
			}
		}
		kept = append(kept, obj)
	}
	if len(kept) != len(arr) {
		doc["gate_metrics"] = kept
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func jsonShape(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "an object"
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	}
	return "a value of an unexpected type"
}

func stringSet(arr []any) (map[string]bool, bool) {
	out := make(map[string]bool, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		out[s] = true
	}
	return out, true
}

// stringArrayField reads a sibling manifest key. A key that is present but not
// an array of strings counts as absent — the rule then has nothing total to
// build from and the caller reports the fatal.
func stringArrayField(doc map[string]any, key string) ([]string, bool) {
	raw, present := doc[key]
	if !present || raw == nil {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, isStr := v.(string)
		if !isStr {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func cloneObject(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneObject(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneValue(e)
		}
		return out
	}
	return v
}
