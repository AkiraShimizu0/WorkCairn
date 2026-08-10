package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/interaction"
)

type InteractionCommitResult struct {
	Record    interaction.Record `json:"session"`
	Committed bool               `json:"committed"`
}

type InteractionService struct{ store interaction.Store }

func NewInteractionService(store interaction.Store) (*InteractionService, error) {
	if serviceDependencyIsNil(store) {
		return nil, fmt.Errorf("Interaction Store is required")
	}
	return &InteractionService{store: store}, nil
}

func (service *InteractionService) Create(ctx context.Context, candidate interaction.Record) (InteractionCommitResult, error) {
	if ctx == nil || candidate.Validate() != nil || candidate.Version != 1 {
		return InteractionCommitResult{}, interaction.ErrInvalidSession
	}
	err := service.store.Create(ctx, candidate)
	if err == nil {
		return InteractionCommitResult{Record: candidate.Clone(), Committed: true}, nil
	}
	return service.confirmCommit(ctx, candidate, err)
}

func (service *InteractionService) Update(ctx context.Context, next interaction.Record, expectedVersion uint64) (InteractionCommitResult, error) {
	if ctx == nil || next.Validate() != nil || expectedVersion == 0 {
		return InteractionCommitResult{}, interaction.ErrInvalidSession
	}
	err := service.store.Update(ctx, next, expectedVersion)
	if err == nil {
		return InteractionCommitResult{Record: next.Clone(), Committed: true}, nil
	}
	return service.confirmCommit(ctx, next, err)
}

func (service *InteractionService) Get(ctx context.Context, sessionID string) (interaction.Record, error) {
	if ctx == nil {
		return interaction.Record{}, interaction.ErrInvalidSession
	}
	return service.store.Get(ctx, sessionID)
}

func (service *InteractionService) List(ctx context.Context) ([]interaction.Record, error) {
	if ctx == nil {
		return nil, interaction.ErrInvalidSession
	}
	return service.store.List(ctx)
}

func (service *InteractionService) confirmCommit(ctx context.Context, candidate interaction.Record, commitErr error) (InteractionCommitResult, error) {
	confirmContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	stored, readErr := service.store.Get(confirmContext, candidate.SessionID)
	if readErr == nil && sameInteractionRecord(stored, candidate) {
		return InteractionCommitResult{Record: stored, Committed: true}, commitErr
	}
	return InteractionCommitResult{Record: candidate.Clone(), Committed: false}, commitErr
}

func sameInteractionRecord(left, right interaction.Record) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
