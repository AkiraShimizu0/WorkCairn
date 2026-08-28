// Package deliverablestore provides storage Adapters for Deliverable records.
package deliverablestore

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/deliverable"
)

// InMemory is a race-safe immutable Deliverable Store for tests and
// non-persistent Kernel compositions.
type InMemory struct {
	mu        sync.RWMutex
	documents map[string]deliverable.Document
}

func NewInMemory() *InMemory {
	return &InMemory{documents: make(map[string]deliverable.Document)}
}

func (store *InMemory) Save(ctx context.Context, document deliverable.Document) (deliverable.Record, error) {
	if ctx == nil {
		return deliverable.Record{}, fmt.Errorf("%w: context is required", deliverable.ErrInvalidDocument)
	}
	if err := ctx.Err(); err != nil {
		return deliverable.Record{}, err
	}
	if err := document.Validate(); err != nil {
		return deliverable.Record{}, err
	}
	key := document.ProjectID + "/" + document.Execution.TaskID
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.documents[key]; exists {
		return deliverable.Record{}, deliverable.ErrAlreadyExists
	}
	store.documents[key] = cloneDocument(document)
	return deliverable.Record{
		TaskID:       document.Execution.TaskID,
		RelativePath: filepath.ToSlash(filepath.Join("Deliverables", document.Execution.TaskID+".md")),
	}, nil
}

func (store *InMemory) Get(projectID, taskID string) (deliverable.Document, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	document, exists := store.documents[projectID+"/"+taskID]
	return cloneDocument(document), exists
}

func cloneDocument(document deliverable.Document) deliverable.Document {
	cloned := document
	if document.Execution.Metadata != nil {
		cloned.Execution.Metadata = make(map[string]string, len(document.Execution.Metadata))
		for key, value := range document.Execution.Metadata {
			cloned.Execution.Metadata[key] = value
		}
	}
	if document.Execution.Usage.InputTokens != nil {
		value := *document.Execution.Usage.InputTokens
		cloned.Execution.Usage.InputTokens = &value
	}
	if document.Execution.Usage.OutputTokens != nil {
		value := *document.Execution.Usage.OutputTokens
		cloned.Execution.Usage.OutputTokens = &value
	}
	return cloned
}

var _ deliverable.Store = (*InMemory)(nil)
