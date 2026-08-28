package process

import (
	"context"
	"errors"
	"time"

	"github.com/AkiraShimizu0/WorkCairn/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/WorkCairn/go/internal/project"
)

var ErrProjectDependenciesApproval = errors.New("explicit Project dependencies approval is required")

type ProjectDependenciesInput struct {
	VaultRoot   string
	ProjectName string
	Rows        []project.TaskDependency
	CurrentTime time.Time
	CommandID   string
}

func PlanProjectDependencies(ctx context.Context, input ProjectDependenciesInput) (vault.ProjectDependencyRecord, error) {
	store, err := vault.NewProjectStore(input.VaultRoot)
	if err != nil {
		return vault.ProjectDependencyRecord{}, err
	}
	return store.PlanTaskDependencies(ctx, input.ProjectName, input.Rows, input.CurrentTime)
}

func ExecuteProjectDependencies(ctx context.Context, input ProjectDependenciesInput, approved bool) (vault.ProjectDependencyRecord, error) {
	if !approved {
		return vault.ProjectDependencyRecord{}, ErrProjectDependenciesApproval
	}
	claim, err := claimProjectCommand(ctx, input.VaultRoot, input.ProjectName, input.CommandID, "project.dependencies.create", "task-dependencies", struct {
		ProjectName string                   `json:"project_name"`
		Rows        []project.TaskDependency `json:"rows"`
		CurrentTime time.Time                `json:"current_time"`
	}{input.ProjectName, input.Rows, input.CurrentTime})
	if err != nil {
		return vault.ProjectDependencyRecord{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[vault.ProjectDependencyRecord](claim); ok {
		return replayed, replayErr
	}
	store, err := vault.NewProjectStore(input.VaultRoot)
	if err != nil {
		return vault.ProjectDependencyRecord{}, finishDurableCommand(ctx, claim, vault.ProjectDependencyRecord{}, err, "PROJECT_DEPENDENCIES_CREATE_FAILED", "project_dependencies", false)
	}
	record, createErr := store.CreateTaskDependencies(ctx, input.ProjectName, input.Rows, input.CurrentTime)
	return record, finishDurableCommand(ctx, claim, record, createErr, "PROJECT_DEPENDENCIES_CREATE_FAILED", "project_dependencies", record.Committed)
}
