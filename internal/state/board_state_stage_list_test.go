package state

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestAllPipelineStagesMatchesDeclaredConstants is the #1969 follow-up guard
// AllPipelineStages is a hand-written slice, and a
// hand-written list can silently fall behind a new PipelineStage constant
// the same way the ORIGINAL #1969 bug fell behind a new stage — a defect
// invisible to a reader iterating the list, because the list itself never
// mentions what it is missing.
//
// This test parses board_state.go's own AST rather than trust the slice: it
// walks every top-level `const` block, collects every ValueSpec whose
// declared type is the identifier `PipelineStage`, and asserts the set of
// names matches AllPipelineStages exactly — order-independent, but neither
// list may contain an entry the other lacks.
func TestAllPipelineStagesMatchesDeclaredConstants(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "board_state.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse board_state.go: %v", err)
	}

	declared := map[string]bool{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "PipelineStage" {
				continue
			}
			for _, name := range vs.Names {
				declared[name.Name] = true
			}
		}
	}
	if len(declared) == 0 {
		t.Fatalf("parsed zero PipelineStage constants out of board_state.go — the const block's shape " +
			"changed; fix this parser before trusting it to catch anything")
	}

	// AllPipelineStages holds VALUES (PipelineStage strings), not the
	// constants' Go identifiers, so resolve each declared name to its own
	// value through the package's live constant table below, then compare
	// value sets — the AST walk cannot see which literal a constant name
	// binds to without also parsing every string literal, which the name
	// lookup table already gives us for free and cannot itself drift from
	// the constants (it's the constants).
	nameToValue := map[string]PipelineStage{
		"StageIssuePickup":      StageIssuePickup,
		"StageFeaturePlanning":  StageFeaturePlanning,
		"StageFeatureDev":       StageFeatureDev,
		"StageFeatureValidate":  StageFeatureValidate,
		"StagePRCreate":         StagePRCreate,
		"StagePRMerge":          StagePRMerge,
		"StageSpikeMaterialize": StageSpikeMaterialize,
		"StageIssueRefine":      StageIssueRefine,
	}

	declaredValues := map[PipelineStage]string{}
	for name := range declared {
		val, ok := nameToValue[name]
		if !ok {
			t.Errorf("board_state.go declares PipelineStage constant %q, but this test's own "+
				"nameToValue table does not know it — add it there AND to AllPipelineStages", name)
			continue
		}
		declaredValues[val] = name
	}

	listValues := map[PipelineStage]bool{}
	for _, s := range AllPipelineStages {
		listValues[s] = true
	}

	for val, name := range declaredValues {
		if !listValues[val] {
			t.Errorf("PipelineStage constant %s (%q) is declared in board_state.go but missing from "+
				"AllPipelineStages — exactly the shape of #1969's own bug (spike-materialize existed as "+
				"a constant but was absent from a hand-maintained list a reachability check relied on)",
				name, val)
		}
	}
	for val := range listValues {
		if _, ok := declaredValues[val]; !ok {
			t.Errorf("AllPipelineStages contains %q, which is not a declared PipelineStage constant in "+
				"board_state.go — stale entry (renamed or removed constant)", val)
		}
	}
}
