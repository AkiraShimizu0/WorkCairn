package claude

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/ceoplan"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/failure"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/review"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/worker"
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

func TestRunnerSerializesCEOIntentRequestFixture(t *testing.T) {
	var received []byte
	runner := configuredRunner(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		received, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		intent := `{"project_name":"P","objective":"O","summary":"S","steps":[{"kind":"write","description":"D","required_role":"R"}],"ceo_questions":[]}`
		body := `{"model":"claude-sonnet-5","content":[{"type":"text","text":` + jsonQuote(t, intent) + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
		return jsonResponse(http.StatusOK, body, "req-ceo-intent"), nil
	}))

	// Fixed, deterministic Organization Role enum (ADR-0048) — matches the
	// fixture file's steps[].required_role.enum exactly.
	schema, err := ceoplan.IntentJSONSchema([]string{"Content Writer", "Product Manager", "QA Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: schema}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	fixturePath := filepath.Join("..", "..", "..", "..", "fixtures", "provider", "claude_ceo_intent_request_v1.json")
	want, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal(received, &gotJSON); err != nil {
		t.Fatalf("serialized request is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("request fixture is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("serialized CEO Intent request does not match fixed Provider fixture\ngot:  %s\nwant: %s", received, want)
	}
}

// TestRunnerSerializesReviewTypedDecisionRequestFixture pins the exact
// output_config.format bytes this Adapter sends Anthropic for Review
// execution against a fixed fixture file, the same way
// TestRunnerSerializesCEOIntentRequestFixture pins CEO Intent's. It exists
// to make an accidental future regression in TypedDecisionJSONSchema (e.g.
// summary dropped from one anyOf branch's required list, or moved outside
// each branch's own properties) fail a byte-level assertion instead of
// only the schema package's own shape test.
func TestRunnerSerializesReviewTypedDecisionRequestFixture(t *testing.T) {
	var received []byte
	runner := configuredRunner(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		received, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		decision := `{"verdict":"Approve","issues":[],"summary":"S"}`
		body := `{"model":"claude-sonnet-5","content":[{"type":"text","text":` + jsonQuote(t, decision) + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
		return jsonResponse(http.StatusOK, body, "req-review-typed-decision"), nil
	}))

	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	fixturePath := filepath.Join("..", "..", "..", "..", "fixtures", "provider", "claude_review_typed_decision_request_v1.json")
	want, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var gotJSON, wantJSON any
	if err := json.Unmarshal(received, &gotJSON); err != nil {
		t.Fatalf("serialized request is not JSON: %v", err)
	}
	if err := json.Unmarshal(want, &wantJSON); err != nil {
		t.Fatalf("request fixture is not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("serialized Review request does not match fixed Provider fixture\ngot:  %s\nwant: %s", received, want)
	}

	// Both anyOf branches must independently require summary — the
	// concrete regression this fixture guards against.
	schema, ok := wantJSON.(map[string]any)["output_config"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
	if !ok {
		t.Fatalf("fixture schema shape = %#v", wantJSON)
	}
	variants, ok := schema["anyOf"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("fixture anyOf = %#v", schema["anyOf"])
	}
	for index, rawVariant := range variants {
		variant := rawVariant.(map[string]any)
		required, ok := variant["required"].([]any)
		if !ok {
			t.Fatalf("variant %d required = %#v", index, variant["required"])
		}
		found := false
		for _, field := range required {
			if field == "summary" {
				found = true
			}
		}
		if !found {
			t.Fatalf("variant %d does not require summary: %#v", index, required)
		}
	}
}

// TestRunnerReviewStructuredOutputContentSurvivesExtractionUnchanged proves,
// at the Adapter boundary rather than by code reading, that
// messageResponse.structuredJSON() neither loses nor alters the
// Provider's raw JSON text: Runner.Result.Content must be byte-identical
// to the Provider's own text block, both when it carries "summary" and
// when it does not. The Runner is Domain-neutral and has no opinion about
// which top-level keys a Review Typed Decision requires — a response
// missing "summary" is still a structurally valid Structured Output (one
// JSON text block) and must succeed at this layer; only the Domain parser
// (review.ParseTypedDecision) may reject it. This is the concrete test
// evidence for whether an Adapter decode/re-marshal step could ever drop
// "summary": InspectStructuredFieldPresence on the raw mock response text
// and on the Runner's extracted Content must agree in both cases.
func TestRunnerReviewStructuredOutputContentSurvivesExtractionUnchanged(t *testing.T) {
	tests := []struct {
		name         string
		decision     string
		wantPresence map[string]bool
	}{
		{"summary present", `{"verdict":"Approve","issues":[],"summary":"S"}`, map[string]bool{"verdict": true, "issues": true, "summary": true}},
		{"summary key absent", `{"verdict":"Approve","issues":[]}`, map[string]bool{"verdict": true, "issues": true, "summary": false}},
		{"summary present but empty string", `{"verdict":"Approve","issues":[],"summary":""}`, map[string]bool{"verdict": true, "issues": true, "summary": true}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				body := `{"model":"claude-sonnet-5","content":[{"type":"text","text":` + jsonQuote(t, current.decision) + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
				return jsonResponse(http.StatusOK, body, "req-review-content-passthrough"), nil
			}))
			request := validRunRequest()
			request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
			result, err := runner.Run(context.Background(), request)
			if err != nil {
				t.Fatalf("Runner must not reject a structurally valid Structured Output response regardless of which Review keys it carries: %v", err)
			}
			if result.Content != current.decision {
				t.Fatalf("Content = %q, want byte-identical to Provider text %q", result.Content, current.decision)
			}
			if !reflect.DeepEqual(result.StructuredOutputPresence, current.wantPresence) {
				t.Fatalf("StructuredOutputPresence = %#v, want %#v", result.StructuredOutputPresence, current.wantPresence)
			}
		})
	}
}

// TestRunnerReviewStructuredOutputMissingSummaryFailsOnlyAtDomainParser
// completes the same boundary proof end-to-end: a Provider response
// missing "summary" must fail exactly once, inside
// review.ParseTypedDecision, as missing_required_field/summary — never
// earlier, at the Runner/Adapter layer. The Runner still captures and
// returns the presence diagnostic even though it does not itself reject
// the response, since Case A/B disambiguation depends on presence having
// been observed before the Domain parser ever runs.
func TestRunnerReviewStructuredOutputMissingSummaryFailsOnlyAtDomainParser(t *testing.T) {
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		decision := `{"verdict":"Approve","issues":[]}`
		body := `{"model":"claude-sonnet-5","content":[{"type":"text","text":` + jsonQuote(t, decision) + `}],"usage":{"input_tokens":1,"output_tokens":1}}`
		return jsonResponse(http.StatusOK, body, "req-review-missing-summary"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Runner (Domain-neutral) must succeed: %v", err)
	}
	if result.StructuredOutputPresence["summary"] {
		t.Fatalf("StructuredOutputPresence = %#v, want summary=false", result.StructuredOutputPresence)
	}
	_, parseErr := review.ParseTypedDecision(result.Content)
	var typedErr *review.ParseError
	if !errors.As(parseErr, &typedErr) || typedErr.Reason != review.ParseFailureMissingRequiredField || typedErr.Field != "summary" {
		t.Fatalf("review.ParseTypedDecision(%q) = %v, want missing_required_field/summary", result.Content, parseErr)
	}
}

// TestStructuredOutputFieldPresenceReportsSchemaDeclaredKeysOnly locks the
// Adapter-owned, Provider-neutral presence computation itself: it derives
// which top-level keys to check entirely from the schema's own declared
// "properties" (including the union across a top-level "anyOf"), never
// from a hardcoded Domain field name.
func TestStructuredOutputFieldPresenceReportsSchemaDeclaredKeysOnly(t *testing.T) {
	schema := review.TypedDecisionJSONSchema()
	tests := []struct {
		name    string
		content string
		want    map[string]bool
	}{
		{"summary present", `{"verdict":"Approve","issues":[],"summary":"S"}`, map[string]bool{"verdict": true, "issues": true, "summary": true}},
		{"summary key absent", `{"verdict":"Approve","issues":[]}`, map[string]bool{"verdict": true, "issues": true, "summary": false}},
		{"summary present but empty string", `{"verdict":"Approve","issues":[],"summary":""}`, map[string]bool{"verdict": true, "issues": true, "summary": true}},
		{"summary present but null", `{"verdict":"Approve","issues":[],"summary":null}`, map[string]bool{"verdict": true, "issues": true, "summary": true}},
		{"all absent", `{}`, map[string]bool{"verdict": false, "issues": false, "summary": false}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			presence := structuredOutputFieldPresence(schema, current.content)
			if !reflect.DeepEqual(presence, current.want) {
				t.Fatalf("presence = %#v, want %#v", presence, current.want)
			}
		})
	}
}

// TestStructuredOutputFieldPresenceNeverGuessesOnMalformedContent proves
// the diagnostic returns nil (no diagnosis) rather than a guessed value
// when the content is not itself a JSON object.
func TestStructuredOutputFieldPresenceNeverGuessesOnMalformedContent(t *testing.T) {
	schema := review.TypedDecisionJSONSchema()
	for _, content := range []string{"", "not json", "[]", `"a string"`, "null", `{"verdict":`} {
		if presence := structuredOutputFieldPresence(schema, content); presence != nil {
			t.Fatalf("content %q: presence = %#v, want nil", content, presence)
		}
	}
}

// TestStructuredOutputFieldPresenceReturnsNilForUndeclaredSchemaShape
// covers a schema with no declared top-level "properties" at all (e.g. the
// free-form ContentField schema TestRunnerUnwrapsStructuredOutputContentField
// uses) — there is nothing to check presence against, so the diagnostic is
// nil rather than an empty, misleadingly "complete" map.
// TestStructuredOutputFieldShapeReportsSummaryShapeWithoutContent locks the
// content-blind shape diagnostic used to disambiguate missing_required_field
// when StructuredOutputPresence alone cannot (summary key present but empty,
// null, or non-string).
func TestStructuredOutputFieldShapeReportsSummaryShapeWithoutContent(t *testing.T) {
	schema := review.TypedDecisionJSONSchema()
	tests := []struct {
		name    string
		content string
		want    failure.StructuredOutputFieldShape
	}{
		{
			"non-blank string",
			`{"verdict":"Approve","issues":[],"summary":"S"}`,
			failure.StructuredOutputFieldShape{Present: true, JSONType: "string", NonBlank: boolPtr(true)},
		},
		{
			"empty string",
			`{"verdict":"Approve","issues":[],"summary":""}`,
			failure.StructuredOutputFieldShape{Present: true, JSONType: "string", NonBlank: boolPtr(false)},
		},
		{
			"whitespace string",
			`{"verdict":"Approve","issues":[],"summary":"   "}`,
			failure.StructuredOutputFieldShape{Present: true, JSONType: "string", NonBlank: boolPtr(false)},
		},
		{
			"null",
			`{"verdict":"Approve","issues":[],"summary":null}`,
			failure.StructuredOutputFieldShape{Present: true, JSONType: "null"},
		},
		{
			"key absent",
			`{"verdict":"Approve","issues":[]}`,
			failure.StructuredOutputFieldShape{Present: false},
		},
		{
			"non-string",
			`{"verdict":"Approve","issues":[],"summary":1}`,
			failure.StructuredOutputFieldShape{Present: true, JSONType: "other"},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			shapes := structuredOutputFieldShape(schema, current.content)
			got := shapes["summary"]
			if got.Present != current.want.Present || got.JSONType != current.want.JSONType {
				t.Fatalf("shape = %#v, want %#v", got, current.want)
			}
			if !boolPtrEqual(got.NonBlank, current.want.NonBlank) {
				t.Fatalf("NonBlank = %#v, want %#v", got.NonBlank, current.want.NonBlank)
			}
		})
	}
}

// TestStructuredOutputStepDescriptionShapeReportsPerStepShapeAcrossMultiStepIntent
// locks the content-blind diagnostic added for the CMD-E35C1166
// investigation: a missing_required_field/steps.description failure alone
// cannot distinguish an absent key from an empty string, a whitespace-only
// string, an explicit null, or a non-string value. This asserts every one
// of those five shapes is reported independently and correctly across a
// single multi-step Intent response, keyed by step index, and that a
// valid non-blank description is reported too (not just failure shapes).
func TestStructuredOutputStepDescriptionShapeReportsPerStepShapeAcrossMultiStepIntent(t *testing.T) {
	schema, err := ceoplan.IntentJSONSchema([]string{"Content Writer"})
	if err != nil {
		t.Fatal(err)
	}
	content := `{"steps":[` +
		`{"kind":"write","description":"D","required_role":"Content Writer"},` +
		`{"kind":"write","required_role":"Content Writer"},` +
		`{"kind":"write","description":"","required_role":"Content Writer"},` +
		`{"kind":"write","description":"   ","required_role":"Content Writer"},` +
		`{"kind":"write","description":null,"required_role":"Content Writer"},` +
		`{"kind":"write","description":42,"required_role":"Content Writer"}` +
		`],"ceo_questions":[]}`
	shapes := structuredOutputStepDescriptionShape(schema, content)
	want := map[string]failure.StructuredOutputFieldShape{
		"steps.0.description": {Present: true, JSONType: "string", NonBlank: boolPtr(true)},
		"steps.1.description": {Present: false},
		"steps.2.description": {Present: true, JSONType: "string", NonBlank: boolPtr(false)},
		"steps.3.description": {Present: true, JSONType: "string", NonBlank: boolPtr(false)},
		"steps.4.description": {Present: true, JSONType: "null"},
		"steps.5.description": {Present: true, JSONType: "other"},
	}
	if len(shapes) != len(want) {
		t.Fatalf("shapes = %#v, want %d entries", shapes, len(want))
	}
	for key, wantShape := range want {
		got, ok := shapes[key]
		if !ok {
			t.Fatalf("shapes missing key %q: %#v", key, shapes)
		}
		if got.Present != wantShape.Present || got.JSONType != wantShape.JSONType || !boolPtrEqual(got.NonBlank, wantShape.NonBlank) {
			t.Fatalf("shapes[%q] = %#v, want %#v", key, got, wantShape)
		}
	}
}

// TestStructuredOutputStepDescriptionShapeIsNilForNonCEOIntentSchema locks
// that this diagnostic only activates for a schema declaring a top-level
// "steps" array (CEO Intent) -- every other Structured Output caller
// (e.g. Review) must see a nil result, never a spurious empty map.
func TestStructuredOutputStepDescriptionShapeIsNilForNonCEOIntentSchema(t *testing.T) {
	schema := review.TypedDecisionJSONSchema()
	if shapes := structuredOutputStepDescriptionShape(schema, `{"verdict":"Approve","issues":[],"summary":"S"}`); shapes != nil {
		t.Fatalf("shapes = %#v, want nil", shapes)
	}
}

func boolPtr(value bool) *bool { return &value }

func boolPtrEqual(left, right *bool) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	return *left == *right
}

func TestStructuredOutputFieldPresenceReturnsNilForUndeclaredSchemaShape(t *testing.T) {
	if presence := structuredOutputFieldPresence(map[string]any{"type": "object"}, `{"a":1}`); presence != nil {
		t.Fatalf("presence = %#v, want nil for a schema with no declared properties", presence)
	}
}

// TestStructuredOutputFieldPresenceIsGenericAcrossDomainSchemas
// demonstrates, without wiring any new propagation for CEO Intent this
// round, that the mechanism is genuinely schema-driven rather than
// Review-specific: it derives the same correct presence map from CEO
// Intent's plain-object (non-anyOf) schema with zero Review-specific code.
func TestStructuredOutputFieldPresenceIsGenericAcrossDomainSchemas(t *testing.T) {
	schema, err := ceoplan.IntentJSONSchema([]string{"Content Writer", "Product Manager", "QA Engineer"})
	if err != nil {
		t.Fatal(err)
	}
	content := `{"project_name":"P","objective":"O","steps":[],"ceo_questions":[]}`
	want := map[string]bool{"project_name": true, "objective": true, "summary": false, "steps": true, "ceo_questions": true}
	if presence := structuredOutputFieldPresence(schema, content); !reflect.DeepEqual(presence, want) {
		t.Fatalf("presence = %#v, want %#v", presence, want)
	}
}

func TestRunnerSendsReviewStructuredOutputAndExtractsOneJSONTextBlock(t *testing.T) {
	fixtures := loadStructuredOutputExtractionFixtures(t)
	var payload map[string]json.RawMessage
	runner := configuredRunner(t, doerFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, string(fixtures["single_json_with_thinking"]), "req-review-structured"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != `{"verdict":"Approve","issues":[],"summary":"Approved."}` {
		t.Fatalf("structured Review content = %q", result.Content)
	}

	wantKeys := []string{"max_tokens", "messages", "model", "output_config", "system"}
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !reflect.DeepEqual(keys, wantKeys) {
		t.Fatalf("Review request keys = %v, want %v", keys, wantKeys)
	}
	var config outputConfig
	if err := json.Unmarshal(payload["output_config"], &config); err != nil {
		t.Fatal(err)
	}
	gotSchema, err := json.Marshal(config.Format.Schema)
	if err != nil {
		t.Fatal(err)
	}
	wantSchema, err := json.Marshal(review.TypedDecisionJSONSchema())
	if err != nil {
		t.Fatal(err)
	}
	if config.Format.Type != "json_schema" || string(gotSchema) != string(wantSchema) {
		t.Fatalf("Review output_config = %#v", config)
	}
}

func TestRunnerRejectsStructuredOutputResponseContractViolations(t *testing.T) {
	fixtures := loadStructuredOutputExtractionFixtures(t)
	for _, name := range []string{"multiple_text_blocks", "trailing_prose", "second_json_value", "unexpected_content_block"} {
		t.Run(name, func(t *testing.T) {
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, string(fixtures[name]), "req-review-invalid"), nil
			}))
			request := validRunRequest()
			request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
			_, err := runner.Run(context.Background(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Category != FailureStructuredOutputInvalid || failure.RequestID != "req-review-invalid" {
				t.Fatalf("structured response failure = %#v, %v", failure, err)
			}
		})
	}
}

// TestRunnerClassifiesStructuredOutputInvalidReasonsIndependently locks the
// closed, content-blind StructuredOutputInvalidReason for each distinct
// extraction failure shape (PB-3ah.1): an unexpected content block, an
// invalid text-block count, an empty text block, and the three ways the
// text block's own bytes can fail to be exactly one JSON document (invalid
// syntax, a second complete document following it, or non-JSON content
// following it). Every case has stop_reason absent or "end_turn" -- none of
// them is a Provider output-ceiling truncation, so all must still fail as
// FailureStructuredOutputInvalid with the specific reason, never a generic
// unclassified error.
// TestErrorStructuredOutputReasonDegradesUnknownOrForgedValuesToEmpty is the
// PB-3ah.3 regression for the private-field/closed-accessor boundary: the
// unexported structuredOutputReason field is only reachable from within
// this package, but the public StructuredOutputReason() accessor is the
// only way any caller (in-package or not) may read it, and it must degrade
// silently to "" for the zero value and for any string this package's own
// construction never actually produces -- never pass an unvalidated value
// through. This proves a forged or unrecognized value can never leave this
// package as if it were a genuine classification.
func TestErrorStructuredOutputReasonDegradesUnknownOrForgedValuesToEmpty(t *testing.T) {
	var nilError *Error
	if got := nilError.StructuredOutputReason(); got != "" {
		t.Fatalf("nil *Error: StructuredOutputReason() = %q, want empty", got)
	}
	for _, forged := range []StructuredOutputInvalidReason{
		"", "not_a_real_reason", "UNEXPECTED_CONTENT_BLOCK", "unexpected_content_block ", " ",
	} {
		err := &Error{Category: FailureStructuredOutputInvalid, structuredOutputReason: forged}
		if got := err.StructuredOutputReason(); got != "" {
			t.Fatalf("forged reason %q: StructuredOutputReason() = %q, want empty", forged, got)
		}
	}
	for _, valid := range []StructuredOutputInvalidReason{
		StructuredOutputUnexpectedBlock, StructuredOutputBlockCountInvalid, StructuredOutputEmptyText,
		StructuredOutputInvalidJSON, StructuredOutputMultipleJSON, StructuredOutputTrailingJSON,
	} {
		err := &Error{Category: FailureStructuredOutputInvalid, structuredOutputReason: valid}
		if got := err.StructuredOutputReason(); got != valid {
			t.Fatalf("valid reason %q: StructuredOutputReason() = %q, want unchanged", valid, got)
		}
	}
	// A valid allow-listed reason value must still degrade to empty when
	// Category is anything other than FailureStructuredOutputInvalid -- the
	// accessor's own contract ("never a reason outside
	// structured_output_invalid") must hold on its own, not merely because
	// every current caller happens to check Category first.
	for _, category := range []FailureCategory{
		FailureUnavailable, FailureRateLimited, FailureAuthentication, FailureRefusal, "", FailureUnknown,
	} {
		err := &Error{Category: category, structuredOutputReason: StructuredOutputInvalidJSON}
		if got := err.StructuredOutputReason(); got != "" {
			t.Fatalf("valid reason with wrong category %q: StructuredOutputReason() = %q, want empty", category, got)
		}
	}
}

func TestRunnerClassifiesStructuredOutputInvalidReasonsIndependently(t *testing.T) {
	fixtures := loadStructuredOutputExtractionFixtures(t)
	tests := []struct {
		fixture string
		want    StructuredOutputInvalidReason
	}{
		{"unexpected_content_block", StructuredOutputUnexpectedBlock},
		{"multiple_text_blocks", StructuredOutputBlockCountInvalid},
		{"empty_text", StructuredOutputEmptyText},
		{"invalid_json_end_turn", StructuredOutputInvalidJSON},
		{"second_json_value", StructuredOutputMultipleJSON},
		{"trailing_prose", StructuredOutputTrailingJSON},
	}
	for _, test := range tests {
		t.Run(string(test.want), func(t *testing.T) {
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, string(fixtures[test.fixture]), "req-reason"), nil
			}))
			request := validRunRequest()
			request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
			_, err := runner.Run(context.Background(), request)
			var failure *Error
			if !errors.As(err, &failure) || failure.Category != FailureStructuredOutputInvalid || failure.StructuredOutputReason() != test.want {
				t.Fatalf("fixture %q: err = %v, failure = %#v, want reason %q", test.fixture, err, failure, test.want)
			}
			if strings.Contains(failure.Error(), "Approve") || strings.Contains(err.Error(), "Approve") {
				t.Fatalf("Error() leaked Provider content: %q", err.Error())
			}
		})
	}
}

// TestClassifyJSONShapeStrictTopLevelEOF exercises classifyJSONShape
// directly (a pure function of content string), covering every top-level
// EOF shape the PB-3ah.2 review flagged decoder.More() as getting wrong: a
// stray trailing "]" or "}" immediately after an otherwise-complete
// document must be rejected as trailing content, not silently accepted as
// "no more elements" the way More()'s peek-based heuristic did.
func TestClassifyJSONShapeStrictTopLevelEOF(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    StructuredOutputInvalidReason // "" means valid (nil)
	}{
		{"valid single object", `{"a":1}`, ""},
		{"valid single array", `[1,2,3]`, ""},
		{"valid followed by trailing whitespace only", "{\"a\":1}  \n\t ", ""},
		{"malformed first json", `{"a":`, StructuredOutputInvalidJSON},
		{"trailing close brace after object", `{"a":1}}`, StructuredOutputTrailingJSON},
		{"trailing close bracket after object", `{"a":1}]`, StructuredOutputTrailingJSON},
		{"trailing close bracket after array", `[1,2,3]]`, StructuredOutputTrailingJSON},
		{"trailing prose", `{"a":1} not json`, StructuredOutputTrailingJSON},
		{"second object value", `{"a":1}{"b":2}`, StructuredOutputMultipleJSON},
		{"second array value", `{"a":1}[1,2]`, StructuredOutputMultipleJSON},
		{"second string value", `{"a":1}"trailing"`, StructuredOutputMultipleJSON},
		{"second number value", `{"a":1} 42`, StructuredOutputMultipleJSON},
		{"second boolean value", `{"a":1} true`, StructuredOutputMultipleJSON},
		{"second null value", `{"a":1} null`, StructuredOutputMultipleJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyJSONShape(test.content)
			if test.want == "" {
				if got != nil {
					t.Fatalf("classifyJSONShape(%q) = %#v, want nil (valid)", test.content, got)
				}
				return
			}
			if got == nil || got.reason != test.want {
				t.Fatalf("classifyJSONShape(%q) = %#v, want reason %q", test.content, got, test.want)
			}
		})
	}
}

// TestRunnerRoutesMaxTokensStructuredOutputToSuccessInsteadOfInvalid is a
// regression for a possible max_tokens path behind the PB-3ag incident
// (Provider Request ID req_011CegLuKMWjthaUTH2uGuSD): PB-3ag's saved
// evidence confirms the exact Failure code/category/stage this fixture
// reproduces (PROVIDER_RESPONSE_INVALID, structured_output_invalid,
// ceo_plan_runner_failed), but never recorded a stop_reason, so
// stop_reason=max_tokens here is this fixture's own plausible-but-unproven
// mechanism for reaching that same code/category/stage, not a confirmed
// reproduction of PB-3ag's actual cause. A Structured Output response cut
// off by the Provider's own output ceiling produces malformed/incomplete
// JSON by construction. Before this fix, that malformed JSON was extracted
// and classified as FailureStructuredOutputInvalid before stop_reason was
// ever consulted. After this fix, Run() must succeed with
// StopReason=StopReasonMaxTokens and a fixed, content-blind placeholder
// Content -- never the truncated fragment -- so the caller's own existing
// StopReasonMaxTokens check (service.CEOPlanService.Generate,
// service.ReviewService.Execute -- the two production Structured Output
// callers) can route it through OUTPUT_INCOMPLETE semantics instead.
// ExecutionService.Execute is not a Structured Output caller (Task
// execution's output is plain content, not JSON); it independently checks
// the same StopReasonMaxTokens on its own plain-content RunResult, unrelated
// to this placeholder.
func TestRunnerRoutesMaxTokensStructuredOutputToSuccessInsteadOfInvalid(t *testing.T) {
	fixtures := loadStructuredOutputExtractionFixtures(t)
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(fixtures["incomplete_json_max_tokens"]), "req-max-tokens"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() must succeed for a max_tokens-truncated Structured Output response, got error: %v", err)
	}
	if result.StopReason != worker.StopReasonMaxTokens {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, worker.StopReasonMaxTokens)
	}
	if result.Content != truncatedStructuredOutputContent {
		t.Fatalf("Content = %q, want the fixed safe placeholder", result.Content)
	}
	if strings.Contains(result.Content, "verdict") || strings.Contains(result.Content, "Approve") {
		t.Fatal("Content leaked a fragment of the truncated Provider response")
	}
	if result.StructuredOutputPresence != nil || result.StructuredOutputFieldShape != nil || result.StructuredOutputStepDescriptionShape != nil {
		t.Fatalf("structured output diagnostics must be nil when extraction was skipped: presence=%#v shape=%#v stepShape=%#v",
			result.StructuredOutputPresence, result.StructuredOutputFieldShape, result.StructuredOutputStepDescriptionShape)
	}
}

// TestRunnerMaxTokensStructuredOutputWithoutAnyTextBlockStillSucceeds covers
// the edge case where the Provider's output ceiling cut generation off
// before any "text" block was even started (only a "thinking" block is
// present). The safe placeholder must still be used -- Run() must not
// attempt best-effort text extraction from thinking content, and must not
// fail with FailureResponse's "empty content" check either.
func TestRunnerMaxTokensStructuredOutputWithoutAnyTextBlockStillSucceeds(t *testing.T) {
	fixtures := loadStructuredOutputExtractionFixtures(t)
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(fixtures["no_text_max_tokens"]), "req-max-tokens-no-text"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() must succeed even with zero text blocks when stop_reason=max_tokens, got: %v", err)
	}
	if result.StopReason != worker.StopReasonMaxTokens || result.Content != truncatedStructuredOutputContent {
		t.Fatalf("result = %#v", result)
	}
}

// TestRunnerMaxTokensStructuredOutputWithUnexpectedBlockStillSucceeds is
// the PB-3ah.9 regression for content-extraction-before-detection ordering:
// a response with a genuinely unexpected content block type (the same
// shape that produces StructuredOutputUnexpectedBlock outside a max_tokens
// stop) must still succeed with the fixed placeholder when
// stop_reason=max_tokens, because the max_tokens branch is selected before
// either providerResponse.markdown() or providerResponse.structuredJSON()
// -- which is what would classify this block shape -- is ever called.
func TestRunnerMaxTokensStructuredOutputWithUnexpectedBlockStillSucceeds(t *testing.T) {
	encoded, _ := json.Marshal(map[string]any{
		"model":       "claude-sonnet-5",
		"content":     []map[string]string{{"type": "tool_use", "id": "toolu_1", "name": "unexpected"}},
		"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		"stop_reason": "max_tokens",
	})
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(encoded), "req-max-tokens-unexpected-block"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() must succeed for max_tokens even with an unexpected content block, got error: %v", err)
	}
	if result.StopReason != worker.StopReasonMaxTokens || result.Content != truncatedStructuredOutputContent {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.Content, "tool_use") || strings.Contains(result.Content, "unexpected") {
		t.Fatal("Content leaked a fragment of the unexpected block")
	}
}

// TestRunnerMaxTokensStructuredOutputWithEmptyTextBlockStillSucceeds covers
// the shape distinct from "no text block at all"
// (TestRunnerMaxTokensStructuredOutputWithoutAnyTextBlockStillSucceeds
// above): a text block that is present but empty -- the same shape that
// produces StructuredOutputEmptyText outside a max_tokens stop -- must
// still succeed with the fixed placeholder when stop_reason=max_tokens,
// never reaching the empty-text classification.
func TestRunnerMaxTokensStructuredOutputWithEmptyTextBlockStillSucceeds(t *testing.T) {
	encoded, _ := json.Marshal(map[string]any{
		"model":       "claude-sonnet-5",
		"content":     []map[string]string{{"type": "text", "text": ""}},
		"usage":       map[string]int{"input_tokens": 1, "output_tokens": 1},
		"stop_reason": "max_tokens",
	})
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(encoded), "req-max-tokens-empty-text"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run() must succeed for max_tokens even with an empty text block, got error: %v", err)
	}
	if result.StopReason != worker.StopReasonMaxTokens || result.Content != truncatedStructuredOutputContent {
		t.Fatalf("result = %#v", result)
	}
}

// TestRunnerValidSingleStructuredOutputJSONBlockStillSucceeds is the
// control case: exactly one well-formed JSON text block, stop_reason
// end_turn, must succeed exactly as before this fix, unaffected by the
// reordering.
func TestRunnerValidSingleStructuredOutputJSONBlockStillSucceeds(t *testing.T) {
	fixtures := loadStructuredOutputExtractionFixtures(t)
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(fixtures["single_json_with_thinking"]), "req-valid"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != `{"verdict":"Approve","issues":[],"summary":"Approved."}` {
		t.Fatalf("Content = %q", result.Content)
	}
}

func loadStructuredOutputExtractionFixtures(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "provider", "claude_structured_output_extraction_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]json.RawMessage{}
	if err := json.Unmarshal(content, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
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
	fixtures := loadStructuredOutputExtractionFixtures(t)
	runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, string(fixtures["refusal"]), "req-refusal"), nil
	}))
	request := validRunRequest()
	request.StructuredOutput = &worker.StructuredOutputContract{Schema: review.TypedDecisionJSONSchema()}
	_, err := runner.Run(context.Background(), request)
	var failure *Error
	if !errors.As(err, &failure) || failure.Category != FailureRefusal || failure.ProviderType != "cyber" {
		t.Fatalf("err = %v, failure = %#v", err, failure)
	}
}

// TestRunnerNormalizesStopReasonToProviderNeutralValue proves this Adapter
// maps Anthropic's own raw stop_reason string onto the Provider-neutral
// worker.StopReason enum -- Core must never see "end_turn"/"max_tokens"/
// "stop_sequence" directly. "unknown" covers both an absent stop_reason and
// any value this Adapter does not yet classify (e.g. "tool_use").
func TestRunnerNormalizesStopReasonToProviderNeutralValue(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want worker.StopReason
	}{
		{"completed", "end_turn", worker.StopReasonCompleted},
		{"max tokens", "max_tokens", worker.StopReasonMaxTokens},
		{"stop sequence", "stop_sequence", worker.StopReasonStopSequence},
		{"absent", "", worker.StopReasonUnknown},
		{"unrecognized", "tool_use", worker.StopReasonUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stopReasonField := ""
			if test.raw != "" {
				stopReasonField = `,"stop_reason":"` + test.raw + `"`
			}
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				body := `{"model":"claude-sonnet-5","content":[{"type":"text","text":"ok"}],"usage":{}` + stopReasonField + `}`
				return jsonResponse(http.StatusOK, body, "req-stop-reason"), nil
			}))
			result, err := runner.Run(context.Background(), validRunRequest())
			if err != nil {
				t.Fatal(err)
			}
			if result.StopReason != test.want {
				t.Fatalf("StopReason = %q, want %q", result.StopReason, test.want)
			}
			// A non-Structured-Output request (validRunRequest() sets no
			// StructuredOutput contract) always takes the plain markdown()
			// content-extraction path -- even for max_tokens -- never the
			// Structured Output placeholder, which only exists for a
			// Structured Output request cut off mid-generation.
			if result.Content != "ok" {
				t.Fatalf("non-Structured-Output Content = %q, want the plain extracted text %q", result.Content, "ok")
			}
		})
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

func TestRunnerClassifiesTypedTransportFailuresWithoutRawErrorText(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want TransportFailureCategory
	}{
		{"DNS", &url.Error{Op: "Post", URL: "https://provider.invalid", Err: &net.DNSError{Err: "no such host", Name: "provider.invalid"}}, TransportDNSFailed},
		{"connect", &url.Error{Op: "Post", URL: "https://provider.invalid", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}}, TransportConnectFailed},
		{"TLS", &url.Error{Op: "Post", URL: "https://provider.invalid", Err: x509.UnknownAuthorityError{}}, TransportTLSFailed},
		{"timeout", &url.Error{Op: "Post", URL: "https://provider.invalid", Err: context.DeadlineExceeded}, TransportTimeout},
		{"reset", &url.Error{Op: "Post", URL: "https://provider.invalid", Err: syscall.ECONNRESET}, TransportConnectionReset},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := configuredRunner(t, doerFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.err
			}))
			_, err := runner.Run(context.Background(), validRunRequest())
			var providerError *Error
			if !errors.As(err, &providerError) || providerError.Category != FailureTransport || providerError.Transport != test.want ||
				providerError.StatusCode != 0 || providerError.RequestID != "" {
				t.Fatalf("transport failure = %#v, %v", providerError, err)
			}
			if strings.Contains(providerError.Error(), "provider.invalid") || strings.Contains(providerError.Error(), "no such host") {
				t.Fatalf("transport Error() exposed raw diagnostics: %q", providerError.Error())
			}
		})
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
