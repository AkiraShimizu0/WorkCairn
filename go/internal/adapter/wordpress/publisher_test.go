package wordpress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/action"
)

func TestPublisherMapsTypedActionToWordPressWithoutLeakingCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "fake-user" || password != "fake-password" || request.URL.Path != "/wp-json/wp/v2/posts" {
			t.Error("unexpected WordPress request")
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["title"] != "公開タイトル" || payload["content"] != "本文\n# 見出し" || payload["status"] != "publish" {
			t.Errorf("payload = %#v", payload)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":123,"link":"https://example.test/post/123","status":"publish"}`))
	}))
	defer server.Close()
	publisher, err := New(Config{TargetID: "site-main", BaseURL: server.URL, Username: "fake-user", ApplicationPassword: "fake-password"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := publisher.Publish(context.Background(), testIntent())
	if err != nil || result.ExternalID != "123" || result.Status != "published" {
		t.Fatalf("Publish() = %#v, %v", result, err)
	}
}

func TestPublisherRejectsUnsafeConfigTargetMismatchAndProviderError(t *testing.T) {
	if _, err := New(Config{TargetID: "x", BaseURL: "http://example.com", Username: "u", ApplicationPassword: "p"}, http.DefaultClient); err != ErrInvalidConfig {
		t.Fatalf("unsafe HTTP config error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	publisher, err := New(Config{TargetID: "site-other", BaseURL: server.URL, Username: "u", ApplicationPassword: "p"}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Publish(context.Background(), testIntent()); err != action.ErrInvalidAction {
		t.Fatalf("target mismatch error = %v", err)
	}
	publisher, _ = New(Config{TargetID: "site-main", BaseURL: server.URL, Username: "u", ApplicationPassword: "p"}, server.Client())
	_, err = publisher.Publish(context.Background(), testIntent())
	var publishErr *action.PublishError
	if err == nil || !strings.Contains(err.Error(), action.ErrPublishFailed.Error()) || !asPublishError(err, &publishErr) || publishErr.Code != "PROVIDER_REJECTED" {
		t.Fatalf("provider error = %#v", err)
	}
}

func asPublishError(err error, target **action.PublishError) bool {
	for err != nil {
		if typed, ok := err.(*action.PublishError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func testIntent() action.Intent {
	return action.Intent{
		SchemaVersion: action.SchemaVersion, ActionID: "CMD-ACTION-001", Kind: action.KindWordPressPublish,
		TargetID: "site-main", RequestedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
		Source: action.Source{ProjectID: "PROJECT-001", ProjectName: "P", TaskID: "TASK-001", Reference: "Deliverables/TASK-001.md", SHA256: strings.Repeat("a", 64), Title: "公開タイトル", Content: "本文\n# 見出し"},
	}
}
