package domain

import (
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultVirtualModel is used when the plugin configuration omits virtual_model.
	DefaultVirtualModel = "router:auto"
	// DefaultStrategy is the safest V1 strategy because it never calls a classifier.
	DefaultStrategy = "capability"
	// DefaultPreference balances cost and quality when preference is omitted.
	DefaultPreference = "balanced"

	// PreferenceCost biases routing toward the cheapest acceptable model.
	PreferenceCost = "cost"
	// PreferenceBalanced balances cost and quality.
	PreferenceBalanced = "balanced"
	// PreferenceQuality biases routing toward the highest quality model.
	PreferenceQuality = "quality"
)

// Config contains plugin-owned configuration parsed from config_yaml.
// Routing behavior is driven by EffectiveEntries: when `virtual_models` is set,
// each entry is an independently routable virtual model with its own strategy,
// preference, candidates, rules, classifier, cache, and routing policy. When
// `virtual_models` is absent, the legacy top-level `virtual_model` plus the
// top-level routing fields form the single entry (backward compatible).
type Config struct {
	Enabled          bool                   `yaml:"enabled"`
	VirtualModel     string                 `yaml:"virtual_model"`
	VirtualModels    []VirtualModelEntry    `yaml:"virtual_models"`
	Strategy         string                 `yaml:"strategy"`
	Preference       string                 `yaml:"preference"`
	StatePath        string                 `yaml:"state_path"`
	Debug            DebugConfig            `yaml:"debug"`
	Catalog          CatalogConfig          `yaml:"catalog"`
	Pricing          PricingConfig          `yaml:"pricing"`
	Cache            CacheConfig            `yaml:"cache"`
	ExecutorFallback ExecutorFallbackConfig `yaml:"executor_fallback"`
	Classifier       ClassifierConfig       `yaml:"classifier"`
	Routing          RoutingConfig          `yaml:"routing"`
	Routes           []RouteRule            `yaml:"routes"`
	Models           []CandidateConfig      `yaml:"models"`
}

// VirtualModelEntry is one independently routable virtual model. Every field
// except Name falls back to code defaults (the same defaults DefaultConfig
// applies to the legacy top-level fields) when unset, so entries never inherit
// from the top-level routing fields: each entry resolves to a complete,
// self-contained routing configuration. An empty `routing`/`cache`/
// `executor_fallback` block means "use defaults", not "disable everything".
type VirtualModelEntry struct {
	Name             string                 `yaml:"name"`
	Strategy         string                 `yaml:"strategy"`
	Preference       string                 `yaml:"preference"`
	Cache            CacheConfig            `yaml:"cache"`
	ExecutorFallback ExecutorFallbackConfig `yaml:"executor_fallback"`
	Classifier       ClassifierConfig       `yaml:"classifier"`
	Routing          RoutingConfig          `yaml:"routing"`
	Routes           []RouteRule            `yaml:"routes"`
	Models           []CandidateConfig      `yaml:"models"`
}

// RouteRule is one declarative routing rule read from config `routes:`. When the
// `when` conditions all match the current request facts, the Policy Engine routes
// to `model` (and optionally `provider`). Rules are matched by specificity, so
// unset conditions are treated as wildcards. Absent `routes:` keeps legacy behavior.
type RouteRule struct {
	When  RouteCondition `yaml:"when"`
	Model string         `yaml:"model"`
	// Provider disambiguates when the same model id exists under multiple providers.
	Provider string `yaml:"provider"`
}

// RouteCondition is the `when` block of a RouteRule. Every field is optional; an
// empty field is a wildcard that matches anything. This keeps rules terse and
// avoids scattered conditionals: the Policy Engine evaluates them table-driven.
type RouteCondition struct {
	// Task matches the detected Intent (e.g. coding, review, security).
	Task string `yaml:"task"`
	// Language matches the detected programming language (e.g. go, python).
	Language string `yaml:"language"`
	// Complexity matches a coarse bucket: low, medium, high, very_high.
	Complexity string `yaml:"complexity"`
	// ComplexityMin/Max match the numeric complexity score range [0,100].
	ComplexityMin *int `yaml:"complexity_min"`
	ComplexityMax *int `yaml:"complexity_max"`
	// MinFiles matches when the request references at least this many files.
	MinFiles *int `yaml:"min_files"`
	// HasDiff, when set, matches only requests that do (true) or do not (false) carry a diff.
	HasDiff *bool `yaml:"has_diff"`
	// Stream, when set, matches only streaming (true) or non-streaming (false) requests.
	Stream *bool `yaml:"stream"`
}

// DebugConfig controls non-sensitive route decision logs.
type DebugConfig struct {
	Enabled bool   `yaml:"enabled"`
	LogPath string `yaml:"log_path"`
}

// CatalogConfig controls live catalog refresh from CLIProxyAPI /v1/models.
type CatalogConfig struct {
	Source             string `yaml:"source"`
	BaseURL            string `yaml:"base_url"`
	APIKey             string `yaml:"api_key"`
	RefreshInterval    string `yaml:"refresh_interval"`
	IncludeRouterModel bool   `yaml:"include_router_model"`
}

// PricingConfig controls external model pricing refresh.
type PricingConfig struct {
	Enabled         bool   `yaml:"enabled"`
	URL             string `yaml:"url"`
	RefreshInterval string `yaml:"refresh_interval"`
}

// CacheConfig controls route decision caching.
type CacheConfig struct {
	Enabled    bool   `yaml:"enabled"`
	MaxEntries int    `yaml:"max_entries"`
	TTL        string `yaml:"ttl"`
}

// ExecutorFallbackConfig controls same-request non-streaming fallback.
type ExecutorFallbackConfig struct {
	Enabled     bool `yaml:"enabled"`
	MaxAttempts int  `yaml:"max_attempts"`
}

// ClassifierConfig controls optional LLM-based classification.
type ClassifierConfig struct {
	Enabled     bool              `yaml:"enabled"`
	Models      []ClassifierModel `yaml:"models"`
	Timeout     string            `yaml:"timeout"`
	MaxAttempts int               `yaml:"max_attempts"`
}

// ClassifierModel is one ordered fallback classifier target.
type ClassifierModel struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// RoutingConfig controls policy-level routing preferences.
type RoutingConfig struct {
	PreferLowCost           bool    `yaml:"prefer_low_cost"`
	PreferLowLatency        bool    `yaml:"prefer_low_latency"`
	PreferHighQuality       bool    `yaml:"prefer_high_quality"`
	MaxCostPerRequest       float64 `yaml:"max_cost_per_request"`
	MaxInputTokens          int64   `yaml:"max_input_tokens"`
	KeepSameModelPerSession bool    `yaml:"keep_same_model_per_session"`
	AllowFallback           bool    `yaml:"allow_fallback"`
	SwitchThreshold         float64 `yaml:"switch_threshold"`
	BenchmarkWeight         float64 `yaml:"benchmark_weight"`
	LLMRouterWeight         float64 `yaml:"llm_router_weight"`
	CapabilityWeight        float64 `yaml:"capability_weight"`
}

// CandidateConfig describes one routable provider/model candidate.
type CandidateConfig struct {
	Provider     string   `yaml:"provider"`
	Model        string   `yaml:"model"`
	Capabilities []string `yaml:"capabilities"`
	Cost         string   `yaml:"cost"`
	Quality      string   `yaml:"quality"`
}

// DefaultConfig returns a safe default configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:      true,
		VirtualModel: DefaultVirtualModel,
		Strategy:     DefaultStrategy,
		Preference:   DefaultPreference,
		Routing: RoutingConfig{
			PreferLowCost:           true,
			KeepSameModelPerSession: true,
			AllowFallback:           true,
			SwitchThreshold:         0.15,
			BenchmarkWeight:         0.4,
			LLMRouterWeight:         0.3,
			CapabilityWeight:        0.3,
		},
		Cache:            CacheConfig{Enabled: true, MaxEntries: 1024},
		ExecutorFallback: ExecutorFallbackConfig{MaxAttempts: 3},
	}
}

// UnmarshalYAML seeds the entry with code defaults before decoding, so a YAML
// entry that only sets name+models still gets the same cache, session-affinity,
// and fallback defaults as the legacy top-level fields. Decoding only touches
// keys present in the YAML, so explicitly set values (including false) always
// win over the seeded defaults.
func (e *VirtualModelEntry) UnmarshalYAML(value *yaml.Node) error {
	def := DefaultConfig()
	e.Strategy = def.Strategy
	e.Preference = def.Preference
	e.Cache = def.Cache
	e.ExecutorFallback = def.ExecutorFallback
	e.Routing = def.Routing
	type plainEntry VirtualModelEntry
	return value.Decode((*plainEntry)(e))
}

// ResolvedEntry is one fully-resolved routable virtual model: its name plus a
// complete routing configuration. Entries never inherit routing fields from the
// top level; unset entry fields fall back to code defaults (the same defaults
// DefaultConfig applies to the legacy top-level fields).
type ResolvedEntry struct {
	Name             string
	Strategy         string
	Preference       string
	Cache            CacheConfig
	ExecutorFallback ExecutorFallbackConfig
	Classifier       ClassifierConfig
	Routing          RoutingConfig
	Routes           []RouteRule
	Models           []CandidateConfig
}

// Candidates converts the entry's models into normalized routing candidates.
func (e ResolvedEntry) Candidates() []Candidate {
	out := make([]Candidate, 0, len(e.Models))
	for _, item := range e.Models {
		out = append(out, CandidateFromConfig(item))
	}
	return out
}

// ResolveEntries returns every routable virtual model. When `virtual_models`
// holds at least one named entry, each entry resolves independently. Otherwise
// the legacy top-level routing fields form the single entry, so old configs
// keep working unchanged.
func (c Config) ResolveEntries() []ResolvedEntry {
	cfg := c.Normalize()
	if len(cfg.VirtualModels) == 0 {
		return []ResolvedEntry{{
			Name:             cfg.VirtualModel,
			Strategy:         cfg.Strategy,
			Preference:       cfg.Preference,
			Cache:            cfg.Cache,
			ExecutorFallback: cfg.ExecutorFallback,
			Classifier:       cfg.Classifier,
			Routing:          cfg.Routing,
			Routes:           cfg.Routes,
			Models:           cfg.Models,
		}}
	}
	out := make([]ResolvedEntry, 0, len(cfg.VirtualModels))
	seen := make(map[string]struct{}, len(cfg.VirtualModels))
	for _, item := range cfg.VirtualModels {
		if _, dup := seen[item.Name]; dup {
			continue
		}
		seen[item.Name] = struct{}{}
		out = append(out, ResolvedEntry{
			Name:             item.Name,
			Strategy:         item.Strategy,
			Preference:       item.Preference,
			Cache:            item.Cache,
			ExecutorFallback: item.ExecutorFallback,
			Classifier:       item.Classifier,
			Routing:          item.Routing,
			Routes:           item.Routes,
			Models:           item.Models,
		})
	}
	return out
}

// stripThinkingSuffix removes a host thinking suffix ("model(high)" -> "model")
// so entry lookup works even if the host forwards the raw requested name.
// The host normally strips suffixes before routing; this is belt and braces.
func stripThinkingSuffix(name string) string {
	open := strings.LastIndex(name, "(")
	if open <= 0 || !strings.HasSuffix(name, ")") {
		return name
	}
	base := strings.TrimSpace(name[:open])
	suffix := strings.TrimSpace(name[open+1 : len(name)-1])
	if base == "" || suffix == "" {
		return name
	}
	return base
}

// LookupEntry finds the entry whose name exactly matches the requested model.
// Matching is case-sensitive (like the legacy single-model check); callers
// trim the requested name before lookup. A thinking suffix is stripped first.
func (c Config) LookupEntry(name string) (ResolvedEntry, bool) {
	name = stripThinkingSuffix(strings.TrimSpace(name))
	if name == "" {
		return ResolvedEntry{}, false
	}
	for _, entry := range c.ResolveEntries() {
		if entry.Name == name {
			return entry, true
		}
	}
	return ResolvedEntry{}, false
}

// WithEntry returns an entry-scoped Config copy for the requested model name:
// VirtualModel plus all routing fields come from the matched entry, while
// plugin-wide fields (Enabled, StatePath, Debug, Catalog, Pricing) stay from
// the top level. Downstream code keeps using Config unchanged.
func (c Config) WithEntry(name string) (Config, bool) {
	entry, ok := c.LookupEntry(name)
	if !ok {
		return Config{}, false
	}
	scoped := c.Normalize()
	scoped.VirtualModel = entry.Name
	scoped.Strategy = entry.Strategy
	scoped.Preference = entry.Preference
	scoped.Cache = entry.Cache
	scoped.ExecutorFallback = entry.ExecutorFallback
	scoped.Classifier = entry.Classifier
	scoped.Routing = entry.Routing
	scoped.Routes = entry.Routes
	scoped.Models = entry.Models
	return scoped, true
}

// VirtualModelNames returns every routable virtual model name in config order.
func (c Config) VirtualModelNames() []string {
	entries := c.ResolveEntries()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

// cloneStrings copies a string slice so normalization never mutates shared backing arrays.
func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// cloneCandidateConfigs deep-copies candidates (including capability lists).
func cloneCandidateConfigs(in []CandidateConfig) []CandidateConfig {
	if in == nil {
		return nil
	}
	out := make([]CandidateConfig, len(in))
	copy(out, in)
	for i := range out {
		out[i].Capabilities = cloneStrings(in[i].Capabilities)
	}
	return out
}

// cloneClassifierModels copies the ordered classifier targets.
func cloneClassifierModels(in []ClassifierModel) []ClassifierModel {
	if in == nil {
		return nil
	}
	out := make([]ClassifierModel, len(in))
	copy(out, in)
	return out
}

// cloneRoutes copies route rules. Condition scalar pointers are read-only
// during normalization and routing, so sharing them is safe.
func cloneRoutes(in []RouteRule) []RouteRule {
	if in == nil {
		return nil
	}
	out := make([]RouteRule, len(in))
	copy(out, in)
	return out
}

// cloneEntries deep-copies virtual model entries with their nested slices.
func cloneEntries(in []VirtualModelEntry) []VirtualModelEntry {
	if in == nil {
		return nil
	}
	out := make([]VirtualModelEntry, len(in))
	copy(out, in)
	for i := range out {
		out[i].Models = cloneCandidateConfigs(in[i].Models)
		out[i].Routes = cloneRoutes(in[i].Routes)
		out[i].Classifier.Models = cloneClassifierModels(in[i].Classifier.Models)
	}
	return out
}

// EffectiveCacheMaxEntries returns the route-cache capacity for the shared
// global cache map: the largest max_entries among cache-enabled entries.
// The map is shared across entries (keys already include the requested model),
// so per-entry values cannot partition it; using the max keeps a small entry
// from evicting every other entry's routes.
func (c Config) EffectiveCacheMaxEntries() int {
	max := 0
	for _, entry := range c.ResolveEntries() {
		if entry.Cache.Enabled && entry.Cache.MaxEntries > max {
			max = entry.Cache.MaxEntries
		}
	}
	if max <= 0 {
		return 1024
	}
	return max
}

// Normalize fills defaults and trims user-provided strings.
// It deep-copies every slice first: ConfigStore holds one shared Config value
// and Load hands out shallow copies, so in-place normalization would race on
// the shared backing arrays under concurrent multi-model requests.
func (c Config) Normalize() Config {
	c.Models = cloneCandidateConfigs(c.Models)
	c.Routes = cloneRoutes(c.Routes)
	c.Classifier.Models = cloneClassifierModels(c.Classifier.Models)
	c.VirtualModels = cloneEntries(c.VirtualModels)
	if strings.TrimSpace(c.VirtualModel) == "" {
		c.VirtualModel = DefaultVirtualModel
	}
	if strings.TrimSpace(c.Strategy) == "" {
		c.Strategy = DefaultStrategy
	}
	c.VirtualModel = strings.TrimSpace(c.VirtualModel)
	c.Strategy = strings.ToLower(strings.TrimSpace(c.Strategy))
	c.Preference = normalizePreference(c.Preference)
	c.StatePath = strings.TrimSpace(c.StatePath)
	c.Debug.LogPath = strings.TrimSpace(c.Debug.LogPath)
	c.Catalog.Source = strings.TrimSpace(c.Catalog.Source)
	c.Catalog.BaseURL = strings.TrimRight(strings.TrimSpace(c.Catalog.BaseURL), "/")
	c.Catalog.APIKey = strings.TrimSpace(c.Catalog.APIKey)
	c.Catalog.RefreshInterval = strings.TrimSpace(c.Catalog.RefreshInterval)
	c.Pricing.URL = strings.TrimSpace(c.Pricing.URL)
	c.Pricing.RefreshInterval = strings.TrimSpace(c.Pricing.RefreshInterval)
	normalizeCache(&c.Cache)
	normalizeExecutorFallback(&c.ExecutorFallback)
	normalizeClassifier(&c.Classifier)
	normalizeCandidates(c.Models)
	normalizeRoutes(c.Routes)
	for i := range c.VirtualModels {
		entry := &c.VirtualModels[i]
		entry.Name = strings.TrimSpace(entry.Name)
		if strings.TrimSpace(entry.Strategy) == "" {
			entry.Strategy = DefaultStrategy
		}
		entry.Strategy = strings.ToLower(strings.TrimSpace(entry.Strategy))
		entry.Preference = normalizePreference(entry.Preference)
		normalizeCache(&entry.Cache)
		normalizeExecutorFallback(&entry.ExecutorFallback)
		normalizeClassifier(&entry.Classifier)
		normalizeCandidates(entry.Models)
		normalizeRoutes(entry.Routes)
	}
	kept := c.VirtualModels[:0]
	for _, entry := range c.VirtualModels {
		if entry.Name != "" {
			kept = append(kept, entry)
		}
	}
	c.VirtualModels = kept
	return c
}

// normalizePreference lowercases the preference and falls back to balanced.
func normalizePreference(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PreferenceCost, PreferenceBalanced, PreferenceQuality:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return DefaultPreference
	}
}

// normalizeCache applies the cache entry-count default and trims the TTL.
func normalizeCache(cache *CacheConfig) {
	if cache.MaxEntries <= 0 {
		cache.MaxEntries = 1024
	}
	cache.TTL = strings.TrimSpace(cache.TTL)
}

// normalizeExecutorFallback applies the max-attempts default.
func normalizeExecutorFallback(fallback *ExecutorFallbackConfig) {
	if fallback.MaxAttempts <= 0 {
		fallback.MaxAttempts = 3
	}
}

// normalizeClassifier trims classifier fields and candidate references.
func normalizeClassifier(classifier *ClassifierConfig) {
	classifier.Timeout = strings.TrimSpace(classifier.Timeout)
	for i := range classifier.Models {
		classifier.Models[i].Provider = strings.ToLower(strings.TrimSpace(classifier.Models[i].Provider))
		classifier.Models[i].Model = strings.TrimSpace(classifier.Models[i].Model)
	}
}

// normalizeCandidates trims provider/model/cost/quality per candidate.
func normalizeCandidates(models []CandidateConfig) {
	for i := range models {
		models[i].Provider = strings.ToLower(strings.TrimSpace(models[i].Provider))
		models[i].Model = strings.TrimSpace(models[i].Model)
		models[i].Cost = strings.ToLower(strings.TrimSpace(models[i].Cost))
		models[i].Quality = strings.ToLower(strings.TrimSpace(models[i].Quality))
	}
}

// normalizeRoutes trims rule targets and match conditions.
func normalizeRoutes(routes []RouteRule) {
	for i := range routes {
		routes[i].Model = strings.TrimSpace(routes[i].Model)
		routes[i].Provider = strings.ToLower(strings.TrimSpace(routes[i].Provider))
		routes[i].When.Task = strings.ToLower(strings.TrimSpace(routes[i].When.Task))
		routes[i].When.Language = strings.ToLower(strings.TrimSpace(routes[i].When.Language))
		routes[i].When.Complexity = strings.ToLower(strings.TrimSpace(routes[i].When.Complexity))
	}
}

// EnabledForRouting reports whether the plugin should handle route requests.
func (c Config) EnabledForRouting() bool {
	return c.Normalize().Enabled
}

// Candidates converts the configured models into normalized routing candidates.
// It is the single conversion shared by the Router, Policy Engine wiring, and
// preference tiebreak so no caller re-implements the models -> candidates loop.
func (c Config) Candidates() []Candidate {
	out := make([]Candidate, 0, len(c.Models))
	for _, item := range c.Models {
		out = append(out, CandidateFromConfig(item))
	}
	return out
}
