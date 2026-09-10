package domain

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func multiEntryYAML() Config {
	var cfg Config
	raw := `
enabled: true
virtual_models:
  - name: router-cheap
    strategy: hybrid
    preference: cost
    models:
      - provider: codex
        model: gpt-5.4-mini
        cost: low
        quality: medium
  - name: router-security
    strategy: decision_engine
    preference: quality
    models:
      - provider: claude
        model: claude-opus-4-8
        cost: very_high
        quality: highest
    routes:
      - when: {task: security}
        provider: claude
        model: claude-opus-4-8
`
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		panic(err)
	}
	return cfg.Normalize()
}

func TestResolveEntriesMulti(t *testing.T) {
	entries := multiEntryYAML().ResolveEntries()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Name != "router-cheap" || entries[0].Strategy != "hybrid" || entries[0].Preference != "cost" {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[1].Name != "router-security" || entries[1].Strategy != "decision_engine" {
		t.Fatalf("second entry = %+v", entries[1])
	}
	// Entries never share routing state: each keeps its own models.
	if len(entries[0].Models) != 1 || entries[0].Models[0].Model != "gpt-5.4-mini" {
		t.Fatalf("cheap models = %+v", entries[0].Models)
	}
	if len(entries[1].Models) != 1 || entries[1].Models[0].Model != "claude-opus-4-8" {
		t.Fatalf("security models = %+v", entries[1].Models)
	}
}

func TestLegacySingleEntryCompat(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VirtualModel = "smart:auto"
	entries := cfg.Normalize().ResolveEntries()
	if len(entries) != 1 || entries[0].Name != "smart:auto" {
		t.Fatalf("legacy entries = %+v", entries)
	}
}

func TestDuplicateEntryNamesDeduped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VirtualModels = []VirtualModelEntry{{Name: "a"}, {Name: "a"}, {Name: "b"}}
	names := cfg.Normalize().VirtualModelNames()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %v", names)
	}
}

func TestLookupAndWithEntry(t *testing.T) {
	cfg := multiEntryYAML()
	entry, ok := cfg.LookupEntry("router-security")
	if !ok || entry.Strategy != "decision_engine" {
		t.Fatalf("lookup = %+v %v", entry, ok)
	}
	if _, ok := cfg.LookupEntry("nope"); ok {
		t.Fatal("lookup of unknown model should fail")
	}
	scoped, ok := cfg.WithEntry("router-cheap")
	if !ok || scoped.VirtualModel != "router-cheap" || scoped.Preference != "cost" {
		t.Fatalf("scoped = %+v %v", scoped, ok)
	}
	// Plugin-wide fields stay from the top level.
	if !scoped.Enabled {
		t.Fatal("scoped should keep top-level enabled")
	}
}

func TestEntryDefaultsSeededFromCode(t *testing.T) {
	// An entry that only sets name+models keeps code defaults for cache,
	// session affinity, and fallback (same as legacy top-level fields).
	cfg := multiEntryYAML()
	entry, ok := cfg.LookupEntry("router-cheap")
	if !ok {
		t.Fatal("router-cheap not found")
	}
	if !entry.Cache.Enabled {
		t.Fatal("entry cache should default to enabled")
	}
	if !entry.Routing.KeepSameModelPerSession {
		t.Fatal("entry session affinity should default to enabled")
	}
	if entry.Routing.PreferLowCost != true {
		t.Fatal("entry prefer_low_cost should default to true")
	}
	if entry.ExecutorFallback.MaxAttempts != 3 {
		t.Fatalf("entry fallback attempts = %d, want 3", entry.ExecutorFallback.MaxAttempts)
	}
	// Explicit strategy/preference still win.
	if entry.Strategy != "hybrid" || entry.Preference != "cost" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestEffectiveCacheMaxEntriesUsesMax(t *testing.T) {
	cfg := DefaultConfig()
	cfg.VirtualModels = []VirtualModelEntry{
		{Name: "a", Cache: CacheConfig{Enabled: true, MaxEntries: 1}},
		{Name: "b", Cache: CacheConfig{Enabled: true, MaxEntries: 50}},
		{Name: "c", Cache: CacheConfig{Enabled: false, MaxEntries: 999}},
	}
	if got := cfg.Normalize().EffectiveCacheMaxEntries(); got != 50 {
		t.Fatalf("max entries = %d, want 50", got)
	}
}

func TestNormalizeDoesNotMutateSharedSlices(t *testing.T) {
	cfg := multiEntryYAML()
	before := cfg.VirtualModels[0].Models[0].Model
	for i := 0; i < 5; i++ {
		_ = cfg.Normalize()
	}
	if cfg.VirtualModels[0].Models[0].Model != before {
		t.Fatalf("shared config mutated: %q", cfg.VirtualModels[0].Models[0].Model)
	}
	first := cfg.Normalize()
	second := cfg.Normalize()
	first.VirtualModels[0].Models[0].Model = "mutated"
	if second.VirtualModels[0].Models[0].Model == "mutated" {
		t.Fatal("normalized copies share backing arrays")
	}
}

func TestLookupEntryStripsThinkingSuffix(t *testing.T) {
	cfg := multiEntryYAML()
	for _, name := range []string{"router-cheap(high)", "router-cheap", " router-security(xhigh) "} {
		if _, ok := cfg.LookupEntry(name); !ok {
			t.Fatalf("lookup %q should match", name)
		}
	}
	if _, ok := cfg.LookupEntry("nope(high)"); ok {
		t.Fatal("unknown model with suffix should not match")
	}
}

func TestNormalizeClassifierHeaders(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Classifier.Models = []ClassifierModel{{
		Provider: " codex ",
		Model:    " m ",
		Headers:  map[string]string{" X-Opencode-Session ": " ses1 ", "": "x", "Empty": "  "},
	}}
	got := cfg.Normalize().Classifier.Models[0]
	if got.Provider != "codex" || got.Model != "m" {
		t.Fatalf("model = %+v", got)
	}
	if len(got.Headers) != 1 || got.Headers["X-Opencode-Session"] != "ses1" {
		t.Fatalf("headers = %+v", got.Headers)
	}
}

func TestCloneClassifierModelsDeepCopiesHeaders(t *testing.T) {
	cfg := multiEntryYAML()
	first := cfg.Normalize()
	second := cfg.Normalize()
	first.Classifier.Models = append(first.Classifier.Models, ClassifierModel{Model: "x"})
	if len(second.Classifier.Models) == len(first.Classifier.Models) {
		t.Fatal("normalized classifier models share backing arrays")
	}
}
