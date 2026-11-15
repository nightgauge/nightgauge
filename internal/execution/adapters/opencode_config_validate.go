package adapters

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// openCodeLegacyKeys are the 1.x top-level keys the per-run document used to
// carry. 2.x translates some and drops others without a word (spike #3197
// § 2), so a document holding one is refused by name rather than trusted to a
// translation nobody re-observes.
var openCodeLegacyKeys = []string{
	"agent", "autoupdate", "enabled_providers", "disabled_providers", "mode",
	"permission", "plugin", "provider", "small_model", "snapshot", "subagent_depth",
}

// openCodePinnedAgents are the built-in agents every document names, each with
// the dispatched model.
var openCodePinnedAgents = []string{"build", "plan", "general", "explore", "title", "summary", "compaction"}

// validateOpenCodeConfigDocument refuses a per-run config document that
// OpenCode 2.x would load with a control missing. 2.2.7 drops an unknown key,
// an untranslated 1.x key and a value of the wrong type and exits 0
// (`{"model":5}` loads as an empty document), and `debug config` no longer
// prints the merged config, so no probe of the binary catches a dropped key:
// this check is the gate, and it runs on every document before dispatch
// (BuildOpenCodeConfig, then addNightgaugePluginToConfig).
//
// It refuses: text that is not one JSON object; a 1.x key; any key the 2.x
// document type does not declare, at any depth it types; a value of the wrong
// type; a rule effect other than allow or deny (never ask, ADR-022 § 9);
// share other than "disabled" or update other than "disable"; policies that
// do not deny provider.use for "*" first and allow the dispatched provider; a
// missing pinned agent or one whose
// model is not the dispatched model; a subagent-mode agent without the
// subagent deny; and an empty permissions list.
func validateOpenCodeConfigDocument(content string) error {
	wrap := func(err error) error {
		return fmt.Errorf("opencode config: refused, the per-run document would load on OpenCode 2.x with a control missing: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &top); err != nil {
		return wrap(fmt.Errorf("not a JSON object: %w", err))
	}
	for _, key := range openCodeLegacyKeys {
		if _, ok := top[key]; ok {
			return wrap(fmt.Errorf("1.x key %q, which 2.x translates or drops without a word", key))
		}
	}
	// json.Unmarshal above has already refused trailing data.
	dec := json.NewDecoder(strings.NewReader(content))
	dec.DisallowUnknownFields()
	var doc openCodeConfigJSON
	if err := dec.Decode(&doc); err != nil {
		return wrap(err)
	}
	// A null decodes to the zero value without an error; the checks below
	// catch every key whose zero value is unsafe.
	for key, raw := range top {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return wrap(fmt.Errorf("%q is null", key))
		}
	}

	if doc.Model.ProviderID == "" || doc.Model.Model == "" {
		return wrap(errors.New("model must name a providerID and a model"))
	}
	if doc.DefaultAgent != openCodeDefaultAgent {
		return wrap(fmt.Errorf("default_agent is %q, not build", doc.DefaultAgent))
	}
	if doc.Share != openCodeShareDisabled {
		return wrap(fmt.Errorf("share is %q, not %q", doc.Share, openCodeShareDisabled))
	}
	if doc.Update != openCodeUpdateDisable {
		return wrap(fmt.Errorf("update is %q, not %q", doc.Update, openCodeUpdateDisable))
	}
	if err := openCodeCheckRules("policies", doc.Policies); err != nil {
		return wrap(err)
	}
	if len(doc.Policies) == 0 || doc.Policies[0] != (openCodeRuleJSON{Action: openCodeProviderUse, Resource: openCodeAnyResource, Effect: openCodeDeny}) {
		return wrap(errors.New(`policies must open with provider.use denied for "*"`))
	}
	// 2.x applies the last matching rule, so any other rule could reopen a
	// provider the deny closed: the list is exactly deny "*", allow the key.
	if !slices.Equal(doc.Policies, openCodeProviderPolicies(doc.Model.ProviderID)) {
		return wrap(fmt.Errorf("policies must deny provider.use for \"*\" and allow the dispatched provider %q alone, nothing else", doc.Model.ProviderID))
	}
	if len(doc.Permissions) == 0 {
		return wrap(errors.New("permissions is empty"))
	}
	if err := openCodeCheckRules("permissions", doc.Permissions); err != nil {
		return wrap(err)
	}
	for _, name := range openCodePinnedAgents {
		agent, ok := doc.Agents[name]
		if !ok {
			return wrap(fmt.Errorf("agents.%s is missing", name))
		}
		if agent.Model != doc.Model {
			return wrap(fmt.Errorf("agents.%s.model is %s/%s, not the dispatched %s/%s",
				name, agent.Model.ProviderID, agent.Model.Model, doc.Model.ProviderID, doc.Model.Model))
		}
	}
	for name, agent := range doc.Agents {
		if err := openCodeCheckRules("agents."+name+".permissions", agent.Permissions); err != nil {
			return wrap(err)
		}
	}
	for _, name := range openCodeSubagentAgents {
		if !openCodeDeniesSubagent(doc.Agents[name].Permissions) {
			return wrap(fmt.Errorf("agents.%s does not deny subagent, so a subagent could start another (2.x has no subagent_depth)", name))
		}
	}
	for i, p := range doc.Plugins {
		if p == "" {
			return wrap(fmt.Errorf("plugins[%d] is empty", i))
		}
	}
	return nil
}

// openCodeCheckRules refuses a rule with an empty action or resource, or an
// effect other than allow or deny: ask would prompt a human nobody is there to
// be (ADR-022 § 9).
func openCodeCheckRules(where string, rules []openCodeRuleJSON) error {
	for i, r := range rules {
		if r.Action == "" || r.Resource == "" {
			return fmt.Errorf("%s[%d] has an empty action or resource", where, i)
		}
		if r.Effect != openCodeAllow && r.Effect != openCodeDeny {
			return fmt.Errorf("%s[%d] (%s %s) has effect %q; only allow and deny are written, never ask", where, i, r.Action, r.Resource, r.Effect)
		}
	}
	return nil
}

// openCodeDeniesSubagent reports whether rules end denying the subagent
// action for every resource. 2.x applies the last matching rule, so a later
// allow reopens it, for every resource ("*") or for one subagent.
func openCodeDeniesSubagent(rules []openCodeRuleJSON) bool {
	denied := false
	for _, r := range rules {
		if r.Action != openCodeSubagentAction && r.Action != openCodeAnyResource {
			continue
		}
		switch {
		case r.Resource == openCodeAnyResource:
			denied = r.Effect == openCodeDeny
		case r.Effect == openCodeAllow:
			denied = false
		}
	}
	return denied
}
