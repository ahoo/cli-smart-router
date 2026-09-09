package application

import (
	"fmt"

	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
)

// Router handles the model routing use case.
type Router struct {
	Config domain.Config
}

// Route chooses a provider/model for the requested virtual model. Requests for
// models outside the configured virtual model set return Handled:false.
func (r Router) Route(req infrastructure.ModelRouteRequest) domain.RouteDecision {
	cfg := r.Config.Normalize()
	if !cfg.Enabled {
		return domain.RouteDecision{Handled: false}
	}
	scoped, ok := cfg.WithEntry(req.RequestedModel)
	if !ok {
		return domain.RouteDecision{Handled: false}
	}
	cfg = scoped
	candidates := cfg.Candidates()
	prompt := infrastructure.ExtractUserPrompt(req.Body)
	score, ok := domain.SelectCandidateWithConfidence(candidates, req.AvailableProviders, prompt, cfg.Preference)
	if !ok {
		return domain.RouteDecision{Handled: false, Reason: "no_available_candidate"}
	}
	candidate := score.Candidate
	return domain.RouteDecision{
		Handled:        true,
		TargetProvider: candidate.Provider,
		TargetModel:    candidate.Model,
		Reason:         fmt.Sprintf("deterministic_fallback strategy:%s %s provider:%s cost:%s", cfg.Strategy, score.Reason, candidate.Provider, candidate.Cost),
		Confident:      score.LocalConfident(),
	}
}
