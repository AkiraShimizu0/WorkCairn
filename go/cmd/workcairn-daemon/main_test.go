package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/AkiraShimizu0/workcairn/go/internal/httpapi"
	workspaceprocess "github.com/AkiraShimizu0/workcairn/go/internal/process"
)

type restartCredentialStore struct {
	credential string
	loads      int
}

func (store *restartCredentialStore) Load(context.Context) (string, error) {
	store.loads++
	return store.credential, nil
}

func (store *restartCredentialStore) RequestAndStore(context.Context) (string, error) {
	return store.credential, nil
}

func TestDaemonRestartLoadsStoredClaudeCredentialIntoProviderStatus(t *testing.T) {
	store := &restartCredentialStore{credential: "stored-secret-never-exposed"}
	// A new ProcessExecutor models a daemon restart: no in-memory credential
	// survives, so startup must load the same persistent CredentialStore.
	credential := resolveClaudeCredential(context.Background(), "", store)
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
	if credential := resolveClaudeCredential(context.Background(), " explicit-override ", store); credential != "explicit-override" || store.loads != 0 {
		t.Fatalf("override resolution loads=%d", store.loads)
	}
}

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
