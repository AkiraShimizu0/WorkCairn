package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/localos"
	workcairnruntime "github.com/AkiraShimizu0/WorkCairn/go/internal/runtime"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/synthesisacceptance"
)

type dependencies struct {
	lookupEnv      func(string) string
	loadCredential func(context.Context) (string, error)
	newHTTPClient  func() claude.HTTPDoer
}

func main() {
	if handled, exitCode := localos.RunCredentialHelperIfRequested(); handled {
		os.Exit(exitCode)
	}
	store := localos.NewClaudeCredentialStore()
	deps := dependencies{
		lookupEnv: os.Getenv,
		loadCredential: func(ctx context.Context) (string, error) {
			return store.Load(ctx)
		},
		newHTTPClient: func() claude.HTTPDoer {
			return workcairnruntime.NewProviderHTTPClient(workcairnruntime.DefaultProviderRequestTimeout)
		},
	}
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, deps))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("workcairn-synthesis-acceptance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", synthesisacceptance.ProviderFakeGood, "fake-good, fake-bad, or claude")
	execute := flags.Bool("execute", false, "perform the bounded acceptance run; omitted means dry-run")
	artifactPath := flags.String("artifact-path", "", "optional: write the Human Review Artifact (canonical Synthesis Deliverable + safe metadata) to this file, outside the Git working tree and any real Vault; omitted writes nothing")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	config := synthesisacceptance.Config{Provider: strings.TrimSpace(*provider), Execute: *execute, ArtifactPath: strings.TrimSpace(*artifactPath)}
	if config.Provider == synthesisacceptance.ProviderClaude && config.Execute {
		config.APIKey = strings.TrimSpace(deps.lookupEnv("ANTHROPIC_API_KEY"))
		if config.APIKey == "" {
			credential, err := deps.loadCredential(ctx)
			if err != nil {
				fmt.Fprintln(stderr, "Claude connection is not available for the explicit acceptance run")
				return 1
			}
			config.APIKey = strings.TrimSpace(credential)
		}
		config.HTTPClient = deps.newHTTPClient()
	}
	result, err := synthesisacceptance.Run(ctx, config)
	if encodeErr := json.NewEncoder(stdout).Encode(result); encodeErr != nil {
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "Synthesis acceptance did not pass; inspect the safe JSON report")
		return 1
	}
	return 0
}
