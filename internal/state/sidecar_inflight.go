package state

// InFlightSidecar reports the issue a current-run.json sidecar at path vouches
// for as in flight (its orchestrator's pid is alive) and that pid, or 0 when
// nothing does. warning is non-empty for a sidecar that exists and cannot be
// parsed. ActiveIssuesFromSnapshots reads every checkout's sidecar at its
// CHECKOUT location; the layout migration uses this to read the ones an older
// build left at a legacy location (ADR-024 § 15).
func InFlightSidecar(path string) (issue, pid int, warning string) {
	return sidecarInFlightIssue(path)
}
