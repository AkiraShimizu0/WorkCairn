package policy

import (
	"context"
	"strings"
)

// ProgressDecision is the small closed vocabulary a ProgressPolicy may
// return. It is a decision boundary only -- nothing in this package ever
// writes Task state (TaskService remains the sole owner, Article 6); a
// caller decides what each decision means for its own dispatch loop.
type ProgressDecision string

const (
	// ProgressContinue means nothing about this lineage looks stuck --
	// proceed exactly as the caller already would have.
	ProgressContinue ProgressDecision = "continue"
	// ProgressHold suggests the caller stop dispatching further work for
	// this lineage without treating it as an outright failure.
	ProgressHold ProgressDecision = "hold"
	// ProgressEscalate suggests the caller stop and surface this lineage
	// for human attention (e.g. Revision Limit Recovery) rather than
	// silently continuing to spend Revisions/Provider calls on it.
	ProgressEscalate ProgressDecision = "escalate"
	// ProgressCancel suggests the caller stop this lineage outright.
	ProgressCancel ProgressDecision = "cancel"
)

func (decision ProgressDecision) Valid() bool {
	switch decision {
	case ProgressContinue, ProgressHold, ProgressEscalate, ProgressCancel:
		return true
	}
	return false
}

// ProgressSignal is the typed, already-sanitized evidence a caller supplies
// to a ProgressPolicy after a Review verdict, before deciding whether to
// create another Revision. It never carries raw Deliverable content,
// Provider response text, or credentials -- only signals the caller has
// already computed from its own canonical evidence (Review Decision,
// Revision count, Task lineage identity).
type ProgressSignal struct {
	// TaskLineageID identifies one branch's own chain of Tasks (e.g. the
	// original source Task ID a chain of Revisions traces back to), so a
	// Policy reasons about one lineage at a time and never mixes evidence
	// across unrelated lineages -- callers must supply the same
	// TaskLineageID for every Review in one chain and a different one for
	// any other chain.
	TaskLineageID string
	// RevisionCount is how many Revisions this lineage has already gone
	// through (0 on the very first Review of the lineage's first Task).
	RevisionCount int
	// NormalizedFeedback is a caller-computed signature of the latest
	// Review's Issues+Summary -- a plain comparison key (e.g. a stable
	// normalized, sorted, joined string), never raw Review or Deliverable
	// text and never sent to a Provider or embedding model by this
	// package.
	NormalizedFeedback string
	// ConsecutiveSameFeedbackCount is how many times in a row (including
	// this one) NormalizedFeedback has repeated for this lineage. 1 means
	// "first time seeing this exact feedback."
	ConsecutiveSameFeedbackCount int
}

func (signal ProgressSignal) validate() error {
	if strings.TrimSpace(signal.TaskLineageID) == "" || signal.RevisionCount < 0 || signal.ConsecutiveSameFeedbackCount < 0 {
		return ErrInvalidProgressInput
	}
	return nil
}

// ProgressPolicy is a pure decision boundary evaluated after a Review
// verdict and before a caller decides whether to create another Revision
// (ADR-TBD, No-Progress Foundation): it exists to stop wasted Revision/
// Provider calls, time, and cost early -- not to make execution "smarter."
// It never mutates Task state and never depends on a Provider; it only
// reasons over typed signals a caller already computed.
type ProgressPolicy interface {
	Evaluate(ctx context.Context, signal ProgressSignal) (ProgressDecision, error)
}

// RepeatedFeedbackProgressPolicy is No-Progress v0: a minimal, non-AI
// detector treating the same normalized Review feedback repeating
// RepeatThreshold times in a row within one Task lineage as a no-progress
// candidate. It deliberately does not inspect Deliverable content, call a
// Provider, or use embeddings -- a plain equality comparison the caller
// already reduced Review evidence to.
type RepeatedFeedbackProgressPolicy struct {
	// RepeatThreshold is how many consecutive identical
	// NormalizedFeedback occurrences trigger Escalate. A value <= 0 is
	// treated as 2 -- the same conservative default as
	// autonomy.DefaultMaxRevisionCount, so v0's default behavior only
	// meaningfully engages once a caller raises MaxRevisionCount above its
	// own default in a future Checkpoint; it never fires earlier than the
	// existing Revision Guard would already have stopped a branch under
	// today's defaults.
	RepeatThreshold int
}

func (policy RepeatedFeedbackProgressPolicy) Evaluate(ctx context.Context, signal ProgressSignal) (ProgressDecision, error) {
	if ctx == nil {
		return "", ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := signal.validate(); err != nil {
		return "", err
	}
	threshold := policy.RepeatThreshold
	if threshold <= 0 {
		threshold = 2
	}
	if signal.NormalizedFeedback != "" && signal.ConsecutiveSameFeedbackCount >= threshold {
		return ProgressEscalate, nil
	}
	return ProgressContinue, nil
}

var _ ProgressPolicy = RepeatedFeedbackProgressPolicy{}
