package spike

import (
	"strings"
	"testing"
)

// validBody returns a complete spike issue body that passes all ValidateBody checks.
func validBody(path string) string {
	return `## Spike Contract (Path A)

**Artifact**: ` + "`" + path + "`" + `

Some description of the spike.

` + "```" + `yaml recommendations
spike: 1
recommendations:
  - id: implement-feature
    action: adopt
    title: "Implement the feature"
    type: feature
    priority: high
    size: M
` + "```" + `
`
}

func TestValidateBody_MissingYamlBlock(t *testing.T) {
	body := "## Spike Contract (Path A)\n\nSome text without yaml block.\n\ndocs/spikes/1-some-spike.md\n"
	err := ValidateBody(body)
	if err == nil {
		t.Fatal("expected error for missing yaml block, got nil")
	}
	if !strings.Contains(err.Error(), "missing a fenced") {
		t.Errorf("expected 'missing a fenced' in error, got: %v", err)
	}
}

func TestValidateBody_PresentYamlBlock_MissingSpikeField(t *testing.T) {
	body := `## Spike Contract (Path A)

docs/spikes/1-some-spike.md

` + "```" + `yaml recommendations
recommendations:
  - id: some-rec
    action: adopt
    title: "Some rec"
    type: feature
    priority: high
    size: M
` + "```" + `
`
	err := ValidateBody(body)
	if err == nil {
		t.Fatal("expected error for missing spike field, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation") {
		t.Errorf("expected schema validation error, got: %v", err)
	}
}

func TestValidateBody_InvalidActionValue(t *testing.T) {
	body := `## Spike Contract (Path A)

docs/spikes/1-some-spike.md

` + "```" + `yaml recommendations
spike: 1
recommendations:
  - id: some-rec
    action: invalid-action
    title: "Some rec"
    type: feature
    priority: high
    size: M
` + "```" + `
`
	err := ValidateBody(body)
	if err == nil {
		t.Fatal("expected error for invalid action, got nil")
	}
	if !strings.Contains(err.Error(), "schema validation") {
		t.Errorf("expected schema validation error, got: %v", err)
	}
}

func TestValidateBody_ValidYaml_MissingPathDeclaration(t *testing.T) {
	body := `Some body without a path declaration heading.

docs/spikes/1-some-spike.md

` + "```" + `yaml recommendations
spike: 1
recommendations:
  - id: some-rec
    action: adopt
    title: "Some rec"
    type: feature
    priority: high
    size: M
` + "```" + `
`
	err := ValidateBody(body)
	if err == nil {
		t.Fatal("expected error for missing path declaration, got nil")
	}
	if !strings.Contains(err.Error(), "path declaration") {
		t.Errorf("expected 'path declaration' in error, got: %v", err)
	}
}

func TestValidateBody_ValidYaml_PathDeclaration_MissingArtifactPath(t *testing.T) {
	body := `## Spike Contract (Path B)

No artifact path reference here.

` + "```" + `yaml recommendations
spike: 1
recommendations:
  - id: some-rec
    action: adopt
    title: "Some rec"
    type: feature
    priority: high
    size: M
` + "```" + `
`
	err := ValidateBody(body)
	if err == nil {
		t.Fatal("expected error for missing artifact path, got nil")
	}
	if !strings.Contains(err.Error(), "artifact path") {
		t.Errorf("expected 'artifact path' in error, got: %v", err)
	}
}

func TestValidateBody_PathA_Success(t *testing.T) {
	body := validBody("docs/spikes/42-evaluate-caching.md")
	if err := ValidateBody(body); err != nil {
		t.Errorf("expected nil for valid body (Path A), got: %v", err)
	}
}

func TestValidateBody_PathB_Success(t *testing.T) {
	body := strings.Replace(validBody("docs/spikes/42-evaluate-caching.md"), "Path A", "Path B", 1)
	if err := ValidateBody(body); err != nil {
		t.Errorf("expected nil for valid body (Path B), got: %v", err)
	}
}

func TestValidateBody_PathC_Success(t *testing.T) {
	body := strings.Replace(validBody("docs/decisions/042-workspace-data-model.md"), "Path A", "Path C", 1)
	if err := ValidateBody(body); err != nil {
		t.Errorf("expected nil for valid body (Path C, decisions path), got: %v", err)
	}
}
