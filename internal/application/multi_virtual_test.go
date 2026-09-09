package application

import (
	"testing"

	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
)

func multiConfig() domain.Config {
	return domain.Config{
		Enabled: true,
		VirtualModels: []domain.VirtualModelEntry{
			{
				Name:       "router-cheap",
				Strategy:   "capability",
				Preference: "cost",
				Models: []domain.CandidateConfig{
					{Provider: "codex", Model: "gpt-5.4-mini", Cost: "low", Quality: "medium"},
				},
			},
			{
				Name:       "router-security",
				Strategy:   "decision_engine",
				Preference: "quality",
				Models: []domain.CandidateConfig{
					{Provider: "claude", Model: "claude-opus-4-8", Cost: "very_high", Quality: "highest"},
				},
				Routes: []domain.RouteRule{
					{When: domain.RouteCondition{Task: "security"}, Provider: "claude", Model: "claude-opus-4-8"},
				},
			},
		},
	}.Normalize()
}

func TestRegistrarRegistersAllVirtualModels(t *testing.T) {
	got := (Registrar{Config: multiConfig()}).Register()
	if len(got.Models) != 2 {
		t.Fatalf("models = %#v, want 2", got.Models)
	}
	if got.Models[0].ID != "router-cheap" || got.Models[1].ID != "router-security" {
		t.Fatalf("models = %#v", got.Models)
	}
}

func TestRouterRoutesPerEntry(t *testing.T) {
	cfg := multiConfig()
	router := Router{Config: cfg}

	cheap := router.Route(infrastructure.ModelRouteRequest{
		RequestedModel:     "router-cheap",
		AvailableProviders: []string{"codex", "claude"},
	})
	if !cheap.Handled || cheap.TargetModel != "gpt-5.4-mini" {
		t.Fatalf("cheap = %#v", cheap)
	}

	sec := router.Route(infrastructure.ModelRouteRequest{
		RequestedModel:     "router-security",
		AvailableProviders: []string{"codex", "claude"},
	})
	if !sec.Handled || sec.TargetModel != "claude-opus-4-8" {
		t.Fatalf("security = %#v", sec)
	}

	unknown := router.Route(infrastructure.ModelRouteRequest{
		RequestedModel:     "router:auto",
		AvailableProviders: []string{"codex", "claude"},
	})
	if unknown.Handled {
		t.Fatalf("unknown virtual model should not be handled: %#v", unknown)
	}
}

func TestRouterLegacySingleModelStillWorks(t *testing.T) {
	cfg := domain.DefaultConfig()
	cfg.Models = []domain.CandidateConfig{{Provider: "codex", Model: "gpt-5.4-mini"}}
	router := Router{Config: cfg}
	got := router.Route(infrastructure.ModelRouteRequest{
		RequestedModel:     domain.DefaultVirtualModel,
		AvailableProviders: []string{"codex"},
	})
	if !got.Handled || got.TargetModel != "gpt-5.4-mini" {
		t.Fatalf("legacy = %#v", got)
	}
}
