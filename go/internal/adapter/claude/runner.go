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
)

// HTTPDoer is the transport seam used by Runtime composition and contract
// tests. A configured *http.Client satisfies it.
type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

// Config contains Provider-specific settings supplied by a future Runtime.
// It never reads environment variables or secret stores itself.
type Config struct {
	APIKey        string
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
	config.ProviderModel = strings.TrimSpace(config.ProviderModel)
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.APIVersion = strings.TrimSpace(config.APIVersion)

	if config.APIKey == "" {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidConfig)
	}
	if config.ProviderModel == "" {
		return nil, fmt.Errorf("%w: Provider model is required", ErrInvalidConfig)
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

	payload, err := json.Marshal(messageRequest{
		Model:     claude.providerModel,
		MaxTokens: claude.maxTokens,
		System:    request.SystemPrompt,
		Messages:  []message{{Role: "user", Content: request.UserPrompt}},
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
		return worker.RunResult{}, &Error{Kind: ErrTransport, Err: err}
	}
	if response == nil || response.Body == nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse}
	}
	defer response.Body.Close()

	requestID := strings.TrimSpace(response.Header.Get("request-id"))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, response.Body)
		return worker.RunResult{}, &Error{
			Kind:       ErrProvider,
			StatusCode: response.StatusCode,
			RequestID:  requestID,
		}
	}

	var providerResponse messageResponse
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&providerResponse); err != nil {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID, Err: err}
	}
	content := providerResponse.markdown()
	if content == "" || strings.TrimSpace(providerResponse.Model) == "" {
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID}
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
		return worker.RunResult{}, &Error{Kind: ErrInvalidResponse, RequestID: requestID, Err: err}
	}
	return result, nil
}

type messageRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messageResponse struct {
	Model   string         `json:"model"`
	Content []contentBlock `json:"content"`
	Usage   usage          `json:"usage"`
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
