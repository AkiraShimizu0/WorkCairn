package execution

import (
	"errors"
	"fmt"
)

var ErrInvalidRequest = errors.New("invalid workflow execution request")

type Stage string

const (
	StageValidation         Stage = "validation"
	StageReadiness          Stage = "readiness"
	StageApproval           Stage = "approval"
	StageDependencyEvidence Stage = "dependency_evidence"
	StageTaskStart          Stage = "task_start"
	StageWorker             Stage = "worker"
	StageDeliverable        Stage = "deliverable_save"
	StageTaskComplete       Stage = "task_complete"
	StageTaskFail           Stage = "task_fail"
	StageFailurePolicy      Stage = "failure_policy"
	StageTaskHold           Stage = "task_hold"
)

type ErrorKind string

const (
	ErrorInvalidRequest            ErrorKind = "INVALID_REQUEST"
	ErrorTaskNotReady              ErrorKind = "TASK_NOT_READY"
	ErrorApprovalRejected          ErrorKind = "APPROVAL_REJECTED"
	ErrorPolicyFailed              ErrorKind = "POLICY_FAILED"
	ErrorDependencyEvidenceMissing ErrorKind = "DEPENDENCY_EVIDENCE_MISSING"
	ErrorTaskStartFailed           ErrorKind = "TASK_START_FAILED"
	ErrorWorkerFailed              ErrorKind = "WORKER_FAILED"
	ErrorDeliverableSaveFailed     ErrorKind = "DELIVERABLE_SAVE_FAILED"
	ErrorTaskCompleteFailed        ErrorKind = "TASK_COMPLETE_FAILED"
	ErrorTaskFailRecordFailed      ErrorKind = "TASK_FAIL_RECORD_FAILED"
	ErrorTaskHoldFailed            ErrorKind = "TASK_HOLD_FAILED"
	ErrorEventPublicationPartial   ErrorKind = "EVENT_PUBLICATION_PARTIAL"
	ErrorCanceled                  ErrorKind = "CANCELED"
	ErrorTimeout                   ErrorKind = "TIMEOUT"
	// ErrorOutputIncomplete means the Provider call itself succeeded (no
	// transport/HTTP/API error) but its own output was cut off before
	// completion (e.g. the Provider's own output token ceiling), so the
	// generated content is not accepted as a finished Task deliverable.
	// This is deliberately distinct from ErrorWorkerFailed: the Runner did
	// not fail, the output is simply incomplete -- Provider call success
	// and Deliverable completeness are different questions (ADR-0058).
	ErrorOutputIncomplete ErrorKind = "OUTPUT_INCOMPLETE"
)

// ExecutionError identifies the exact failed stage and carries the structured
// partial Result. Its public text does not expose provider error details.
type ExecutionError struct {
	Stage  Stage
	Kind   ErrorKind
	Result Result
	Err    error
}

func (executionError *ExecutionError) Error() string {
	return fmt.Sprintf("workflow execution failed at %s: %s", executionError.Stage, executionError.Kind)
}

func (executionError *ExecutionError) Unwrap() error { return executionError.Err }
