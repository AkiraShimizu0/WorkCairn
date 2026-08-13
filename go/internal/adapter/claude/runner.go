// Package claude implements the Anthropic Claude Provider Adapter.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/runner"
	"github.com/AkiraShimizu0/workcairn/go/internal/worker"
)

const (
	Name              = "ClaudeRunner"
	defaultBaseURL    = "https://api.anthropic.com"
	defaultAPIVersion = "2023-06-01"
	defaultMaxTokens  = 3000
	maxErrorBodyBytes = 64 << 10
)

// HTTPDoer is the transport seam used by Runtime composition and contract
// tests. A configured *http.Client satisfies it.
type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Config contains Provider-specific settings supplied by a future Runtime.
// It never reads environment variables or secret stores itself.
type Config struct {
	APIKey string
	// ProviderModel is an optional explicit Adapter override. Product Runtime
	// leaves it empty so ResolveModel applies the Automatic supported policy.
	ProviderModel string
	MaxTokens     int
	BaseURL       string
	APIVersion    string
}

// Runner translates the provider-neutral Worker request to Anthropic's
// Messages API and translates its response back to the Worker contract.
type Runner struct {
	apiKey        string
	providerModel string
	maxTokens     int
	endpoint      string
	apiVersion    string
	client        HTTPDoer
}

var _ runner.Runner = (*Runner)(nil)

func New(config Config, client HTTPDoer) (*Runner, error) {
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.APIVersion = strings.TrimSpace(config.APIVersion)
	resolution, err := ResolveModel(config.ProviderModel)
	if err != nil {
		return nil, err
	}
	config.ProviderModel = resolution.ProviderModel

	if config.APIKey == "" {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidConfig)
	}
	if isNilHTTPDoer(client) {
		return nil, fmt.Errorf("%w: HTTP client is required", ErrInvalidConfig)
	}
	if config.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: max tokens must be positive", ErrInvalidConfig)
	}
	if config.MaxTokens == 0 {
		config.MaxTokens = defaultMaxTokens
	}
	if config.BaseURL == "" {
		config.BaseURL = defaultBaseURL
	}
	parsedBaseURL, err := url.Parse(config.BaseURL)
	if err != nil || (parsedBaseURL.Scheme != "http" && parsedBaseURL.Scheme != "https") || parsedBaseURL.Host == "" {
		return nil, fmt.Errorf("%w: valid HTTP(S) base URL is required", ErrInvalidConfig)
	}
	if parsedBaseURL.User != nil || parsedBaseURL.RawQuery != "" || parsedBaseURL.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must not contain credentials, query, or fragment", ErrInvalidConfig)
	}
	if config.APIVersion == "" {
		config.APIVersion = defaultAPIVersion
	}

	return &Runner{
		apiKey:        config.APIKey,
		providerModel: config.ProviderModel,
		maxTokens:     config.MaxTokens,
		endpoint:      strings.TrimRight(parsedBaseURL.String(), "/") + "/v1/messages",
		apiVersion:    config.APIVersion,
		client:        client,
	}, nil
}

func (*Runner) Name() string { return Name }

func (claude *Runner) Run(ctx context.Context, request worker.RunRequest) (worker.RunResult, error) {
	if ctx == nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidRequest}
	}
	if err := ctx.Err(); err != nil {
		return worker.RunResult{}, err
	}
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.SystemPrompt) == "" || strings.TrimSpace(request.UserPrompt) == "" {
		return worker.RunResult{}, &Error{Kind: ErrInvalidRequest}
	}

	var structuredOutput *outputConfig
	if request.StructuredOutput != nil {
		if len(request.StructuredOutput.Schema) == 0 {
			return worker.RunResult{}, &Error{Kind: ErrInvalidRequest}
		}
		structuredOutput = &outputConfig{Format: jsonOutputFormat{
			Type:   "json_schema",
			Schema: request.StructuredOutput.Schema,
		}}
	}

	payload, err := json.Marshal(messageRequest{
		Model:        claude.providerModel,
		MaxTokens:    claude.maxTokens,
		System:       request.SystemPrompt,
		Messages:     []message{{Role: "user", Content: request.UserPrompt}},
		OutputConfig: structuredOutput,
	})
	if err != nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidRequest, Err: err}
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, claude.endpoint, bytes.NewReader(payload))
	if err != nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidRequest, Err: err}
	}
	httpRequest.Header.Set("content-type", "application/json")
	httpRequest.Header.Set("x-api-key", claude.apiKey)
	httpRequest.Header.Set("anthropic-version", claude.apiVersion)

	startedAt := time.Now()
	response, err := claude.client.Do(httpRequest)
	duration := time.Since(startedAt)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return worker.RunResult{}, contextError
		}
		return worker.RunResult{}, &Error{Kind: ErrTransport, Category: FailureTransport, Err: err}
	}
	if response == nil || response.Body == nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, Category: FailureResponse}
	}
	defer response.Body.Close()

	requestID := strings.TrimSpace(response.Header.Get("request-id"))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		providerType, bodyRequestID := readProviderFailure(response.Body)
		if requestID == "" {
			requestID = bodyRequestID
		}
		return worker.RunResult{}, &Error{
			Kind:         ErrProvider,
			StatusCode:   response.StatusCode,
			RequestID:    requestID,
			ProviderType: providerType,
			Category:     classifyProviderFailure(response.StatusCode, providerType),
		}
	}

	var providerResponse messageResponse
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&providerResponse); err != nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID, Category: FailureResponse, Err: err}
	}
	if providerResponse.StopReason == "refusal" {
		return worker.RunResult{}, &Error{
			Kind: ErrInvalidResponse, RequestID: requestID, Category: FailureRefusal,
			ProviderType: safeDiagnosticToken(providerResponse.StopDetails.Category, 64),
		}
	}
	content := providerResponse.markdown()
	if content == "" || strings.TrimSpace(providerResponse.Model) == "" {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID, Category: FailureResponse}
	}
	if request.StructuredOutput != nil && request.StructuredOutput.ContentField != "" {
		unwrapped, err := unwrapStructuredOutputField(content, request.StructuredOutput.ContentField)
		if err != nil {
			return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID, Category: FailureStructuredOutputInvalid, Err: err}
		}
		content = unwrapped
	}

	result := worker.RunResult{
		Content: content,
		Runner:  Name,
		Model:   request.Model,
		Usage: worker.TokenUsage{
			InputTokens:  providerResponse.Usage.InputTokens,
			OutputTokens: providerResponse.Usage.OutputTokens,
		},
		Duration: duration,
		Metadata: cloneMetadata(request.Metadata),
	}
	if err := result.Validate(); err != nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID, Category: FailureResponse, Err: err}
	}
	return result, nil
}

// unwrapStructuredOutputField decodes a Structured Output response envelope
// (guaranteed by the Provider to be a JSON object matching the schema this
// Adapter requested) and returns the single named string field's value. It
// rejects any shape other than exactly the one declared field, so a
// Provider response that violates the contract it agreed to is never
// treated as success.
func unwrapStructuredOutputField(content, field string) (string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return "", fmt.Errorf("structured output envelope is not a JSON object: %w", err)
	}
	if len(envelope) != 1 {
		return "", fmt.Errorf("structured output envelope has %d fields, want exactly 1", len(envelope))
	}
	raw, exists := envelope[field]
	if !exists {
		return "", fmt.Errorf("structured output envelope is missing field %q", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("structured output field %q is not a non-empty string", field)
	}
	return value, nil
}

func readProviderFailure(body io.Reader) (string, string) {
	limited := io.LimitReader(body, maxErrorBodyBytes)
	var envelope struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if json.NewDecoder(limited).Decode(&envelope) != nil {
		return "", ""
	}
	return safeDiagnosticToken(envelope.Error.Type, 64), safeDiagnosticToken(envelope.RequestID, 128)
}

func safeDiagnosticToken(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit {
		return ""
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return ""
		}
	}
	return value
}

func classifyProviderFailure(status int, providerType string) FailureCategory {
	switch providerType {
	case "authentication_error":
		return FailureAuthentication
	case "billing_error":
		return FailureBilling
	case "permission_error":
		return FailurePermission
	case "invalid_request_error", "not_found_error", "request_too_large":
		return FailureInvalidRequest
	case "rate_limit_error":
		return FailureRateLimited
	case "api_error", "timeout_error", "overloaded_error":
		return FailureUnavailable
	}
	switch status {
	case http.StatusUnauthorized:
		return FailureAuthentication
	case http.StatusPaymentRequired:
		return FailureBilling
	case http.StatusForbidden:
		return FailurePermission
	case http.StatusBadRequest, http.StatusNotFound, http.StatusRequestEntityTooLarge:
		return FailureInvalidRequest
	case http.StatusTooManyRequests:
		return FailureRateLimited
	}
	if status >= http.StatusInternalServerError {
		return FailureUnavailable
	}
	return FailureUnknown
}

type messageRequest struct {
	Model        string        `json:"model"`
	MaxTokens    int           `json:"max_tokens"`
	System       string        `json:"system"`
	Messages     []message     `json:"messages"`
	OutputConfig *outputConfig `json:"output_config,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// outputConfig and jsonOutputFormat are the Anthropic-specific wire shapes
// for Structured Outputs (output_config.format, type "json_schema"). They
// are private to this Adapter; the Provider-neutral Runner input only
// carries a JSON Schema (worker.StructuredOutputContract).
type outputConfig struct {
	Format jsonOutputFormat `json:"format"`
}

type jsonOutputFormat struct {
	Type   string         `json:"type"`
	Schema map[string]any `json:"schema"`
}

type messageResponse struct {
	Model       string         `json:"model"`
	Content     []contentBlock `json:"content"`
	Usage       usage          `json:"usage"`
	StopReason  string         `json:"stop_reason"`
	StopDetails refusalDetails `json:"stop_details"`
}

type refusalDetails struct {
	Category string `json:"category"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
}

func (response messageResponse) markdown() string {
	texts := make([]string, 0, len(response.Content))
	for _, block := range response.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(texts, "\n")
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func isNilHTTPDoer(client HTTPDoer) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
