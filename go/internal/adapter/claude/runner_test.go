package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
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
	valid := Config{APIKey: "fake-api-key"}
	doer := doerFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	runner, err := New(valid, doer)
	if err != nil {
		t.Fatal(err)
	}
	if runner.endpoint != "https://api.anthropic.com/v1/messages" || runner.apiVersion != "2023-06-01" || runner.maxTokens != 3000 || runner.providerModel != "claude-sonnet-5" {
		t.Fatalf("defaults = endpoint %q, API version %q, max tokens %d, Provider model %q", runner.endpoint, runner.apiVersion, runner.maxTokens, runner.providerModel)
	}

	var nilDoer *http.Client
	for _, test := range []struct {
		name   string
		config Config
		client HTTPDoer
	}{
		{"API key", Config{ProviderModel: "claude-sonnet-5"}, doer},
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

func TestAutomaticModelPolicyResolvesOnlySupportedLogicalRoute(t *testing.T) {
	for _, requested := range []string{"", AutomaticRoute} {
		resolution, err := ResolveModel(requested)
		if err != nil || resolution.LogicalRoute != AutomaticRoute || resolution.PolicyVersion != AutomaticModelPolicyVersion || resolution.ProviderModel != "claude-sonnet-5" {
			t.Fatalf("ResolveModel(%q) = %#v, %v", requested, resolution, err)
		}
	}
	override, err := ResolveModel("mock-provider-model")
	if err != nil || override.LogicalRoute != "explicit" || override.ProviderModel != "mock-provider-model" {
		t.Fatalf("explicit override = %#v, %v", override, err)
	}
	if _, err := ResolveModel("workcairn-unknown"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unknown logical route error = %v", err)
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

func TestRunnerClassifiesProviderErrorsWithoutRetainingMessages(t *testing.T) {
	for _, test := range []struct {
		name, providerType string
		status             int
		category           FailureCategory
	}{
		{"authentication", "authentication_error", 401, FailureAuthentication},
		{"billing", "billing_error", 402, FailureBilling},
		{"permission", "permission_error", 403, FailurePermission},
		{"request", "invalid_request_error", 400, FailureInvalidRequest},
		{"rate limit", "rate_limit_error", 429, FailureRateLimited},
		{"overloaded", "overloaded_error", 529, FailureUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":"secret provider detail"},"request_id":"req_safe_123"}`, test.providerType)
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(test.status, body, ""), nil
			}))
			_, err := runner.Run(context.Background(), validRunRequest())
			var failure *Error
			if !errors.As(err, &failure) || failure.StatusCode != test.status || failure.ProviderType != test.providerType ||
				failure.Category != test.category || failure.RequestID != "req_safe_123" || strings.Contains(err.Error(), "secret provider detail") {
				t.Fatalf("Provider failure = %#v, %v", failure, err)
			}
		})
	}
}

func TestRunnerSendsSonnetFiveCompatibleMinimalPayload(t *testing.T) {
	var payload map[string]any
	runner := configuredRunner(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, `{"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`, "req-compatible"), nil
	}))
	if _, err := runner.Run(context.Background(), validRunRequest()); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"max_tokens", "messages", "model", "system"}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("Messages payload keys = %v, want %v", keys, wantKeys)
	}
	for _, forbidden := range []string{"thinking", "temperature", "top_p", "top_k"} {
		if _, exists := payload[forbidden]; exists {
			t.Fatalf("Messages payload contains Sonnet 5-incompatible field %q", forbidden)
		}
	}
}

func TestRunnerSendsStructuredOutputConfigWhenRequested(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"project_name": map[string]any{"type": "string"}},
		"required":             []string{"project_name"},
		"additionalProperties": false,
	}
	var received messageRequest
	runner := configuredRunner(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, `{"model":"claude-sonnet-5","content":[{"type":"text","text":"{\"project_name\":\"P\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, "req-so"), nil
	}))

	requestWithSchema := validRunRequest()
	requestWithSchema.StructuredOutput = &worker.StructuredOutputContract{Schema: schema}
	result, err := runner.Run(context.Background(), requestWithSchema)
	if err != nil {
		t.Fatal(err)
	}
	wantSchema, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	gotSchema, err := json.Marshal(received.OutputConfig.Format.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if received.OutputConfig == nil || received.OutputConfig.Format.Type != "json_schema" || string(gotSchema) != string(wantSchema) {
		t.Fatalf("output_config = %#v", received.OutputConfig)
	}
	// No ContentField: the schema's own JSON is the Content verbatim, exactly
	// like a non-Structured-Output response.
	if result.Content != `{"project_name":"P"}` {
		t.Fatalf("Content = %q", result.Content)
	}

	// A request without StructuredOutput must not send output_config at all
	// (verified against the same server, reusing TestRunnerSendsSonnetFiveCompatibleMinimalPayload's payload-key check would be redundant).
	received = messageRequest{}
	if _, err := runner.Run(context.Background(), validRunRequest()); err != nil {
		t.Fatal(err)
	}
	if received.OutputConfig != nil {
		t.Fatalf("output_config leaked onto a request without StructuredOutput: %#v", received.OutputConfig)
	}
}

// TestRunnerUnwrapsStructuredOutputContentField locks the generic
// ContentField unwrap mechanism itself (worker.StructuredOutputContract),
// independent of any specific Domain package's use of it. No production
// caller currently sets ContentField (both ceoplan and review request the
// schema's own JSON as Content directly), but the mechanism stays generic
// and reusable for a future caller whose contract is a free-form string a
// JSON Schema cannot itself express.
func TestRunnerUnwrapsStructuredOutputContentField(t *testing.T) {
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		envelope := `{"response":"free-form content a JSON Schema object cannot itself express"}`
		body := `{"model":"claude-sonnet-5","content":[{"type":"text","text":` + jsonQuote(t, envelope) + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
		return jsonResponse(http.StatusOK, body, "req-unwrap"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{
		Schema:       map[string]any{"type": "object"},
		ContentField: "response",
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := "free-form content a JSON Schema object cannot itself express"
	if result.Content != want {
		t.Fatalf("unwrapped Content = %q, want %q", result.Content, want)
	}
}

func TestRunnerRejectsStructuredOutputContractViolations(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"not JSON", `{"model":"claude-sonnet-5","content":[{"type":"text","text":"not json"}],"usage":{}}`},
		{"missing declared field", `{"model":"claude-sonnet-5","content":[{"type":"text","text":"{\"other\":\"x\"}"}],"usage":{}}`},
		{"extra field beyond envelope", `{"model":"claude-sonnet-5","content":[{"type":"text","text":"{\"response\":\"x\",\"extra\":\"y\"}"}],"usage":{}}`},
		{"empty declared field", `{"model":"claude-sonnet-5","content":[{"type":"text","text":"{\"response\":\"\"}"}],"usage":{}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, test.body, "req-so-invalid"), nil
			}))
			request := validRunRequest()
			request.StructuredOutput = &worker.StructuredOutputContract{Schema: map[string]any{"type": "object"}, ContentField: "response"}
			_, err := runner.Run(context.Background(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Category != FailureStructuredOutputInvalid {
				t.Fatalf("err = %v, failure = %#v", err, failure)
			}
		})
	}
}

func TestRunnerRejectsEmptyStructuredOutputSchemaWithoutCallingTransport(t *testing.T) {
	calls := 0
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{}
	if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Run() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d", calls)
	}
}

func TestRunnerClassifiesRefusalStopReasonWithoutContent(t *testing.T) {
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK,
			`{"model":"claude-sonnet-5","content":[],"stop_reason":"refusal","stop_details":{"category":"cyber"},"usage":{"input_tokens":1,"output_tokens":0}}`,
			"req-refusal"), nil
	}))
	_, err := runner.Run(context.Background(), validRunRequest())
	var failure *Error
	if !errors.As(err, &failure) || failure.Category != FailureRefusal || failure.ProviderType != "cyber" {
		t.Fatalf("err = %v, failure = %#v", err, failure)
	}
}

func jsonQuote(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
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
