package domain

import (
	"strings"
	"testing"
)

// The new prompt-shape dimensions must compose with the existing eight without
// changing how any of them match.
func TestPromptTemplateConditionMatching(t *testing.T) {
	rules := []RouteRule{
		{When: RouteCondition{PromptTemplate: TemplateBoundedOutput}, Provider: "codex", Model: "cheap"},
		{When: RouteCondition{PromptTemplate: TemplateContinuation}, Provider: "codex", Model: "sticky"},
	}
	candidates := candidatesFor("codex", "cheap", "sticky")

	tests := []struct {
		name      string
		facts     RouteFacts
		wantModel string
	}{
		{
			name:      "bounded output matches the micro-task rule",
			facts:     RouteFacts{PromptTemplate: TemplateBoundedOutput},
			wantModel: "cheap",
		},
		{
			name:      "continuation matches the sticky rule",
			facts:     RouteFacts{PromptTemplate: TemplateContinuation},
			wantModel: "sticky",
		},
		{
			name:  "unrecognized shape matches neither",
			facts: RouteFacts{PromptTemplate: ""},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := EvaluateRoutes(rules, test.facts, candidates, nil)
			if test.wantModel == "" {
				if decision.Matched {
					t.Fatalf("expected no match, got %s", decision.Model)
				}
				return
			}
			if !decision.Matched || decision.Model != test.wantModel {
				t.Fatalf("decision = %+v, want model %s", decision, test.wantModel)
			}
		})
	}
}

func TestMaxOutputHintConditionMatching(t *testing.T) {
	onlyHint := true
	rules := []RouteRule{
		{When: RouteCondition{MaxOutputHint: &onlyHint}, Provider: "codex", Model: "cheap"},
	}
	candidates := candidatesFor("codex", "cheap")

	if decision := EvaluateRoutes(rules, RouteFacts{PromptBoundsOutput: true}, candidates, nil); !decision.Matched {
		t.Fatal("expected a match when the prompt bounds its output")
	}
	if decision := EvaluateRoutes(rules, RouteFacts{PromptBoundsOutput: false}, candidates, nil); decision.Matched {
		t.Fatal("expected no match when the prompt does not bound its output")
	}
}

// A bounded-output prompt often also carries other facts. The new dimension adds
// one point of specificity, so a rule combining it with an existing dimension
// must outrank a rule that only sets one of them.
func TestPromptTemplateSpecificityComposesWithExistingDimensions(t *testing.T) {
	rules := []RouteRule{
		{When: RouteCondition{MaxOutputHint: boolPtr(true)}, Provider: "codex", Model: "hint-only"},
		{When: RouteCondition{Task: string(IntentCoding), MaxOutputHint: boolPtr(true)}, Provider: "codex", Model: "hint-plus-task"},
	}
	candidates := candidatesFor("codex", "hint-only", "hint-plus-task")
	facts := RouteFacts{Task: IntentCoding, PromptBoundsOutput: true}

	decision := EvaluateRoutes(rules, facts, candidates, nil)
	if !decision.Matched || decision.Model != "hint-plus-task" {
		t.Fatalf("decision = %+v, want the more specific hint-plus-task rule", decision)
	}
	if decision.Specificity != 2 {
		t.Fatalf("specificity = %d, want 2", decision.Specificity)
	}
}

func TestRouteConditionParsesPromptShapeYAML(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Routes = []RouteRule{{
		When:     RouteCondition{PromptTemplate: TemplateBoundedOutput, MaxOutputHint: boolPtr(true)},
		Provider: "codex",
		Model:    "m",
	}}
	normalized := cfg.Normalize()
	if len(normalized.Routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(normalized.Routes))
	}
	got := normalized.Routes[0].When
	if got.PromptTemplate != TemplateBoundedOutput || got.MaxOutputHint == nil || !*got.MaxOutputHint {
		t.Fatalf("condition = %+v", got)
	}
}

// The decision log records ruleReason, so a matched prompt-shape rule must be
// identifiable there instead of being reported as an anonymous catch-all.
func TestRuleReasonNamesPromptShapeConditions(t *testing.T) {
	when := RouteCondition{PromptTemplate: TemplateBoundedOutput, MaxOutputHint: boolPtr(true)}
	reason := ruleReason(when, RouteFacts{PromptBoundsOutput: true})
	if reason == "rule:catch_all" {
		t.Fatal("prompt-shape rule reported as catch_all")
	}
	for _, want := range []string{"prompt_template=" + TemplateBoundedOutput, "max_output_hint"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q missing %q", reason, want)
		}
	}
}

func candidatesFor(provider string, models ...string) []Candidate {
	out := make([]Candidate, 0, len(models))
	for _, model := range models {
		out = append(out, Candidate{Provider: provider, Model: model, Cost: "low", Quality: "medium"})
	}
	return out
}
