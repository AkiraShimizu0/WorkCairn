package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workcairn/go/internal/synthesisacceptance"
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
	var report synthesisacceptance.Result
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || report.Executed || !report.Ready || report.Status != "dry_run_ready" {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}
