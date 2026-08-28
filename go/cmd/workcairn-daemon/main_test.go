package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"strings"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/localos"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/httpapi"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
	workspaceruntime "github.com/AkiraShimizu0/WorkCairn/go/internal/runtime"
)

type restartCredentialStore struct {
	credential string
	loads      int
	requests   int
}

func (store *restartCredentialStore) Load(context.Context) (string, error) {
	store.loads++
	return store.credential, nil
}

func (store *restartCredentialStore) RequestAndStore(context.Context) (string, error) {
	store.requests++
	return store.credential, nil
}

func TestDaemonRestartLoadsStoredClaudeCredentialIntoProviderStatus(t *testing.T) {
	store := &restartCredentialStore{credential: "stored-secret-never-exposed"}
	// A new ProcessExecutor models a daemon restart: no in-memory credential
	// survives, so startup must load the same persistent CredentialStore.
	credential, err := resolveDaemonClaudeCredential(context.Background(), workspaceruntime.ClaudeCredentialKeychain, workspaceruntime.CredentialReaders{Keychain: store.Load})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := httpapi.NewProcessExecutor(t.TempDir(), workspaceprocess.ClaudeProcessConfig{APIKey: credential}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	status := executor.InspectProviderStatus()
	if store.loads != 1 || !status.Configured || len(status.Missing) != 0 {
		t.Fatalf("restart loads=%d status=%#v", store.loads, status)
	}
}

func TestDaemonEnvironmentOverrideDoesNotReadKeychain(t *testing.T) {
	store := &restartCredentialStore{credential: "stored-secret"}
	credential, err := resolveDaemonClaudeCredential(context.Background(), workspaceruntime.ClaudeCredentialEnvironment, workspaceruntime.CredentialReaders{
		Environment: func() string { return " explicit-override " }, Keychain: store.Load,
	})
	if err != nil || credential != "explicit-override" || store.loads != 0 {
		t.Fatalf("override resolution = %q, %v loads=%d", credential, err, store.loads)
	}
}

func TestDaemonHeadlessCredentialConstructsProviderWithoutKeychainOrInteractiveHelper(t *testing.T) {
	keychainCalls, environmentCalls, headlessCalls := 0, 0, 0
	credential, err := resolveDaemonClaudeCredential(context.Background(), workspaceruntime.ClaudeCredentialHeadlessLocal, workspaceruntime.CredentialReaders{
		Environment: func() string { environmentCalls++; return "environment-must-not-be-read" },
		Keychain: func(context.Context) (string, error) {
			keychainCalls++
			return "", errors.New("Keychain must not be read")
		},
		HeadlessLocal: func(context.Context) (string, error) { headlessCalls++; return "fake-headless-credential", nil },
	})
	if err != nil || keychainCalls != 0 || environmentCalls != 0 || headlessCalls != 1 {
		t.Fatalf("headless resolution = %q, %v env=%d Keychain=%d headless=%d", credential, err, environmentCalls, keychainCalls, headlessCalls)
	}
	executor, err := httpapi.NewProcessExecutor(t.TempDir(), workspaceprocess.ClaudeProcessConfig{APIKey: credential}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if status := executor.InspectProviderStatus(); !status.Configured {
		t.Fatalf("headless Provider status = %#v", status)
	} else if encoded, marshalErr := json.Marshal(status); marshalErr != nil {
		t.Fatal(marshalErr)
	} else if strings.Contains(string(encoded), credential) {
		t.Fatal("headless credential escaped through Provider status")
	}
}

func TestDaemonExplicitCredentialSourceMissingFailsClosedAndAutomaticRemainsFirstRunCompatible(t *testing.T) {
	missing := workspaceruntime.CredentialReaders{HeadlessLocal: func(context.Context) (string, error) { return "", errors.New("missing") }}
	if credential, err := resolveDaemonClaudeCredential(context.Background(), workspaceruntime.ClaudeCredentialHeadlessLocal, missing); credential != "" || err == nil {
		t.Fatalf("explicit missing = %q, %v", credential, err)
	}
	if credential, err := resolveDaemonClaudeCredential(context.Background(), workspaceruntime.ClaudeCredentialAutomatic, workspaceruntime.CredentialReaders{
		Environment: func() string { return "" }, Keychain: func(context.Context) (string, error) { return "", errors.New("not configured") },
	}); credential != "" || err != nil {
		t.Fatalf("automatic first run = %q, %v", credential, err)
	}
}

func TestHeadlessAndEnvironmentLocalSetupNeverOpenKeychainInput(t *testing.T) {
	for _, source := range []workspaceruntime.ClaudeCredentialSource{workspaceruntime.ClaudeCredentialHeadlessLocal, workspaceruntime.ClaudeCredentialEnvironment} {
		t.Run(string(source), func(t *testing.T) {
			store := &restartCredentialStore{credential: "must-not-be-used"}
			setup := &daemonLocalSetup{credentialSource: source, credentialStore: store}
			err := setup.ConnectClaude(context.Background())
			var resolutionErr *workspaceruntime.CredentialResolutionError
			if !errors.As(err, &resolutionErr) || resolutionErr.Classification != workspaceruntime.CredentialSourceReadOnly || store.loads != 0 || store.requests != 0 {
				t.Fatalf("ConnectClaude = %#v loads=%d requests=%d", err, store.loads, store.requests)
			}
		})
	}
}

var _ localos.ClaudeCredentialStore = (*restartCredentialStore)(nil)

func TestLoopbackProviderFixtureURLDefaultDeniesNonLoopbackEndpoints(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "IPv4", raw: "http://127.0.0.1:8080/v1/messages", want: true},
		{name: "IPv6", raw: "http://[::1]:8080/v1/messages", want: true},
		{name: "localhost", raw: "http://localhost:8080/v1/messages", want: true},
		{name: "production Provider", raw: "https://api.anthropic.com/v1/messages", want: false},
		{name: "non HTTP scheme", raw: "ftp://localhost:8080/v1/messages", want: false},
		{name: "missing scheme", raw: "127.0.0.1:8080", want: false},
		{name: "empty", raw: "", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := loopbackProviderFixtureURL(test.raw); got != test.want {
				t.Fatalf("loopbackProviderFixtureURL(%q) = %v, want %v", test.raw, got, test.want)
			}
		})
	}
}

// PHASE PB-2.4: --mobile was renamed to --local-network (see ADR-0069). The
// flag's actual behavior -- expand the bind scope from loopback-only to a
// trusted private/link-local address, plus require pairing -- never was
// mobile-device-specific; it was a local-network reachability control that
// happened to be named after its most common use case.

func TestLocalNetworkFlagUnknownMobileFlagIsRejected(t *testing.T) {
	var output bytes.Buffer
	if _, err := parseFlags([]string{"-mobile"}, &output); err == nil {
		t.Fatal("parseFlags accepted the removed -mobile flag")
	}
	if !strings.Contains(output.String(), "mobile") {
		t.Fatalf("expected flag package to name the unknown -mobile flag in its error output, got %q", output.String())
	}
}

func TestLocalNetworkFlagEnablesExpectedBehavior(t *testing.T) {
	config, err := parseFlags([]string{"-local-network"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !config.localNetwork {
		t.Fatal("parseFlags(-local-network) did not set localNetwork")
	}
}

func TestLocalNetworkFlagDefaultIsLoopbackOnly(t *testing.T) {
	config, err := parseFlags(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if config.localNetwork {
		t.Fatal("localNetwork must default to false (loopback-only)")
	}
	if config.address != "127.0.0.1:8787" {
		t.Fatalf("default listen address = %q, want loopback 127.0.0.1:8787", config.address)
	}
}

func TestExplicitListenFlagIsRecordedForLocalNetworkAddressDiscovery(t *testing.T) {
	withoutListen, err := parseFlags([]string{"-local-network"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if withoutListen.listenWasSet {
		t.Fatal("listenWasSet must be false when -listen was not passed")
	}

	withListen, err := parseFlags([]string{"-local-network", "-listen", "192.168.1.20:9000"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !withListen.listenWasSet {
		t.Fatal("listenWasSet must be true when -listen was explicitly passed")
	}
	if withListen.address != "192.168.1.20:9000" {
		t.Fatalf("address = %q, want the explicit -listen value unchanged", withListen.address)
	}
}

func TestDaemonHelpListsLocalNetworkNotMobile(t *testing.T) {
	var output bytes.Buffer
	_, err := parseFlags([]string{"-h"}, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseFlags(-h) error = %v, want flag.ErrHelp", err)
	}
	help := output.String()
	if !strings.Contains(help, "-local-network") {
		t.Fatalf("--help is missing -local-network:\n%s", help)
	}
	if strings.Contains(help, "-mobile") {
		t.Fatalf("--help still mentions the removed -mobile flag:\n%s", help)
	}
}
