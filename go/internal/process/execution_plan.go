// Package process composes effectful Adapters at the product-process edge.
// It does not add Domain rules or read provider credentials.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workcairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workcairn/go/internal/service"
	"github.com/AkiraShimizu0/workcairn/go/internal/task"
	"github.com/AkiraShimizu0/workcairn/go/internal/workflow"
)

var ErrExecutionPlanSnapshotChanged = errors.New("execution plan source changed while reading")

type ExecutionReadinessMode string

const (
	ExecutionReadinessSequential ExecutionReadinessMode = "sequential"
	ExecutionReadinessTargeted   ExecutionReadinessMode = "targeted_revision"
)

// ExecutionPlanInput identifies one prospective Task execution. CurrentTime is
// injected so planning is deterministic and never consults a global clock.
type ExecutionPlanInput struct {
	VaultRoot     string
	ProjectID     string
	ProjectName   string
	TaskID        string
	CurrentTime   time.Time
	ReadinessMode ExecutionReadinessMode
}

// ExecutionPlan is a read-only preflight result. It is not approval evidence
// and cannot itself authorize Runtime effects.
type ExecutionPlan struct {
	ProjectID         string                   `json:"project_id"`
	ProjectName       string                   `json:"project_name"`
	TaskID            string                   `json:"task_id"`
	TaskTitle         string                   `json:"task_title"`
	TaskVersion       uint64                   `json:"task_version"`
	AssigneeID        string                   `json:"assignee_id"`
	EmployeeName      string                   `json:"employee_name"`
	Model             string                   `json:"model"`
	DeliverablePath   string                   `json:"deliverable_path"`
	DeliverableExists bool                     `json:"deliverable_exists"`
	Readiness         workflow.ReadinessResult `json:"readiness"`
	Executable        bool                     `json:"executable"`
	BlockingReasons   []string                 `json:"blocking_reasons"`
	ApprovalRequired  bool                     `json:"approval_required"`
}

// PlanExecution validates the managed Task snapshot and structured Vault
// context without creating locks, writing files, or constructing a Runner.
func PlanExecution(ctx context.Context, input ExecutionPlanInput) (ExecutionPlan, error) {
	if ctx == nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution: context is required")
	}
	input.VaultRoot = strings.TrimSpace(input.VaultRoot)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.TaskID = strings.TrimSpace(input.TaskID)
	if input.ReadinessMode == "" {
		input.ReadinessMode = ExecutionReadinessSequential
	}
	if input.ReadinessMode != ExecutionReadinessSequential && input.ReadinessMode != ExecutionReadinessTargeted {
		return ExecutionPlan{}, fmt.Errorf("plan execution: invalid readiness mode")
	}

	store, err := vault.NewTaskStore(vault.TaskStoreConfig{
		VaultRoot: input.VaultRoot, ProjectName: input.ProjectName,
	})
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution Task snapshot: %w", err)
	}
	persisted, err := store.Inspect(ctx, input.TaskID)
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution Task snapshot: %w", err)
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution context: %w", err)
	}
	request, err := loader.LoadExecutionRequest(ctx, vault.ExecutionInput{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName,
		TaskID: input.TaskID, CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution context: %w", err)
	}
	requested, found := executionTask(request.Tasks, input.TaskID)
	if !found || !sameTaskSnapshot(persisted, requested) {
		return ExecutionPlan{}, ErrExecutionPlanSnapshotChanged
	}
	var readiness workflow.ReadinessResult
	if input.ReadinessMode == ExecutionReadinessTargeted {
		targeted, targetErr := service.NewTargetedWorkflowService(input.TaskID)
		if targetErr != nil {
			return ExecutionPlan{}, fmt.Errorf("plan execution readiness: %w", targetErr)
		}
		readiness, err = targeted.Readiness(request.Tasks, request.Dependencies, request.ExistingEmployees)
	} else {
		readiness, err = service.NewWorkflowService().Readiness(request.Tasks, request.Dependencies, request.ExistingEmployees)
	}
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution readiness: %w", err)
	}

	relativeDeliverable := filepath.ToSlash(filepath.Join("Deliverables", input.TaskID+".md"))
	deliverablePath := filepath.Join(input.VaultRoot, "プロジェクト", input.ProjectName, filepath.FromSlash(relativeDeliverable))
	deliverableExists, err := pathExists(deliverablePath)
	if err != nil {
		return ExecutionPlan{}, fmt.Errorf("plan execution Deliverable: %w", err)
	}
	blockingReasons := append([]string(nil), readiness.BlockingReasons...)
	requestedReady := readiness.Ready && readiness.TaskID == input.TaskID &&
		readiness.AssigneeID != nil && *readiness.AssigneeID == request.Employee.EmployeeID
	if !requestedReady {
		blockingReasons = append(blockingReasons, "requested_task_not_ready")
	}
	if deliverableExists {
		blockingReasons = append(blockingReasons, "deliverable_already_exists")
	}
	return ExecutionPlan{
		ProjectID: input.ProjectID, ProjectName: input.ProjectName,
		TaskID: input.TaskID, TaskTitle: requested.Title, TaskVersion: persisted.Version,
		AssigneeID: request.Employee.EmployeeID, EmployeeName: request.Employee.Name, Model: request.Employee.Model,
		DeliverablePath: relativeDeliverable, DeliverableExists: deliverableExists,
		Readiness: readiness, Executable: requestedReady && !deliverableExists,
		BlockingReasons: blockingReasons, ApprovalRequired: true,
	}, nil
}

func executionTask(tasks []workflow.Task, taskID string) (workflow.Task, bool) {
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			return candidate, true
		}
	}
	return workflow.Task{}, false
}

func sameTaskSnapshot(persisted task.Task, candidate workflow.Task) bool {
	if persisted.ID != candidate.ID || persisted.Title != candidate.Title || string(persisted.Status) != candidate.Status {
		return false
	}
	if persisted.AssigneeID == nil || candidate.AssigneeID == nil {
		return persisted.AssigneeID == nil && candidate.AssigneeID == nil
	}
	return *persisted.AssigneeID == *candidate.AssigneeID
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
