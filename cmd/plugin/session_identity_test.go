package main

import (
	"net/http"
	"testing"

	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
)

func TestSessionIdentityPrefersHostMetadata(t *testing.T) {
	headers := http.Header{"X-Claude-Code-Session-Id": {"abc"}}
	got := sessionIdentity(map[string]any{"execution_session_id": "host-session"}, headers)
	if got != "host-session" {
		t.Fatalf("identity = %q, want the host-supplied session", got)
	}
}

// Plain HTTP requests carry no execution_session_id, so the client header is the
// only thing that keeps session affinity working for /v1/messages.
func TestSessionIdentityFallsBackToClientHeader(t *testing.T) {
	headers := http.Header{"X-Claude-Code-Session-Id": {"  c9c001b0-09c2-4893  "}}
	if got := sessionIdentity(nil, headers); got != "X-Claude-Code-Session-Id:c9c001b0-09c2-4893" {
		t.Fatalf("identity = %q", got)
	}
}

func TestSessionIdentityHeaderPreferenceOrder(t *testing.T) {
	headers := http.Header{
		"X-Session-Id":             {"secondary"},
		"X-Claude-Code-Session-Id": {"primary"},
		"X-Conversation-Id":        {"tertiary"},
	}
	if got := sessionIdentity(nil, headers); got != "X-Claude-Code-Session-Id:primary" {
		t.Fatalf("identity = %q, want the highest-priority header", got)
	}
}

func TestSessionIdentityEmptyWhenNoSignal(t *testing.T) {
	if got := sessionIdentity(nil, http.Header{"User-Agent": {"x"}}); got != "" {
		t.Fatalf("identity = %q, want empty", got)
	}
	if got := sessionIdentity(map[string]any{"execution_session_id": "  "}, nil); got != "" {
		t.Fatalf("identity = %q, want empty for blank metadata", got)
	}
}

// The router writes the pin and the executor reads the chain; both must derive
// the same key or fallback chains silently stop being found.
func TestSessionIdentityIsStableAcrossRequestTypes(t *testing.T) {
	headers := http.Header{"X-Claude-Code-Session-Id": {"shared"}}
	route := sessionIdentity(nil, headers)
	executor := sessionIdentity(nil, headers)
	if route != executor || route == "" {
		t.Fatalf("route=%q executor=%q, want the same non-empty key", route, executor)
	}
}

// A header value must not be mistaken for a host session id when both are absent
// vs present; the namespacing keeps them distinct namespaces.
func TestSessionIdentityDistinguishesSources(t *testing.T) {
	fromMetadata := sessionIdentity(map[string]any{"execution_session_id": "same"}, nil)
	fromHeader := sessionIdentity(nil, http.Header{"X-Session-Id": {"same"}})
	if fromMetadata == fromHeader {
		t.Fatal("metadata and header identities must not collide")
	}
}

// sessionConfig builds a minimal single-entry config with session affinity on,
// mirroring what the deployed virtual models use.
func sessionConfig() domain.Config {
	cfg := domain.DefaultConfig()
	cfg.Enabled = true
	cfg.VirtualModel = "router-cheap"
	cfg.Routing.KeepSameModelPerSession = true
	cfg.Cache.Enabled = false // keep the test focused on the session pin
	return cfg.Normalize()
}

func TestSessionRouteUsesHeaderIdentity(t *testing.T) {
	runtimeState = infrastructure.NewRuntimeState()
	cfg := sessionConfig()
	req := infrastructure.ModelRouteRequest{
		RequestedModel: "router-cheap",
		Headers:        http.Header{"X-Claude-Code-Session-Id": {"sess-1"}},
	}
	entry := infrastructure.RouteCacheEntry{Provider: "codex", Model: "muse-free(high)"}
	storeRoute(cfg, cfg, req, entry)

	got, ok := sessionRoute(cfg, req)
	if !ok {
		t.Fatal("expected a session pin stored under the header identity")
	}
	if got.Model != entry.Model {
		t.Fatalf("pinned model = %q, want %q", got.Model, entry.Model)
	}
}

func TestSessionRouteSkipsWhenSessionAffinityDisabled(t *testing.T) {
	runtimeState = infrastructure.NewRuntimeState()
	cfg := sessionConfig()
	cfg.Routing.KeepSameModelPerSession = false
	req := infrastructure.ModelRouteRequest{Headers: http.Header{"X-Claude-Code-Session-Id": {"sess-2"}}}
	if _, ok := sessionRoute(cfg, req); ok {
		t.Fatal("expected no pin when keep_same_model_per_session is false")
	}
}
