package forgetypes

import pkgtypes "github.com/nightgauge/nightgauge/pkg/types"

// Issue is a forge-agnostic issue with sub-issue and blocking relationships.
// Aliased from pkg/types.Issue so existing callers continue to compile.
type Issue = pkgtypes.Issue

// SubIssueRef is a lightweight reference to a sub-issue.
type SubIssueRef = pkgtypes.SubIssueRef

// BlockingRef is a lightweight reference to a blocking/blockedBy issue.
type BlockingRef = pkgtypes.BlockingRef

// EpicProgress aggregates sub-issue progress for an epic across repos.
type EpicProgress = pkgtypes.EpicProgress

// Priority represents issue priority levels (P0–P3).
type Priority = pkgtypes.Priority

// Size represents issue size labels (XS, S, M, L, XL).
type Size = pkgtypes.Size
