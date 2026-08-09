package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/event"
	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var (
	ErrProjectApprovalRequired = errors.New("explicit Project bootstrap approval is required")
	ErrTaskCreateApproval      = errors.New("explicit Task creation approval is required")
)

type ProjectBootstrapInput struct {
	VaultRoot   string
	ProjectID   string
	ProjectName string
	Description string
	CurrentTime time.Time
	CommandID   string
}

type ProjectBootstrapPlan struct {
	ProjectID        string   `json:"project_id"`
	ProjectName      string   `json:"project_name"`
	ManagedFiles     []string `json:"managed_files"`
	Executable       bool     `json:"executable"`
	BlockingReasons  []string `json:"blocking_reasons"`
	ApprovalRequired bool     `json:"approval_required"`
}

func PlanProjectBootstrap(ctx context.Context, input ProjectBootstrapInput) (ProjectBootstrapPlan, error) {
	if ctx == nil {
		return ProjectBootstrapPlan{}, fmt.Errorf("plan Project bootstrap: context is required")
	}
	definition := project.Definition{ID: strings.TrimSpace(input.ProjectID), Name: strings.TrimSpace(input.ProjectName), Description: input.Description}
	if err := definition.Validate(); err != nil || input.CurrentTime.IsZero() {
		if err != nil {
			return ProjectBootstrapPlan{}, err
		}
		return ProjectBootstrapPlan{}, fmt.Errorf("plan Project bootstrap: time is required")
	}
	store, err := vault.NewProjectStore(input.VaultRoot)
	if err != nil {
		return ProjectBootstrapPlan{}, err
	}
	exists, err := store.Exists(definition)
	if err != nil {
		return ProjectBootstrapPlan{}, err
	}
	blocking := []string{}
	if exists {
		blocking = append(blocking, "project_already_exists")
	}
	return ProjectBootstrapPlan{
		ProjectID: definition.ID, ProjectName: definition.Name,
		ManagedFiles: []string{"Project.md", "Tasks.md", "Decisions.md", "Progress.md"},
		Executable:   len(blocking) == 0, BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteProjectBootstrap(ctx context.Context, input ProjectBootstrapInput, approved bool) (vault.ProjectRecord, error) {
	if !approved {
		return vault.ProjectRecord{}, ErrProjectApprovalRequired
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "project.bootstrap", input.ProjectID, struct {
		ProjectID   string    `json:"project_id"`
		ProjectName string    `json:"project_name"`
		Description string    `json:"description"`
		CurrentTime time.Time `json:"current_time"`
	}{input.ProjectID, input.ProjectName, input.Description, input.CurrentTime})
	if err != nil {
		return vault.ProjectRecord{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[vault.ProjectRecord](claim); ok {
		return replayed, replayErr
	}
	record, bootstrapErr := executeClaimedProjectBootstrap(ctx, input)
	return record, finishDurableCommand(ctx, claim, record, bootstrapErr, "PROJECT_BOOTSTRAP_FAILED", "project_bootstrap", record.Committed)
}

func executeClaimedProjectBootstrap(ctx context.Context, input ProjectBootstrapInput) (vault.ProjectRecord, error) {
	plan, err := PlanProjectBootstrap(ctx, input)
	if err != nil {
		return vault.ProjectRecord{}, err
	}
	if !plan.Executable {
		return vault.ProjectRecord{}, fmt.Errorf("Project bootstrap preflight failed: %s", strings.Join(plan.BlockingReasons, ","))
	}
	store, err := vault.NewProjectStore(input.VaultRoot)
	if err != nil {
		return vault.ProjectRecord{}, err
	}
	return store.Bootstrap(ctx, project.Definition{ID: plan.ProjectID, Name: plan.ProjectName, Description: input.Description}, input.CurrentTime)
}

type TaskCreationInput struct {
	VaultRoot      string
	ProjectName    string
	Title          string
	AssigneeID     *string
	CurrentTime    time.Time
	CommandID      string
	EventObservers []event.Observer
}

type TaskCreationPlan struct {
	ProjectName      string   `json:"project_name"`
	TaskID           string   `json:"task_id"`
	Title            string   `json:"title"`
	AssigneeID       *string  `json:"assignee_id"`
	Executable       bool     `json:"executable"`
	BlockingReasons  []string `json:"blocking_reasons"`
	ApprovalRequired bool     `json:"approval_required"`
}

type TaskCreationResult struct {
	Task           task.Task `json:"task"`
	EventPublished bool      `json:"event_published"`
}

func PlanTaskCreation(ctx context.Context, input TaskCreationInput) (TaskCreationPlan, error) {
	if ctx == nil {
		return TaskCreationPlan{}, fmt.Errorf("plan Task creation: context is required")
	}
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.Title = strings.TrimSpace(input.Title)
	if input.CurrentTime.IsZero() {
		return TaskCreationPlan{}, fmt.Errorf("plan Task creation: time is required")
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName})
	if err != nil {
		return TaskCreationPlan{}, err
	}
	existing, err := store.InspectAll(ctx)
	if err != nil {
		return TaskCreationPlan{}, err
	}
	ids := make([]string, 0, len(existing))
	for _, current := range existing {
		ids = append(ids, current.ID)
	}
	taskID, err := task.NextTaskID(ids)
	if err != nil {
		return TaskCreationPlan{}, err
	}
	assignee := cloneOptionalText(input.AssigneeID)
	blocking := make([]string, 0, 2)
	if _, err := task.New(task.CreateInput{ID: taskID, Title: input.Title, AssigneeID: assignee}); err != nil {
		blocking = append(blocking, "invalid_task")
	}
	if assignee != nil {
		inspection, err := InspectOrganization(ctx, input.VaultRoot)
		if err != nil {
			return TaskCreationPlan{}, err
		}
		matches := 0
		for _, employee := range inspection.Inventory.Employees {
			if employee.ID == *assignee {
				matches++
			}
		}
		if matches == 0 {
			blocking = append(blocking, "assignee_not_found")
		} else if matches > 1 {
			blocking = append(blocking, "assignee_id_duplicate")
		}
	}
	return TaskCreationPlan{ProjectName: input.ProjectName, TaskID: taskID, Title: input.Title, AssigneeID: assignee, Executable: len(blocking) == 0, BlockingReasons: blocking, ApprovalRequired: true}, nil
}

func ExecuteTaskCreation(ctx context.Context, input TaskCreationInput, approved bool) (TaskCreationResult, error) {
	if !approved {
		return TaskCreationResult{}, ErrTaskCreateApproval
	}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "task.create", "task-collection", struct {
		ProjectName string    `json:"project_name"`
		Title       string    `json:"title"`
		AssigneeID  *string   `json:"assignee_id"`
		CurrentTime time.Time `json:"current_time"`
	}{input.ProjectName, input.Title, cloneOptionalText(input.AssigneeID), input.CurrentTime})
	if err != nil {
		return TaskCreationResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[TaskCreationResult](claim); ok {
		return replayed, replayErr
	}
	result, createErr := executeClaimedTaskCreation(ctx, input)
	return result, finishDurableCommand(ctx, claim, result, createErr, "TASK_CREATE_FAILED", "task_create", result.Task.ID != "")
}

func executeClaimedTaskCreation(ctx context.Context, input TaskCreationInput) (TaskCreationResult, error) {
	plan, err := PlanTaskCreation(ctx, input)
	if err != nil {
		return TaskCreationResult{}, err
	}
	if !plan.Executable {
		return TaskCreationResult{}, fmt.Errorf("Task creation preflight failed: %s", strings.Join(plan.BlockingReasons, ","))
	}
	store, err := vault.NewTaskStore(vault.TaskStoreConfig{VaultRoot: input.VaultRoot, ProjectName: input.ProjectName, Clock: func() time.Time { return input.CurrentTime }})
	if err != nil {
		return TaskCreationResult{}, err
	}
	audit, err := vault.NewAuditSubscriber(input.VaultRoot, input.ProjectName)
	if err != nil {
		return TaskCreationResult{}, err
	}
	events := service.NewEventService(nil)
	if _, err := events.Subscribe(event.TaskCreated, audit.Handler()); err != nil {
		return TaskCreationResult{}, err
	}
	for observerIndex, observer := range input.EventObservers {
		if observer.Handler == nil || len(observer.Types) == 0 {
			return TaskCreationResult{}, fmt.Errorf("Task creation Event observer %d is invalid", observerIndex)
		}
		for _, eventType := range observer.Types {
			if _, err := events.Subscribe(eventType, observer.Handler); err != nil {
				return TaskCreationResult{}, err
			}
		}
	}
	tasks, err := service.NewTaskService(store, events)
	if err != nil {
		return TaskCreationResult{}, err
	}
	if err := events.Start(); err != nil {
		return TaskCreationResult{}, err
	}
	if err := tasks.Activate(); err != nil {
		_ = events.Stop()
		return TaskCreationResult{}, err
	}
	created, createErr := tasks.Create(ctx, task.CreateInput{ID: plan.TaskID, Title: plan.Title, AssigneeID: plan.AssigneeID})
	deactivateErr := tasks.Deactivate()
	stopErr := events.Stop()
	result := TaskCreationResult{Task: created, EventPublished: createErr == nil}
	if createErr != nil {
		return result, createErr
	}
	if err := errors.Join(deactivateErr, stopErr); err != nil {
		return result, err
	}
	return result, nil
}

func cloneOptionalText(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	cloned := strings.TrimSpace(*value)
	return &cloned
}
