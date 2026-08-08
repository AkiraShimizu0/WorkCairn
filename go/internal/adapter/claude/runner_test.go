package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (function doerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestRunnerBuildsMessagesRequestAndMapsResponse(t *testing.T) {
	var received messageRequest
	doer := doerFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://provider.example/v1/messages" {
			t.Fatalf("request target = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("content-type") != "application/json" ||
			request.Header.Get("anthropic-version") != "2023-06-01" ||
			request.Header.Get("x-api-key") != "fake-api-key" {
			t.Fatal("required Anthropic headers were not supplied")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, `{
			"model":"claude-sonnet-5",
			"content":[
				{"type":"thinking","thinking":"hidden"},
				{"type":"text","text":"  # 成果物  "},
				{"type":"text","text":"Markdown本文"}
			],
			"usage":{"input_tokens":120,"output_tokens":30}
		}`, "request-123"), nil
	})
	runner, err := New(Config{
		APIKey:        "fake-api-key",
		ProviderModel: "claude-sonnet-5",
		MaxTokens:     1234,
		BaseURL:       "https://provider.example/",
	}, doer)
	if err != nil {
		t.Fatal(err)
	}

	metadata := map[string]string{"correlation_id": "execution-001"}
	result, err := runner.Run(context.Background(), worker.RunRequest{
		Model:        "Claude Sonnet 5",
		SystemPrompt: "System Prompt\n",
		UserPrompt:   "User Prompt\n",
		Metadata:     metadata,
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRequest := messageRequest{
		Model:     "claude-sonnet-5",
		MaxTokens: 1234,
		System:    "System Prompt\n",
		Messages:  []message{{Role: "user", Content: "User Prompt\n"}},
	}
	if !reflect.DeepEqual(received, wantRequest) {
		t.Fatalf("Messages request = %#v, want %#v", received, wantRequest)
	}
	if result.Content != "# 成果物\nMarkdown本文" || result.Runner != Name || result.Model != "Claude Sonnet 5" {
		t.Fatalf("Run() result = %#v", result)
	}
	if result.Usage.InputTokens == nil || *result.Usage.InputTokens != 120 ||
		result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 30 {
		t.Fatalf("Run() usage = %#v", result.Usage)
	}
	if result.Duration < 0 || !reflect.DeepEqual(result.Metadata, metadata) {
		t.Fatalf("Run() metadata/duration = %#v, %v", result.Metadata, result.Duration)
	}
	result.Metadata["correlation_id"] = "changed"
	if metadata["correlation_id"] != "execution-001" {
		t.Fatal("Run() returned request metadata without cloning it")
	}
}

func TestNewValidatesConfigAndAppliesStableDefaults(t *testing.T) {
	valid := Config{APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5"}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	runner, err := New(valid, doer)
	if err != nil {
		t.Fatal(err)
	}
	if runner.endpoint != "https://api.anthropic.com/v1/messages" || runner.apiVersion != "2023-06-01" || runner.maxTokens != 3000 {
		t.Fatalf("defaults = endpoint %q, API version %q, max tokens %d", runner.endpoint, runner.apiVersion, runner.maxTokens)
	}

	var nilDoer *http.Client
	for _, test := range []struct {
		name   string
		config Config
		client HTTPDoer
	}{
		{"API key", Config{ProviderModel: "claude-sonnet-5"}, doer},
		{"Provider model", Config{APIKey: "fake-api-key"}, doer},
		{"HTTP client", valid, nil},
		{"typed nil HTTP client", valid, nilDoer},
		{"negative max tokens", Config{APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", MaxTokens: -1}, doer},
		{"invalid base URL", Config{APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: "file:///tmp/api"}, doer},
		{"credential-bearing base URL", Config{APIKey: "fake-api-key", ProviderModel: "claude-sonnet-5", BaseURL: "https://user@example.test"}, doer},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config, test.client); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestRunnerRejectsInvalidRequestWithoutCallingTransport(t *testing.T) {
	calls := 0
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	}))
	for _, request := range []worker.RunRequest{
		{SystemPrompt: "system", UserPrompt: "user"},
		{Model: "Claude Sonnet 5", UserPrompt: "user"},
		{Model: "Claude Sonnet 5", SystemPrompt: "system"},
	} {
		if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Run() error = %v", err)
		}
	}
	if _, err := runner.Run(nil, worker.RunRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil context error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d", calls)
	}
}

func TestRunnerReturnsTypedProviderErrorWithoutResponseBody(t *testing.T) {
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(
			http.StatusTooManyRequests,
			`{"type":"error","error":{"type":"rate_limit_error","message":"sensitive provider detail"}}`,
			"request-rate-limited",
		), nil
	}))

	_, err := runner.Run(context.Background(), validRunRequest())
	if !errors.Is(err, ErrProvider) {
		t.Fatalf("Run() error = %v", err)
	}
	var providerError *Error
	if !errors.As(err, &providerError) || providerError.StatusCode != http.StatusTooManyRequests || providerError.RequestID != "request-rate-limited" {
		t.Fatalf("typed provider error = %#v", providerError)
	}
	if strings.Contains(err.Error(), "sensitive provider detail") {
		t.Fatal("Provider response body leaked through Error()")
	}
}

func TestRunnerRejectsMalformedOrNonTextResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"model":`},
		{"missing model", `{"content":[{"type":"text","text":"ok"}],"usage":{}}`},
		{"no text block", `{"model":"claude-sonnet-5","content":[{"type":"thinking"}],"usage":{}}`},
		{"negative usage", `{"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":-1,"output_tokens":1}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, test.body, "request-invalid"), nil
			}))
			if _, err := runner.Run(context.Background(), validRunRequest()); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

func TestRunnerMapsTransportAndContextFailures(t *testing.T) {
	transportCause := errors.New("transport unavailable")
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportCause
	}))
	if _, err := runner.Run(context.Background(), validRunRequest()); !errors.Is(err, ErrTransport) || !errors.Is(err, transportCause) {
		t.Fatalf("transport error = %v", err)
	}

	started := make(chan struct{})
	runner = configuredRunner(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, validRunRequest())
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := runner.Run(expired, validRunRequest()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
}

func configuredRunner(t *testing.T, doer HTTPDoer) *Runner {
	t.Helper()
	runner, err := New(Config{
		APIKey:        "fake-api-key",
		ProviderModel: "claude-sonnet-5",
	}, doer)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func validRunRequest() worker.RunRequest {
	return worker.RunRequest{
		Model:        "Claude Sonnet 5",
		SystemPrompt: "System Prompt",
		UserPrompt:   "User Prompt",
	}
}

func jsonResponse(statusCode int, body, requestID string) *http.Response {
	header := make(http.Header)
	header.Set("request-id", requestID)
	return &http.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
