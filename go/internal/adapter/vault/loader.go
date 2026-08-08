// Package vault provides read-only Markdown Adapters for the current Obsidian
// Vault layout. Domain, Service, Kernel, and Runtime packages do not import it.
package vault

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AkiraShimizu0/workspace-os/go/internal/execution"
	"github.com/AkiraShimizu0/workspace-os/go/internal/policy"
	"github.com/AkiraShimizu0/workspace-os/go/internal/review"
	"github.com/AkiraShimizu0/workspace-os/go/internal/task"
	"github.com/AkiraShimizu0/workspace-os/go/internal/worker"
	"github.com/AkiraShimizu0/workspace-os/go/internal/workflow"
)

const maxDocumentBytes = 1 << 20

// Loader reads the existing Vault layout and returns structured Go context. It
// never writes Markdown or keeps a hidden fallback to Python.
type Loader struct {
	root string
}

// ExecutionInput contains values that are not inferred from Markdown. In
// particular, ProjectID, approval, and current time are caller-owned facts.
type ExecutionInput struct {
	ProjectID      string
	ProjectName    string
	TaskID         string
	Approval       *policy.ApprovalEvidence
	CurrentTime    time.Time
	ExecutionID    string
	CommandID      string
	IdempotencyKey string
	Metadata       map[string]string
}

type ReviewInput struct {
	ProjectName string
	TaskID      string
	ReviewerID  string
	CurrentTime time.Time
	Metadata    map[string]string
}

func NewLoader(root string) (*Loader, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("%w: Vault root is required", ErrInvalidInput)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: Vault root is invalid", ErrInvalidInput)
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: Vault root", ErrDocumentNotFound)
	}
	return &Loader{root: absolute}, nil
}

// LoadReviewPromptInput converts existing Vault documents into structured
// Review context. Markdown parsing stays at this Adapter boundary.
func (loader *Loader) LoadReviewPromptInput(ctx context.Context, input ReviewInput) (review.PromptInput, error) {
	if ctx == nil {
		return review.PromptInput{}, fmt.Errorf("%w: context is required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return review.PromptInput{}, err
	}
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.ReviewerID = strings.TrimSpace(input.ReviewerID)
	if !validPathSegment(input.ProjectName) || input.ReviewerID == "" || input.CurrentTime.IsZero() {
		return review.PromptInput{}, fmt.Errorf("%w: Project, reviewer, and current time are required", ErrInvalidInput)
	}
	if _, err := task.ParseTaskID(input.TaskID); err != nil {
		return review.PromptInput{}, fmt.Errorf("%w: Task ID", ErrInvalidInput)
	}
	projectDirectory := filepath.Join(loader.root, "プロジェクト", input.ProjectName)
	projectDocument, err := readDocument(filepath.Join(projectDirectory, "Project.md"), "Project.md")
	if err != nil {
		return review.PromptInput{}, err
	}
	taskDocument, err := readDocument(filepath.Join(projectDirectory, "Tasks.md"), "Tasks.md")
	if err != nil {
		return review.PromptInput{}, err
	}
	tasks, err := parseTasks(string(taskDocument))
	if err != nil {
		return review.PromptInput{}, err
	}
	target, found := findTask(tasks, input.TaskID)
	if !found {
		return review.PromptInput{}, fmt.Errorf("%w: requested Task", ErrDocumentNotFound)
	}
	if target.AssigneeID == nil {
		return review.PromptInput{}, ErrAssigneeMissing
	}
	employees, err := loader.loadEmployees(ctx)
	if err != nil {
		return review.PromptInput{}, err
	}
	source, found := employees[*target.AssigneeID]
	if !found {
		return review.PromptInput{}, fmt.Errorf("%w: assigned employee", ErrDocumentNotFound)
	}
	reviewer, found := employees[input.ReviewerID]
	if !found {
		return review.PromptInput{}, fmt.Errorf("%w: reviewer employee", ErrDocumentNotFound)
	}
	deliverableDocument, err := readDocument(filepath.Join(projectDirectory, "Deliverables", input.TaskID+".md"), "Task Deliverable")
	if err != nil {
		return review.PromptInput{}, err
	}
	frontmatter, err := parseFrontmatter(string(deliverableDocument))
	if err != nil {
		return review.PromptInput{}, err
	}
	promptInput := review.PromptInput{
		Reviewer:       reviewer,
		SourceEmployee: &source,
		Task: worker.TaskContext{
			TaskID: input.TaskID, Title: target.Title, ProjectName: input.ProjectName,
			ProjectOverview: parseProjectOverview(string(projectDocument)), AssigneeID: target.AssigneeID,
		},
		Deliverable: review.DeliverableContext{
			Content: string(deliverableDocument),
			Frontmatter: review.DeliverableFrontmatter{
				Project: frontmatter["project"], TaskID: frontmatter["task_id"], AssigneeID: frontmatter["assignee_id"],
				Runner: frontmatter["runner"], ExecutedAt: frontmatter["executed_at"],
			},
		},
		CurrentTime: input.CurrentTime,
		Metadata:    cloneMetadata(input.Metadata),
	}
	if err := promptInput.Validate(); err != nil {
		return review.PromptInput{}, fmt.Errorf("%w: Review context: %v", ErrInvalidDocument, err)
	}
	return promptInput, nil
}

func (loader *Loader) LoadExecutionRequest(ctx context.Context, input ExecutionInput) (execution.Request, error) {
	if ctx == nil {
		return execution.Request{}, fmt.Errorf("%w: context is required", ErrInvalidInput)
	}
	if err := ctx.Err(); err != nil {
		return execution.Request{}, err
	}
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.ProjectName = strings.TrimSpace(input.ProjectName)
	input.TaskID = strings.TrimSpace(input.TaskID)
	if input.ProjectID == "" || !validPathSegment(input.ProjectName) {
		return execution.Request{}, fmt.Errorf("%w: Project ID and safe name are required", ErrInvalidInput)
	}
	if _, err := task.ParseTaskID(input.TaskID); err != nil {
		return execution.Request{}, fmt.Errorf("%w: Task ID", ErrInvalidInput)
	}
	if input.CurrentTime.IsZero() {
		return execution.Request{}, fmt.Errorf("%w: current datetime is required", ErrInvalidInput)
	}

	projectDirectory := filepath.Join(loader.root, "プロジェクト", input.ProjectName)
	projectDocument, err := readDocument(filepath.Join(projectDirectory, "Project.md"), "Project.md")
	if err != nil {
		return execution.Request{}, err
	}
	taskDocument, err := readDocument(filepath.Join(projectDirectory, "Tasks.md"), "Tasks.md")
	if err != nil {
		return execution.Request{}, err
	}
	dependencyDocument, err := readDocument(filepath.Join(projectDirectory, "Task Dependencies.md"), "Task Dependencies.md")
	if err != nil {
		return execution.Request{}, err
	}
	if err := ctx.Err(); err != nil {
		return execution.Request{}, err
	}

	tasks, err := parseTasks(string(taskDocument))
	if err != nil {
		return execution.Request{}, err
	}
	dependencies, err := parseDependencies(string(dependencyDocument))
	if err != nil {
		return execution.Request{}, err
	}
	target, found := findTask(tasks, input.TaskID)
	if !found {
		return execution.Request{}, fmt.Errorf("%w: requested Task", ErrDocumentNotFound)
	}
	if target.AssigneeID == nil {
		return execution.Request{}, ErrAssigneeMissing
	}

	employees, err := loader.loadEmployees(ctx)
	if err != nil {
		return execution.Request{}, err
	}
	employee, found := employees[*target.AssigneeID]
	if !found {
		return execution.Request{}, fmt.Errorf("%w: assigned employee", ErrDocumentNotFound)
	}
	existingEmployees := make(map[string]bool, len(employees))
	for employeeID := range employees {
		existingEmployees[employeeID] = true
	}

	return execution.Request{
		ProjectID:         input.ProjectID,
		ProjectName:       input.ProjectName,
		ProjectOverview:   parseProjectOverview(string(projectDocument)),
		TaskID:            input.TaskID,
		Employee:          employee,
		Tasks:             tasks,
		Dependencies:      dependencies,
		ExistingEmployees: existingEmployees,
		Approval:          cloneApproval(input.Approval),
		CurrentTime:       input.CurrentTime,
		ExecutionID:       strings.TrimSpace(input.ExecutionID),
		CommandID:         strings.TrimSpace(input.CommandID),
		IdempotencyKey:    strings.TrimSpace(input.IdempotencyKey),
		Metadata:          cloneMetadata(input.Metadata),
	}, nil
}

func (loader *Loader) loadEmployees(ctx context.Context) (map[string]worker.EmployeeContext, error) {
	directory := filepath.Join(loader.root, "社員")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("%w: employee directory", ErrDocumentNotFound)
	}
	employees := make(map[string]worker.EmployeeContext)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" || entry.Name() == "社員.md" {
			continue
		}
		content, err := readDocument(filepath.Join(directory, entry.Name()), "employee Markdown")
		if err != nil {
			return nil, err
		}
		frontmatter, err := parseFrontmatter(string(content))
		if err != nil {
			return nil, err
		}
		employee := worker.EmployeeContext{
			EmployeeID: strings.TrimSpace(frontmatter["id"]),
			Name:       strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())),
			Department: strings.TrimSpace(frontmatter["department"]),
			Role:       strings.TrimSpace(frontmatter["role"]),
			Model:      strings.TrimSpace(frontmatter["model"]),
		}
		if err := employee.Validate(); err != nil {
			return nil, fmt.Errorf("%w: employee fields", ErrInvalidDocument)
		}
		if _, exists := employees[employee.EmployeeID]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateIdentity, employee.EmployeeID)
		}
		employees[employee.EmployeeID] = employee
	}
	return employees, nil
}

func readDocument(path, label string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDocumentNotFound, label)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: unreadable %s", ErrInvalidDocument, label)
	}
	if len(content) > maxDocumentBytes {
		return nil, fmt.Errorf("%w: oversized %s", ErrInvalidDocument, label)
	}
	return content, nil
}

func validPathSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\\\x00`)
}

func findTask(tasks []workflow.Task, taskID string) (workflow.Task, bool) {
	for _, candidate := range tasks {
		if candidate.ID == taskID {
			return candidate, true
		}
	}
	return workflow.Task{}, false
}

func cloneApproval(approval *policy.ApprovalEvidence) *policy.ApprovalEvidence {
	if approval == nil {
		return nil
	}
	cloned := *approval
	return &cloned
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
