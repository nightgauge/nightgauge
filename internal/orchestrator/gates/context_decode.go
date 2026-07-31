package gates

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// describeDecodeFailure turns a json.Unmarshal error into an operator-facing
// reason and evidence, distinguishing a file that is not JSON from one that is
// valid JSON in the wrong shape (#251).
//
// Both used to report "<subject> is not valid JSON". They have different causes
// and imply different responses: a syntax error usually means the stage was cut
// off mid-write, which points at budget or a crash; a shape mismatch means it
// wrote a complete file against the wrong schema, which a retry generally
// clears. #240 failed with a perfectly well-formed context whose files_changed
// was an array instead of {created, modified, deleted}, and the halt card said
// "not valid JSON" — sending triage after a truncated write that did not exist.
//
// subject names the file in operator terms ("dev context", "planning context").
func describeDecodeFailure(subject string, err error) (string, []string) {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "(top level)"
		}
		return fmt.Sprintf("%s does not match the expected schema", subject),
			[]string{
				fmt.Sprintf("field %q: expected %s, got %s", field, goShapeName(typeErr.Type), typeErr.Value),
				fmt.Sprintf("byte offset %d", typeErr.Offset),
			}
	}

	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Sprintf("%s is not valid JSON", subject),
			[]string{
				syntaxErr.Error(),
				fmt.Sprintf("byte offset %d", syntaxErr.Offset),
			}
	}

	return fmt.Sprintf("%s could not be decoded", subject), []string{err.Error()}
}

// goShapeName renders a Go type as the JSON shape an author would recognise,
// so evidence reads "expected object" rather than dumping a struct definition
// with its field tags — which is what the raw error did, and which tells an
// operator nothing they can act on.
func goShapeName(t reflect.Type) string {
	if t == nil {
		return "a different type"
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "object"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Ptr:
		return goShapeName(t.Elem())
	default:
		return t.String()
	}
}
