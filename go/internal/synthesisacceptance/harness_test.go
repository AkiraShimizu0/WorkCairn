package synthesisacceptance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	if result.StopReason != "completed" || result.OutputTruncated {
		t.Fatalf("StopReason/OutputTruncated = %q, %v, want completed/false", result.StopReason, result.OutputTruncated)
	}
}

// TestHarnessWritesHumanReviewArtifactOnlyWhenConfigured proves the Human
// Review Artifact (Checklist B9) is opt-in only: no file appears anywhere
// when Config.ArtifactPath is empty (the default, exercised by every other
// test in this file), and the exact path is written with the canonical
// Synthesis Deliverable and safe metadata -- never a credential -- when a
// caller explicitly sets it.
func TestHarnessWritesHumanReviewArtifactOnlyWhenConfigured(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "review-artifact.json")
	result, err := Run(context.Background(), Config{
		Provider: ProviderFakeGood, Execute: true, VaultRoot: t.TempDir(), ArtifactPath: artifactPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed {
		t.Fatalf("result = %#v", result)
	}
	content, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("Human Review Artifact was not written: %v", err)
	}
	var artifact ReviewArtifact
	if err := json.Unmarshal(content, &artifact); err != nil {
		t.Fatalf("decode Human Review Artifact: %v", err)
	}
	if artifact.ScenarioID != "public-beta-product-growth-ja-v1" || artifact.Provider != ProviderFakeGood ||
		!strings.Contains(artifact.Deliverable, "優先順位付き改善計画") || artifact.Evaluation.Score != MaxScore ||
		!artifact.TokenUsage.Known || artifact.TokenUsage.InputTokens != 420 ||
		artifact.StopReason != "completed" || artifact.OutputTruncated {
		t.Fatalf("Human Review Artifact = %#v", artifact)
	}
	for _, forbidden := range []string{"ANTHROPIC_API_KEY", "Authorization", "x-api-key", "Bearer "} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("Human Review Artifact leaked a credential-shaped token %q", forbidden)
		}
	}
}

func TestHarnessNeverWritesAnArtifactWithoutExplicitConfiguration(t *testing.T) {
	wouldBeArtifactPath := filepath.Join(t.TempDir(), "review-artifact.json")
	result, err := Run(context.Background(), Config{Provider: ProviderFakeGood, Execute: true, VaultRoot: t.TempDir()})
	if err != nil || !result.Passed {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	if _, statErr := os.Stat(wouldBeArtifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("a Human Review Artifact appeared even though ArtifactPath was never set: stat error = %v", statErr)
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
