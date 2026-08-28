package httpapi

import (
	"net/http"
	"testing"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/autonomy"
	workspaceprocess "github.com/AkiraShimizu0/WorkCairn/go/internal/process"
)

func TestProcessExecutorProviderFixtureBudgetIsExplicitAndBounded(t *testing.T) {
	for _, value := range []int{-1, autonomy.MaxProviderCallsCeiling + 1} {
		if _, err := NewProcessExecutorWithActionConfigAndOptions(
			t.TempDir(), workspaceprocess.ClaudeProcessConfig{}, workspaceprocess.WordPressProcessConfig{},
			http.DefaultClient, ProcessExecutorOptions{ProviderFixtureMaxCalls: value},
		); err == nil {
			t.Fatalf("ProviderFixtureMaxCalls=%d accepted", value)
		}
	}
	executor, err := NewProcessExecutorWithActionConfigAndOptions(
		t.TempDir(), workspaceprocess.ClaudeProcessConfig{}, workspaceprocess.WordPressProcessConfig{},
		http.DefaultClient, ProcessExecutorOptions{ProviderFixtureMaxCalls: 6},
	)
	if err != nil || executor.providerFixtureMaxCalls != 6 {
		t.Fatalf("explicit fixture Budget = %d, %v", executor.providerFixtureMaxCalls, err)
	}
	standard, err := NewProcessExecutorWithActionConfig(
		t.TempDir(), workspaceprocess.ClaudeProcessConfig{}, workspaceprocess.WordPressProcessConfig{}, http.DefaultClient,
	)
	if err != nil || standard.providerFixtureMaxCalls != 0 {
		t.Fatalf("production default fixture Budget = %d, %v", standard.providerFixtureMaxCalls, err)
	}
}
