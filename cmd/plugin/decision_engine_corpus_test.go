package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vfeitoza/cli-smart-router/internal/application"
	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
	"gopkg.in/yaml.v3"
)

// This harness scores the LOCAL decision_engine against the same fixed corpus
// the LLM classifier benchmark uses, without any host call, network request, or
// classifier invocation. It exists because production runs decision_engine and
// that path had never been measured against the classifier corpus.
//
// It lives in package main on purpose: decisionEngineRoute, buildRouteFacts, and
// routingTask are unexported, and the cgo-free replica in
// internal/application/decision_engine_bench_test.go omits the X-Router-Task /
// X-Router-Agent override precedence, which is part of the production behavior.

const (
	defaultCorpusPath   = "testdata/classifier_corpus.json"
	defaultTemplatePath = "/home/ubuntu/workspace/cliproxyapi/smart-model-router-5.yaml"

	// corpusDigest is the sha256 of the canonical JSON (sorted keys, no spaces)
	// of the whole corpus document. It guards against silent corpus drift.
	corpusDigest = "e220e9cdcb66ad41d5ef22aa2b97a2d8b36d3aeeef078676d5fa9ed825b7d9b9"

	corpusArtifactEnv = "SMART_MODEL_ROUTER_CORPUS_OUT"
	corpusPathEnv     = "SMART_MODEL_ROUTER_CORPUS"
	templatePathEnv   = "SMART_MODEL_ROUTER_CONFIG"
)

type corpusCandidate struct {
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
	Cost         string   `json:"cost"`
	Quality      string   `json:"quality"`
}

type corpusCase struct {
	Entry      string            `json:"entry"`
	CaseID     string            `json:"case_id"`
	Preference string            `json:"preference"`
	Prompt     string            `json:"prompt"`
	Expected   string            `json:"expected"`
	Candidates []corpusCandidate `json:"candidates"`
}

type corpusDocument struct {
	Cases []corpusCase `json:"cases"`
}

// templateDocument wraps the router subtree. The synchronized template is the
// harness's only configuration source: the live config.yaml carries credentials
// and is deliberately never read here.
type templateDocument struct {
	SmartModelRouter domain.Config `yaml:"smart-model-router"`
}

func envPath(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func loadCorpus(t *testing.T) corpusDocument {
	t.Helper()
	path := envPath(corpusPathEnv, defaultCorpusPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read corpus %s: %v", path, err)
	}
	var document corpusDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse corpus %s: %v", path, err)
	}
	return document
}

func loadTemplateConfig(t *testing.T) domain.Config {
	t.Helper()
	path := envPath(templatePathEnv, defaultTemplatePath)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read template %s: %v", path, err)
	}
	var document templateDocument
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse template %s: %v", path, err)
	}
	cfg := document.SmartModelRouter.Normalize()
	if len(cfg.ResolveEntries()) == 0 {
		t.Fatalf("template %s holds no virtual models", path)
	}
	return cfg
}

// corpusDigestOf reproduces the canonical form the corpus digest is defined
// over. Encoding straight into an untyped value is what sorts object keys:
// Go marshals map[string]any in key order, which matches the Python reference
// (json.dumps with sort_keys=True and no separators).
func corpusDigestOf(t *testing.T, document corpusDocument) string {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal corpus: %v", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		t.Fatalf("canonicalize corpus: %v", err)
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", sum)
}

// messageBody wraps the prompt the way a real OpenAI-format request would carry
// it, so ExtractUserPrompt sees a messages-shaped body rather than bare text.
func messageBody(t *testing.T, prompt string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
	})
	if err != nil {
		t.Fatalf("build request body: %v", err)
	}
	return raw
}

// entryProviders returns the deterministic provider set for an entry: the union
// of its configured candidate providers. An empty list would mean "no
// restriction" and would hide the provider-unavailable path we want exercised.
func entryProviders(entry domain.ResolvedEntry) []string {
	seen := map[string]struct{}{}
	providers := []string{}
	for _, candidate := range entry.Candidates() {
		if candidate.Provider == "" {
			continue
		}
		if _, ok := seen[candidate.Provider]; ok {
			continue
		}
		seen[candidate.Provider] = struct{}{}
		providers = append(providers, candidate.Provider)
	}
	sort.Strings(providers)
	return providers
}

// expectedReachable reports whether the expected model could ever be selected
// under this entry's configuration: it must be a configured candidate AND be
// targeted by at least one rule (an entry's catch-all rule counts).
func expectedReachable(entry domain.ResolvedEntry, expected string) (configured bool, targeted bool) {
	canonical := canonicalModelID(expected)
	for _, candidate := range entry.Candidates() {
		if candidate.Model == expected {
			configured = true
			break
		}
	}
	for _, rule := range entry.Routes {
		if rule.Model == expected || canonicalModelID(rule.Model) == canonical {
			targeted = true
			break
		}
	}
	return configured, targeted
}

// canonicalModelID strips a host thinking suffix so informational comparisons do
// not treat muse-free(high) and muse-free as different models.
func canonicalModelID(model string) string {
	for _, suffix := range []string{"(xhigh)", "(high)"} {
		if strings.HasSuffix(model, suffix) {
			return strings.TrimSuffix(model, suffix)
		}
	}
	return model
}

// hasCatchAll reports whether the entry declares an unconditional rule, which
// means the deterministic Router.Route fallback can never be reached.
func hasCatchAll(entry domain.ResolvedEntry) bool {
	for _, rule := range entry.Routes {
		if rule.When == (domain.RouteCondition{}) && strings.TrimSpace(rule.Model) != "" {
			return true
		}
	}
	return false
}

type corpusOutcome struct {
	CaseID        string `json:"case_id"`
	Entry         string `json:"entry"`
	Intent        string `json:"intent"`
	Complexity    int    `json:"complexity_score"`
	Tier          string `json:"complexity_tier"`
	Policy        string `json:"policy"`
	Selected      string `json:"selected_model"`
	SelectedProv  string `json:"selected_provider"`
	Expected      string `json:"expected_model"`
	Pass          bool   `json:"pass"`
	CanonicalPass bool   `json:"canonical_pass"`
	Configured    bool   `json:"expected_configured"`
	Targeted      bool   `json:"expected_targeted_by_rule"`
}

func TestCorpusDecisionEngine(t *testing.T) {
	document := loadCorpus(t)
	if len(document.Cases) != 20 {
		t.Fatalf("corpus holds %d cases, want 20", len(document.Cases))
	}
	if got := corpusDigestOf(t, document); got != corpusDigest {
		t.Fatalf("corpus digest %s, want %s", got, corpusDigest)
	}

	cfg := loadTemplateConfig(t)
	outcomes := make([]corpusOutcome, 0, len(document.Cases))
	passCount := 0
	passByEntry := map[string]int{}
	totalByEntry := map[string]int{}
	fallbackUsed := 0

	for _, testCase := range document.Cases {
		entry, ok := cfg.LookupEntry(testCase.Entry)
		if !ok {
			t.Fatalf("case %s references unknown entry %q", testCase.CaseID, testCase.Entry)
		}
		scoped, ok := cfg.WithEntry(testCase.Entry)
		if !ok {
			t.Fatalf("case %s: WithEntry(%q) failed", testCase.CaseID, testCase.Entry)
		}

		body := messageBody(t, testCase.Prompt)
		if prompt := infrastructure.ExtractUserPrompt(body); prompt != testCase.Prompt {
			t.Fatalf("case %s: extracted prompt %q != corpus prompt", testCase.CaseID, prompt)
		}

		req := infrastructure.ModelRouteRequest{
			RequestedModel:     testCase.Entry,
			SourceFormat:       "openai",
			Body:               body,
			Headers:            http.Header{},
			Stream:             false,
			AvailableProviders: entryProviders(entry),
		}

		// Production order (cmd/plugin/main.go routeModel): the deterministic
		// local decision is computed first and is the fallback when the policy
		// engine abstains.
		localDecision := application.Router{Config: scoped}.Route(req)
		decision, trace, engineOK := decisionEngineRoute(scoped, req)

		outcome := corpusOutcome{
			CaseID:     testCase.CaseID,
			Entry:      testCase.Entry,
			Intent:     trace.Task,
			Policy:     trace.Policy,
			Expected:   testCase.Expected,
			Complexity: trace.ComplexityScore,
			Tier:       "",
		}
		outcome.Configured, outcome.Targeted = expectedReachable(entry, testCase.Expected)

		switch {
		case engineOK:
			outcome.SelectedProv = decision.TargetProvider
			outcome.Selected = decision.TargetModel
		case localDecision.Handled:
			outcome.SelectedProv = localDecision.TargetProvider
			outcome.Selected = localDecision.TargetModel
			outcome.Policy = "fallback:deterministic " + localDecision.Reason
			fallbackUsed++
		default:
			outcome.Selected = "ABSTAIN"
		}

		outcome.Pass = outcome.Selected == testCase.Expected
		outcome.CanonicalPass = canonicalModelID(outcome.Selected) == canonicalModelID(testCase.Expected)
		if outcome.Pass {
			passCount++
		}
		passByEntry[testCase.Entry] += boolToInt(outcome.Pass)
		totalByEntry[testCase.Entry]++

		outcomes = append(outcomes, outcome)
	}

	fmt.Printf("\n%-24s %-15s %-11s %-14s %-28s %-24s %-24s %s\n",
		"case_id", "entry", "intent", "policy", "selected", "expected", "result", "reachable")
	for _, outcome := range outcomes {
		result := "MISS"
		if outcome.Pass {
			result = "PASS"
		} else if outcome.CanonicalPass {
			result = "PASS(suffix)"
		}
		reachable := "reachable"
		if !outcome.Configured {
			reachable = "UNREACHABLE(not-configured)"
		} else if !outcome.Targeted {
			reachable = "UNREACHABLE(no-rule)"
		}
		policy := outcome.Policy
		if policy == "" {
			policy = "none"
		}
		fmt.Printf("%-24s %-15s %-11s %-14s %-28s %-24s %-24s %s\n",
			outcome.CaseID, outcome.Entry, orDash(outcome.Intent), policy,
			outcome.SelectedProv+"/"+orDash(outcome.Selected), outcome.Expected, result, reachable)
	}

	fmt.Printf("\nscore: %d/%d (%.0f%%)\n", passCount, len(document.Cases),
		100*float64(passCount)/float64(len(document.Cases)))
	entries := make([]string, 0, len(totalByEntry))
	for entry := range totalByEntry {
		entries = append(entries, entry)
	}
	sort.Strings(entries)
	for _, entry := range entries {
		fmt.Printf("  %-16s %d/%d\n", entry, passByEntry[entry], totalByEntry[entry])
	}
	fmt.Printf("\nNOTE: this corpus encodes classifier-style expectations (candidate list +\n")
	fmt.Printf("preference). A MISS means the router config/rules disagree with the corpus,\n")
	fmt.Printf("not that the local engine is wrong.\n")

	if fallbackUsed == 0 {
		fmt.Printf("\nfallback: application.Router.Route was never exercised (every entry has a catch-all rule).\n")
	} else {
		fmt.Printf("\nfallback: application.Router.Route decided %d case(s).\n", fallbackUsed)
	}

	if path := strings.TrimSpace(os.Getenv(corpusArtifactEnv)); path != "" {
		writeCorpusArtifact(t, path, document, outcomes, passCount, passByEntry, totalByEntry)
	}
}

// writeCorpusArtifact writes the sanitized result. It carries case ids, model
// ids, and rule labels only: never prompts, headers, bodies, or credentials.
func writeCorpusArtifact(t *testing.T, path string, document corpusDocument, outcomes []corpusOutcome, passCount int, passByEntry, totalByEntry map[string]int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create artifact dir: %v", err)
	}
	payload := map[string]any{
		"corpus_digest": corpusDigest,
		"score":         passCount,
		"total":         len(document.Cases),
		"by_entry":      passByEntry,
		"entry_totals":  totalByEntry,
		"cases":         outcomes,
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	for _, testCase := range document.Cases {
		if testCase.Prompt != "" && strings.Contains(string(raw), testCase.Prompt) {
			t.Fatalf("artifact would leak prompt text for case %s", testCase.CaseID)
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open artifact: %v", err)
	}
	defer file.Close()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	fmt.Printf("\nsanitized artifact written to %s\n", path)
}

// TestDecisionEngineFallbackPath proves the deterministic Router.Route branch is
// live by stripping every rule: with no policy target the engine abstains and
// the fallback must still produce a routed model.
func TestDecisionEngineFallbackPath(t *testing.T) {
	cfg := loadTemplateConfig(t)
	scoped, ok := cfg.WithEntry("router-cheap")
	if !ok {
		t.Fatal("router-cheap entry missing")
	}
	scoped.Routes = nil

	req := infrastructure.ModelRouteRequest{
		RequestedModel:     "router-cheap",
		SourceFormat:       "openai",
		Body:               messageBody(t, "Translate this sentence to French."),
		Headers:            http.Header{},
		AvailableProviders: []string{"codex", "kimi"},
	}
	if _, _, engineOK := decisionEngineRoute(scoped, req); engineOK {
		t.Fatal("decision engine should abstain when no rules are configured")
	}
	local := application.Router{Config: scoped}.Route(req)
	if !local.Handled || strings.TrimSpace(local.TargetModel) == "" {
		t.Fatalf("deterministic fallback produced no route: %+v", local)
	}
	t.Logf("fallback routed to %s/%s (%s)", local.TargetProvider, local.TargetModel, local.Reason)
}

// TestFallbackAbstainsWhenPromptOutranksEveryCandidate documents a real property
// of the deterministic fallback: it applies a prompt-derived minimum cost tier,
// so a prompt demanding a tier that no configured candidate reaches leaves it
// with nothing to route to. In production the per-entry catch-all rule masks
// this, which is why the behavior is asserted here rather than observed live.
func TestFallbackAbstainsWhenPromptOutranksEveryCandidate(t *testing.T) {
	cfg := loadTemplateConfig(t)
	scoped, ok := cfg.WithEntry("router-cheap")
	if !ok {
		t.Fatal("router-cheap entry missing")
	}
	scoped.Routes = nil

	// "error" puts this prompt in the high cost tier; router-cheap only holds
	// low/medium candidates, so the gate empties the pool.
	req := infrastructure.ModelRouteRequest{
		RequestedModel:     "router-cheap",
		SourceFormat:       "openai",
		Body:               messageBody(t, "Fix a typo in one Go error message and add no new behavior."),
		Headers:            http.Header{},
		AvailableProviders: []string{"codex", "kimi"},
	}
	local := application.Router{Config: scoped}.Route(req)
	if local.Handled {
		t.Fatalf("expected the cost gate to abstain, got %s/%s", local.TargetProvider, local.TargetModel)
	}
	if local.Reason != "no_available_candidate" {
		t.Fatalf("reason = %q, want no_available_candidate", local.Reason)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
