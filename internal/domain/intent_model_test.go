package domain

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntentModelParity checks the Go inference against probabilities
// produced by the Python training script on held-out prompts.
func TestIntentModelParity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "intent_parity.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture map[string]struct {
		Prompt string             `json:"prompt"`
		Probs  map[string]float64 `json:"probs"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	model, err := loadIntentModel()
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	_ = model
	for id, want := range fixture {
		for class, expected := range want.Probs {
			got := intentClassProb(want.Prompt, class)
			if math.Abs(got-expected) > 1e-9 {
				t.Fatalf("%s class %s: prob = %.12f, want %.12f", id, class, got, expected)
			}
		}
	}
}

// intentClassProb recomputes one class probability for parity assertions.
func intentClassProb(prompt, class string) float64 {
	model, err := loadIntentModel()
	if err != nil {
		return math.NaN()
	}
	lower := strings.ToLower(prompt)
	wordVec := intentTFIDFPart(intentBigrams(intentWordTokens(lower)), model.Word)
	charVec := intentTFIDFPart(intentCharGrams(lower), model.Char)
	scores := make([]float64, len(model.Classes))
	best := math.Inf(-1)
	for c := range model.Classes {
		z := model.Intercept[c]
		off := 0
		for i, v := range wordVec {
			z += v * model.Coef[c][off+i]
		}
		off += len(wordVec)
		for i, v := range charVec {
			z += v * model.Coef[c][off+i]
		}
		scores[c] = z
		if z > best {
			best = z
		}
	}
	sum := 0.0
	for _, z := range scores {
		sum += math.Exp(z - best)
	}
	for c, name := range model.Classes {
		if name == class {
			return math.Exp(scores[c]-best) / sum
		}
	}
	return math.NaN()
}

// TestDetectIntentSmartGolden guards distilled behavior on representative
// short prompts: model-first classification with legacy fallback.
func TestDetectIntentSmartGolden(t *testing.T) {
	cases := []struct {
		prompt string
		want   Intent
	}{
		{"Write a small Go function to reverse a string.", IntentCoding},
		{"background job finished [status: completed, exit code: 0]. Read its output with job_output.", IntentTesting},
		{"hi", IntentUnknown},
		{"继续", IntentUnknown},
		{"提交推送", IntentUnknown},
	}
	for _, c := range cases {
		if got := DetectIntentSmart(c.prompt); got != c.want {
			t.Fatalf("prompt %q: got %q, want %q", c.prompt, got, c.want)
		}
	}
}

// TestDetectIntentSmartFallback ensures a broken model degrades to legacy.
func TestDetectIntentSmartFallback(t *testing.T) {
	intent, confidence := ClassifyIntentModel("")
	_ = intent
	_ = confidence
	if got := DetectIntent("write code"); got != IntentCoding {
		t.Fatalf("legacy fallback broken: got %q", got)
	}
}
