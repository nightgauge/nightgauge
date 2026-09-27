package forge

import (
	"fmt"
	"strings"
)

// Pipeline PR stamp (#1479). Every pull / merge request the pipeline opens
// carries a deterministic HTML-comment footer and the PipelineCreatedLabel, so
// pipeline-driven merges can be told apart from interactively-authored ones.
//
// The format is a stability contract documented in docs/PR_CREATE_STAGE.md
// § Pipeline stamp: external tooling (the program office) greps for
// PipelineMarkerPrefix and filters on the label. Change neither without a
// deprecation window.
const (
	// PipelineCreatedLabel is applied to every pipeline-created PR/MR.
	PipelineCreatedLabel = "pipeline:created"
	// PipelineCreatedLabelColor / Description are used when the label has
	// to be created on first use.
	PipelineCreatedLabelColor       = "5319e7"
	PipelineCreatedLabelDescription = "Pull request opened by the Nightgauge pipeline"

	// PipelineMarkerPrefix opens the footer comment. Detection matches on it.
	PipelineMarkerPrefix = "<!-- nightgauge:pipeline "
	// PipelineMarkerUnknownRun stands in for run= when no run id is known.
	PipelineMarkerUnknownRun = "-"
)

// PipelineMarker renders the footer line (without trailing newline):
//
//	<!-- nightgauge:pipeline stage=<stage> issue=<N> run=<id> -->
//
// Empty stage or run render as "-"; whitespace inside values is replaced
// with "_" so the key=value grammar stays unambiguous.
func PipelineMarker(stage string, issue int, run string) string {
	return fmt.Sprintf("%sstage=%s issue=%d run=%s -->",
		PipelineMarkerPrefix, markerValue(stage), issue, markerValue(run))
}

// HasPipelineMarker reports whether body carries a pipeline stamp footer.
func HasPipelineMarker(body string) bool {
	return strings.Contains(body, PipelineMarkerPrefix)
}

func markerValue(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return PipelineMarkerUnknownRun
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(v, "--", "_")), "_")
}
