// Package scheduler defines storage- and transport-neutral one-shot schedule contracts.
package scheduler

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandcontract"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/commandledger"
)

const (
	SchemaVersion  = 1
	CommandVersion = "workspace-command.v1"
)

var (
	ErrInvalidSchedule    = errors.New("invalid schedule")
	ErrAlreadyExists      = errors.New("schedule already exists")
	ErrNotFound           = errors.New("schedule not found")
	ErrVersionConflict    = errors.New("schedule version conflict")
	ErrDefinitionConflict = errors.New("schedule ID was already used for a different definition")
)

var scheduleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type State string

const (
	StatePending          State = "pending"
	StateDispatching      State = "dispatching"
	StateSucceeded        State = "succeeded"
	StateFailed           State = "failed"
	StateRecoveryRequired State = "recovery_required"
)

func (state State) Valid() bool {
	return state == StatePending || state == StateDispatching || state == StateSucceeded || state == StateFailed || state == StateRecoveryRequired
}

func (state State) Terminal() bool {
	return state == StateSucceeded || state == StateFailed || state == StateRecoveryRequired
}

type Command struct {
	Version   string          `json:"version"`
	CommandID string          `json:"command_id"`
	Operation string          `json:"operation"`
	Approved  bool            `json:"approved"`
	Payload   json.RawMessage `json:"payload"`
}

func (command Command) Validate() error {
	if command.Version != CommandVersion || commandledger.ValidateCommandID(command.CommandID) != nil ||
		command.Operation == "" || command.Operation != strings.TrimSpace(command.Operation) || strings.ContainsAny(command.Operation, "\r\n") ||
		!commandcontract.Schedulable(command.Operation) || !command.Approved || len(command.Payload) == 0 || !json.Valid(command.Payload) ||
		commandcontract.ValidatePayload(command.Operation, command.Payload) != nil {
		return ErrInvalidSchedule
	}
	return nil
}

func ValidateScheduleID(scheduleID string) error {
	if !scheduleIDPattern.MatchString(strings.TrimSpace(scheduleID)) {
		return ErrInvalidSchedule
	}
	return nil
}

func (command Command) Clone() Command {
	decoder := json.NewDecoder(bytes.NewReader(command.Payload))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			command.Payload = append(json.RawMessage(nil), canonical...)
			return command
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, command.Payload) == nil {
		command.Payload = append(json.RawMessage(nil), compact.Bytes()...)
	} else {
		command.Payload = append(json.RawMessage(nil), command.Payload...)
	}
	return command
}

type Failure struct {
	Code             string `json:"code"`
	Stage            string `json:"stage,omitempty"`
	RecoveryRequired bool   `json:"recovery_required,omitempty"`
}

type Record struct {
	SchemaVersion     int             `json:"schema_version"`
	ScheduleID        string          `json:"schedule_id"`
	DefinitionDigest  string          `json:"definition_digest"`
	DueAt             time.Time       `json:"due_at"`
	CreatedAt         time.Time       `json:"created_at"`
	ApprovalReference string          `json:"approval_reference,omitempty"`
	Command           Command         `json:"command"`
	State             State           `json:"state"`
	Version           uint64          `json:"version"`
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Failure           *Failure        `json:"failure,omitempty"`
}

func NewPending(scheduleID string, dueAt, createdAt time.Time, approvalReference string, command Command) (Record, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	approvalReference = strings.TrimSpace(approvalReference)
	command = command.Clone()
	if !scheduleIDPattern.MatchString(scheduleID) || dueAt.IsZero() || createdAt.IsZero() || command.Validate() != nil ||
		strings.ContainsAny(approvalReference, "\r\n") {
		return Record{}, ErrInvalidSchedule
	}
	digest, err := definitionDigest(scheduleID, dueAt, approvalReference, command)
	if err != nil {
		return Record{}, err
	}
	record := Record{
		SchemaVersion: SchemaVersion, ScheduleID: scheduleID, DefinitionDigest: digest,
		DueAt: dueAt, CreatedAt: createdAt, ApprovalReference: approvalReference,
		Command: command.Clone(), State: StatePending, Version: 1,
	}
	return record, record.Validate()
}

func (record Record) Validate() error {
	expectedDigest, digestErr := definitionDigest(record.ScheduleID, record.DueAt, record.ApprovalReference, record.Command)
	if record.SchemaVersion != SchemaVersion || !scheduleIDPattern.MatchString(record.ScheduleID) ||
		!validDigest(record.DefinitionDigest) || digestErr != nil || record.DefinitionDigest != expectedDigest || record.DueAt.IsZero() || record.CreatedAt.IsZero() || record.Command.Validate() != nil ||
		!record.State.Valid() || record.Version == 0 || strings.ContainsAny(record.ApprovalReference, "\r\n") {
		return ErrInvalidSchedule
	}
	switch record.State {
	case StatePending:
		if record.Version != 1 || record.StartedAt != nil || record.FinishedAt != nil || len(record.Result) != 0 || record.Failure != nil {
			return ErrInvalidSchedule
		}
	case StateDispatching:
		if record.Version != 2 || record.StartedAt == nil || record.StartedAt.IsZero() || record.StartedAt.Before(record.CreatedAt) || record.StartedAt.Before(record.DueAt) ||
			record.FinishedAt != nil || len(record.Result) != 0 || record.Failure != nil {
			return ErrInvalidSchedule
		}
	case StateSucceeded:
		if record.Version != 3 || record.StartedAt == nil || record.FinishedAt == nil || record.FinishedAt.Before(*record.StartedAt) || len(record.Result) == 0 || !json.Valid(record.Result) || record.Failure != nil {
			return ErrInvalidSchedule
		}
	case StateFailed, StateRecoveryRequired:
		if record.Version != 3 || record.StartedAt == nil || record.FinishedAt == nil || record.FinishedAt.Before(*record.StartedAt) || len(record.Result) == 0 || !json.Valid(record.Result) ||
			record.Failure == nil || strings.TrimSpace(record.Failure.Code) == "" || strings.ContainsAny(record.Failure.Code+record.Failure.Stage, "\r\n") ||
			(record.State == StateRecoveryRequired) != record.Failure.RecoveryRequired {
			return ErrInvalidSchedule
		}
	}
	return nil
}

func (record Record) Due(now time.Time) bool {
	return record.State == StatePending && !now.IsZero() && !now.Before(record.CreatedAt) && !record.DueAt.After(now)
}

func (record Record) Start(at time.Time) (Record, error) {
	if record.Validate() != nil || record.State != StatePending || at.IsZero() || at.Before(record.CreatedAt) || at.Before(record.DueAt) {
		return Record{}, ErrInvalidSchedule
	}
	next := record.Clone()
	next.State, next.Version, next.StartedAt = StateDispatching, 2, timePointer(at)
	return next, next.Validate()
}

func (record Record) Finish(at time.Time, outcome DispatchOutcome) (Record, error) {
	if record.Validate() != nil || record.State != StateDispatching || at.IsZero() || at.Before(*record.StartedAt) || len(outcome.Result) == 0 || !json.Valid(outcome.Result) {
		return Record{}, ErrInvalidSchedule
	}
	next := record.Clone()
	next.Version, next.FinishedAt, next.Result = 3, timePointer(at), append(json.RawMessage(nil), outcome.Result...)
	if outcome.Failure == nil {
		next.State = StateSucceeded
	} else {
		failure := *outcome.Failure
		failure.Code, failure.Stage = strings.TrimSpace(failure.Code), strings.TrimSpace(failure.Stage)
		next.Failure = &failure
		next.State = StateFailed
		if failure.RecoveryRequired {
			next.State = StateRecoveryRequired
		}
	}
	return next, next.Validate()
}

func (record Record) SameDefinition(candidate Record) bool {
	return record.ScheduleID == candidate.ScheduleID && record.DefinitionDigest == candidate.DefinitionDigest
}

func (record Record) Clone() Record {
	cloned := record
	cloned.Command = record.Command.Clone()
	cloned.Result = append(json.RawMessage(nil), record.Result...)
	if record.StartedAt != nil {
		cloned.StartedAt = timePointer(*record.StartedAt)
	}
	if record.FinishedAt != nil {
		cloned.FinishedAt = timePointer(*record.FinishedAt)
	}
	if record.Failure != nil {
		failure := *record.Failure
		cloned.Failure = &failure
	}
	return cloned
}

func ValidateTransition(current, next Record, expectedVersion uint64) error {
	if current.Validate() != nil || next.Validate() != nil || current.ScheduleID != next.ScheduleID ||
		!current.SameDefinition(next) || current.Version != expectedVersion || next.Version != current.Version+1 {
		return ErrVersionConflict
	}
	if current.State == StatePending && next.State == StateDispatching || current.State == StateDispatching && next.State.Terminal() {
		return nil
	}
	return ErrVersionConflict
}

type Store interface {
	Create(ctx context.Context, record Record) error
	Get(ctx context.Context, scheduleID string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Update(ctx context.Context, next Record, expectedVersion uint64) error
}

type DispatchOutcome struct {
	Result  json.RawMessage
	Failure *Failure
}

type Dispatcher interface {
	Dispatch(ctx context.Context, command Command) (DispatchOutcome, error)
}

type RunResult struct {
	At      time.Time `json:"at"`
	Records []Record  `json:"records"`
}

type RunError struct {
	Result RunResult
	Stage  string
	Err    error
}

func (runError *RunError) Error() string {
	return fmt.Sprintf("scheduler failed at %s", runError.Stage)
}
func (runError *RunError) Unwrap() error { return runError.Err }

func timePointer(value time.Time) *time.Time { return &value }

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func definitionDigest(scheduleID string, dueAt time.Time, approvalReference string, command Command) (string, error) {
	return commandledger.RequestDigest(struct {
		ScheduleID        string    `json:"schedule_id"`
		DueAt             time.Time `json:"due_at"`
		ApprovalReference string    `json:"approval_reference,omitempty"`
		Command           Command   `json:"command"`
	}{strings.TrimSpace(scheduleID), dueAt, strings.TrimSpace(approvalReference), command.Clone()})
}
