package process

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/organization"
)

var ErrEmployeeHireApproval = errors.New("explicit Employee hire approval is required")

type EmployeeHireInput struct {
	VaultRoot   string
	Candidate   organization.EmployeeCandidate
	CurrentTime time.Time
	CommandID   string
}
type EmployeeHirePlan struct {
	Candidate          organization.EmployeeCandidate `json:"candidate"`
	IdentityValidation organization.NameValidation    `json:"identity_validation"`
	Executable         bool                           `json:"executable"`
	BlockingReasons    []string                       `json:"blocking_reasons"`
	ApprovalRequired   bool                           `json:"approval_required"`
}

func PlanEmployeeHire(ctx context.Context, input EmployeeHireInput) (EmployeeHirePlan, error) {
	if ctx == nil {
		return EmployeeHirePlan{}, fmt.Errorf("plan Employee hire: context is required")
	}
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return EmployeeHirePlan{}, err
	}
	validation, err := store.PlanHire(ctx, input.Candidate)
	if err != nil {
		return EmployeeHirePlan{Candidate: input.Candidate, IdentityValidation: validation, Executable: false, BlockingReasons: []string{"employee_hire_preflight_failed"}, ApprovalRequired: true}, err
	}
	return EmployeeHirePlan{Candidate: input.Candidate, IdentityValidation: validation, Executable: true, BlockingReasons: []string{}, ApprovalRequired: true}, nil
}

func ExecuteEmployeeHire(ctx context.Context, input EmployeeHireInput, approved bool) (vault.EmployeeHireRecord, error) {
	if !approved {
		return vault.EmployeeHireRecord{}, ErrEmployeeHireApproval
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "organization.employee_hire", input.Candidate.ID, struct {
		Candidate   organization.EmployeeCandidate `json:"candidate"`
		CurrentTime time.Time                      `json:"current_time"`
	}{input.Candidate, input.CurrentTime})
	if err != nil {
		return vault.EmployeeHireRecord{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[vault.EmployeeHireRecord](claim); ok {
		return replayed, replayErr
	}
	record, hireErr := executeClaimedEmployeeHire(ctx, input)
	return record, finishDurableCommand(ctx, claim, record, hireErr, "EMPLOYEE_HIRE_FAILED", "employee_hire", record.CanonicalCommitted)
}

func executeClaimedEmployeeHire(ctx context.Context, input EmployeeHireInput) (vault.EmployeeHireRecord, error) {
	if input.CurrentTime.IsZero() {
		return vault.EmployeeHireRecord{}, fmt.Errorf("Employee hire time is required")
	}
	if _, err := PlanEmployeeHire(ctx, input); err != nil {
		return vault.EmployeeHireRecord{}, err
	}
	store, err := vault.NewEmployeeStore(input.VaultRoot)
	if err != nil {
		return vault.EmployeeHireRecord{}, err
	}
	return store.Hire(ctx, input.Candidate, input.CurrentTime)
}
