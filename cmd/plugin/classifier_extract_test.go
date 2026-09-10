package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
)

func reqForTest() infrastructure.ModelRouteRequest {
	return infrastructure.ModelRouteRequest{
		RequestedModel: "router-cheap",
		Body:           []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
}

func classifierCandidates() map[string]domain.CandidateConfig {
	return map[string]domain.CandidateConfig{
		"muse-free": {Provider: "codex", Model: "muse-free"},
		"sol":       {Provider: "codex", Model: "sol"},
		"kimi":      {Provider: "kimi", Model: "kimi"},
	}
}

func classifierEnvelope(t *testing.T, fields map[string]string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": fields}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseClassifierVerdictSearchesEveryField(t *testing.T) {
	body := classifierEnvelope(t, map[string]string{
		"content":           "not a verdict",
		"reasoning_content": `analysis {"selected_model":"sol"}`,
		"reasoning":         `{"selected_model":"muse-free"}`,
	})
	candidate, source, failure, ok := parseClassifierVerdict(body, classifierCandidates(), nil)
	if !ok || candidate.Model != "sol" || source != "reasoning_content" || failure != "" {
		t.Fatalf("candidate=%+v source=%q failure=%q ok=%v", candidate, source, failure, ok)
	}
}

func TestParseClassifierVerdictSearchesEveryObject(t *testing.T) {
	body := classifierEnvelope(t, map[string]string{
		"content": `metadata {"status":"ready"} verdict {"selected_model":" muse-free "}`,
	})
	candidate, source, _, ok := parseClassifierVerdict(body, classifierCandidates(), nil)
	if !ok || candidate.Model != "muse-free" || source != "content" {
		t.Fatalf("candidate=%+v source=%q ok=%v", candidate, source, ok)
	}
}

func TestParseClassifierVerdictSkipsUnbalancedPrefix(t *testing.T) {
	body := classifierEnvelope(t, map[string]string{
		"reasoning": `unfinished {"analysis": then {"selected_model":"sol"}`,
	})
	candidate, source, _, ok := parseClassifierVerdict(body, classifierCandidates(), nil)
	if !ok || candidate.Model != "sol" || source != "reasoning" {
		t.Fatalf("candidate=%+v source=%q ok=%v", candidate, source, ok)
	}
}

func TestParseClassifierVerdictHandlesBracesAndEscapesInStrings(t *testing.T) {
	body := classifierEnvelope(t, map[string]string{
		"content": `{"note":"literal } and \\\"{\\\"","selected_model":"sol"}`,
	})
	candidate, _, _, ok := parseClassifierVerdict(body, classifierCandidates(), nil)
	if !ok || candidate.Model != "sol" {
		t.Fatalf("candidate=%+v ok=%v", candidate, ok)
	}
}

func TestParseClassifierVerdictContinuesAfterUnknownOrUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		reasoning string
		available []string
	}{
		{
			name:      "unknown",
			content:   `{"selected_model":"invented"}`,
			reasoning: `{"selected_model":"sol"}`,
		},
		{
			name:      "unavailable",
			content:   `{"selected_model":"kimi"}`,
			reasoning: `{"selected_model":"sol"}`,
			available: []string{"codex"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := classifierEnvelope(t, map[string]string{"content": test.content, "reasoning": test.reasoning})
			candidate, source, _, ok := parseClassifierVerdict(body, classifierCandidates(), test.available)
			if !ok || candidate.Model != "sol" || source != "reasoning" {
				t.Fatalf("candidate=%+v source=%q ok=%v", candidate, source, ok)
			}
		})
	}
}

func TestParseClassifierVerdictAcceptsPlainFencedBody(t *testing.T) {
	body := []byte("result:\n```json\n{\"selected_model\":\"sol\",\"confidence\":0.9,\"reason\":\"legacy\"}\n```")
	candidate, source, _, ok := parseClassifierVerdict(body, classifierCandidates(), nil)
	if !ok || candidate.Model != "sol" || source != "body" {
		t.Fatalf("candidate=%+v source=%q ok=%v", candidate, source, ok)
	}
}

func TestParseClassifierVerdictFailureCategory(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		available []string
		want      string
	}{
		{name: "invalid", body: classifierEnvelope(t, map[string]string{"content": `{"answer":"none"}`}), want: "invalid_verdict"},
		{name: "unavailable", body: classifierEnvelope(t, map[string]string{"content": `{"selected_model":"kimi"}`}), available: []string{"codex"}, want: "unavailable_selection"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, failure, ok := parseClassifierVerdict(test.body, classifierCandidates(), test.available)
			if ok || failure != test.want {
				t.Fatalf("failure=%q ok=%v, want %q false", failure, ok, test.want)
			}
		})
	}
}

func TestClassifierRequestBodyUsesConfiguredBudgetAndOverrides(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Classifier.MaxTokens = 1000
	classifier := domain.ClassifierModel{
		Model: "qwen3.5",
		RequestOverrides: map[string]any{
			"response_format": map[string]any{"type": "json_object"},
			"thinking_budget": 128,
			"model":           "wrong",
			"stream":          true,
			"temperature":     1,
			"max_tokens":      1,
			"messages":        []any{"wrong"},
		},
	}
	var decoded map[string]any
	if err := json.Unmarshal(classifierRequestBody(classifier, cfg, reqForTest()), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["model"] != "qwen3.5" || decoded["stream"] != false || decoded["temperature"] != float64(0) || decoded["max_tokens"] != float64(1000) {
		t.Fatalf("reserved request fields were overridden: %+v", decoded)
	}
	if decoded["thinking_budget"] != float64(128) {
		t.Fatalf("thinking_budget = %v", decoded["thinking_budget"])
	}
	format, ok := decoded["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Fatalf("response_format = %#v", decoded["response_format"])
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", decoded["messages"])
	}
	system := messages[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, `{"selected_model":"<exact-id>"}`) || strings.Contains(system, `"reason"`) || strings.Contains(system, `"confidence"`) {
		t.Fatalf("unexpected system prompt: %q", system)
	}
}

func TestClassifierRequestBodyKeepsLegacyDefaultBudget(t *testing.T) {
	cfg := domain.Config{}
	var decoded map[string]any
	if err := json.Unmarshal(classifierRequestBody(domain.ClassifierModel{Model: "m"}, cfg, reqForTest()), &decoded); err != nil {
		t.Fatal(err)
	}
	if got := int(decoded["max_tokens"].(float64)); got != domain.DefaultClassifierMaxTokens {
		t.Fatalf("max_tokens = %d, want %d", got, domain.DefaultClassifierMaxTokens)
	}
}

func TestVirtualModelStatusExcludesClassifierSecretsAndOverrides(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.VirtualModels = []domain.VirtualModelEntry{{
		Name:       "router-test",
		Strategy:   "llm",
		Preference: "cost",
		Classifier: domain.ClassifierConfig{
			Enabled:     true,
			MaxAttempts: 1,
			MaxTokens:   1000,
			Models: []domain.ClassifierModel{{
				Provider:         "provider",
				Model:            "qwen3.5",
				Headers:          map[string]string{"Authorization": "secret"},
				RequestOverrides: map[string]any{"response_format": map[string]any{"type": "json_object"}},
			}},
		},
	}}
	status := virtualModelStatus(cfg)
	if len(status) != 1 {
		t.Fatalf("status entries = %d, want 1", len(status))
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"Authorization", "secret", "response_format", "json_object"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("status leaks %q: %s", forbidden, text)
		}
	}
	classifier := status[0]["classifier"].(map[string]any)
	if classifier["max_tokens"] != 1000 || classifier["max_attempts"] != 1 {
		t.Fatalf("classifier status = %+v", classifier)
	}
}
