package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
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
// Deliverable fingerprint, Revision count, Task lineage identity).
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
	// package. Kept for RepeatedFeedbackProgressPolicy (No-Progress v0);
	// CompoundProgressPolicy (Progress Intelligence v1) reads
	// ReviewSignature/ConsecutiveSameReviewCount instead -- see those
	// fields' own doc comments for why a structural signature is used in
	// v1 rather than this literal-text comparison.
	NormalizedFeedback string
	// ConsecutiveSameFeedbackCount is how many times in a row (including
	// this one) NormalizedFeedback has repeated for this lineage. 1 means
	// "first time seeing this exact feedback."
	ConsecutiveSameFeedbackCount int
	// ReviewSignature is the latest Review Decision reduced to a
	// structural comparison key (Verdict + typed Issue Category/Severity
	// sets), computed by NewReviewSignature. Unlike NormalizedFeedback, it
	// is deliberately insensitive to how a Reviewer phrased a finding's
	// free-text Description/SuggestedAction, so a Provider rewording the
	// same underlying finding is not mistaken for "different feedback."
	ReviewSignature ReviewSignature
	// ConsecutiveSameReviewCount is how many times in a row (including
	// this one) ReviewSignature has repeated for this lineage. 1 means
	// "first time seeing this exact signature."
	ConsecutiveSameReviewCount int
	// DeliverableChanged reports whether this attempt's Deliverable
	// fingerprint (NewDeliverableFingerprint) differs from the
	// immediately preceding attempt's fingerprint within the same
	// lineage. There is nothing to compare on a lineage's first attempt,
	// so callers must report true (the conservative/safe default -- never
	// claim "unchanged" without a genuine prior attempt to compare
	// against).
	DeliverableChanged bool
	// ConsecutiveUnchangedDeliverableCount is how many consecutive
	// attempts (not counting the first, which has nothing to compare
	// against) produced the same Deliverable fingerprint as the one
	// before it. 0 means the Deliverable changed on the most recent
	// comparison, or no comparison has happened yet.
	ConsecutiveUnchangedDeliverableCount int
	// ProviderCallCount is how many Provider calls (Task execution +
	// Review, summed) this lineage has made so far. Purely observational
	// in v1 -- no shipped ProgressPolicy gates a decision on it -- kept on
	// the signal so a Policy or a future caller can reason about resource
	// consumption without this package inventing a Cost estimate (see
	// this package's own doc notes on Cost being explicitly out of scope
	// until a dedicated Budget accounting foundation exists).
	ProviderCallCount int
	// ElapsedDuration is the summed wall-clock Duration this lineage's
	// Task execution and Review calls have already taken (from the
	// existing worker.ExecutionResult/review.ExecutionResult Duration
	// fields a caller already has -- never a new timing measurement this
	// package performs itself). Purely observational in v1, for the same
	// reason as ProviderCallCount above.
	ElapsedDuration time.Duration
}

func (signal ProgressSignal) validate() error {
	if strings.TrimSpace(signal.TaskLineageID) == "" || signal.RevisionCount < 0 ||
		signal.ConsecutiveSameFeedbackCount < 0 || signal.ConsecutiveSameReviewCount < 0 ||
		signal.ConsecutiveUnchangedDeliverableCount < 0 || signal.ProviderCallCount < 0 || signal.ElapsedDuration < 0 {
		return ErrInvalidProgressInput
	}
	return nil
}

// ReviewSignature is a structural, non-AI comparison key for one Review
// Decision (Progress Intelligence v1): built only from the Verdict and the
// typed, enum-constrained Issue Category/Severity sets (review.Issue),
// never the free-text Description/SuggestedAction/Summary a Reviewer wrote.
// This is deliberate -- comparing raw Review text would mistake a Provider
// merely rephrasing the same finding for genuine progress (or genuine
// regress), which is exactly the false signal this type exists to avoid.
// Two Decisions raising the same set of Category/Severity combinations, in
// any order and with any wording, produce an identical ReviewSignature.
type ReviewSignature struct {
	Verdict review.Verdict `json:"verdict"`
	// IssueCategories is the sorted, deduplicated set of every Issue's
	// Category in this Decision (review.Issue.Category is already a
	// small closed enum -- "date"/"format"/"requirements"/"context"/
	// "todo"/"other" -- so this set is inherently stable across wording
	// changes).
	IssueCategories []string `json:"issue_categories"`
	// IssueSeverities is the sorted, deduplicated set of every Issue's
	// Severity ("high"/"medium"/"low").
	IssueSeverities []string `json:"issue_severities"`
	// IssueCount is the total number of Issues, included so "the same 2
	// categories repeated across 4 issues" is not conflated with "the
	// same 2 categories across 1 issue each."
	IssueCount int `json:"issue_count"`
}

// NewReviewSignature reduces a review.Decision to its structural
// ReviewSignature. It is a pure function: no Vault, Provider, Event, or
// Audit access, and it never inspects Description/SuggestedAction/Summary.
func NewReviewSignature(decision review.Decision) ReviewSignature {
	categorySet := make(map[string]struct{}, len(decision.Issues))
	severitySet := make(map[string]struct{}, len(decision.Issues))
	for _, issue := range decision.Issues {
		if issue.Category != "" {
			categorySet[issue.Category] = struct{}{}
		}
		if issue.Severity != "" {
			severitySet[issue.Severity] = struct{}{}
		}
	}
	return ReviewSignature{
		Verdict:         decision.Verdict,
		IssueCategories: sortedSetKeys(categorySet),
		IssueSeverities: sortedSetKeys(severitySet),
		IssueCount:      len(decision.Issues),
	}
}

// Equal reports whether two ReviewSignatures represent the same
// structural finding shape. Order-independent by construction (both
// IssueCategories/IssueSeverities are already sorted by NewReviewSignature),
// so this never depends on Go's randomized map iteration order.
func (signature ReviewSignature) Equal(other ReviewSignature) bool {
	return signature.Verdict == other.Verdict && signature.IssueCount == other.IssueCount &&
		slices.Equal(signature.IssueCategories, other.IssueCategories) &&
		slices.Equal(signature.IssueSeverities, other.IssueSeverities)
}

func sortedSetKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// DeliverableFingerprint is an opaque internal comparison value for one
// Deliverable body -- never the content itself, never persisted by this
// package, and never intended for external JSON Contract exposure. It
// exists solely so a caller can answer "did the Deliverable actually
// change between two attempts" without comparing (or logging, or
// Audit-recording) raw content, and without any semantic/embedding
// judgement of *how much* it changed.
type DeliverableFingerprint string

// NewDeliverableFingerprint reduces Deliverable content to an opaque
// SHA-256 fingerprint after light, content-blind normalization (line
// ending unification, per-line trailing whitespace trim, and outer
// whitespace trim) -- never a Markdown-aware transformation, so this
// package never needs to know Deliverable structure. Two contents that
// differ only in line endings or trailing whitespace fingerprint
// identically; any other content difference fingerprints differently.
func NewDeliverableFingerprint(content string) DeliverableFingerprint {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t")
	}
	normalized = strings.TrimSpace(strings.Join(lines, "\n"))
	sum := sha256.Sum256([]byte(normalized))
	return DeliverableFingerprint(hex.EncodeToString(sum[:]))
}

// ProgressPolicy is a pure decision boundary evaluated after a Review
// verdict and before a caller decides whether to create another Revision
// (ADR-0052, No-Progress Foundation): it exists to stop wasted Revision/
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

// CompoundProgressPolicy is Progress Intelligence v1 (ADR-0053): it treats a
// lineage as a no-progress candidate only when three independent,
// deterministic signals all agree that nothing is converging --
// deliberately conservative, so a single coincidental repeat never stops a
// branch that is genuinely still improving:
//
//  1. Review Progress: the same structural ReviewSignature (Verdict +
//     Issue Category/Severity set, never free-text wording) has repeated
//     ReviewRepeatThreshold times in a row.
//  2. Deliverable Progress: the Deliverable fingerprint has stayed
//     unchanged for ConsecutiveUnchangedDeliverableCount >=
//     DeliverableUnchangedThreshold consecutive attempts.
//  3. Execution Progress: at least RevisionCountThreshold Revisions have
//     already been spent on this lineage.
//
// Resource signals (ProviderCallCount, ElapsedDuration) are read from
// ProgressSignal only for validation -- v1 does not gate the decision on
// them; they exist so a caller (or a future Policy) can observe resource
// consumption without this package inventing a Cost estimate. Like every
// ProgressPolicy, this type never mutates Task state and never depends on
// a Provider or embedding model.
type CompoundProgressPolicy struct {
	// ReviewRepeatThreshold is how many consecutive identical
	// ReviewSignature occurrences count toward Review Progress stalling.
	// A value <= 0 is treated as 2 (same conservative default as
	// RepeatedFeedbackProgressPolicy).
	ReviewRepeatThreshold int
	// DeliverableUnchangedThreshold is how many consecutive unchanged
	// Deliverable fingerprints count toward Deliverable Progress
	// stalling. A value <= 0 is treated as 2.
	DeliverableUnchangedThreshold int
	// RevisionCountThreshold is the minimum number of Revisions already
	// spent on this lineage before Execution Progress is considered
	// exhausted. A value <= 0 is treated as 2 -- the same conservative
	// default as autonomy.DefaultMaxRevisionCount, so this never fires
	// earlier than the Revision Guard itself would already have stopped a
	// branch under today's defaults.
	RevisionCountThreshold int
}

func (policy CompoundProgressPolicy) Evaluate(ctx context.Context, signal ProgressSignal) (ProgressDecision, error) {
	if ctx == nil {
		return "", ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := signal.validate(); err != nil {
		return "", err
	}
	reviewThreshold := policy.ReviewRepeatThreshold
	if reviewThreshold <= 0 {
		reviewThreshold = 2
	}
	deliverableThreshold := policy.DeliverableUnchangedThreshold
	if deliverableThreshold <= 0 {
		deliverableThreshold = 2
	}
	revisionThreshold := policy.RevisionCountThreshold
	if revisionThreshold <= 0 {
		revisionThreshold = 2
	}
	reviewStalled := signal.ConsecutiveSameReviewCount >= reviewThreshold
	deliverableStalled := signal.ConsecutiveUnchangedDeliverableCount >= deliverableThreshold
	revisionsExhausted := signal.RevisionCount >= revisionThreshold
	if reviewStalled && deliverableStalled && revisionsExhausted {
		return ProgressEscalate, nil
	}
	return ProgressContinue, nil
}

var _ ProgressPolicy = CompoundProgressPolicy{}
