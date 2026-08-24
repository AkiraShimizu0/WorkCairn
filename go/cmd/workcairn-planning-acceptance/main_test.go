package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/planningacceptance"
)

func TestClaudeDryRunNeedsNoCredentialAndMakesNoProviderClient(t *testing.T) {
	var output bytes.Buffer
	credentialLoads, clientBuilds := 0, 0
	exit := run(context.Background(), []string{"--provider", "claude"}, &output, &bytes.Buffer{}, dependencies{
		lookupEnv: func(string) string { return "" },
		loadCredential: func(context.Context) (string, error) {
			credentialLoads++
			return "", errors.New("must not load")
		},
		newHTTPClient: func() claude.HTTPDoer {
			clientBuilds++
			return nil
		},
	})
	if exit != 0 || credentialLoads != 0 || clientBuilds != 0 {
		t.Fatalf("exit=%d credentialLoads=%d clientBuilds=%d", exit, credentialLoads, clientBuilds)
	}
	var report planningacceptance.Result
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Executed || report.Status != "dry_run_ready" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestFakeGoodDefaultProviderNeedsNoCredentialAndScoresFullMarks(t *testing.T) {
	var output bytes.Buffer
	exit := run(context.Background(), []string{}, &output, &bytes.Buffer{}, dependencies{
		lookupEnv:      func(string) string { t.Fatal("fake-good must not read Provider environment"); return "" },
		loadCredential: func(context.Context) (string, error) { t.Fatal("fake-good must not load a credential"); return "", nil },
		newHTTPClient:  func() claude.HTTPDoer { t.Fatal("fake-good must not construct a real HTTP client"); return nil },
	})
	var report planningacceptance.Result
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v, output=%s", err, output.String())
	}
	if exit != 0 || report.Provider != planningacceptance.ProviderFakeGood || report.StructuralGate != planningacceptance.StructuralGatePassed ||
		report.Evaluation == nil || report.Evaluation.Score != planningacceptance.MaxScore {
		t.Fatalf("exit=%d report=%#v", exit, report)
	}
}

func TestClaudeExecuteWithoutCredentialFailsPreflight(t *testing.T) {
	var output bytes.Buffer
	var stderr bytes.Buffer
	exit := run(context.Background(), []string{"--provider", "claude", "--execute"}, &output, &stderr, dependencies{
		lookupEnv:      func(string) string { return "" },
		loadCredential: func(context.Context) (string, error) { return "", errors.New("not available") },
		newHTTPClient:  func() claude.HTTPDoer { t.Fatal("must not construct an HTTP client without a credential"); return nil },
	})
	if exit != 1 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
}
