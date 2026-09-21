package domain

import (
	"sort"
	"strings"
)

// RouteFacts are the normalized inputs the Policy Engine matches rules against.
// They are produced by the Prompt/Context/Complexity analyzers, so the engine
// itself does no text analysis — it only evaluates declarative rules.
type RouteFacts struct {
	Task            Intent // detected intent
	Language        string // detected language (lowercase, "" = unknown)
	ComplexityScore int    // [0,100]
	ComplexityTier  int    // 0=low..3=very_high
	FileCount       int
	HasDiff         bool
	Stream          bool
	// PromptTemplate is the detected request shape ("" = unrecognized), e.g.
	// bounded_output for harness micro-tasks or continuation for bare "continue".
	PromptTemplate string
	// PromptBoundsOutput reports whether the prompt sizes its own answer.
	PromptBoundsOutput bool
}

// PolicyDecision is the Policy Engine output. Matched reports whether a rule fired;
// when false, callers fall back to the existing deterministic routing.
type PolicyDecision struct {
	Matched     bool
	Provider    string
	Model       string
	RuleIndex   int // index of the winning rule in cfg.Routes (-1 when none)
	Specificity int // number of conditions the winning rule matched on
	Reason      string
}

// complexityTierName maps a numeric tier to the label used in rule conditions.
var complexityTierName = map[int]string{
	0: "low",
	1: "medium",
	2: "high",
	3: "very_high",
}

// condition is one table-driven predicate over a RouteCondition and RouteFacts.
// active reports whether the condition is set in the rule (non-wildcard);
// match reports whether the facts satisfy it. Keeping predicates in a slice
// avoids scattered if-statements: EvaluateRoutes iterates this table uniformly.
type condition struct {
	active func(RouteCondition) bool
	match  func(RouteCondition, RouteFacts) bool
}

// conditions is the ordered rule-matching table. Adding a new matchable dimension
// means appending one entry here — no branching logic elsewhere changes.
var conditions = []condition{
	{
		active: func(c RouteCondition) bool { return c.Task != "" },
		match:  func(c RouteCondition, f RouteFacts) bool { return c.Task == string(f.Task) },
	},
	{
		active: func(c RouteCondition) bool { return c.Language != "" },
		match:  func(c RouteCondition, f RouteFacts) bool { return c.Language == f.Language },
	},
	{
		active: func(c RouteCondition) bool { return c.Complexity != "" },
		match: func(c RouteCondition, f RouteFacts) bool {
			return c.Complexity == complexityTierName[f.ComplexityTier]
		},
	},
	{
		active: func(c RouteCondition) bool { return c.ComplexityMin != nil },
		match:  func(c RouteCondition, f RouteFacts) bool { return f.ComplexityScore >= *c.ComplexityMin },
	},
	{
		active: func(c RouteCondition) bool { return c.ComplexityMax != nil },
		match:  func(c RouteCondition, f RouteFacts) bool { return f.ComplexityScore <= *c.ComplexityMax },
	},
	{
		active: func(c RouteCondition) bool { return c.MinFiles != nil },
		match:  func(c RouteCondition, f RouteFacts) bool { return f.FileCount >= *c.MinFiles },
	},
	{
		active: func(c RouteCondition) bool { return c.HasDiff != nil },
		match:  func(c RouteCondition, f RouteFacts) bool { return *c.HasDiff == f.HasDiff },
	},
	{
		active: func(c RouteCondition) bool { return c.Stream != nil },
		match:  func(c RouteCondition, f RouteFacts) bool { return *c.Stream == f.Stream },
	},
	{
		active: func(c RouteCondition) bool { return c.PromptTemplate != "" },
		match:  func(c RouteCondition, f RouteFacts) bool { return c.PromptTemplate == f.PromptTemplate },
	},
	{
		active: func(c RouteCondition) bool { return c.MaxOutputHint != nil },
		match:  func(c RouteCondition, f RouteFacts) bool { return *c.MaxOutputHint == f.PromptBoundsOutput },
	},
}

// EvaluateRoutes runs the rules-based Policy Engine. It returns the most specific
// matching rule (the rule whose active conditions are all satisfied and that has
// the highest number of active conditions). Ties are broken by rule order (first
// wins), making outcomes deterministic. A rule with an empty `when` acts as a
// catch-all with specificity 0.
//
// The winning model is validated against the configured candidates so the engine
// never routes to an unknown or unavailable model; a rule pointing at an invalid
// model is skipped, letting a less specific rule (or deterministic fallback) win.
func EvaluateRoutes(rules []RouteRule, facts RouteFacts, candidates []Candidate, availableProviders []string) PolicyDecision {
	best := PolicyDecision{Matched: false, RuleIndex: -1, Specificity: -1}
	for index, rule := range rules {
		if rule.Model == "" {
			continue
		}
		matched, specificity := ruleMatches(rule.When, facts)
		if !matched || specificity <= best.Specificity {
			continue
		}
		candidate, ok := resolveRuleTarget(rule, candidates, availableProviders)
		if !ok {
			continue
		}
		best = PolicyDecision{
			Matched:     true,
			Provider:    candidate.Provider,
			Model:       candidate.Model,
			RuleIndex:   index,
			Specificity: specificity,
			Reason:      ruleReason(rule.When, facts),
		}
	}
	if !best.Matched {
		best.Specificity = 0
	}
	return best
}

// EvaluateRoutesRanked returns the ordered chain of policy-allowed targets for the
// given facts: every rule whose conditions match, most specific first, ties broken
// by rule order. Each target is deduplicated by provider/model so the Fallback
// Engine can walk the chain and pick the next allowed model after a failure. The
// slice is empty when no rule matches, in which case callers fall back to the
// existing deterministic routing.
func EvaluateRoutesRanked(rules []RouteRule, facts RouteFacts, candidates []Candidate, availableProviders []string) []PolicyDecision {
	type scored struct {
		decision    PolicyDecision
		specificity int
		order       int
	}
	matches := make([]scored, 0, len(rules))
	for index, rule := range rules {
		if rule.Model == "" {
			continue
		}
		matched, specificity := ruleMatches(rule.When, facts)
		if !matched {
			continue
		}
		candidate, ok := resolveRuleTarget(rule, candidates, availableProviders)
		if !ok {
			continue
		}
		matches = append(matches, scored{
			decision: PolicyDecision{
				Matched:     true,
				Provider:    candidate.Provider,
				Model:       candidate.Model,
				RuleIndex:   index,
				Specificity: specificity,
				Reason:      ruleReason(rule.When, facts),
			},
			specificity: specificity,
			order:       index,
		})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].specificity != matches[j].specificity {
			return matches[i].specificity > matches[j].specificity
		}
		return matches[i].order < matches[j].order
	})
	out := make([]PolicyDecision, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, item := range matches {
		key := item.decision.Provider + "/" + item.decision.Model
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item.decision)
	}
	return out
}

// ruleMatches evaluates every active condition of a rule against facts using the
// condition table. It returns whether all active conditions matched and how many
// were active (the rule's specificity).
func ruleMatches(when RouteCondition, facts RouteFacts) (bool, int) {
	specificity := 0
	for _, cond := range conditions {
		if !cond.active(when) {
			continue
		}
		specificity++
		if !cond.match(when, facts) {
			return false, specificity
		}
	}
	return true, specificity
}

// resolveRuleTarget finds the configured candidate a rule points to and checks its
// provider is available. When the rule sets a provider, both must match; otherwise
// the first candidate with the model id is used.
func resolveRuleTarget(rule RouteRule, candidates []Candidate, availableProviders []string) (Candidate, bool) {
	available := AvailableSet(availableProviders)
	for _, candidate := range candidates {
		if !candidate.Valid() || candidate.Model != rule.Model {
			continue
		}
		if rule.Provider != "" && candidate.Provider != rule.Provider {
			continue
		}
		if len(available) > 0 {
			if _, ok := available[candidate.Provider]; !ok {
				continue
			}
		}
		return candidate, true
	}
	return Candidate{}, false
}

// ruleReason builds a short, auditable explanation of the matched conditions.
func ruleReason(when RouteCondition, facts RouteFacts) string {
	parts := make([]string, 0, len(conditions))
	appendPart := func(active bool, text string) {
		if active {
			parts = append(parts, text)
		}
	}
	appendPart(when.Task != "", "task="+when.Task)
	appendPart(when.Language != "", "language="+when.Language)
	appendPart(when.Complexity != "", "complexity="+when.Complexity)
	appendPart(when.ComplexityMin != nil, "complexity_min")
	appendPart(when.ComplexityMax != nil, "complexity_max")
	appendPart(when.MinFiles != nil, "min_files")
	appendPart(when.HasDiff != nil, "has_diff")
	appendPart(when.Stream != nil, "stream")
	appendPart(when.PromptTemplate != "", "prompt_template="+when.PromptTemplate)
	appendPart(when.MaxOutputHint != nil, "max_output_hint")
	if len(parts) == 0 {
		return "rule:catch_all"
	}
	return "rule:" + strings.Join(parts, ",")
}
