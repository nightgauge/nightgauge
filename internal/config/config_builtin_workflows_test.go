package config

import "testing"

func TestParseYAMLNestedBuiltinWorkflows(t *testing.T) {
	cfg, err := parseYAMLNested([]byte("owner: o\nrepo: r\nproject:\n  number: 3\n  builtin_workflows:\n    item_closed_to_done: true\n    auto_archive_days: 14\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := cfg.BuiltinWorkflows["item_closed_to_done"].(bool); !ok || !v {
		t.Errorf("item_closed_to_done = %#v", cfg.BuiltinWorkflows["item_closed_to_done"])
	}
	if v, ok := cfg.BuiltinWorkflows["auto_archive_days"].(int); !ok || v != 14 {
		t.Errorf("auto_archive_days = %#v", cfg.BuiltinWorkflows["auto_archive_days"])
	}
	absent, err := parseYAMLNested([]byte("owner: o\nrepo: r\nproject:\n  number: 3\n"))
	if err != nil || absent.BuiltinWorkflows != nil {
		t.Errorf("absent key: %v, %v", absent.BuiltinWorkflows, err)
	}
}
