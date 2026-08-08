package process

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/claude"
	"github.com/AkiraShimizu0/workspace-os/go/internal/adapter/vault"
	"github.com/AkiraShimizu0/workspace-os/go/internal/ceoplan"
	"github.com/AkiraShimizu0/workspace-os/go/internal/project"
	"github.com/AkiraShimizu0/workspace-os/go/internal/service"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
)

var (
	ErrCEOPlanGenerationApproval = errors.New("explicit CEO plan generation approval is required")
	ErrCEOPlanApplyApproval      = errors.New("explicit CEO plan apply approval is required")
)

type CEOPlanGenerationInput struct {
	VaultRoot string
	Request   string
	Model     string
	Approved  bool
}

func GenerateCEOPlan(ctx context.Context, input CEOPlanGenerationInput, provider ClaudeProcessConfig, httpClient claude.HTTPDoer) (service.CEOPlanResult, error) {
	if !input.Approved {
		return service.CEOPlanResult{}, ErrCEOPlanGenerationApproval
	}
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return service.CEOPlanResult{}, err
	}
	inventory, err := loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return service.CEOPlanResult{}, err
	}
	planRunner, err := claude.New(claude.Config{
		APIKey: provider.APIKey, ProviderModel: provider.ProviderModel,
		BaseURL: provider.BaseURL, MaxTokens: provider.MaxTokens,
	}, httpClient)
	if err != nil {
		return service.CEOPlanResult{}, err
	}
	planService, err := service.NewCEOPlanService(planRunner)
	if err != nil {
		return service.CEOPlanResult{}, err
	}
	return planService.Generate(ctx, service.CEOPlanInput{Request: input.Request, Employees: inventory.Employees, Model: input.Model})
}

type CEOPlanApplyInput struct {
	VaultRoot   string
	ProjectID   string
	Plan        ceoplan.Plan
	CurrentTime time.Time
	CommandID   string
}

type CEOPlanApplyPlan struct {
	ProjectID        string            `json:"project_id"`
	Plan             ceoplan.Plan      `json:"plan"`
	TaskIDs          map[string]string `json:"proposed_to_task_id_map"`
	Warnings         []string          `json:"warnings"`
	Executable       bool              `json:"executable"`
	BlockingReasons  []string          `json:"blocking_reasons"`
	ApprovalRequired bool              `json:"approval_required"`
}

type CEOPlanApplyResult struct {
	Status          string                         `json:"status"`
	ProjectName     string                         `json:"project_name"`
	Project         *vault.ProjectRecord           `json:"project,omitempty"`
	Tasks           []TaskCreationResult           `json:"created_tasks"`
	TaskIDs         map[string]string              `json:"proposed_to_task_id_map"`
	Dependencies    *vault.ProjectDependencyRecord `json:"dependencies,omitempty"`
	UnassignedTasks []string                       `json:"unassigned_tasks"`
	MissingRoles    []string                       `json:"missing_roles"`
	Warnings        []string                       `json:"warnings"`
}

type CEOPlanApplyStage string

const (
	CEOApplyProjectStage      CEOPlanApplyStage = "ceo_apply_project"
	CEOApplyTaskStage         CEOPlanApplyStage = "ceo_apply_task"
	CEOApplyDependenciesStage CEOPlanApplyStage = "ceo_apply_dependencies"
)

type CEOPlanApplyError struct {
	Stage  CEOPlanApplyStage
	Result CEOPlanApplyResult
	Err    error
}

func (applyError *CEOPlanApplyError) Error() string {
	return "CEO plan apply failed at " + string(applyError.Stage)
}
func (applyError *CEOPlanApplyError) Unwrap() error { return applyError.Err }

func PlanCEOPlanApply(ctx context.Context, input CEOPlanApplyInput) (CEOPlanApplyPlan, error) {
	if ctx == nil || input.CurrentTime.IsZero() {
		return CEOPlanApplyPlan{}, fmt.Errorf("invalid CEO apply context or time")
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	loader, err := vault.NewLoader(input.VaultRoot)
	if err != nil {
		return CEOPlanApplyPlan{}, err
	}
	inventory, err := loader.LoadOrganizationInventory(ctx, nil)
	if err != nil {
		return CEOPlanApplyPlan{}, err
	}
	validated, err := ceoplan.ValidateApprovedPlan(input.Plan, inventory.Employees)
	if err != nil {
		return CEOPlanApplyPlan{}, err
	}
	projectPlan, err := PlanProjectBootstrap(ctx, ProjectBootstrapInput{
		VaultRoot: input.VaultRoot, ProjectID: input.ProjectID,
		ProjectName: validated.ProjectName, Description: planDescription(validated), CurrentTime: input.CurrentTime,
	})
	if err != nil {
		return CEOPlanApplyPlan{}, err
	}
	taskIDs := make(map[string]string, len(validated.ProposedTasks))
	blocking := append([]string(nil), projectPlan.BlockingReasons...)
	for index, proposed := range validated.ProposedTasks {
		taskID := fmt.Sprintf("TASK-%03d", index+1)
		if _, err := task.New(task.CreateInput{ID: taskID, Title: proposed.Title, AssigneeID: proposed.AssigneeID}); err != nil {
			blocking = append(blocking, "invalid_task")
		}
		taskIDs[proposed.ProposalID] = taskID
	}
	warnings := planWarnings(validated)
	return CEOPlanApplyPlan{
		ProjectID: input.ProjectID, Plan: validated, TaskIDs: taskIDs, Warnings: warnings,
		Executable: len(blocking) == 0, BlockingReasons: blocking, ApprovalRequired: true,
	}, nil
}

func ExecuteCEOPlanApply(ctx context.Context, input CEOPlanApplyInput, approved bool) (CEOPlanApplyResult, error) {
	if !approved {
		return CEOPlanApplyResult{}, ErrCEOPlanApplyApproval
	}
	claim, err := claimWorkspaceCommand(ctx, input.VaultRoot, input.CommandID, "ceo_plan.apply", input.ProjectID, struct {
		ProjectID   string       `json:"project_id"`
		Plan        ceoplan.Plan `json:"plan"`
		CurrentTime time.Time    `json:"current_time"`
	}{input.ProjectID, input.Plan, input.CurrentTime})
	if err != nil {
		return CEOPlanApplyResult{}, err
	}
	if replayed, ok, replayErr := replayDurableCommand[CEOPlanApplyResult](claim); ok {
		return replayed, replayErr
	}
	result, applyErr := executeClaimedCEOPlanApply(ctx, input)
	code, stage := ceoApplyCommandFailure(applyErr)
	partial := result.Project != nil || len(result.Tasks) > 0 || result.Dependencies != nil
	return result, finishDurableCommand(ctx, claim, result, applyErr, code, stage, partial)
}

func executeClaimedCEOPlanApply(ctx context.Context, input CEOPlanApplyInput) (CEOPlanApplyResult, error) {
	plan, err := PlanCEOPlanApply(ctx, input)
	if err != nil {
		return CEOPlanApplyResult{}, err
	}
	if !plan.Executable {
		return CEOPlanApplyResult{}, fmt.Errorf("CEO plan apply preflight failed: %s", strings.Join(plan.BlockingReasons, ","))
	}
	result := CEOPlanApplyResult{
		Status: "applying", ProjectName: plan.Plan.ProjectName,
		Tasks: []TaskCreationResult{}, TaskIDs: map[string]string{},
		UnassignedTasks: []string{}, MissingRoles: plan.Plan.MissingRoles, Warnings: plan.Warnings,
	}
	projectRecord, err := ExecuteProjectBootstrap(ctx, ProjectBootstrapInput{
		VaultRoot: input.VaultRoot, ProjectID: plan.ProjectID, ProjectName: plan.Plan.ProjectName,
		Description: planDescription(plan.Plan), CurrentTime: input.CurrentTime,
	}, true)
	if projectRecord.Committed {
		result.Project = &projectRecord
	}
	if err != nil {
		return result, &CEOPlanApplyError{Stage: CEOApplyProjectStage, Result: result, Err: err}
	}
	for _, proposed := range plan.Plan.ProposedTasks {
		created, createErr := ExecuteTaskCreation(ctx, TaskCreationInput{
			VaultRoot: input.VaultRoot, ProjectName: plan.Plan.ProjectName,
			Title: proposed.Title, AssigneeID: proposed.AssigneeID, CurrentTime: input.CurrentTime,
		}, true)
		if created.Task.ID != "" {
			result.Tasks = append(result.Tasks, created)
			result.TaskIDs[proposed.ProposalID] = created.Task.ID
			if created.Task.AssigneeID == nil {
				result.UnassignedTasks = append(result.UnassignedTasks, created.Task.ID)
			}
		}
		if createErr != nil {
			return result, &CEOPlanApplyError{Stage: CEOApplyTaskStage, Result: result, Err: createErr}
		}
	}
	rows := make([]project.TaskDependency, 0, len(plan.Plan.ProposedTasks))
	for _, proposed := range plan.Plan.ProposedTasks {
		dependencies := make([]string, 0, len(proposed.DependencyIDs))
		for _, dependencyID := range proposed.DependencyIDs {
			dependencies = append(dependencies, result.TaskIDs[dependencyID])
		}
		rows = append(rows, project.TaskDependency{
			TaskID: result.TaskIDs[proposed.ProposalID], ProposalID: proposed.ProposalID,
			DependsOn: dependencies, Rationale: proposed.Rationale,
		})
	}
	dependencyRecord, err := ExecuteProjectDependencies(ctx, ProjectDependenciesInput{
		VaultRoot: input.VaultRoot, ProjectName: plan.Plan.ProjectName,
		Rows: rows, CurrentTime: input.CurrentTime,
	}, true)
	if dependencyRecord.Committed {
		result.Dependencies = &dependencyRecord
	}
	if err != nil {
		return result, &CEOPlanApplyError{Stage: CEOApplyDependenciesStage, Result: result, Err: err}
	}
	result.Status = "applied"
	return result, nil
}

func ceoApplyCommandFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	var applyError *CEOPlanApplyError
	if errors.As(err, &applyError) {
		return "CEO_PLAN_APPLY_FAILED", string(applyError.Stage)
	}
	return "CEO_PLAN_APPLY_FAILED", "preflight"
}

func planDescription(plan ceoplan.Plan) string {
	if strings.TrimSpace(plan.Summary) == "" {
		return plan.Objective
	}
	return plan.Objective + "\n\n" + plan.Summary
}

func planWarnings(plan ceoplan.Plan) []string {
	warnings := []string{}
	if len(plan.MissingRoles) > 0 {
		warnings = append(warnings, "不足ロールがあります。自動採用は行いません: "+strings.Join(plan.MissingRoles, ", "))
	}
	unassigned := 0
	for _, proposed := range plan.ProposedTasks {
		if proposed.AssigneeID == nil {
			unassigned++
		}
	}
	if unassigned > 0 {
		warnings = append(warnings, fmt.Sprintf("未割当タスクが%d件あります。", unassigned))
	}
	return warnings
}
