package application

import (
	"time"

	"github.com/vfeitoza/cli-smart-router/internal/domain"
	"github.com/vfeitoza/cli-smart-router/internal/infrastructure"
)

const providerName = "smart-model-router"

// Registrar handles virtual model registration.
type Registrar struct {
	Config domain.Config
}

// Register returns one entry per configured virtual model.
func (r Registrar) Register() infrastructure.ModelRegistrationResponse {
	cfg := r.Config.Normalize()
	entries := cfg.ResolveEntries()
	models := make([]infrastructure.ModelInfo, 0, len(entries))
	now := time.Now().Unix()
	for _, entry := range entries {
		models = append(models, infrastructure.ModelInfo{
			ID:                         entry.Name,
			Object:                     "model",
			Created:                    now,
			OwnedBy:                    providerName,
			DisplayName:                "Smart Model Router",
			Description:                "Virtual model routed by smart-model-router.",
			SupportedGenerationMethods: []string{"chat"},
			UserDefined:                true,
		})
	}
	return infrastructure.ModelRegistrationResponse{
		Provider: providerName,
		Models:   models,
	}
}
