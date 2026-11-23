package stages

import (
	"regexp"
	"strings"
)

var (
	deliveryOrderHeadingRE = regexp.MustCompile(`^(#{1,6})[ \t]+(.*)$`)
	deliveryOrderRefsRE    = regexp.MustCompile(`(?i)\brefs\b`)
)

// DeliveryOrderSaysRefs reports whether the issue body's first
// "Delivery order" section asks each PR to reference the issue with Refs
// (#3510). The section runs from the first heading whose text starts with
// "Delivery order" (case-insensitive) to the next heading of the same or a
// higher level outside a fenced code block, or to the end of the body. Every
// line in it is searched for the whole word "refs"; no step is parsed.
func DeliveryOrderSaysRefs(body string) bool {
	inFence := false
	fenceMarker := ""
	level := 0 // 0: section not found yet
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if marker := fenceOpener(trimmed); marker != "" {
			if !inFence {
				inFence, fenceMarker = true, marker
			} else if strings.HasPrefix(trimmed, fenceMarker) && strings.TrimSpace(strings.TrimLeft(trimmed, fenceMarker[:1])) == "" {
				inFence = false
			}
			if level > 0 && deliveryOrderRefsRE.MatchString(line) {
				return true
			}
			continue
		}
		if !inFence {
			if m := deliveryOrderHeadingRE.FindStringSubmatch(line); m != nil {
				hl := len(m[1])
				if level > 0 && hl <= level {
					return false
				}
				if level == 0 {
					if strings.HasPrefix(strings.ToLower(strings.TrimSpace(m[2])), "delivery order") {
						level = hl
					}
					continue
				}
			}
		}
		if level > 0 && deliveryOrderRefsRE.MatchString(line) {
			return true
		}
	}
	return false
}

// fenceOpener returns the fence marker ("```" or "~~~") a line starts with.
func fenceOpener(trimmed string) string {
	for _, m := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, m) {
			return m
		}
	}
	return ""
}
