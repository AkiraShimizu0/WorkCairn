package policy

import (
	"context"
	"testing"
	"time"
)

func validBudgetSignal() BudgetSignal {
	return BudgetSignal{ElapsedRuntime: time.Minute, ProviderCallCount: 5}
}

func TestFixedBudgetPolicyContinuesUnderBothLimits(t *testing.T) {
	policy := FixedBudgetPolicy{MaxRuntime: time.Hour, MaxProviderCalls: 100}
	decision, err := policy.Evaluate(context.Background(), validBudgetSignal())
	if err != nil || decision != BudgetContinue {
		t.Fatalf("Evaluate() = %v, %v, want BudgetContinue", decision, err)
	}
}

func TestFixedBudgetPolicyEscalatesExactlyAtProviderCallLimit(t *testing.T) {
	policy := FixedBudgetPolicy{MaxProviderCalls: 5}
	decision, err := policy.Evaluate(context.Background(), BudgetSignal{ProviderCallCount: 5})
	if err != nil || decision != BudgetEscalate {
		t.Fatalf("Evaluate() at exactly the limit = %v, %v, want BudgetEscalate", decision, err)
	}
	belowDecision, err := policy.Evaluate(context.Background(), BudgetSignal{ProviderCallCount: 4})
	if err != nil || belowDecision != BudgetContinue {
		t.Fatalf("Evaluate() one below the limit = %v, %v, want BudgetContinue", belowDecision, err)
	}
}

func TestFixedBudgetPolicyEscalatesOverProviderCallLimit(t *testing.T) {
	policy := FixedBudgetPolicy{MaxProviderCalls: 5}
	decision, err := policy.Evaluate(context.Background(), BudgetSignal{ProviderCallCount: 9})
	if err != nil || decision != BudgetEscalate {
		t.Fatalf("Evaluate() over the limit = %v, %v, want BudgetEscalate", decision, err)
	}
}

func TestFixedBudgetPolicyEscalatesAtRuntimeLimit(t *testing.T) {
	policy := FixedBudgetPolicy{MaxRuntime: 10 * time.Minute}
	decision, err := policy.Evaluate(context.Background(), BudgetSignal{ElapsedRuntime: 10 * time.Minute})
	if err != nil || decision != BudgetEscalate {
		t.Fatalf("Evaluate() at Runtime limit = %v, %v, want BudgetEscalate", decision, err)
	}
	belowDecision, err := policy.Evaluate(context.Background(), BudgetSignal{ElapsedRuntime: 9*time.Minute + 59*time.Second})
	if err != nil || belowDecision != BudgetContinue {
		t.Fatalf("Evaluate() one second below Runtime limit = %v, %v, want BudgetContinue", belowDecision, err)
	}
}

func TestFixedBudgetPolicyZeroOrNegativeLimitMeansUnlimited(t *testing.T) {
	policy := FixedBudgetPolicy{} // both fields zero-value
	decision, err := policy.Evaluate(context.Background(), BudgetSignal{ElapsedRuntime: 100 * time.Hour, ProviderCallCount: 1_000_000})
	if err != nil || decision != BudgetContinue {
		t.Fatalf("Evaluate() with unset limits = %v, %v, want BudgetContinue (0 means unlimited, matching autonomy.Contract's own 0-is-unset convention)", decision, err)
	}
}

func TestFixedBudgetPolicyEitherLimitAloneIsEnoughToEscalate(t *testing.T) {
	// Unlike CompoundProgressPolicy's conservative AND-of-signals, Budget is
	// a safety ceiling: exceeding *either* Runtime or Provider-call alone
	// must escalate.
	runtimeOnly := FixedBudgetPolicy{MaxRuntime: time.Minute, MaxProviderCalls: 100}
	decision, err := runtimeOnly.Evaluate(context.Background(), BudgetSignal{ElapsedRuntime: time.Hour, ProviderCallCount: 1})
	if err != nil || decision != BudgetEscalate {
		t.Fatalf("Evaluate() Runtime-only breach = %v, %v, want BudgetEscalate", decision, err)
	}
	providerCallOnly := FixedBudgetPolicy{MaxRuntime: time.Hour, MaxProviderCalls: 1}
	decision, err = providerCallOnly.Evaluate(context.Background(), BudgetSignal{ElapsedRuntime: time.Second, ProviderCallCount: 5})
	if err != nil || decision != BudgetEscalate {
		t.Fatalf("Evaluate() Provider-call-only breach = %v, %v, want BudgetEscalate", decision, err)
	}
}

func TestFixedBudgetPolicyRejectsNilContextAndInvalidSignal(t *testing.T) {
	policy := FixedBudgetPolicy{MaxRuntime: time.Hour}
	if _, err := policy.Evaluate(nil, validBudgetSignal()); err == nil {
		t.Fatal("Evaluate() with nil context should fail")
	}
	if _, err := policy.Evaluate(context.Background(), BudgetSignal{ElapsedRuntime: -1}); err == nil {
		t.Fatal("Evaluate() with negative ElapsedRuntime should fail")
	}
	if _, err := policy.Evaluate(context.Background(), BudgetSignal{ProviderCallCount: -1}); err == nil {
		t.Fatal("Evaluate() with negative ProviderCallCount should fail")
	}
	if _, err := policy.Evaluate(context.Background(), BudgetSignal{TokenUsage: TokenUsageSignal{InputTokens: -1}}); err == nil {
		t.Fatal("Evaluate() with negative TokenUsage.InputTokens should fail")
	}
}

func TestFixedBudgetPolicyExceededReasonPrefersRuntime(t *testing.T) {
	policy := FixedBudgetPolicy{MaxRuntime: time.Minute, MaxProviderCalls: 1}
	reason, exceeded := policy.ExceededReason(BudgetSignal{ElapsedRuntime: time.Hour, ProviderCallCount: 5})
	if !exceeded || reason != BudgetReasonRuntime {
		t.Fatalf("ExceededReason() = %v, %v, want BudgetReasonRuntime when both are simultaneously exceeded", reason, exceeded)
	}
	reason, exceeded = policy.ExceededReason(BudgetSignal{ElapsedRuntime: time.Second, ProviderCallCount: 5})
	if !exceeded || reason != BudgetReasonProviderCall {
		t.Fatalf("ExceededReason() = %v, %v, want BudgetReasonProviderCall when only that limit is exceeded", reason, exceeded)
	}
	_, exceeded = policy.ExceededReason(BudgetSignal{ElapsedRuntime: time.Second, ProviderCallCount: 0})
	if exceeded {
		t.Fatal("ExceededReason() should report not-exceeded when neither limit is reached")
	}
}

func TestBudgetDecisionValid(t *testing.T) {
	for _, decision := range []BudgetDecision{BudgetContinue, BudgetEscalate} {
		if !decision.Valid() {
			t.Fatalf("%q should be valid", decision)
		}
	}
	if BudgetDecision("bogus").Valid() {
		t.Fatal("unknown BudgetDecision should not be valid")
	}
}

func TestTokenUsageSignalKnownSemantics(t *testing.T) {
	// A zero-value TokenUsageSignal (Known: false) must never be
	// interpreted as "zero tokens were used" -- it must be distinguishable
	// from a genuinely observed zero-usage call (Known: true, both counts
	// 0). This test only pins the type's own zero-value shape; the
	// process of actually setting Known correctly from Provider responses
	// belongs to the service-layer budgetTracker (see its own tests).
	var unset TokenUsageSignal
	if unset.Known {
		t.Fatal("zero-value TokenUsageSignal must default Known to false, never true")
	}
	known := TokenUsageSignal{InputTokens: 0, OutputTokens: 0, Known: true}
	if !known.Known || known.InputTokens != 0 {
		t.Fatalf("explicit known-zero usage must stay distinguishable from unset usage: %#v", known)
	}
}
