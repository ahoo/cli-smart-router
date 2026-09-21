package domain

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//go:embed intent_model.json
var intentModelJSON []byte

// intentModel is the exported routing-intent student: multinomial logistic
// regression over word (1,2) + char_wb (3,6) TF-IDF, distilled from SystemOne
// teacher labels on historical + synthetic trilingual prompts.
type intentModel struct {
	Classes   []string    `json:"classes"`
	Word      intentVocab `json:"word"`
	Char      intentVocab `json:"char"`
	Coef      [][]float64 `json:"coef"`
	Intercept []float64   `json:"intercept"`
	Threshold float64     `json:"threshold"`
}

type intentVocab struct {
	Vocab map[string]int `json:"vocab"`
	IDF   []float64      `json:"idf"`
}

var intentState = struct {
	sync.Once
	model *intentModel
	err   error
}{}

func loadIntentModel() (*intentModel, error) {
	intentState.Once.Do(func() {
		var m intentModel
		if err := json.Unmarshal(intentModelJSON, &m); err != nil {
			intentState.err = fmt.Errorf("domain: invalid embedded intent model: %w", err)
			return
		}
		if len(m.Classes) == 0 || len(m.Coef) != len(m.Classes) || len(m.Intercept) != len(m.Classes) {
			intentState.err = fmt.Errorf("domain: embedded intent model has inconsistent dimensions")
			return
		}
		intentState.model = &m
	})
	return intentState.model, intentState.err
}

// intentModelThresholdOverride lets the host tune the abstention gate; the
// artifact default (0.0, CV-selected) applies until set. Negative restores
// the artifact default.
var intentModelThresholdOverride = math.NaN()

// SetIntentModelThreshold overrides the abstention gate: classes below the
// threshold fall back to legacy DetectIntent. Pass math.NaN() to restore.
func SetIntentModelThreshold(threshold float64) {
	intentModelThresholdOverride = threshold
}

func intentModelThreshold() float64 {
	model, err := loadIntentModel()
	def := 0.0
	if err == nil {
		def = model.Threshold
	}
	if math.IsNaN(intentModelThresholdOverride) {
		return def
	}
	return intentModelThresholdOverride
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// intentWordTokens replicates sklearn (?u)\b\w\w+\b: maximal word-char runs
// of at least 2 runes on lowercased text.
func intentWordTokens(lower string) []string {
	var tokens []string
	start := -1
	for i, r := range lower {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if utf8.RuneCountInString(lower[start:i]) >= 2 {
				tokens = append(tokens, lower[start:i])
			}
			start = -1
		}
	}
	if start >= 0 && utf8.RuneCountInString(lower[start:]) >= 2 {
		tokens = append(tokens, lower[start:])
	}
	return tokens
}

// intentCharGrams replicates sklearn char_wb (3,6): whitespace-split words
// padded with blanks, rune n-grams 3..6.
func intentCharGrams(lower string) []string {
	var grams []string
	for _, word := range strings.Fields(lower) {
		padded := " " + word + " "
		runes := []rune(padded)
		for n := 3; n <= 6; n++ {
			for i := 0; i+n <= len(runes); i++ {
				grams = append(grams, string(runes[i:i+n]))
			}
		}
	}
	return grams
}

func intentTFIDFPart(terms []string, vocab intentVocab) []float64 {
	counts := map[int]float64{}
	for _, term := range terms {
		if idx, ok := vocab.Vocab[term]; ok && idx >= 0 && idx < len(vocab.IDF) {
			counts[idx]++
		}
	}
	vec := make([]float64, len(vocab.IDF))
	norm := 0.0
	for idx, tf := range counts {
		v := tf * vocab.IDF[idx]
		vec[idx] = v
		norm += v * v
	}
	if norm > 0 {
		inv := 1 / math.Sqrt(norm)
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec
}

// intentBigrams joins consecutive word tokens with a single space.
func intentBigrams(tokens []string) []string {
	terms := make([]string, 0, len(tokens)*2)
	terms = append(terms, tokens...)
	for i := 0; i+1 < len(tokens); i++ {
		terms = append(terms, tokens[i]+" "+tokens[i+1])
	}
	return terms
}

// ClassifyIntentModel runs the distilled student and returns the winning
// intent plus its softmax probability. Unknown inputs surface as
// IntentUnknown only through the caller's threshold gate; an internal error
// returns (IntentUnknown, -1) so callers fall back to legacy detection.
func ClassifyIntentModel(prompt string) (Intent, float64) {
	model, err := loadIntentModel()
	if err != nil {
		return IntentUnknown, -1
	}
	lower := strings.ToLower(prompt)
	wordVec := intentTFIDFPart(intentBigrams(intentWordTokens(lower)), model.Word)
	charVec := intentTFIDFPart(intentCharGrams(lower), model.Char)
	best := -1
	bestScore := math.Inf(-1)
	scores := make([]float64, len(model.Classes))
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
		if z > bestScore {
			bestScore = z
			best = c
		}
	}
	if best < 0 {
		return IntentUnknown, -1
	}
	sum := 0.0
	for _, z := range scores {
		sum += math.Exp(z - bestScore)
	}
	intent, ok := parseIntentName(model.Classes[best])
	if !ok {
		return IntentUnknown, -1
	}
	return intent, math.Exp(bestScore-bestScore) / sum
}

func parseIntentName(name string) (Intent, bool) {
	intent := ParseIntent(name)
	if intent == IntentUnknown {
		return IntentUnknown, false
	}
	return intent, true
}

// DetectIntentSmart classifies with the distilled student first and falls
// back to legacy keyword DetectIntent when the student is unavailable or
// below the abstention threshold.
func DetectIntentSmart(prompt string) Intent {
	intent, confidence := ClassifyIntentModel(prompt)
	if confidence >= intentModelThreshold() && intent.Valid() {
		return intent
	}
	return DetectIntent(prompt)
}
