package domain

import (
	"regexp"
	"strings"
)

// PromptTemplate identifiers. They are stable config-facing names, never user
// text: rules match on these exact strings via `when.prompt_template`.
const (
	// TemplateBoundedOutput marks prompts that constrain their own answer to a
	// small size ("in under 40 words", "3-5 words", "at most 36 characters").
	// Harness-generated micro-summaries, titles, and recaps all take this shape,
	// so matching it routes cheap work away from reasoning-tier models.
	TemplateBoundedOutput = "bounded_output"
	// TemplateContinuation marks very short "keep going" instructions such as
	// "继续" or "continue". They carry no classifiable signal of their own; the
	// intended handling is session affinity, not a fresh routing decision.
	TemplateContinuation = "continuation"
)

// maxContinuationRunes bounds how long a continuation instruction may be. Bare
// "继续" / "continue" style directives are a few characters; anything longer is
// real content and must not be swallowed by this shape.
const maxContinuationRunes = 24

// boundedOutputSignals match an explicit size limit stated in the prompt. Each
// entry is anchored on a unit word so ordinary prose about words or characters
// ("the word list", "character encoding") cannot match by accident.
var boundedOutputSignals = []*regexp.Regexp{
	// "in under 40 words", "at most 36 characters", "within 2 lines", "no more than 5 sentences"
	regexp.MustCompile(`\b(?:in|at most|under|within|no more than|max(?:imum)?(?: of)?)\s+\d+\s*(?:words?|characters?|chars?|sentences?|lines?|tokens?)\b`),
	// "3-5 words", "1-2 plain sentences"
	regexp.MustCompile(`\b\d+\s*(?:-|to|or)\s*\d+\s*(?:words?|characters?|chars?|sentences?|lines?)\b`),
	// "single-line task title", "one-sentence summary"
	regexp.MustCompile(`\bsingle[\s-](?:line|sentence)\b`),
	// "one or two plain-text sentences"
	regexp.MustCompile(`\bone\s+or\s+two\b`),
}

// boundedOutputTerseSignals are weaker adjectives that still reliably mark a
// size-bounded instruction. They are deliberately few: each one widens the
// chance of catching a genuine request that merely wants a concise answer, so
// only unambiguous directive forms are listed.
var boundedOutputTerseSignals = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:be|keep it|make it|write a|give a|provide a)\s+brief\b`),
	regexp.MustCompile(`\b(?:be|keep it|make it|write a|give a|provide a)\s+concise\b`),
	regexp.MustCompile(`\bconcise,?\s+single[\s-]line\b`),
}

// continuationSignals are bare "keep going" directives. They are matched only
// on short prompts (see maxContinuationRunes) so a real sentence that happens to
// contain "continue" is not treated as a continuation.
var continuationSignals = []string{
	"继续",
	"提交推送",
	"接着",
	"go on",
	"continue",
	"keep going",
	"carry on",
	"proceed",
}

// continuationPrefixes are client-added wrappers that decorate a directive
// without changing it. They are stripped before the length gate so an
// interrupted-turn marker cannot push an otherwise bare "继续" over the limit.
var continuationPrefixes = []string{
	"[request interrupted by user]",
	"request interrupted by user",
}

// DetectPromptTemplate returns the stable template identifier matching the
// prompt shape, or "" when the prompt has no recognized shape. It performs
// lexical matching only: no host, network, or configuration access.
//
// bounded_output is checked first because it is the higher-value signal; a
// prompt that bounds its output is never simultaneously a bare continuation.
func DetectPromptTemplate(prompt string) string {
	if PromptBoundsOutput(prompt) {
		return TemplateBoundedOutput
	}
	if IsContinuationPrompt(prompt) {
		return TemplateContinuation
	}
	return ""
}

// PromptBoundsOutput reports whether the prompt explicitly sizes its own answer.
// This is the routing signal for harness-generated micro-tasks (recaps, titles,
// short summaries) that need no reasoning-tier model.
func PromptBoundsOutput(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return false
	}
	for _, signal := range boundedOutputSignals {
		if signal.MatchString(lower) {
			return true
		}
	}
	for _, signal := range boundedOutputTerseSignals {
		if signal.MatchString(lower) {
			return true
		}
	}
	return false
}

// IsContinuationPrompt reports whether the prompt is a bare instruction to
// carry on. The length gate keeps ordinary prose from matching merely because
// it contains the word "continue".
func IsContinuationPrompt(prompt string) bool {
	trimmed := stripContinuationPrefixes(strings.TrimSpace(prompt))
	if trimmed == "" || len([]rune(trimmed)) > maxContinuationRunes {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, signal := range continuationSignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}

// stripContinuationPrefixes removes client-added wrapper markers from the front
// of a directive. Only leading wrappers are stripped: a marker deeper in the
// text means the prompt carries real content.
func stripContinuationPrefixes(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	for {
		stripped := false
		lower := strings.ToLower(trimmed)
		for _, prefix := range continuationPrefixes {
			if strings.HasPrefix(lower, prefix) {
				trimmed = strings.TrimSpace(trimmed[len(prefix):])
				stripped = true
				break
			}
		}
		if !stripped {
			return trimmed
		}
	}
}
