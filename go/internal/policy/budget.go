package policy

import (
	"context"
	"time"
)

// BudgetDecision is the small closed vocabulary a BudgetPolicy may return.
// Like ProgressDecision, it is a decision boundary only -- nothing in this
// package ever writes Task state; a caller decides what each decision
// means for its own dispatch loop (ADR-0054, BudgetGuard v1).
type BudgetDecision string

const (
	// BudgetContinue means resource usage observed so far is within the
	// configured limits -- proceed exactly as the caller already would
	// have.
	BudgetContinue BudgetDecision = "continue"
	// BudgetEscalate means at least one configured resource limit has been
	// reached or exceeded -- the caller must stop dispatching further
	// Provider-invoking work for this scope and surface this for human
	// attention (Recovery), never retry automatically.
	BudgetEscalate BudgetDecision = "escalate"
)

func (decision BudgetDecision) Valid() bool {
	switch decision {
	case BudgetContinue, BudgetEscalate:
		return true
	}
	return false
}

// TokenUsageSignal is a caller-computed, already-sanitized summary of Token
// usage observed so far in one Budget scope. Known is false whenever any
// observed Provider call did not report usage (worker.TokenUsage's own
// InputTokens/OutputTokens are *int, nil meaning "not reported") -- a
// caller must never treat "unknown usage" as "zero usage" by leaving Known
// true with zero counts, since that would let a Budget silently underrun
// its own accounting. This package never estimates or guesses a token
// count on a Provider's behalf.
type TokenUsageSignal struct {
	InputTokens  int
	OutputTokens int
	// Known is true only when every Provider call observed in this scope
	// reported usage. Once any call's usage is unreported, Known becomes
	// (and stays) false for the rest of the scope -- the accumulated
	// counts above are then a floor, not a ground truth total.
	Known bool
}

// BudgetSignal is the typed, already-sanitized evidence a caller supplies
// to a BudgetPolicy: how much of a fixed resource envelope one Workflow
// execution scope has consumed so far. It is distinct from
// policy.ProgressSignal on purpose -- BudgetGuard answers "have we used
// too much," never "is the result actually improving" (that remains
// ProgressPolicy's own, separate responsibility, ADR-0053). It never
// carries raw Deliverable content, Provider response text, or
// credentials.
type BudgetSignal struct {
	// ElapsedRuntime is the wall-clock duration since this Budget scope
	// began (typically one Reviewed Workflow execution, ADR-0054).
	ElapsedRuntime time.Duration
	// ProviderCallCount is how many actual Provider invocations (Task
	// execution + Review, summed across every branch in this scope) have
	// already been made -- never a replayed Command result or a
	// rejected preflight, only genuine new invocations (see
	// go/internal/service's budgetTracker, which is the sole place this
	// count is incremented).
	ProviderCallCount int
	// TokenUsage is the accumulated Token usage across every Provider
	// call observed in this scope so far. Purely observational in v1 --
	// no shipped BudgetPolicy gates a decision on it (see this package's
	// own Cost-accounting notes: no fake per-token pricing is computed
	// here or anywhere in this Checkpoint).
	TokenUsage TokenUsageSignal
	// TaskCount and RevisionCount are observational context (how much
	// structural work this scope has already produced), included so a
	// future BudgetPolicy can reason about them without this package
	// inventing new fields later. FixedBudgetPolicy (v1) does not gate on
	// either.
	TaskCount     int
	RevisionCount int
}

func (signal BudgetSignal) validate() error {
	if signal.ElapsedRuntime < 0 || signal.ProviderCallCount < 0 || signal.TaskCount < 0 || signal.RevisionCount < 0 ||
		signal.TokenUsage.InputTokens < 0 || signal.TokenUsage.OutputTokens < 0 {
		return ErrInvalidBudgetInput
	}
	return nil
}

// BudgetPolicy is a pure decision boundary evaluated before a caller
// starts a new Provider-invoking attempt (ADR-0054, BudgetGuard v1): it
// exists to guarantee one Workflow execution eventually stops even if
// Progress Intelligence never detects a stall -- a genuinely improving
// branch can still exhaust a finite Runtime/Provider-call envelope. It
// never mutates Task state and never depends on a Provider; it only reads
// a typed signal a caller already computed. It never estimates Cost.
type BudgetPolicy interface {
	Evaluate(ctx context.Context, signal BudgetSignal) (BudgetDecision, error)
}

// FixedBudgetPolicy is BudgetGuard v1: two independent hard, deterministic
// limits (MaxRuntime, MaxProviderCalls) -- either one reaching or
// exceeding its configured value alone is enough to Escalate, unlike
// CompoundProgressPolicy's conservative AND-of-signals. This is
// intentional: Budget is a safety ceiling, not a convergence judgement --
// exceeding *any one* resource envelope is already a reason to stop and
// ask a human, regardless of whether the other resources still have
// headroom. TokenUsage is read only for validation in v1; no configured
// Token limit exists yet (see this package's Cost/Token accounting notes
// -- Token Budget enforcement is intentionally deferred to a future
// Checkpoint once usage is confirmed reliable across every Provider this
// codebase may add).
type FixedBudgetPolicy struct {
	// MaxRuntime is the wall-clock ceiling for one Budget scope. A value
	// <= 0 is treated as "no Runtime limit" -- callers that do not
	// configure this field keep today's unbounded-runtime behavior.
	MaxRuntime time.Duration
	// MaxProviderCalls is the ceiling on actual Provider invocations for
	// one Budget scope. A value <= 0 is treated as "no Provider Call
	// limit."
	MaxProviderCalls int
}

func (policy FixedBudgetPolicy) Evaluate(ctx context.Context, signal BudgetSignal) (BudgetDecision, error) {
	if ctx == nil {
		return "", ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := signal.validate(); err != nil {
		return "", err
	}
	if policy.MaxRuntime > 0 && signal.ElapsedRuntime >= policy.MaxRuntime {
		return BudgetEscalate, nil
	}
	if policy.MaxProviderCalls > 0 && signal.ProviderCallCount >= policy.MaxProviderCalls {
		return BudgetEscalate, nil
	}
	return BudgetContinue, nil
}

var _ BudgetPolicy = FixedBudgetPolicy{}

// BudgetExceededReason names which configured limit a BudgetGuard stop was
// attributed to -- used only to build the typed FailureEnvelope/Audit
// classification a caller already owns; this package does not persist or
// publish it itself.
type BudgetExceededReason string

const (
	BudgetReasonRuntime      BudgetExceededReason = "runtime"
	BudgetReasonProviderCall BudgetExceededReason = "provider_call"
)

// ExceededReason reports which configured limit the given signal has
// already reached or exceeded under this Policy's own configuration,
// preferring Runtime when both are simultaneously exceeded (Runtime is
// checked first in Evaluate too, for the same reason: a scope that is
// literally out of time is the more fundamental stop). Returns false if
// neither limit is exceeded.
func (policy FixedBudgetPolicy) ExceededReason(signal BudgetSignal) (BudgetExceededReason, bool) {
	if policy.MaxRuntime > 0 && signal.ElapsedRuntime >= policy.MaxRuntime {
		return BudgetReasonRuntime, true
	}
	if policy.MaxProviderCalls > 0 && signal.ProviderCallCount >= policy.MaxProviderCalls {
		return BudgetReasonProviderCall, true
	}
	return "", false
}
