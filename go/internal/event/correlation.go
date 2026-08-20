package event

import "context"

// Correlation carries lineage identifiers for Events constructed further
// down the same call chain: which Root Command produced this Event
// (CorrelationID), and which immediate Command caused it (CausationID). See
// docs/adr/ADR-0051 -- this reuses Event's existing, previously-unpopulated
// CorrelationID/CausationID fields rather than adding new fields to Task or
// any other Domain type.
type Correlation struct {
	CorrelationID string
	CausationID   string
}

type correlationContextKey struct{}

// WithCorrelation attaches a Correlation to ctx for any Event built by
// TaskService (via CorrelationFrom) further down this same call chain. This
// is the only context.Value usage in this codebase, deliberately scoped to
// Event lineage metadata alone: it never carries business parameters or
// affects control flow, and a ctx with no Correlation attached simply
// yields a zero Correlation (both IDs empty), identical to today's
// behavior. Explicit typed parameters remain the rule everywhere else.
func WithCorrelation(ctx context.Context, correlation Correlation) context.Context {
	return context.WithValue(ctx, correlationContextKey{}, correlation)
}

// CorrelationFrom returns the Correlation attached to ctx, or a zero
// Correlation if none was attached.
func CorrelationFrom(ctx context.Context) Correlation {
	correlation, _ := ctx.Value(correlationContextKey{}).(Correlation)
	return correlation
}
