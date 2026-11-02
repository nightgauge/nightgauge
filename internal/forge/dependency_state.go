package forge

// Dependency states (#2854). A dependency key names a number, and a number is
// an issue, a pull request or nothing. DepStateOpen and DepStateClosed keep the
// strings a forge reports for an issue, so an issue dependency reads as it
// always has. A pull request has its own values because its outcome is not an
// issue's: merged delivers the work, closed without merging delivers none, and
// reading either as CLOSED would send it through a validation gate that reads
// only issues.
const (
	DepStateOpen     = "OPEN"
	DepStateClosed   = "CLOSED"
	DepStatePROpen   = "PR_OPEN"
	DepStatePRMerged = "PR_MERGED"
	DepStatePRClosed = "PR_CLOSED"
	// DepStateMissing says the forge confirmed that no issue or pull request
	// has the number. An unread number has no state at all; it is never this.
	DepStateMissing = "MISSING"
)
