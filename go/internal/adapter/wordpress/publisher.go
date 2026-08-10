// Package wordpress implements the external Action Publisher port using the
// WordPress REST API. Credentials remain injected configuration at the edge.
package wordpress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/AkiraShimizu0/workcairn/go/internal/action"
)

const Name = "wordpress"
const maxResponseBytes = 1 << 20

var ErrInvalidConfig = errors.New("invalid WordPress configuration")

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Config struct {
	TargetID            string
	BaseURL             string
	Username            string
	ApplicationPassword string
}

type Publisher struct {
	config Config
	client HTTPDoer
	url    string
}

func New(config Config, client HTTPDoer) (*Publisher, error) {
	config.TargetID = strings.TrimSpace(config.TargetID)
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.Username = strings.TrimSpace(config.Username)
	if config.TargetID == "" || config.BaseURL == "" || config.Username == "" || config.ApplicationPassword == "" || client == nil {
		return nil, ErrInvalidConfig
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname()))) {
		return nil, ErrInvalidConfig
	}
	return &Publisher{config: config, client: client, url: config.BaseURL + "/wp-json/wp/v2/posts"}, nil
}

func (publisher *Publisher) Publish(ctx context.Context, intent action.Intent) (action.Publication, error) {
	if ctx == nil || intent.Validate() != nil || intent.Kind != action.KindWordPressPublish || intent.TargetID != publisher.config.TargetID {
		return action.Publication{}, action.ErrInvalidAction
	}
	payload, err := json.Marshal(struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Status  string `json:"status"`
	}{Title: intent.Source.Title, Content: intent.Source.Content, Status: "publish"})
	if err != nil {
		return action.Publication{}, &action.PublishError{Code: "REQUEST_ENCODING_FAILED", Err: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, publisher.url, bytes.NewReader(payload))
	if err != nil {
		return action.Publication{}, &action.PublishError{Code: "REQUEST_BUILD_FAILED", Err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth(publisher.config.Username, publisher.config.ApplicationPassword)
	response, err := publisher.client.Do(request)
	if err != nil {
		return action.Publication{}, &action.PublishError{Code: "TRANSPORT_FAILED", Err: err}
	}
	defer response.Body.Close()
	content, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if readErr != nil || len(content) > maxResponseBytes {
		return action.Publication{}, &action.PublishError{Code: "RESPONSE_READ_FAILED", Err: readErr}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return action.Publication{}, &action.PublishError{Code: "PROVIDER_REJECTED", Err: fmt.Errorf("WordPress status %d", response.StatusCode)}
	}
	var decoded struct {
		ID     json.Number `json:"id"`
		Link   string      `json:"link"`
		Status string      `json:"status"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return action.Publication{}, &action.PublishError{Code: "INVALID_RESPONSE", Err: err}
	}
	externalID := decoded.ID.String()
	if _, err := strconv.ParseUint(externalID, 10, 64); err != nil || strings.TrimSpace(decoded.Link) == "" || decoded.Status != "publish" {
		return action.Publication{}, &action.PublishError{Code: "INVALID_RESPONSE", Err: action.ErrInvalidAction}
	}
	result := action.Publication{Provider: Name, ExternalID: externalID, URL: decoded.Link, Status: "published"}
	if result.Validate() != nil {
		return action.Publication{}, &action.PublishError{Code: "INVALID_RESPONSE", Err: action.ErrInvalidAction}
	}
	return result, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	return net.ParseIP(host).IsLoopback()
}

var _ action.Publisher = (*Publisher)(nil)
