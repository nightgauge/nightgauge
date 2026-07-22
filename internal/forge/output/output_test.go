package output

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type sampleDTO struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	hidden string //nolint:unused
}

type humanDTO struct {
	Name string `json:"name"`
}

func (h humanDTO) RenderHuman(w io.Writer) error {
	_, err := io.WriteString(w, "custom: "+h.Name+"\n")
	return err
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		jsonFlag bool
		tpl      string
		want     Mode
	}{
		{"default human", false, "", ModeHuman},
		{"json flag", true, "", ModeJSON},
		{"template wins over json", true, "{{.Title}}", ModeTemplate},
		{"template alone", false, "{{.Title}}", ModeTemplate},
		{"whitespace template treated as empty", false, "   ", ModeHuman},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.jsonFlag, tt.tpl)
			if got != tt.want {
				t.Errorf("Resolve(%v, %q) = %q, want %q", tt.jsonFlag, tt.tpl, got, tt.want)
			}
		})
	}
}

func TestRender_JSON(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{Number: 42, Title: "hello"}, ModeJSON, "", &buf)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"number": 42`) {
		t.Errorf("missing number field: %q", out)
	}
	if !strings.Contains(out, `"title": "hello"`) {
		t.Errorf("missing title field: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("output should end with newline: %q", out)
	}
}

func TestRender_Template(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{Number: 7, Title: "issue"}, ModeTemplate, "#{{.Number}}: {{.Title}}", &buf)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got, want := buf.String(), "#7: issue"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRender_Template_EmptyFails(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{}, ModeTemplate, "", &buf)
	if err == nil {
		t.Fatal("expected error for empty template")
	}
}

func TestRender_Template_ParseError(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{}, ModeTemplate, "{{.Number", &buf)
	if err == nil {
		t.Fatal("expected parse error for unclosed action")
	}
}

func TestRender_Human_Custom(t *testing.T) {
	var buf bytes.Buffer
	err := Render(humanDTO{Name: "alice"}, ModeHuman, "", &buf)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := buf.String(); got != "custom: alice\n" {
		t.Errorf("got %q, want custom: alice", got)
	}
}

func TestRender_Human_Generic(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{Number: 3, Title: "hi"}, ModeHuman, "", &buf)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "number:\t3") {
		t.Errorf("expected number row, got %q", out)
	}
	if !strings.Contains(out, "title:\thi") {
		t.Errorf("expected title row, got %q", out)
	}
}

func TestRender_Human_Pointer(t *testing.T) {
	var buf bytes.Buffer
	dto := &sampleDTO{Number: 9, Title: "ptr"}
	if err := Render(dto, ModeHuman, "", &buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "title:\tptr") {
		t.Errorf("pointer dereference failed: %q", buf.String())
	}
}

func TestRender_UnknownMode(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{}, Mode("xml"), "", &buf)
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestRender_DefaultModeIsJSON(t *testing.T) {
	var buf bytes.Buffer
	err := Render(sampleDTO{Number: 1, Title: "x"}, "", "", &buf)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), `"number": 1`) {
		t.Errorf("default mode should marshal JSON, got: %q", buf.String())
	}
}
