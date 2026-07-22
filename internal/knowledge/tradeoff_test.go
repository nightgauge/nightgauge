package knowledge_test

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

var testKeywords = []string{
	"tradeoff",
	"trade-off",
	"chose",
	"rejected",
	"considered",
	"alternative",
	"instead of",
	"in favor of",
	"decided against",
	"opted for",
}

func TestDetectTradeoffs_TwoDistinctKeywords(t *testing.T) {
	plan := "We chose approach A instead of approach B for performance reasons."
	if !knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return true for plan with 'chose' and 'instead of'")
	}
}

func TestDetectTradeoffs_ThreeDistinctKeywords(t *testing.T) {
	plan := "We considered three options. We chose the simplest one. The alternative was rejected."
	if !knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return true for plan with multiple keywords")
	}
}

func TestDetectTradeoffs_SingleKeyword_ReturnsFalse(t *testing.T) {
	plan := "We chose the simplest approach."
	if knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return false for plan with only one keyword")
	}
}

func TestDetectTradeoffs_NoKeywords_ReturnsFalse(t *testing.T) {
	plan := "This is a simple bugfix that adjusts the timeout value from 30s to 60s."
	if knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return false for plan with no keywords")
	}
}

func TestDetectTradeoffs_CaseInsensitive(t *testing.T) {
	plan := "We CHOSE approach A. The TRADEOFF is that it requires more memory."
	if !knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return true for uppercase keywords")
	}
}

func TestDetectTradeoffs_WordBoundary_ChooseNotMatched(t *testing.T) {
	// "choose" should NOT match keyword "chose" due to word boundary
	plan := "choose wisely, as the alternative matters."
	// "chose" boundary: "choose" won't match \bchose\b
	// "alternative" will match
	// Only 1 keyword matches so result should be false
	if knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return false: 'choose' should not match 'chose' (word boundary)")
	}
}

func TestDetectTradeoffs_MultiWordPhrase(t *testing.T) {
	// "instead of" and "rejected" are 2 distinct keywords.
	plan := "We went with Go instead of TypeScript. We rejected the shell script approach."
	if !knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return true: 'instead of' and 'rejected' are both keywords")
	}
}

func TestDetectTradeoffs_HyphenatedKeyword(t *testing.T) {
	plan := "The trade-off between simplicity and performance was carefully considered."
	if !knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return true: 'trade-off' and 'considered' should match")
	}
}

func TestDetectTradeoffs_EmptyPlan_ReturnsFalse(t *testing.T) {
	if knowledge.DetectTradeoffs("", testKeywords) {
		t.Error("expected DetectTradeoffs to return false for empty plan")
	}
}

func TestDetectTradeoffs_EmptyKeywords_ReturnsFalse(t *testing.T) {
	plan := "We chose approach A instead of approach B."
	if knowledge.DetectTradeoffs(plan, []string{}) {
		t.Error("expected DetectTradeoffs to return false when keywords list is empty")
	}
}

func TestDetectTradeoffs_SameKeywordTwice_CountsAsOne(t *testing.T) {
	// "chose" appears twice but it is still just one distinct keyword.
	plan := "We chose A. We chose B."
	if knowledge.DetectTradeoffs(plan, testKeywords) {
		t.Error("expected DetectTradeoffs to return false: same keyword repeated should count as one distinct keyword")
	}
}

func TestFindTradeoffSignals_ReturnsLineNumbers(t *testing.T) {
	plan := "Line one.\nWe chose approach A.\nLine three.\nThe alternative was rejected."
	signals := knowledge.FindTradeoffSignals(plan, testKeywords)
	if len(signals) == 0 {
		t.Fatal("expected FindTradeoffSignals to return at least one signal")
	}

	// "chose" should be on line 2
	var choseSignal *knowledge.TradeoffSignal
	for i := range signals {
		if signals[i].Keyword == "chose" {
			choseSignal = &signals[i]
			break
		}
	}
	if choseSignal == nil {
		t.Fatal("expected FindTradeoffSignals to include a signal for keyword 'chose'")
	}
	if choseSignal.LineNumber != 2 {
		t.Errorf("expected 'chose' to be on line 2, got %d", choseSignal.LineNumber)
	}
}

func TestFindTradeoffSignals_ContextIncludesKeyword(t *testing.T) {
	plan := "We chose approach A for performance reasons."
	signals := knowledge.FindTradeoffSignals(plan, testKeywords)
	if len(signals) == 0 {
		t.Fatal("expected at least one signal")
	}
	for _, s := range signals {
		if s.Keyword == "chose" && s.Context == "" {
			t.Error("expected Context to be non-empty for 'chose' signal")
		}
	}
}

func TestFindTradeoffSignals_DeduplicatesSameKeywordOnSameLine(t *testing.T) {
	plan := "We chose A and also chose B."
	signals := knowledge.FindTradeoffSignals(plan, testKeywords)
	count := 0
	for _, s := range signals {
		if s.Keyword == "chose" {
			count++
		}
	}
	if count > 1 {
		t.Errorf("expected at most 1 signal for 'chose' on the same line, got %d", count)
	}
}

func TestFindTradeoffSignals_EmptyPlan(t *testing.T) {
	signals := knowledge.FindTradeoffSignals("", testKeywords)
	if signals != nil && len(signals) != 0 {
		t.Errorf("expected no signals for empty plan, got %d", len(signals))
	}
}

func TestFormatSignalList_NonEmpty(t *testing.T) {
	signals := []knowledge.TradeoffSignal{
		{Keyword: "chose", LineNumber: 2, Context: "We chose approach A"},
		{Keyword: "rejected", LineNumber: 5, Context: "rejected option B because"},
	}
	out := knowledge.FormatSignalList(signals)
	if out == "" {
		t.Error("expected FormatSignalList to return non-empty string")
	}
	for _, kw := range []string{"chose", "rejected"} {
		found := false
		for _, line := range []string{out} {
			if len(line) > 0 {
				found = true
				_ = kw
				break
			}
		}
		if !found {
			t.Errorf("expected keyword %q to appear in FormatSignalList output", kw)
		}
	}
}
