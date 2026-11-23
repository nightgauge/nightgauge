package acparse

import "testing"

func TestDeliveryOrderSaysRefs(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"list item":            {"## Delivery order\n\n1. Step one. Refs #1.\n", true},
		"unindented wrap":      {"## Delivery order\n\n1. Step one lands first and each PR\nrefs the issue.\n", true},
		"nested item":          {"## Delivery order\n\n- Step one\n  - its PR refs the issue\n", true},
		"after fenced heading": {"## Delivery order\n\n- Step one\n\n```text\n## Other\n```\n\n- Step two refs it\n", true},
		"absent from section":  {"## Delivery order\n\n- Step one prefs references\n\n## Notes\n\nnothing\n", false},
		"only outside":         {"Refs here.\n\n## Delivery order\n\n- Step one\n\n## Notes\n\nEach PR refs #1.\n", false},
		"no heading":           {"Each PR refs #1.\n", false},
		"case insensitive":     {"### DELIVERY ORDER (steps)\n\nREFS #1\n", true},
		"deeper subheading":    {"## Delivery order\n\n### Step 1\n\nrefs #1\n", true},
		"fenced heading first": {"```\n## Delivery order\nrefs\n```\n\n## Delivery order\n\nnone\n", false},
		"heading needs space":  {"##Delivery order\n\nrefs\n", false},
		"fence closes":         {"## Delivery order\n\n```\nx\n```\n\n## Notes\n\nrefs\n", false},
		"tilde fence":          {"## Delivery order\n\n~~~\n## Other\n~~~\n\nrefs\n", true},
		"higher level ends":    {"### Delivery order\n\nnone\n\n## Notes\n\nrefs\n", false},
		"prefix not contains":  {"## Notes on delivery order\n\nrefs\n", false},
		"first section only":   {"## Delivery order\n\nnone\n\n## Delivery order again\n\nrefs\n", false},
		"crlf":                 {"## Delivery order\r\n\r\n- refs #1\r\n", true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := DeliveryOrderSaysRefs(tc.body); got != tc.want {
				t.Errorf("DeliveryOrderSaysRefs = %v, want %v", got, tc.want)
			}
		})
	}
}
