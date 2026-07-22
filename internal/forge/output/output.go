// Package output centralises the rendering layer used by every
// `nightgauge forge` subcommand. It supports three modes — JSON
// (default for `--json`), Go text/template (`--template '{{.Field}}'`),
// and a human-readable fallback. The package is forge-agnostic; it does
// not import any service code from internal/forge.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/template"
)

// Mode selects the render strategy.
type Mode string

const (
	// ModeHuman is the tab-separated "key: value" fallback used for
	// interactive shells. The human renderer falls back to JSON for any
	// type that does not implement HumanRenderer.
	ModeHuman Mode = "human"

	// ModeJSON renders v as JSON with two-space indentation followed by a
	// trailing newline. Mirrors `gh ... --json`'s shape so jq pipelines
	// can be reused verbatim.
	ModeJSON Mode = "json"

	// ModeTemplate renders v through a Go text/template. The template
	// string is supplied via --template.
	ModeTemplate Mode = "template"
)

// HumanRenderer is the optional interface a DTO can implement to
// customise the human-readable output. Output.Render falls back to a
// generic key/value dump when the type does not satisfy this.
type HumanRenderer interface {
	RenderHuman(w io.Writer) error
}

// Resolve picks a Mode from CLI flag values. Precedence: --template
// (non-empty) wins over --json; otherwise human.
func Resolve(jsonFlag bool, tpl string) Mode {
	if strings.TrimSpace(tpl) != "" {
		return ModeTemplate
	}
	if jsonFlag {
		return ModeJSON
	}
	return ModeHuman
}

// Render writes v to w in the requested mode. The tpl argument is only
// consulted when mode == ModeTemplate; in other modes it is ignored.
// Returns a wrapped error on failure so callers can use errors.Is /
// errors.As against template.ErrParse, json.UnmarshalTypeError, etc.
func Render(v any, mode Mode, tpl string, w io.Writer) error {
	switch mode {
	case ModeJSON, "":
		return renderJSON(v, w)
	case ModeTemplate:
		return renderTemplate(v, tpl, w)
	case ModeHuman:
		return renderHuman(v, w)
	default:
		return fmt.Errorf("output: unknown mode %q (want json, template, or human)", mode)
	}
}

func renderJSON(v any, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("output: json encode: %w", err)
	}
	return nil
}

func renderTemplate(v any, tpl string, w io.Writer) error {
	if strings.TrimSpace(tpl) == "" {
		return fmt.Errorf("output: --template requires a non-empty template string")
	}
	t, err := template.New("forge").Parse(tpl)
	if err != nil {
		return fmt.Errorf("output: parse template: %w", err)
	}
	if err := t.Execute(w, v); err != nil {
		return fmt.Errorf("output: execute template: %w", err)
	}
	return nil
}

func renderHuman(v any, w io.Writer) error {
	if hr, ok := v.(HumanRenderer); ok {
		return hr.RenderHuman(w)
	}
	return renderHumanGeneric(v, w)
}

// renderHumanGeneric is the fallback dump for DTOs that do not
// implement HumanRenderer. It emits one "name: value" line per
// exported struct field using JSON tags when present. Non-struct
// values are JSON-marshalled as a one-shot fallback.
func renderHumanGeneric(v any, w io.Writer) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			_, err := fmt.Fprintln(w, "(nil)")
			return err
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		// Fall back to JSON for slices / maps / scalars.
		return renderJSON(v, w)
	}
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, ok := f.Tag.Lookup("json"); ok {
			parts := strings.Split(tag, ",")
			if parts[0] != "" && parts[0] != "-" {
				name = parts[0]
			}
		}
		val := rv.Field(i)
		if _, err := fmt.Fprintf(w, "%s:\t%v\n", name, val.Interface()); err != nil {
			return err
		}
	}
	return nil
}
