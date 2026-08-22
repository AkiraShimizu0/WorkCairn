package synthesisacceptance

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
)

func TestHarnessRunsGoodSynthesisThroughCanonicalProductionPath(t *testing.T) {
	result, err := Run(context.Background(), Config{
		Provider: ProviderFakeGood, Execute: true, VaultRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Status != "passed" || !result.CanonicalDeliverable ||
		result.Evaluation == nil || result.Evaluation.Score != MaxScore ||
		result.ProviderInvocations != 2 || result.ExternalProviderCalls != 0 ||
		!result.TokenUsage.Known || result.TokenUsage.InputTokens != 420 || result.TokenUsage.OutputTokens != 180 ||
		len(result.Prompt.EvidenceTaskIDs) != 3 || result.MaxProviderCalls != acceptanceMaxProviderCalls {
		t.Fatalf("result = %#v", result)
	}
}

func TestHarnessRejectsBadConcatenationAfterCanonicalCommit(t *testing.T) {
	result, err := Run(context.Background(), Config{
		Provider: ProviderFakeBad, Execute: true, VaultRoot: t.TempDir(),
	})
	if !errors.Is(err, ErrFailed) || result.Passed || result.FailureCategory != FailureQuality ||
		!result.CanonicalDeliverable || result.Evaluation == nil ||
		scoreFor(*result.Evaluation, RubricEvidenceCoverage) != 2 ||
		scoreFor(*result.Evaluation, RubricCrossEvidenceSynthesis) != 0 {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}

func TestClaudeDryRunBuildsPromptWithoutCredentialOrExternalCall(t *testing.T) {
	client := &panicHTTPDoer{t: t}
	result, err := Run(context.Background(), Config{Provider: ProviderClaude, Execute: false, HTTPClient: client})
	if err != nil || result.Executed || !result.Ready || result.Status != "dry_run_ready" ||
		result.ExternalProviderCalls != 0 || len(result.Prompt.EvidenceTaskIDs) != 3 || result.Prompt.UserBytes == 0 {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}

func TestHarnessAttributesExternalTransportFailureToProvider(t *testing.T) {
	result, err := Run(context.Background(), Config{
		Provider: ProviderClaude, Execute: true, APIKey: "acceptance-test-only",
		HTTPClient: failingHTTPDoer{}, VaultRoot: t.TempDir(),
	})
	if err == nil || result.FailureCategory != FailureProvider || result.ExternalProviderCalls != 1 ||
		result.CanonicalDeliverable || len(result.Prompt.EvidenceTaskIDs) != 3 {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
}

type panicHTTPDoer struct{ t *testing.T }

func (client *panicHTTPDoer) Do(*http.Request) (*http.Response, error) {
	client.t.Fatal("dry-run must not call a Provider")
	return nil, nil
}

type failingHTTPDoer struct{}

func (failingHTTPDoer) Do(*http.Request) (*http.Response, error) {
	return nil, &claude.Error{Kind: claude.ErrTransport, Category: claude.FailureTransport}
}
