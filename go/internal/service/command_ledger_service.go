package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
)

type CommandLedgerService struct{ store commandledger.Store }

func NewCommandLedgerService(store commandledger.Store) (*CommandLedgerService, error) {
	if serviceDependencyIsNil(store) {
		return nil, fmt.Errorf("Command Ledger Store is required")
	}
	return &CommandLedgerService{store: store}, nil
}

type CommandBeginResult struct {
	Record  commandledger.Record
	Created bool
}

// Begin claims one Command ID before business effects. Existing terminal
// records are returned for deterministic replay; running records are never
// automatically resumed.
func (service *CommandLedgerService) Begin(ctx context.Context, candidate commandledger.Record) (CommandBeginResult, error) {
	if ctx == nil {
		return CommandBeginResult{}, commandledger.ErrInvalidRecord
	}
	if err := candidate.Validate(); err != nil || candidate.State != commandledger.StateRunning || candidate.Version != 1 {
		return CommandBeginResult{}, commandledger.ErrInvalidRecord
	}
	if err := service.store.Create(ctx, candidate); err == nil {
		return CommandBeginResult{Record: candidate.Clone(), Created: true}, nil
	} else if !errors.Is(err, commandledger.ErrAlreadyExists) {
		return CommandBeginResult{}, err
	}
	existing, err := service.store.Get(ctx, candidate.CommandID)
	if err != nil {
		return CommandBeginResult{}, err
	}
	if !existing.SameRequest(candidate) {
		return CommandBeginResult{Record: existing}, commandledger.ErrRequestConflict
	}
	if existing.State == commandledger.StateRunning {
		return CommandBeginResult{Record: existing}, commandledger.ErrInProgress
	}
	return CommandBeginResult{Record: existing}, nil
}

func (service *CommandLedgerService) Finish(
	ctx context.Context,
	running commandledger.Record,
	state commandledger.State,
	result json.RawMessage,
	failure *commandledger.Failure,
) (commandledger.Record, error) {
	if ctx == nil {
		return commandledger.Record{}, commandledger.ErrInvalidRecord
	}
	finished, err := running.Finish(state, result, failure)
	if err != nil {
		return commandledger.Record{}, err
	}
	if err := service.store.Update(ctx, finished, running.Version); err != nil {
		return commandledger.Record{}, err
	}
	return finished, nil
}
