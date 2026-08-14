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
