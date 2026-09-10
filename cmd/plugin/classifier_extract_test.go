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

func TestClassifierContentPrefersContent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"{\"selected_model\":\"m\"}","reasoning_content":"think","reasoning":"think2"}}]}`)
	if got := string(classifierContent(body)); !strings.Contains(got, "selected_model") || strings.Contains(got, "think") {
		t.Fatalf("want content first, got %q", got)
	}
}

func TestClassifierContentFallsBackToReasoningFields(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		raw, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{field: "verdict {\"selected_model\":\"muse-free\"} done"}}},
		})
		got := string(classifierContent(raw))
		if !strings.Contains(got, "selected_model") {
			t.Fatalf("field %s: want verdict fallback, got %q", field, got)
		}
		if blob := extractJSONObject([]byte(got)); blob == nil {
			t.Fatalf("field %s: no JSON extractable from %q", field, got)
		}
	}
}

func TestClassifierContentEmptyMessageFallsBackToBody(t *testing.T) {
	raw := []byte(`{"choices":[{"message":{}}]}`)
	if got := string(classifierContent(raw)); string(raw) != got {
		t.Fatalf("want raw body fallback, got %q", got)
	}
}

func TestClassifierRequestBodyHasTokenBudget(t *testing.T) {
	cfg := domain.DefaultConfig()
	var decoded map[string]any
	if err := json.Unmarshal(classifierRequestBody("m", cfg, reqForTest()), &decoded); err != nil {
		t.Fatal(err)
	}
	tokens, ok := decoded["max_tokens"].(float64)
	if !ok || int(tokens) != domain.DefaultClassifierMaxTokens {
		t.Fatalf("max_tokens = %v, want %d", decoded["max_tokens"], domain.DefaultClassifierMaxTokens)
	}
}
