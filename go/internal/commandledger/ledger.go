// Package commandledger defines storage-neutral durable command identity and outcome contracts.
package commandledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const SchemaVersion = 1

var (
	ErrInvalidRecord   = errors.New("invalid Command Ledger record")
	ErrAlreadyExists   = errors.New("Command Ledger record already exists")
	ErrNotFound        = errors.New("Command Ledger record not found")
	ErrVersionConflict = errors.New("Command Ledger version conflict")
	ErrRequestConflict = errors.New("Command ID was already used for a different request")
	ErrInProgress      = errors.New("Command is already in progress or requires recovery")
	ErrRecordedFailure = errors.New("Command has a recorded terminal failure")
)

var commandIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type State string

const (
	StateRunning        State = "running"
	StateSucceeded      State = "succeeded"
	StateFailed         State = "failed"
	StatePartialFailure State = "partial_failure"
)

func (state State) Valid() bool {
	return state == StateRunning || state == StateSucceeded || state == StateFailed || state == StatePartialFailure
}

func (state State) Terminal() bool { return state != StateRunning && state.Valid() }

type Failure struct {
	Code  string `json:"code"`
	Stage string `json:"stage,omitempty"`
}

type Record struct {
	SchemaVersion int             `json:"schema_version"`
	CommandID     string          `json:"command_id"`
	Operation     string          `json:"operation"`
	ProjectName   string          `json:"project_name"`
	AggregateID   string          `json:"aggregate_id"`
	RequestDigest string          `json:"request_digest"`
	State         State           `json:"state"`
	Version       uint64          `json:"version"`
	Result        json.RawMessage `json:"result,omitempty"`
	Failure       *Failure        `json:"failure,omitempty"`
}

func NewRunning(commandID, operation, projectName, aggregateID, requestDigest string) (Record, error) {
	record := Record{
		SchemaVersion: SchemaVersion, CommandID: strings.TrimSpace(commandID),
		Operation: strings.TrimSpace(operation), ProjectName: strings.TrimSpace(projectName),
		AggregateID: strings.TrimSpace(aggregateID), RequestDigest: strings.TrimSpace(requestDigest),
		State: StateRunning, Version: 1,
	}
	return record, record.Validate()
}

func ValidateCommandID(commandID string) error {
	if !commandIDPattern.MatchString(strings.TrimSpace(commandID)) {
		return ErrInvalidRecord
	}
	return nil
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion || !commandIDPattern.MatchString(record.CommandID) ||
		!canonicalText(record.Operation) || !canonicalText(record.ProjectName) || !canonicalText(record.AggregateID) ||
		!validDigest(record.RequestDigest) || !record.State.Valid() || record.Version == 0 {
		return ErrInvalidRecord
	}
	switch record.State {
	case StateRunning:
		if len(record.Result) != 0 || record.Failure != nil {
			return ErrInvalidRecord
		}
	case StateSucceeded:
		if len(record.Result) == 0 || !json.Valid(record.Result) || record.Failure != nil {
			return ErrInvalidRecord
		}
	case StateFailed, StatePartialFailure:
		if len(record.Result) == 0 || !json.Valid(record.Result) || record.Failure == nil || !canonicalText(record.Failure.Code) ||
			(record.Failure.Stage != "" && !canonicalText(record.Failure.Stage)) {
			return ErrInvalidRecord
		}
	}
	return nil
}

func canonicalText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\r\n")
}

func (record Record) Finish(state State, result json.RawMessage, failure *Failure) (Record, error) {
	if err := record.Validate(); err != nil || record.State != StateRunning || !state.Terminal() {
		return Record{}, ErrInvalidRecord
	}
	next := record.Clone()
	next.State = state
	next.Version++
	next.Result = append(json.RawMessage(nil), result...)
	if failure != nil {
		cloned := *failure
		cloned.Code = strings.TrimSpace(cloned.Code)
		cloned.Stage = strings.TrimSpace(cloned.Stage)
		next.Failure = &cloned
	}
	return next, next.Validate()
}

func (record Record) SameRequest(candidate Record) bool {
	return record.CommandID == candidate.CommandID && record.Operation == candidate.Operation &&
		record.ProjectName == candidate.ProjectName && record.AggregateID == candidate.AggregateID &&
		record.RequestDigest == candidate.RequestDigest
}

func (record Record) Clone() Record {
	cloned := record
	cloned.Result = append(json.RawMessage(nil), record.Result...)
	if record.Failure != nil {
		failure := *record.Failure
		cloned.Failure = &failure
	}
	return cloned
}

func ValidateTransition(current, next Record, expectedVersion uint64) error {
	if err := current.Validate(); err != nil {
		return err
	}
	if err := next.Validate(); err != nil || current.State != StateRunning || !next.State.Terminal() ||
		expectedVersion != current.Version || next.Version != current.Version+1 || !current.SameRequest(next) {
		return ErrVersionConflict
	}
	return nil
}

type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, commandID string) (Record, error)
	Update(ctx context.Context, next Record, expectedVersion uint64) error
}

func RequestDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode Command request: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// DeriveChildCommandID gives one deterministic durable identity to a
// side-effect command owned by a parent orchestration command.
func DeriveChildCommandID(parentCommandID, aggregateID string) (string, error) {
	parentCommandID = strings.TrimSpace(parentCommandID)
	aggregateID = strings.TrimSpace(aggregateID)
	if ValidateCommandID(parentCommandID) != nil || !canonicalText(aggregateID) {
		return "", ErrInvalidRecord
	}
	digest := sha256.Sum256([]byte(parentCommandID + "\x00" + aggregateID))
	return "CHILD-" + hex.EncodeToString(digest[:16]), nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
