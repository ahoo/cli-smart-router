package infrastructure

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"
)

// defaultMaxEntries bounds the in-memory maps (route cache, sessions, fallback
// chains) so a long-running proxy does not accumulate one entry per session
// forever. It is the same default used for the route cache when unconfigured.
const defaultMaxEntries = 1024

// RuntimeState stores non-sensitive router state.
type RuntimeState struct {
	mu             sync.Mutex
	data           RuntimeStateData
	lastDiskSave   time.Time
	fallbackChains map[string]FallbackChain
}

// FallbackChain is an ephemeral, in-memory ordered list of policy-allowed targets
// for a session, used by the executor path to try the next allowed model on
// failure. It is never persisted (no sensitive data, and it is short-lived).
type FallbackChain struct {
	Providers []string
	Models    []string
	// LastUsed drives LRU eviction so the in-memory map stays bounded; it is not
	// persisted (the whole map is ephemeral).
	LastUsed time.Time
}

// RuntimeStateData is persisted as JSON when state_path is configured.
type RuntimeStateData struct {
	Catalog       CatalogSnapshot            `json:"catalog"`
	Pricing       PricingSnapshot            `json:"pricing"`
	Usage         map[string]UsageStats      `json:"usage"`
	Sessions      map[string]RouteCacheEntry `json:"sessions"`
	RouteCache    map[string]RouteCacheEntry `json:"route_cache"`
	Counters      map[string]int64           `json:"counters"`
	LastDecision  *DecisionSnapshot          `json:"last_decision,omitempty"`
	LastStateSave time.Time                  `json:"last_state_save"`
}

// DecisionSnapshot is the last Decision Engine outcome, exposed for observability
// via the management status endpoint and debug logs. It contains only
// non-sensitive routing metadata (never prompts, bodies, or credentials).
type DecisionSnapshot struct {
	Task           string    `json:"task"`
	Language       string    `json:"language"`
	Score          int       `json:"score"`
	Policy         string    `json:"policy"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	Reason         string    `json:"reason"`
	Matched        bool      `json:"matched"`
	DecisionTimeUS int64     `json:"decision_time_us"`
	At             time.Time `json:"at"`
}

// CatalogSnapshot is the last successful /v1/models result.
type CatalogSnapshot struct {
	Models    []string  `json:"models"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
}

// PricingSnapshot is the last successful pricing fetch metadata.
type PricingSnapshot struct {
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
	Bytes     int       `json:"bytes"`
}

// UsageStats stores aggregate usage by provider/model.
type UsageStats struct {
	Requests     int64         `json:"requests"`
	Failures     int64         `json:"failures"`
	TotalLatency time.Duration `json:"total_latency"`
	TotalTokens  int64         `json:"total_tokens"`
}

// RouteCacheEntry stores a non-sensitive route decision.
type RouteCacheEntry struct {
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
}

// NewRuntimeState creates empty runtime state.
func NewRuntimeState() *RuntimeState {
	return &RuntimeState{
		data: RuntimeStateData{
			Usage:      map[string]UsageStats{},
			Sessions:   map[string]RouteCacheEntry{},
			RouteCache: map[string]RouteCacheEntry{},
			Counters:   map[string]int64{},
		},
		fallbackChains: map[string]FallbackChain{},
	}
}

// SetFallbackChain stores the ordered policy-allowed targets for a session.
func (s *RuntimeState) SetFallbackChain(sessionID string, providers, models []string) {
	if s == nil || strings.TrimSpace(sessionID) == "" || len(models) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fallbackChains == nil {
		s.fallbackChains = map[string]FallbackChain{}
	}
	if _, exists := s.fallbackChains[sessionID]; !exists {
		for len(s.fallbackChains) >= defaultMaxEntries {
			evictLRU(s.fallbackChains, func(c FallbackChain) time.Time { return c.LastUsed })
		}
	}
	s.fallbackChains[sessionID] = FallbackChain{
		Providers: append([]string(nil), providers...),
		Models:    append([]string(nil), models...),
		LastUsed:  time.Now(),
	}
}

// GetFallbackChain returns the stored fallback chain for a session, if any.
func (s *RuntimeState) GetFallbackChain(sessionID string) (FallbackChain, bool) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return FallbackChain{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	chain, ok := s.fallbackChains[sessionID]
	if !ok || len(chain.Models) == 0 {
		return FallbackChain{}, false
	}
	chain.LastUsed = time.Now()
	s.fallbackChains[sessionID] = chain
	return chain, true
}

// LoadFromFile loads state from path when available.
func (s *RuntimeState) LoadFromFile(path string) error {
	path = strings.TrimSpace(path)
	if s == nil || path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var data RuntimeStateData
	if err := json.Unmarshal(raw, &data); err != nil {
		return err
	}
	if data.Usage == nil {
		data.Usage = map[string]UsageStats{}
	}
	if data.Sessions == nil {
		data.Sessions = map[string]RouteCacheEntry{}
	}
	if data.RouteCache == nil {
		data.RouteCache = map[string]RouteCacheEntry{}
	}
	if data.Counters == nil {
		data.Counters = map[string]int64{}
	}
	s.mu.Lock()
	s.data = data
	s.mu.Unlock()
	return nil
}

// SaveToFile persists state without prompts, request bodies, or credentials.
func (s *RuntimeState) SaveToFile(path string) error {
	path = strings.TrimSpace(path)
	if s == nil || path == "" {
		return nil
	}
	s.mu.Lock()
	s.data.LastStateSave = time.Now()
	data := s.data
	s.mu.Unlock()
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}

// SaveThrottled persists state at most once per interval to avoid rewriting the
// whole file on every request. Returns true when a write actually happened.
func (s *RuntimeState) SaveThrottled(path string, interval time.Duration) bool {
	path = strings.TrimSpace(path)
	if s == nil || path == "" {
		return false
	}
	s.mu.Lock()
	if interval > 0 && !s.lastDiskSave.IsZero() && time.Since(s.lastDiskSave) < interval {
		s.mu.Unlock()
		return false
	}
	s.lastDiskSave = time.Now()
	s.mu.Unlock()
	return s.SaveToFile(path) == nil
}

// Snapshot returns a copy of current state data.
func (s *RuntimeState) Snapshot() RuntimeStateData {
	if s == nil {
		return RuntimeStateData{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.data)
	var out RuntimeStateData
	_ = json.Unmarshal(raw, &out)
	return out
}

// Inc increments a named counter.
func (s *RuntimeState) Inc(name string) {
	if s == nil || strings.TrimSpace(name) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Counters == nil {
		s.data.Counters = map[string]int64{}
	}
	s.data.Counters[name]++
}

// SetLastDecision stores the last Decision Engine outcome for observability.
func (s *RuntimeState) SetLastDecision(snapshot DecisionSnapshot) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := snapshot
	s.data.LastDecision = &copy
}

// SetCatalog stores the last catalog result.
func (s *RuntimeState) SetCatalog(models []string, errText string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Catalog = CatalogSnapshot{Models: append([]string(nil), models...), FetchedAt: time.Now(), Error: errText}
}

// SetPricing stores the last pricing fetch result metadata.
func (s *RuntimeState) SetPricing(bytes int, errText string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Pricing = PricingSnapshot{FetchedAt: time.Now(), Error: errText, Bytes: bytes}
}

// RecordUsage stores aggregate usage without sensitive payloads.
func (s *RuntimeState) RecordUsage(record UsageRecord) {
	if s == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(record.Provider)) + "/" + strings.TrimSpace(record.Model)
	if key == "/" {
		key = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Usage == nil {
		s.data.Usage = map[string]UsageStats{}
	}
	stats := s.data.Usage[key]
	stats.Requests++
	if record.Failed {
		stats.Failures++
	}
	stats.TotalLatency += record.Latency
	stats.TotalTokens += record.Detail.TotalTokens
	s.data.Usage[key] = stats
}

// GetCachedRoute returns a cached route by key, refreshing its LRU timestamp.
// Entries older than ttl (when ttl > 0) are treated as expired and removed.
func (s *RuntimeState) GetCachedRoute(key string, ttl time.Duration) (RouteCacheEntry, bool) {
	if s == nil || key == "" {
		return RouteCacheEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data.RouteCache[key]
	if !ok {
		return RouteCacheEntry{}, false
	}
	if ttl > 0 && time.Since(entry.CreatedAt) > ttl {
		delete(s.data.RouteCache, key)
		return RouteCacheEntry{}, false
	}
	entry.LastUsed = time.Now()
	s.data.RouteCache[key] = entry
	return entry, true
}

// SetCachedRoute stores a cached route and evicts the least recently used entry when full.
func (s *RuntimeState) SetCachedRoute(key string, entry RouteCacheEntry, maxEntries int) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.RouteCache == nil {
		s.data.RouteCache = map[string]RouteCacheEntry{}
	}
	if maxEntries <= 0 {
		maxEntries = defaultMaxEntries
	}
	if entry.LastUsed.IsZero() {
		entry.LastUsed = time.Now()
	}
	if _, exists := s.data.RouteCache[key]; !exists {
		for len(s.data.RouteCache) >= maxEntries {
			evictLRU(s.data.RouteCache, func(e RouteCacheEntry) time.Time { return e.LastUsed })
		}
	}
	s.data.RouteCache[key] = entry
}

// evictLRU removes the least recently used entry from any map keyed by string,
// using lastUsed to read each value's recency timestamp. It is the single
// eviction implementation shared by the route cache, sessions, and fallback
// chains so all bounded maps evict identically. Caller must hold the lock.
func evictLRU[V any](m map[string]V, lastUsed func(V) time.Time) {
	var oldestKey string
	var oldest time.Time
	first := true
	for key, value := range m {
		used := lastUsed(value)
		if first || used.Before(oldest) {
			oldestKey = key
			oldest = used
			first = false
		}
	}
	if oldestKey != "" {
		delete(m, oldestKey)
	}
}

// GetSessionRoute returns a pinned session route.
func (s *RuntimeState) GetSessionRoute(sessionID string) (RouteCacheEntry, bool) {
	if s == nil || sessionID == "" {
		return RouteCacheEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.data.Sessions[sessionID]
	if !ok {
		return RouteCacheEntry{}, false
	}
	entry.LastUsed = time.Now()
	s.data.Sessions[sessionID] = entry
	return entry, true
}

// DeleteSessionRoute removes a pinned session route, if present.
func (s *RuntimeState) DeleteSessionRoute(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Sessions, sessionID)
}

// SetSessionRoute pins a route to a session, evicting the least recently used
// session when the map is full so it stays bounded on long-running proxies.
func (s *RuntimeState) SetSessionRoute(sessionID string, entry RouteCacheEntry) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Sessions == nil {
		s.data.Sessions = map[string]RouteCacheEntry{}
	}
	if entry.LastUsed.IsZero() {
		entry.LastUsed = time.Now()
	}
	if _, exists := s.data.Sessions[sessionID]; !exists {
		for len(s.data.Sessions) >= defaultMaxEntries {
			evictLRU(s.data.Sessions, func(e RouteCacheEntry) time.Time { return e.LastUsed })
		}
	}
	s.data.Sessions[sessionID] = entry
}
