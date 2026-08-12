package claude

import (
	"fmt"
	"strings"
)

const (
	// AutomaticRoute is the Provider-neutral logical route used by product
	// callers. It is resolved only at the Claude Adapter edge.
	AutomaticRoute = "workcairn-auto"
	// AutomaticModelPolicyVersion binds durable commands to the supported-model
	// policy that selected their concrete Provider model.
	AutomaticModelPolicyVersion = "workcairn-claude-model-policy.v1"
	// automaticProviderModel is the supported connection default for the
	// Anthropic Messages API. Concrete model IDs stay inside this Adapter.
	automaticProviderModel = "claude-sonnet-5"
)

// ModelResolution is Provider-edge routing evidence. ProviderModel must never
// be exposed through the Local Web UI or Provider status API.
type ModelResolution struct {
	LogicalRoute  string
	PolicyVersion string
	ProviderModel string
}

// ResolveModel selects the concrete Claude model managed by WorkCairn. An
// explicitly injected Provider model is treated as an Adapter override for
// tests and future Advanced Settings; it is never inferred or used as fallback.
func ResolveModel(requested string) (ModelResolution, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == AutomaticRoute {
		if automaticProviderModel == "" {
			return ModelResolution{}, fmt.Errorf("%w: automatic Claude model is unavailable", ErrInvalidConfig)
		}
		return ModelResolution{
			LogicalRoute: AutomaticRoute, PolicyVersion: AutomaticModelPolicyVersion, ProviderModel: automaticProviderModel,
		}, nil
	}
	if strings.HasPrefix(requested, "workcairn-") {
		return ModelResolution{}, fmt.Errorf("%w: unsupported logical route", ErrInvalidConfig)
	}
	return ModelResolution{LogicalRoute: "explicit", PolicyVersion: AutomaticModelPolicyVersion, ProviderModel: requested}, nil
}
