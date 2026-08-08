package vault

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidInput       = errors.New("invalid Vault Adapter input")
	ErrDocumentNotFound   = errors.New("required Vault document was not found")
	ErrInvalidDocument    = errors.New("invalid Vault document")
	ErrDuplicateIdentity  = errors.New("duplicate employee identity")
	ErrAssigneeMissing    = errors.New("Task assignee is missing")
	ErrMetadataMissing    = errors.New("Task metadata block is missing")
	ErrMetadataInvalid    = errors.New("Task metadata block is invalid")
	ErrMetadataDuplicate  = errors.New("Task metadata block contains duplicates")
	ErrMetadataMismatch   = errors.New("Tasks table and metadata do not match")
	ErrAtomicWrite        = errors.New("atomic Vault write failed")
	ErrAtomicTargetExists = errors.New("atomic Vault target already exists")
	ErrMigrationApproval  = errors.New("Task metadata migration requires explicit approval")
	ErrMigrationStale     = errors.New("Task metadata migration plan is stale")
	ErrMigrationUnsafe    = errors.New("Task metadata migration cannot preserve Task state")
	ErrMigrationNotNeeded = errors.New("Task metadata migration is not needed")
)

// AtomicWriteError reports whether the atomic publication commit point completed.
// A committed error must never be retried as though the original file were
// certainly still present.
type AtomicWriteError struct {
	Stage     string
	Committed bool
	Err       error
}

func (writeError *AtomicWriteError) Error() string {
	return fmt.Sprintf(
		"%s at %s (committed=%t)",
		ErrAtomicWrite,
		writeError.Stage,
		writeError.Committed,
	)
}

func (writeError *AtomicWriteError) Unwrap() error {
	return writeError.Err
}

func (writeError *AtomicWriteError) Is(target error) bool {
	return target == ErrAtomicWrite
}
