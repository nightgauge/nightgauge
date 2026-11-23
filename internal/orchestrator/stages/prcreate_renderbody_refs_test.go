package stages

import (
	"strings"
	"testing"
)

func TestRenderBody_DeliveryOrderRefs(t *testing.T) {
	met := func() PRCreateSnapshot {
		s := richSnap()
		s.ACVerdicts = ResolveACVerdicts([]string{"a", "b"}, []ACVerdict{{Index: 0, Text: "a", Status: "met"}, {Index: 1, Text: "b", Status: "met"}}, nil, nil)
		return s
	}
	s := met()
	s.DeliveryOrderRefs = true
	body := RenderBody(s)
	if !strings.Contains(body, "Refs #42") || BodyClosesIssue(body, "", 42) {
		t.Errorf("a Refs delivery order must say Refs #42 with no closing keyword:\n%s", body)
	}
	if !strings.Contains(body, "Delivery order section") {
		t.Errorf("body does not say why it leaves the issue open:\n%s", body)
	}
	body = RenderBody(met())
	if !strings.Contains(body, "Closes #42") {
		t.Errorf("without a Refs delivery order an all-met body must close:\n%s", body)
	}
}
