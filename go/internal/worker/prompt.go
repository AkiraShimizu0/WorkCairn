package worker

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MetadataCEORecoveryGuidance is the typed, provider-neutral metadata key
// used only when a CEO explicitly continues an already-created Revision
// Task after a guarded stop. The durable source remains the Interaction
// recovery Turn; this key only carries that instruction into the one Worker
// prompt executed by the new Recovery Command.
const MetadataCEORecoveryGuidance = "ceo_recovery_guidance"

// PromptInput is intentionally separate from ExecutionRequest so future
// company policy, prior decisions, and deliverables can be added at this port.
type PromptInput struct {
	Employee    EmployeeContext   `json:"employee"`
	Task        TaskContext       `json:"task"`
	CurrentTime time.Time         `json:"current_datetime"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Prompt is the provider-neutral input generated for a Runner.
type Prompt struct {
	System string `json:"system"`
	User   string `json:"user"`
}

// PromptBuilder is a port. Prompt content remains outside WorkerService and is
// supplied by an injected provider-neutral implementation.
type PromptBuilder interface {
	Build(ctx context.Context, input PromptInput) (Prompt, error)
}

func (prompt Prompt) Validate() error {
	if strings.TrimSpace(prompt.System) == "" {
		return fmt.Errorf("%w: system prompt is required", ErrInvalidPrompt)
	}
	if strings.TrimSpace(prompt.User) == "" {
		return fmt.Errorf("%w: user prompt is required", ErrInvalidPrompt)
	}
	return nil
}
