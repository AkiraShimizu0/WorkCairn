// Package action defines provider- and storage-neutral external publication
// contracts. It does not know Vault, HTTP, credentials, Tasks, or Audit.
package action

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	SchemaVersion             = "workspace-action.v1"
	KindWordPressPublish Kind = "wordpress.post.publish"
)

var (
	ErrInvalidAction = errors.New("invalid external Action")
	ErrAlreadyExists = errors.New("external Action evidence already exists")
	ErrNotFound      = errors.New("external Action evidence not found")
	ErrPublishFailed = errors.New("external Action publish failed")
	ErrSaveFailed    = errors.New("external Action evidence save failed")
)

type Kind string

func (kind Kind) Valid() bool { return kind == KindWordPressPublish }

type Source struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	TaskID      string `json:"task_id"`
	Reference   string `json:"reference"`
	SHA256      string `json:"sha256"`
	Title       string `json:"-"`
	Content     string `json:"-"`
}

type Intent struct {
	SchemaVersion string    `json:"schema_version"`
	ActionID      string    `json:"action_id"`
	Kind          Kind      `json:"kind"`
	TargetID      string    `json:"target_id"`
	RequestedAt   time.Time `json:"requested_at"`
	Source        Source    `json:"source"`
}

func (intent Intent) Validate() error {
	if intent.SchemaVersion != SchemaVersion || invalidID(intent.ActionID) || !intent.Kind.Valid() ||
		invalidID(intent.TargetID) || intent.RequestedAt.IsZero() || invalidID(intent.Source.ProjectID) ||
		invalidID(intent.Source.ProjectName) || invalidID(intent.Source.TaskID) || invalidReference(intent.Source.Reference) ||
		!validSHA256(intent.Source.SHA256) || strings.TrimSpace(intent.Source.Title) == "" || strings.ContainsAny(intent.Source.Title, "\r\n") ||
		strings.TrimSpace(intent.Source.Content) == "" {
		return ErrInvalidAction
	}
	return nil
}

type Publication struct {
	Provider   string `json:"provider"`
	ExternalID string `json:"external_id"`
	URL        string `json:"url"`
	Status     string `json:"status"`
}

func (publication Publication) Validate() error {
	if invalidID(publication.Provider) || invalidID(publication.ExternalID) || strings.TrimSpace(publication.URL) == "" ||
		strings.ContainsAny(publication.URL, "\r\n") || publication.Status != "published" {
		return ErrInvalidAction
	}
	return nil
}

type Outcome struct {
	SchemaVersion string      `json:"schema_version"`
	ActionID      string      `json:"action_id"`
	CompletedAt   time.Time   `json:"completed_at"`
	SourceSHA256  string      `json:"source_sha256"`
	Publication   Publication `json:"publication"`
}

func (outcome Outcome) Validate() error {
	if outcome.SchemaVersion != SchemaVersion || invalidID(outcome.ActionID) || outcome.CompletedAt.IsZero() ||
		!validSHA256(outcome.SourceSHA256) || outcome.Publication.Validate() != nil {
		return ErrInvalidAction
	}
	return nil
}

type Evidence struct {
	RelativePath string `json:"relative_path"`
	Committed    bool   `json:"committed"`
}

type Result struct {
	Status         string       `json:"status"`
	Intent         *Evidence    `json:"intent,omitempty"`
	Publication    *Publication `json:"publication,omitempty"`
	Outcome        *Evidence    `json:"outcome,omitempty"`
	EventID        string       `json:"event_id,omitempty"`
	EventPublished bool         `json:"event_published"`
}

type SaveError struct {
	Evidence Evidence
	Err      error
}

func (*SaveError) Error() string        { return ErrSaveFailed.Error() }
func (save *SaveError) Unwrap() error   { return save.Err }
func (*SaveError) Is(target error) bool { return target == ErrSaveFailed }

type PublishError struct {
	Code string
	Err  error
}

func (*PublishError) Error() string         { return ErrPublishFailed.Error() }
func (publish *PublishError) Unwrap() error { return publish.Err }
func (*PublishError) Is(target error) bool  { return target == ErrPublishFailed }

type Store interface {
	SaveIntent(context.Context, Intent) (Evidence, error)
	SaveOutcome(context.Context, Outcome) (Evidence, error)
	Exists(context.Context, string) (intentExists, outcomeExists bool, err error)
}

type Publisher interface {
	Publish(context.Context, Intent) (Publication, error)
}

func SourceDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func ValidateSourceDigest(value string) error {
	if !validSHA256(value) {
		return ErrInvalidAction
	}
	return nil
}

func invalidID(value string) bool {
	return value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.ContainsAny(value, "\r\n")
}

func invalidReference(value string) bool {
	if invalidID(value) || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return true
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func NewIntent(actionID, targetID string, requestedAt time.Time, source Source) (Intent, error) {
	intent := Intent{SchemaVersion: SchemaVersion, ActionID: strings.TrimSpace(actionID), Kind: KindWordPressPublish, TargetID: strings.TrimSpace(targetID), RequestedAt: requestedAt, Source: source}
	if err := intent.Validate(); err != nil {
		return Intent{}, fmt.Errorf("%w: intent", err)
	}
	return intent, nil
}
