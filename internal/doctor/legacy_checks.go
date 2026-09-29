package doctor

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// Legacy check adapter (#2088). Every check here still produces a CheckItem
// plus the free-text errors and warnings the pre-registry doctor emitted; the
// adapter wraps that into Findings at the registry boundary. The checks are in
// one block per owning migration sub-issue, so each sibling deletes only its
// own block when it converts those checks to native findings, and the four can
// merge in any order. The removal chore (#2098) deletes this file.
//
// Severity rule for adapted checks: a legacy error is a blocker and a legacy
// warning is a warning, except that a check ADR-025 classifies as housekeeping
// or info reports at that severity, so cleanup items and context stop turning
// the status "degraded". A check that is not OK but emitted no message reports
// an info finding, so it never reads as healthy.

// legacyOutcome is what a pre-registry check computed.
type legacyOutcome struct {
	item     CheckItem
	present  bool     // false: the check wrote no row (not applicable here)
	errors   []string // blocking messages
	warnings []string // non-blocking messages
}

func legacyRow(item CheckItem) legacyOutcome { return legacyOutcome{item: item, present: true} }

// rowWarn is the common (CheckItem, warning) shape.
func rowWarn(item CheckItem, warning string) legacyOutcome {
	o := legacyRow(item)
	if warning != "" {
		o.warnings = []string{warning}
	}
	return o
}

// builtinChecks is filled by the blocks below, in source order.
var builtinChecks []Check

// legacy registers a pre-registry check through the adapter.
func legacy(id, title, group, code string, adr Severity, timeout time.Duration, deps []string,
	fn func(ctx context.Context, env *Env) legacyOutcome) {
	builtinChecks = append(builtinChecks, Check{
		ID: id, Title: title, Group: group, Code: code, Timeout: timeout, DependsOn: deps,
		Run: func(ctx context.Context, env *Env) []Finding {
			return adaptLegacy(id, code, adr, env, fn(ctx, env))
		},
	})
}

func adaptLegacy(id, code string, adr Severity, env *Env, out legacyOutcome) []Finding {
	if !out.present {
		env.SetDetail(id, "not applicable in this workspace")
		return nil
	}
	env.recordItem(id, out.item)
	detail := out.item.Detail
	if detail == "" && out.item.OK {
		detail = "ok"
	}
	env.SetDetail(id, detail)

	capSeverity := func(s Severity) Severity {
		if adr == SeverityHousekeeping || adr == SeverityInfo {
			return adr
		}
		return s
	}
	var findings []Finding
	add := func(sev Severity, msg string) {
		cause := out.item.Error
		if cause == "" || cause == msg {
			cause = out.item.Detail
		}
		ev := map[string]string{}
		if out.item.Detail != "" {
			ev["detail"] = out.item.Detail
		}
		for i, sf := range out.item.Findings {
			ev["hit."+strconv.Itoa(i)] = fmt.Sprintf("%s:%d %s %s", sf.Path, sf.Line, sf.Pattern, sf.Redacted)
		}
		findings = append(findings, Finding{
			Code:        code,
			Check:       id,
			Severity:    sev,
			Title:       msg,
			Cause:       cause,
			Evidence:    ev,
			Docs:        DocsAnchor(code),
			Fingerprint: Fingerprint(code, id, string(sev), strconv.Itoa(len(findings))),
			Remedies:    []Remedy{},
		})
	}
	for _, e := range out.errors {
		add(capSeverity(SeverityBlocker), e)
	}
	for _, w := range out.warnings {
		add(capSeverity(SeverityWarning), w)
	}
	if len(findings) == 0 && !out.item.OK {
		msg := out.item.Error
		if msg == "" {
			msg = out.item.Detail
		}
		if msg == "" {
			msg = id + " did not pass"
		}
		add(SeverityInfo, msg)
	}
	return findings
}

// recordItem keeps the legacy CheckItem for in-process callers until #2098
// deletes CheckItem.
func (e *Env) recordItem(id string, item CheckItem) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.items == nil {
		e.items = map[string]CheckItem{}
	}
	e.items[id] = item
}

// ---------------------------------------------------------------------------
// #2092 — adapter health
// ---------------------------------------------------------------------------

func init() {
	// Per-adapter health, only when --adapters names adapters. An
	// unhealthy adapter is a warning, never a blocker.
	legacy("adapters", "Adapter health", "adapters", "NGD100", SeverityWarning, 30*time.Second, nil,
		func(ctx context.Context, env *Env) legacyOutcome {
			if len(env.Adapters) == 0 {
				return legacyOutcome{}
			}
			health := CheckAdapters(env.Adapters)
			env.setAdapters(health)
			o := legacyRow(CheckItem{OK: true, Detail: fmt.Sprintf("%d adapter(s) checked", len(health))})
			for _, a := range health {
				if !a.OK {
					detail := a.Remediation
					if detail == "" {
						detail = "adapter not ready"
					}
					o.warnings = append(o.warnings, fmt.Sprintf("adapter %q not ready: %s", a.Adapter, detail))
				}
				for _, w := range a.Warnings {
					o.warnings = append(o.warnings, fmt.Sprintf("adapter %q: %s", a.Adapter, w))
				}
			}
			return o
		})
}
