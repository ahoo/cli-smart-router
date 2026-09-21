package domain

import "testing"

func TestPromptBoundsOutputMatchesRealHarnessTemplates(t *testing.T) {
	// Verbatim shapes observed in production traffic.
	prompts := []string{
		"The user stepped away and is coming back. Recap in under 40 words, 1-2 plain sentences, no markdown.",
		"Write a brief catch-up for a user returning to this Codex task. In at most 40 words and one or two plain-text sentences.",
		"Generate a concise, single-line task title of at most 36 characters and under five words where possible.",
		"Describe your most recent action in 3-5 words using present tense (-ing).",
		"Summarize in 2 lines.",
		"Reply with no more than 5 sentences.",
		"Give me a concise, single-line summary.",
		"Be brief.",
	}
	for _, prompt := range prompts {
		if !PromptBoundsOutput(prompt) {
			t.Errorf("expected bounded output for %q", prompt)
		}
		if got := DetectPromptTemplate(prompt); got != TemplateBoundedOutput {
			t.Errorf("template(%q) = %q, want %q", prompt, got, TemplateBoundedOutput)
		}
	}
}

func TestPromptBoundsOutputRejectsOrdinaryRequests(t *testing.T) {
	prompts := []string{
		// Plain user instructions must never be treated as size-bounded.
		"Design a multi-region database migration with rollback and consistency analysis.",
		"Implement a concurrent Go worker pool across several files with cancellation and tests.",
		"渲染器的 emoji 用法整理一下",
		"Review this authorization architecture.",
		// Unit words appearing without a size limit.
		"Explain how UTF-8 character encoding works.",
		"Refactor the word list parser.",
		// "continue" in real prose, not a bare directive.
		"Continue the migration by adding the rollback branch and tests for the failure path.",
	}
	for _, prompt := range prompts {
		if PromptBoundsOutput(prompt) {
			t.Errorf("unexpected bounded output for %q", prompt)
		}
	}
}

func TestIsContinuationPrompt(t *testing.T) {
	accept := []string{"继续", "继续推进", "提交推送", "continue", "go on", "[Request interrupted by user] 继续"}
	for _, prompt := range accept {
		if !IsContinuationPrompt(prompt) {
			t.Errorf("expected continuation for %q", prompt)
		}
	}
	reject := []string{
		"",
		"Design a multi-region database migration with rollback strategy.",
		"继续推进这个重构，并且补充对应的单元测试覆盖边界情况",
	}
	for _, prompt := range reject {
		if IsContinuationPrompt(prompt) {
			t.Errorf("unexpected continuation for %q", prompt)
		}
	}
}

func TestContinuationDoesNotClaimBoundedOutput(t *testing.T) {
	if got := DetectPromptTemplate("继续"); got != TemplateContinuation {
		t.Fatalf("template(继续) = %q, want %q", got, TemplateContinuation)
	}
	if PromptBoundsOutput("继续") {
		t.Fatal("继续 must not be treated as bounded output")
	}
}

func TestDetectPromptTemplateUnknownReturnsEmpty(t *testing.T) {
	for _, prompt := range []string{"", "   ", "Design a multi-region database migration."} {
		if got := DetectPromptTemplate(prompt); got != "" {
			t.Errorf("template(%q) = %q, want empty", prompt, got)
		}
	}
}
