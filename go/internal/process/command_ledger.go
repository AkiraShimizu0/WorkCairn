package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/commandledger"
	"github.com/AkiraShimizu0/workcairn/go/internal/failure"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
)

const workspaceCommandScope = "workspace"

type durableCommandClaim struct {
	ledger  *service.CommandLedgerService
	running commandledger.Record
	replay  *commandledger.Record
}

func claimProjectCommand(
	ctx context.Context,
	root, projectName, commandID, operation, aggregateID string,
	request any,
) (durableCommandClaim, error) {
	if strings.TrimSpace(commandID) == "" {
		return durableCommandClaim{}, nil
	}
	store, err := vault.NewCommandLedgerStore(root, projectName)
	if err != nil {
		return durableCommandClaim{}, err
	}
	return claimDurableCommand(ctx, store, commandID, operation, projectName, aggregateID, request)
}

func claimWorkspaceCommand(
	ctx context.Context,
	root, commandID, operation, aggregateID string,
	request any,
) (durableCommandClaim, error) {
	if strings.TrimSpace(commandID) == "" {
		return durableCommandClaim{}, nil
	}
	store, err := vault.NewWorkspaceCommandLedgerStore(root)
	if err != nil {
		return durableCommandClaim{}, err
	}
	return claimDurableCommand(ctx, store, commandID, operation, workspaceCommandScope, aggregateID, request)
}

func claimDurableCommand(
	ctx context.Context,
	store commandledger.Store,
	commandID, operation, scope, aggregateID string,
	request any,
) (durableCommandClaim, error) {
	digest, err := commandledger.RequestDigest(request)
	if err != nil {
		return durableCommandClaim{}, err
	}
	running, err := commandledger.NewRunning(commandID, operation, strings.TrimSpace(scope), strings.TrimSpace(aggregateID), digest)
	if err != nil {
		return durableCommandClaim{}, err
	}
	ledger, err := service.NewCommandLedgerService(store)
	if err != nil {
		return durableCommandClaim{}, err
	}
	begin, err := ledger.Begin(ctx, running)
	if err != nil {
		return durableCommandClaim{}, err
	}
	if begin.Created {
		return durableCommandClaim{ledger: ledger, running: begin.Record}, nil
	}
	replayed := begin.Record.Clone()
	return durableCommandClaim{replay: &replayed}, nil
}

func replayDurableCommand[T any](claim durableCommandClaim) (T, bool, error) {
	var result T
	if claim.replay == nil {
		return result, false, nil
	}
	if err := json.Unmarshal(claim.replay.Result, &result); err != nil {
		return result, true, commandledger.ErrInvalidRecord
	}
	if claim.replay.State == commandledger.StateSucceeded {
		return result, true, nil
	}
	if claim.replay.Failure == nil {
		return result, true, commandledger.ErrInvalidRecord
	}
	return result, true, &RecordedCommandError{
		Code: claim.replay.Failure.Code, Stage: claim.replay.Failure.Stage,
		Partial:  claim.replay.State == commandledger.StatePartialFailure,
		Envelope: claim.replay.Failure.Details,
	}
}

// finishDurableCommand is the pre-Envelope entry point, kept unchanged for
// every Command not yet migrated to failure.Envelope propagation. Its
// behavior and signature are exactly what they were before that migration
// started.
func finishDurableCommand(
	ctx context.Context,
	claim durableCommandClaim,
	result any,
	commandErr error,
	failureCode, failureStage string,
	partial bool,
) error {
	var ledgerFailure *commandledger.Failure
	if commandErr != nil {
		ledgerFailure = &commandledger.Failure{Code: failureCode, Stage: failureStage}
	}
	return finishDurableCommandRecord(ctx, claim, result, commandErr, ledgerFailure, partial)
}

// finishDurableCommandWithEnvelope is the Envelope-carrying sibling used by
// Commands migrated to failure.Envelope propagation. The caller passes the
// already-classified Envelope computed once at its own boundary (or
// forwarded verbatim from a child) — this function never reclassifies it.
func finishDurableCommandWithEnvelope(
	ctx context.Context,
	claim durableCommandClaim,
	result any,
	commandErr error,
	envelope *failure.Envelope,
	partial bool,
) error {
	var ledgerFailure *commandledger.Failure
	if envelope != nil {
		ledgerFailure = &commandledger.Failure{Code: envelope.Code, Stage: envelope.Stage, Details: envelope}
	}
	return finishDurableCommandRecord(ctx, claim, result, commandErr, ledgerFailure, partial)
}

func finishDurableCommandRecord(
	ctx context.Context,
	claim durableCommandClaim,
	result any,
	commandErr error,
	ledgerFailure *commandledger.Failure,
	partial bool,
) error {
	if claim.ledger == nil {
		return commandErr
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return errors.Join(commandErr, &CommandLedgerCommitError{Err: err})
	}
	state := commandledger.StateSucceeded
	if commandErr != nil {
		state = commandledger.StateFailed
		if partial {
			state = commandledger.StatePartialFailure
		}
	}
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := claim.ledger.Finish(finishContext, claim.running, state, encoded, ledgerFailure); err != nil {
		return errors.Join(commandErr, &CommandLedgerCommitError{Err: err})
	}
	if commandErr != nil && ledgerFailure != nil {
		// This Command's own freshly recorded classification goes first:
		// errors.As walks a Join tree depth-first and returns the first
		// matching type it finds, so if commandErr already carries an older
		// *RecordedCommandError from a child Command's own finish call
		// (ADR-0049's chain continuations), putting it first would let
		// errors.As silently return that stale inner classification instead
		// of this Command's own.
		return errors.Join(&RecordedCommandError{
			Code: ledgerFailure.Code, Stage: ledgerFailure.Stage, Partial: partial, Envelope: ledgerFailure.Details,
		}, commandErr)
	}
	return commandErr
}
