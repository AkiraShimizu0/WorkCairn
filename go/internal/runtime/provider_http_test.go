package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
)

func TestProviderHTTPClientUsesBoundedPublicBetaDefaultAndExplicitOverride(t *testing.T) {
	if DefaultProviderRequestTimeout != 5*time.Minute || DefaultProviderRequestTimeout <= time.Minute {
		t.Fatalf("DefaultProviderRequestTimeout = %s", DefaultProviderRequestTimeout)
	}
	if client := NewProviderHTTPClient(0); client.Timeout != DefaultProviderRequestTimeout {
		t.Fatalf("default client timeout = %s", client.Timeout)
	}
	if client := NewProviderHTTPClient(37 * time.Second); client.Timeout != 37*time.Second {
		t.Fatalf("override client timeout = %s", client.Timeout)
	}
}

func TestProviderHTTPClientAllowsResponseWithinConfiguredBound(t *testing.T) {
	// Scale the former and current policies down so the test proves the
	// boundary relationship without waiting real minutes.
	formerLimit, responseDelay, configuredLimit := 10*time.Millisecond, 20*time.Millisecond, 500*time.Millisecond
	if responseDelay <= formerLimit || responseDelay >= configuredLimit {
		t.Fatal("invalid scaled timeout test bounds")
	}
	client := NewProviderHTTPClient(configuredLimit)
	client.Transport = roundTripperFunc(func(*http.Request) (*http.Response, error) {
		time.Sleep(responseDelay)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"content-type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
			)),
		}, nil
	})

	runner := providerPolicyRunner(t, "https://provider.invalid", client)
	if _, err := runner.Run(context.Background(), providerPolicyRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestProviderHTTPClientTimeoutCancelsRequestWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	requestCanceled := make(chan struct{})
	client := NewProviderHTTPClient(30 * time.Millisecond)
	client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-request.Context().Done()
		close(requestCanceled)
		return nil, request.Context().Err()
	})

	runner := providerPolicyRunner(t, "https://provider.invalid", client)
	_, err := runner.Run(context.Background(), providerPolicyRequest())
	var providerError *claude.Error
	if !errors.As(err, &providerError) || providerError.Category != claude.FailureTransport || providerError.Transport != claude.TransportTimeout {
		t.Fatalf("timeout error = %#v, %v", providerError, err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("timed-out Provider request context was not canceled")
	}
	if calls.Load() != 1 {
		t.Fatalf("Provider calls = %d", calls.Load())
	}
}

func TestProviderHTTPClientPreservesCallerCancellation(t *testing.T) {
	started := make(chan struct{})
	client := NewProviderHTTPClient(time.Second)
	client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})

	runner := providerPolicyRunner(t, "https://provider.invalid", client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, providerPolicyRequest())
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation error = %v", err)
	}
}

func providerPolicyRunner(t *testing.T, baseURL string, client *http.Client) *claude.Runner {
	t.Helper()
	runner, err := claude.New(claude.Config{APIKey: "fake", ProviderModel: "claude-sonnet-5", BaseURL: baseURL}, client)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func providerPolicyRequest() worker.RunRequest {
	return worker.RunRequest{Model: "Claude Sonnet 5", SystemPrompt: "system", UserPrompt: "user"}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
