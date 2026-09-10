package main

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
)

// This replay scores the prompt-shape detectors against prompts captured from
// real production traffic. The corpus is intentionally not vendored: it holds
// genuine user text, so it lives outside the repository and the test skips when
// it is absent. Point SMART_MODEL_ROUTER_REPLAY_CORPUS at the JSONL file to run
// it; each line carries {"prompt": ..., "model": ...} and nothing else is read.

const replayCorpusEnv = "SMART_MODEL_ROUTER_REPLAY_CORPUS"

type replaySample struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
}

// harnessTemplateSignals identify machine-generated harness prompts. They are
// used to EXCLUDE samples from the genuine-user set, because several contain
// words like "Check" or "Review" that would otherwise look human-written.
var harnessTemplateSignals = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^this session is being continued`),
	regexp.MustCompile(`(?i)^the user stepped away`),
	regexp.MustCompile(`(?i)^write a brief catch-up`),
	regexp.MustCompile(`(?i)^generate a concise, single-line task title`),
	regexp.MustCompile(`(?i)^describe your most recent action`),
	regexp.MustCompile(`(?i)^current runtime context`),
	regexp.MustCompile(`(?i)^background (job|subagent)`),
	regexp.MustCompile(`(?i)^stop hook feedback`),
	regexp.MustCompile(`(?i)^model catalog:`),
	regexp.MustCompile(`(?i)^\[queued user message`),
	regexp.MustCompile(`(?i)scheduled cron job|heartbeat|\brespond with text only\b`),
	regexp.MustCompile(`<local-command-caveat>|<command-message>`),
}

// genuineUserSignals identify samples a human typed: Chinese prose (the harness
// emits English), or explicit first-person request phrasing. Any sample matching
// one of these MUST NOT be classified as bounded_output — that shape routes work
// to a cheaper model, and misclassifying a real request would silently downgrade
// it. Missing a harness template is a cost issue; catching a real request is a
// correctness issue, so the false-positive budget is zero.
var genuineUserSignals = []*regexp.Regexp{
	regexp.MustCompile(`[一-鿿]`),
	regexp.MustCompile(`(?i)\b(i need|i want|please help|help me|look at|let me|my repo|our repo)\b`),
}

func isHarnessTemplate(prompt string) bool {
	for _, signal := range harnessTemplateSignals {
		if signal.MatchString(prompt) {
			return true
		}
	}
	return false
}

func loadReplayCorpus(t *testing.T) []replaySample {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(replayCorpusEnv))
	if path == "" {
		t.Skipf("%s not set; skipping production corpus replay", replayCorpusEnv)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open replay corpus: %v", err)
	}
	defer file.Close()

	samples := []replaySample{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sample replaySample
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			t.Fatalf("parse replay corpus line: %v", err)
		}
		if strings.TrimSpace(sample.Prompt) == "" {
			continue
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read replay corpus: %v", err)
	}
	if len(samples) == 0 {
		t.Fatal("replay corpus held no usable samples")
	}
	return samples
}

func isGenuineUserPrompt(prompt string) bool {
	if isHarnessTemplate(prompt) {
		return false
	}
	for _, signal := range genuineUserSignals {
		if signal.MatchString(prompt) {
			return true
		}
	}
	return false
}

func TestPromptShapeReplayAgainstProductionCorpus(t *testing.T) {
	samples := loadReplayCorpus(t)

	bounded, continuation, unrecognized := 0, 0, 0
	falsePositives := []string{}
	byModel := map[string]int{}

	for _, sample := range samples {
		template := domain.DetectPromptTemplate(sample.Prompt)
		switch template {
		case domain.TemplateBoundedOutput:
			bounded++
			byModel[sample.Model]++
			if isGenuineUserPrompt(sample.Prompt) {
				falsePositives = append(falsePositives, truncateLogString(sample.Prompt, 60))
			}
		case domain.TemplateContinuation:
			continuation++
			byModel[sample.Model]++
		default:
			unrecognized++
		}
	}

	total := len(samples)
	t.Logf("corpus=%d bounded_output=%d continuation=%d unrecognized=%d", total, bounded, continuation, unrecognized)
	for model, count := range byModel {
		t.Logf("  classified under %-18s %d", model, count)
	}

	if len(falsePositives) > 0 {
		t.Errorf("bounded_output matched %d genuine user prompt(s); a false positive routes real work to a cheaper model", len(falsePositives))
		for _, sample := range falsePositives {
			t.Logf("  false positive: %s", sample)
		}
	}

	// The production corpus showed harness templates dominating traffic; if the
	// detector stops recognizing any of them, that is a silent cost regression.
	if bounded == 0 {
		t.Error("bounded_output matched nothing in production traffic; the detector has regressed")
	}
}

// The shape detectors must agree with the extracted prompt, not the raw body:
// a conversation's earlier turns can contain bounded-output text without the
// current turn being a micro-task.
func TestPromptShapeUsesExtractedPromptNotWholeBody(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Summarize this in under 40 words."},
			map[string]any{"role": "assistant", "content": "ok"},
			map[string]any{"role": "user", "content": "现在实现完整的数据对账流程，包含重试和幂等"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := infrastructure.ExtractUserPrompt(body)
	if got := domain.DetectPromptTemplate(prompt); got != "" {
		t.Fatalf("template = %q, want empty: the current turn is not a micro-task", got)
	}
}
